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

	srv := NewServer(&mockDispatcher{}, nil)
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

	srv := NewServer(dispatcher, nil)
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

func TestServer_SystemIntroductionPrompt(t *testing.T) {
	t.Parallel()

	srv := NewServer(&mockDispatcher{}, nil)
	res, err := srv.handleSystemIntroduction(context.Background(), &mcp.GetPromptRequest{})
	if err != nil {
		t.Fatalf("handleSystemIntroduction failed: %v", err)
	}

	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}

	textContent, ok := res.Messages[0].Content.(*mcp.TextContent)
	if !ok {
		t.Fatal("expected TextContent")
	}

	if textContent.Text == "" {
		t.Error("expected non-empty text content in prompt")
	}
}
