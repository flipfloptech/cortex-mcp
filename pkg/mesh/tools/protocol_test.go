package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- ServeTools tests ---

func TestServeTools_SingleRequest(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(nil)
	registry.Register(ToolDefinition{
		Name:        "echo",
		Description: "Echo back the input",
		Category:    "test",
	}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return &ToolResult{Content: args}, nil
	})

	// Create an in-memory connection pair.
	client, server := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the server in a goroutine.
	var serveErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveErr = ServeToolConn(ctx, server, registry)
	}()

	// Client sends a tool request and reads the response.
	result, err := DialInvoke(ctx, client, "echo", json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("DialInvoke error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error result: %s", result.Content)
	}

	var got map[string]string
	if err := json.Unmarshal(result.Content, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got["msg"] != "hello" {
		t.Fatalf("got %q, want %q", got["msg"], "hello")
	}

	// Close the client connection to let the server exit.
	if err := client.Close(); err != nil {
		t.Logf("close client: %v", err)
	}
	wg.Wait()

	// Server should exit cleanly when client disconnects (EOF is not an error).
	if serveErr != nil {
		t.Fatalf("ServeToolConn error: %v", serveErr)
	}
}

func TestServeTools_ToolNotFound(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(nil)
	// Empty registry — no tools registered.

	client, server := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = ServeToolConn(ctx, server, registry)
	}()

	result, err := DialInvoke(ctx, client, "nonexistent", nil)
	if err != nil {
		t.Fatalf("DialInvoke should not return transport error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected error result for nonexistent tool")
	}

	if !strings.Contains(string(result.Content), "nonexistent") {
		t.Fatalf("error should mention tool name: %s", result.Content)
	}

	if err := client.Close(); err != nil {
		t.Logf("close client: %v", err)
	}
	wg.Wait()
}

func TestServeTools_ToolHandlerError(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(nil)
	registry.Register(ToolDefinition{
		Name:     "fail",
		Category: "test",
	}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return nil, errors.New("handler exploded")
	})

	client, server := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = ServeToolConn(ctx, server, registry)
	}()

	result, err := DialInvoke(ctx, client, "fail", nil)
	if err != nil {
		t.Fatalf("DialInvoke should not return transport error: %v", err)
	}

	if !result.IsError {
		t.Fatal("expected error result when handler returns error")
	}

	if err := client.Close(); err != nil {
		t.Logf("close client: %v", err)
	}
	wg.Wait()
}

func TestServeTools_ContextCancellation(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(nil)

	_, server := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- ServeToolConn(ctx, server, registry)
	}()

	// Cancel the context — the server should stop.
	cancel()

	select {
	case err := <-done:
		// Server should exit without error on cancellation.
		if err != nil {
			t.Fatalf("expected nil error on cancel, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeToolConn did not exit after context cancellation")
	}
}

// --- DialInvoke tests ---

func TestDialInvoke_ConnectionClosed(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	// Close the server side immediately — simulates dead peer.
	if err := server.Close(); err != nil {
		t.Fatalf("close server: %v", err)
	}

	ctx := context.Background()
	_, err := DialInvoke(ctx, client, "echo", nil)
	if err == nil {
		t.Fatal("DialInvoke should fail when peer is closed")
	}
}

func TestDialInvoke_ContextTimeout(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() {
		_ = server.Close()
	}()

	// Server never reads — client will block.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := DialInvoke(ctx, client, "echo", nil)
	if err == nil {
		t.Fatal("DialInvoke should fail on context timeout")
	}
}

// --- ServeToolListener tests ---

func TestServeToolListener_AcceptsMultipleConnections(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(nil)
	registry.Register(ToolDefinition{
		Name:     "whoami",
		Category: "test",
	}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return NewTextResult("node-42"), nil
	})

	// Create a TCP listener for the test.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ServeToolListener(ctx, lis, registry)

	// Make two sequential connections.
	for i := 0; i < 2; i++ {
		conn, err := net.Dial("tcp", lis.Addr().String())
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}

		result, err := DialInvoke(ctx, conn, "whoami", nil)
		if err != nil {
			t.Fatalf("invoke %d: %v", i, err)
		}

		if string(result.Content) != `{"text":"node-42"}` {
			t.Fatalf("call %d: got %q, want %q", i, result.Content, `{"text":"node-42"}`)
		}

		if err := conn.Close(); err != nil {
			t.Logf("close conn %d: %v", i, err)
		}
	}
}

// --- End-to-end: pipe roundtrip ---

func TestToolProtocol_EndToEnd_Pipe(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(nil)
	registry.Register(ToolDefinition{
		Name:     "system_info",
		Category: "system",
	}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		info := map[string]interface{}{
			"os":   "linux",
			"cpus": 8,
		}
		data, _ := json.Marshal(info)
		return &ToolResult{Content: data}, nil
	})

	client, server := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = ServeToolConn(ctx, server, registry) }()

	// First call.
	result1, err := DialInvoke(ctx, client, "system_info", nil)
	if err != nil {
		t.Fatalf("first invoke: %v", err)
	}

	var info map[string]interface{}
	if err := json.Unmarshal(result1.Content, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info["os"] != "linux" {
		t.Fatalf("got os=%v, want linux", info["os"])
	}

	// Second call on same connection (verify conn reuse).
	result2, err := DialInvoke(ctx, client, "system_info", nil)
	if err != nil {
		t.Fatalf("second invoke: %v", err)
	}
	if result2.IsError {
		t.Fatal("second call should succeed")
	}

	if err := client.Close(); err != nil {
		_ = err
	}
}

// Verify ServeToolConn with nil args works (empty args_json in proto).
func TestServeTools_NilArgs(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(nil)
	registry.Register(ToolDefinition{
		Name:     "ping",
		Category: "test",
	}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		if len(args) == 0 {
			return NewTextResult("pong"), nil
		}
		return NewTextResult("pong:" + string(args)), nil
	})

	client, server := net.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = ServeToolConn(ctx, server, registry) }()

	result, err := DialInvoke(ctx, client, "ping", nil)
	if err != nil {
		t.Fatalf("DialInvoke error: %v", err)
	}
	if string(result.Content) != `{"text":"pong"}` {
		t.Fatalf("got %q, want %q", result.Content, `{"text":"pong"}`)
	}

	_ = client.Close()
}

// Verify the unused import doesn't cause issues.
var _ = io.EOF
