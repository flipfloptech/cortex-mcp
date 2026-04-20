package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mockDispatcher implements a simple Dispatcher for testing.
type mockDispatcher struct {
	dispatchFunc func(ctx context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error)
}

func (m *mockDispatcher) Dispatch(ctx context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error) {
	if m.dispatchFunc != nil {
		return m.dispatchFunc(ctx, toolName, args)
	}
	return &tools.ToolResult{Content: json.RawMessage(`"mock success"`)}, nil
}

func TestServer_RegisterMetaTools(t *testing.T) {
	t.Parallel()

	srv := NewServer(&mockDispatcher{})
	if srv == nil {
		t.Fatal("expected server to be created")
	}
}

func TestServer_CallMetaTool(t *testing.T) {
	t.Parallel()

	called := false
	dispatcher := &mockDispatcher{
		dispatchFunc: func(ctx context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error) {
			called = true
			if toolName != "list_tools" {
				t.Errorf("expected toolName list_tools, got %s", toolName)
			}
			return &tools.ToolResult{Content: json.RawMessage(`"success"`), IsError: false}, nil
		},
	}

	srv := NewServer(dispatcher)
	res, _, err := srv.handleListTools(context.Background(), &mcp.CallToolRequest{}, EmptyInput{})
	if err != nil {
		t.Fatalf("handleListTools failed: %v", err)
	}

	if !called {
		t.Error("expected dispatcher to be called")
	}
	if res.IsError {
		t.Error("expected success result")
	}
	if len(res.Content) == 0 {
		t.Error("expected content in result")
	}
}
