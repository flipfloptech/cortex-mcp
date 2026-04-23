package lifecycle

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAcquireLock_SuccessAndConflict(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socketName := fmt.Sprintf("@cortex-mcp-test-lock-%d", time.Now().UnixNano())

	// First acquisition should succeed
	closer, err := AcquireLock(ctx, socketName)
	if err != nil {
		t.Fatalf("expected first lock to succeed, got %v", err)
	}
	defer func() { _ = closer.Close() }()

	// Second acquisition should fail
	_, err2 := AcquireLock(ctx, socketName)
	if err2 == nil {
		t.Fatal("expected second lock to fail, but it succeeded")
	}
}

func TestAcquireLock_ProvidesJSONPayload(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	socketName := fmt.Sprintf("@cortex-mcp-test-kill-%d", time.Now().UnixNano())

	// Acquire lock so we have a process serving its JSON payload
	closer, err := AcquireLock(ctx, socketName)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	defer func() { _ = closer.Close() }()

	// Test the JSON reading mechanism directly by dialing
	conn, err := net.DialTimeout("unix", socketName, 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("failed to read payload: %v", err)
	}

	payloadStr := string(buf[:n])

	// Fast way to check if it's JSON and has the PID
	expectedPid := fmt.Sprintf(`"pid":%d`, os.Getpid())
	if !strings.Contains(payloadStr, expectedPid) {
		t.Errorf("expected payload to contain %s, got %s", expectedPid, payloadStr)
	}
	if !strings.Contains(payloadStr, `"path":`) {
		t.Errorf("expected payload to contain path, got %s", payloadStr)
	}
}

func BenchmarkAcquireLock(b *testing.B) {
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		socketName := fmt.Sprintf("@cortex-mcp-bench-lock-%d-%d", time.Now().UnixNano(), i)
		closer, err := AcquireLock(ctx, socketName)
		if err == nil {
			_ = closer.Close()
		}
	}
}

func BenchmarkKillLockedProcess(b *testing.B) {
	ctx := context.Background()
	socketName := fmt.Sprintf("@cortex-mcp-bench-kill-%d", time.Now().UnixNano())
	closer, _ := AcquireLock(ctx, socketName)
	defer func() { _ = closer.Close() }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Just dial to test connection performance without actually killing
		conn, err := net.DialTimeout("unix", socketName, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
		}
	}
}

func BenchmarkReadPayloadFromSocket(b *testing.B) {
	ctx := context.Background()
	socketName := fmt.Sprintf("@cortex-mcp-bench-read-%d", time.Now().UnixNano())
	closer, _ := AcquireLock(ctx, socketName)
	defer func() { _ = closer.Close() }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readPayloadFromSocket(ctx, socketName)
	}
}
