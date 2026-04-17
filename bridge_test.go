package main

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// --- runBridge tests ---
// The bridge is a dumb pipe: connects stdin/stdout to a local TCP addr.
// Uses SignalReady magic, exits when stdin closes.

func TestRunBridge_RelaysData(t *testing.T) {
	t.Parallel()

	// Start a TCP listener to simulate the local mesh node.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = lis.Close() }()

	// Accept one connection and echo back whatever we receive.
	var serverWg sync.WaitGroup
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Echo server: read and write back.
		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	// Create pipes to simulate stdin/stdout.
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run bridge in background.
	var bridgeErr error
	var bridgeWg sync.WaitGroup
	bridgeWg.Add(1)
	go func() {
		defer bridgeWg.Done()
		bridgeErr = runBridge(ctx, lis.Addr().String(), stdinR, stdoutW)
	}()

	// Read the 4-byte readiness magic from stdout.
	magic := make([]byte, 4)
	if _, err := io.ReadFull(stdoutR, magic); err != nil {
		t.Fatalf("read ready magic: %v", err)
	}

	// Verify it's the DeployReadyMagic: "CM" 0x01 0x01
	if magic[0] != 'C' || magic[1] != 'M' || magic[2] != 0x01 || magic[3] != 0x01 {
		t.Fatalf("unexpected magic: %x", magic)
	}

	// Send data through stdin → bridge → echo server → bridge → stdout.
	payload := []byte("hello through the bridge")
	if _, err := stdinW.Write(payload); err != nil {
		t.Fatalf("write stdin: %v", err)
	}

	// Read the echoed response from stdout.
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(stdoutR, response); err != nil {
		t.Fatalf("read stdout: %v", err)
	}

	if string(response) != string(payload) {
		t.Errorf("expected %q, got %q", payload, response)
	}

	// Close stdin to simulate SSH disconnect → bridge should exit.
	_ = stdinW.Close()
	bridgeWg.Wait()

	// Bridge should exit without error on stdin close.
	if bridgeErr != nil {
		t.Errorf("bridge returned error on stdin close: %v", bridgeErr)
	}
}

func TestRunBridge_UnreachableAddr(t *testing.T) {
	t.Parallel()

	// Find a port that nothing is listening on.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	stdinR, _ := io.Pipe()
	_, stdoutW := io.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = runBridge(ctx, addr, stdinR, stdoutW)
	if err == nil {
		t.Fatal("runBridge should fail for unreachable addr")
	}
}

func TestRunBridge_ExitsOnStdinClose(t *testing.T) {
	t.Parallel()

	// Start a TCP listener.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = lis.Close() }()

	// Accept and hold the connection open.
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Block until connection closes.
		buf := make([]byte, 1)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runBridge(ctx, lis.Addr().String(), stdinR, stdoutW)
	}()

	// Drain the readiness magic.
	go func() {
		buf := make([]byte, 4)
		_, _ = io.ReadFull(stdoutR, buf)
	}()

	// Give bridge time to start, then close stdin.
	time.Sleep(100 * time.Millisecond)
	_ = stdinW.Close()

	select {
	case err := <-done:
		// Bridge should exit cleanly (nil or EOF-related).
		if err != nil {
			t.Logf("bridge exited with: %v (acceptable)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not exit after stdin close — stale process risk")
	}
}

// --- checkServiceActive tests ---
// Verifies the command and output parsing for systemctl is-active.

func TestServiceActiveCommand(t *testing.T) {
	t.Parallel()

	cmd := serviceActiveCommand()
	if cmd != "systemctl is-active cortex-mesh" {
		t.Errorf("expected 'systemctl is-active cortex-mesh', got %q", cmd)
	}
}

func TestParseServiceActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		output   string
		exitCode int
		expected bool
	}{
		{"active", "active\n", 0, true},
		{"inactive", "inactive\n", 3, false},
		{"failed", "failed\n", 3, false},
		{"unknown", "unknown\n", 3, false},
		{"activating", "activating\n", 0, false},
		{"empty", "", 1, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseServiceActive(tc.output)
			if got != tc.expected {
				t.Errorf("parseServiceActive(%q) = %v, want %v", tc.output, got, tc.expected)
			}
		})
	}
}
