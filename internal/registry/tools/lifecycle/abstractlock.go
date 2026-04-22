package lifecycle

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

// AcquireLock attempts to bind to the abstract Unix socket to guarantee
// single-instance execution. If successful, it launches a background goroutine
// to serve its PID to any connecting clients.
func AcquireLock(ctx context.Context, socketName string) (io.Closer, error) {
	listener, err := net.Listen("unix", socketName)
	if err != nil {
		return nil, fmt.Errorf("failed to bind lock socket (another instance may be running): %w", err)
	}

	// Serve the PID to any incoming connections
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				// Listener closed
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				pidStr := strconv.Itoa(os.Getpid())
				_, _ = c.Write([]byte(pidStr))
			}(conn)
		}
	}()

	return listener, nil
}

// readPIDFromSocket connects to the abstract Unix socket and reads the PID.
func readPIDFromSocket(ctx context.Context, socketName string) (int, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketName)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return 0, err
	}

	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil {
		return 0, err
	}

	pid, err := strconv.Atoi(string(buf[:n]))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// KillLockedProcess connects to the abstract Unix socket, reads the PID
// of the running process, and sends SIGKILL to it.
func KillLockedProcess(ctx context.Context, socketName string) error {
	pid, err := readPIDFromSocket(ctx, socketName)
	if err != nil {
		return fmt.Errorf("read PID from socket: %w", err)
	}

	zap.S().Infow("read PID from abstract socket", "pid", pid)

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process: %w", err)
	}

	if err := process.Kill(); err != nil {
		return fmt.Errorf("kill process %d: %w", pid, err)
	}

	zap.S().Infow("sent SIGKILL to process", "pid", pid)
	return nil
}
