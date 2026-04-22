package harness

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cortex-mesh/cortex-mesh/tools"
)

type dummyDispatcher struct{}

func (d dummyDispatcher) Dispatch(ctx context.Context, name string, rawMessage json.RawMessage) (*tools.ToolResult, error) {
	return &tools.ToolResult{}, nil
}

func BenchmarkNewHarness(b *testing.B) {
	d := dummyDispatcher{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewHarness(d)
	}
}

func BenchmarkRunToolCheck(b *testing.B) {
	h := NewHarness(dummyDispatcher{})
	ctx := context.Background()
	toolsList := []string{"get_system_info"}
	nodeIDs := []string{"node1"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.RunToolCheck(ctx, toolsList, nodeIDs)
	}
}
