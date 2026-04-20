package stream_test

import (
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry/stream"
)

// TestAggregator_BasicGroupBy verifies basic group-by aggregation.
func TestAggregator_BasicGroupBy(t *testing.T) {
	t.Parallel()

	agg := stream.NewAggregator(100)
	agg.Add("oss1", 42.0)
	agg.Add("oss2", 17.0)
	agg.Add("oss1", 58.0)

	gs := agg.Get("oss1")
	if gs == nil {
		t.Fatal("Get(\"oss1\") returned nil")
	}
	if gs.Count != 2 {
		t.Errorf("Count = %d, want 2", gs.Count)
	}
	if gs.Sum != 100.0 {
		t.Errorf("Sum = %f, want 100.0", gs.Sum)
	}
	if gs.Min != 42.0 {
		t.Errorf("Min = %f, want 42.0", gs.Min)
	}
	if gs.Max != 58.0 {
		t.Errorf("Max = %f, want 58.0", gs.Max)
	}
	if gs.Avg() != 50.0 {
		t.Errorf("Avg() = %f, want 50.0", gs.Avg())
	}
}

// TestAggregator_Eviction verifies FIFO group eviction when maxGroups is exceeded.
func TestAggregator_Eviction(t *testing.T) {
	t.Parallel()

	agg := stream.NewAggregator(2)
	agg.Add("a", 1.0)
	agg.Add("b", 2.0)
	agg.Add("c", 3.0) // evicts "a"

	if agg.Get("a") != nil {
		t.Error("Get(\"a\") should return nil after eviction")
	}
	if agg.Get("c") == nil {
		t.Error("Get(\"c\") should not be nil")
	}
	if agg.Groups() != 2 {
		t.Errorf("Groups() = %d, want 2", agg.Groups())
	}
}

// TestAggregator_EmptyGroupAvg verifies that Avg returns 0 for zero-count groups.
func TestAggregator_EmptyGroupAvg(t *testing.T) {
	t.Parallel()

	gs := &stream.GroupStats{}
	if gs.Avg() != 0 {
		t.Errorf("Avg() = %f, want 0 for empty group", gs.Avg())
	}
}

// TestAggregator_All verifies the All snapshot method.
func TestAggregator_All(t *testing.T) {
	t.Parallel()

	agg := stream.NewAggregator(100)
	agg.Add("x", 10.0)
	agg.Add("y", 20.0)

	all := agg.All()
	if len(all) != 2 {
		t.Fatalf("All() returned %d groups, want 2", len(all))
	}
	if all["x"].Sum != 10.0 {
		t.Errorf("All()[\"x\"].Sum = %f, want 10.0", all["x"].Sum)
	}
}

// TestAggregator_GetNonexistent verifies Get returns nil for unknown keys.
func TestAggregator_GetNonexistent(t *testing.T) {
	t.Parallel()

	agg := stream.NewAggregator(100)
	if agg.Get("missing") != nil {
		t.Error("Get(\"missing\") should return nil")
	}
}

// TestAggregator_PanicOnZeroMaxGroups verifies the panic guard.
func TestAggregator_PanicOnZeroMaxGroups(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("NewAggregator(0) should panic")
		}
	}()
	stream.NewAggregator(0)
}
