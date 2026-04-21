package harness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cortex-mesh/cortex-mesh/tools"
)

type dummyDispatcher struct{}
func (d dummyDispatcher) Dispatch(ctx context.Context, name string, rawMessage json.RawMessage) (*tools.ToolResult, error) {
	return &tools.ToolResult{}, nil
}

type dummyDeployer struct{}
func (d dummyDeployer) Deploy(ctx context.Context) error { return nil }
func (d dummyDeployer) Uninstall(ctx context.Context) error { return nil }

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
	toolsList := []string{"system_info"}
	nodeIDs := []string{"node1"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.RunToolCheck(ctx, toolsList, nodeIDs)
	}
}

func BenchmarkRunToolSoak(b *testing.B) {
	h := NewHarness(dummyDispatcher{})
	ctx := context.Background()
	toolsList := []string{"system_info"}
	nodeIDs := []string{"node1"}
	// We want to bench the scaffolding, not long sleeps, so duration 0 and count 1
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.RunToolSoak(ctx, toolsList, nodeIDs, 1, 0*time.Second)
	}
}

func BenchmarkRunDeploySoak(b *testing.B) {
	h := NewHarness(dummyDispatcher{})
	ctx := context.Background()
	deployer := dummyDeployer{}
	// count 1, duration 0 to minimize blocking
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.RunDeploySoak(ctx, deployer, 1, 0*time.Second)
	}
}
