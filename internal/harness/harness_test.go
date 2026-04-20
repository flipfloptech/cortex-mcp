package harness

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortex-mesh/cortex-mesh/tools"
)

type mockDispatcher struct {
	dispatchFunc func(ctx context.Context, name string, rawMessage json.RawMessage) (*tools.ToolResult, error)
}

func (m *mockDispatcher) Dispatch(ctx context.Context, name string, rawMessage json.RawMessage) (*tools.ToolResult, error) {
	if m.dispatchFunc != nil {
		return m.dispatchFunc(ctx, name, rawMessage)
	}
	return &tools.ToolResult{Content: []byte("success"), IsError: false}, nil
}

func TestHarness_RunToolCheck(t *testing.T) {
	t.Parallel()

	var calls int32 = 0
	md := &mockDispatcher{
		dispatchFunc: func(ctx context.Context, name string, rawMessage json.RawMessage) (*tools.ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			if name != "call_tool" {
				t.Errorf("expected call_tool, got %s", name)
			}
			return &tools.ToolResult{Content: []byte("success"), IsError: false}, nil
		},
	}

	h := NewHarness(md)

	nodes := []string{"node1", "node2"}
	toolsList := []string{"sysinfo", "uptime"}

	report := h.RunToolCheck(context.Background(), toolsList, nodes)

	if report.Total != 4 {
		t.Errorf("expected 4 total calls, got %d", report.Total)
	}
	if report.Successful != 4 {
		t.Errorf("expected 4 successful calls, got %d", report.Successful)
	}
	if report.Failed != 0 {
		t.Errorf("expected 0 failed calls, got %d", report.Failed)
	}
	if atomic.LoadInt32(&calls) != 4 {
		t.Errorf("expected 4 dispatcher calls, got %d", atomic.LoadInt32(&calls))
	}
}

func TestHarness_RunToolCheck_WithErrors(t *testing.T) {
	t.Parallel()

	md := &mockDispatcher{
		dispatchFunc: func(ctx context.Context, name string, rawMessage json.RawMessage) (*tools.ToolResult, error) {
			var args map[string]interface{}
			_ = json.Unmarshal(rawMessage, &args)

			if args["node_name"] == "node_fail_dispatch" {
				return nil, errors.New("network error")
			}
			if args["node_name"] == "node_fail_tool" {
				return &tools.ToolResult{Content: []byte("tool error"), IsError: true}, nil
			}

			return &tools.ToolResult{Content: []byte("success"), IsError: false}, nil
		},
	}

	h := NewHarness(md)

	nodes := []string{"node_good", "node_fail_dispatch", "node_fail_tool"}
	toolsList := []string{"sysinfo"}

	report := h.RunToolCheck(context.Background(), toolsList, nodes)

	if report.Total != 3 {
		t.Errorf("expected 3 total calls, got %d", report.Total)
	}
	if report.Successful != 1 {
		t.Errorf("expected 1 successful calls, got %d", report.Successful)
	}
	if report.Failed != 2 {
		t.Errorf("expected 2 failed calls, got %d", report.Failed)
	}
}

func TestHarness_RunToolSoak(t *testing.T) {
	t.Parallel()

	md := &mockDispatcher{
		dispatchFunc: func(ctx context.Context, name string, rawMessage json.RawMessage) (*tools.ToolResult, error) {
			return &tools.ToolResult{Content: []byte("success"), IsError: false}, nil
		},
	}

	h := NewHarness(md)

	nodes := []string{"node1", "node2"}
	toolsList := []string{"sysinfo", "uptime"}

	report := h.RunToolSoak(context.Background(), toolsList, nodes, 10, 0)

	if report.Total != 10 {
		t.Errorf("expected 10 total calls, got %d", report.Total)
	}
	if report.Successful != 10 {
		t.Errorf("expected 10 successful calls, got %d", report.Successful)
	}
}

func TestHarness_RunToolSoak_ContextCancel(t *testing.T) {
	t.Parallel()

	md := &mockDispatcher{
		dispatchFunc: func(ctx context.Context, name string, rawMessage json.RawMessage) (*tools.ToolResult, error) {
			time.Sleep(10 * time.Millisecond) // slow down slightly
			return &tools.ToolResult{Content: []byte("success"), IsError: false}, nil
		},
	}

	h := NewHarness(md)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	nodes := []string{"node1"}
	toolsList := []string{"sysinfo"}

	report := h.RunToolSoak(ctx, toolsList, nodes, 100, 0)

	// Since we check context cancellation in the loop before dispatching, Total should be 0.
	if report.Total != 0 {
		t.Errorf("expected 0 total calls due to cancellation, got %d", report.Total)
	}
}
