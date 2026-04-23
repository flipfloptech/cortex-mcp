package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// LockPayload is served over the abstract socket to identify the running process.
type LockPayload struct {
	PID  int    `json:"pid"`
	Path string `json:"path"`
}

// AcquireLock attempts to bind to the abstract Unix socket to guarantee
// single-instance execution. If successful, it launches a background goroutine
// to serve its payload to any connecting clients.
func AcquireLock(ctx context.Context, socketName string) (io.Closer, error) {
	listener, err := net.Listen("unix", socketName)
	if err != nil {
		return nil, fmt.Errorf("failed to bind lock socket (another instance may be running): %w", err)
	}

	exePath, _ := os.Executable()
	payload := LockPayload{
		PID:  os.Getpid(),
		Path: exePath,
	}
	data, _ := json.Marshal(payload)

	// Serve the payload to any incoming connections
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				// Listener closed
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = c.Write(data)
			}(conn)
		}
	}()

	return listener, nil
}

// readPayloadFromSocket connects to the abstract Unix socket and reads the JSON payload.
func readPayloadFromSocket(ctx context.Context, socketName string) (*LockPayload, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return nil, err
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}

	var payload LockPayload
	if err := json.Unmarshal(buf[:n], &payload); err != nil {
		// Fallback for old versions that just returned PID string
		pid, atoiErr := strconv.Atoi(string(buf[:n]))
		if atoiErr != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
		return &LockPayload{PID: pid}, nil
	}
	return &payload, nil
}

// KillLockedProcess connects to the abstract Unix socket, reads the payload
// of the running process, sends SIGKILL to it, and cleans up the binary if it was ephemeral.
func KillLockedProcess(ctx context.Context, socketName string) error {
	payload, err := readPayloadFromSocket(ctx, socketName)
	if err != nil {
		return fmt.Errorf("read payload from socket: %w", err)
	}

	zap.S().Infow("read payload from abstract socket", "pid", payload.PID, "path", payload.Path)

	process, err := os.FindProcess(payload.PID)
	if err != nil {
		return fmt.Errorf("find process: %w", err)
	}

	if err := process.Kill(); err != nil {
		return fmt.Errorf("kill process %d: %w", payload.PID, err)
	}

	zap.S().Infow("sent SIGKILL to process", "pid", payload.PID)

	// If the path starts with /tmp/ and is not the daemon install path, clean it up
	if payload.Path != "" && strings.HasPrefix(payload.Path, "/tmp/") {
		if err := os.Remove(payload.Path); err != nil {
			zap.S().Warnw("failed to clean up ephemeral binary", "path", payload.Path, "error", err)
		} else {
			zap.S().Infow("cleaned up ephemeral binary", "path", payload.Path)
		}
	}

	return nil
}
