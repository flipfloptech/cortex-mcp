package registry

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

// mockTool implements Tool for testing cache behavior.
type mockTool struct {
	callCount int32
	result    *ToolResult
}

func (m *mockTool) Name() string                { return "mock_tool" }
func (m *mockTool) Description() string         { return "mock" }
func (m *mockTool) Help() string                { return "mock" }
func (m *mockTool) Category() string            { return "test" }
func (m *mockTool) Parameters() []ToolParam     { return nil }
func (m *mockTool) Hidden() bool                { return false }
func (m *mockTool) IsSupported() (bool, string) { return true, "" }
func (m *mockTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	atomic.AddInt32(&m.callCount, 1)
	return m.result, nil
}

func TestWithCache_HitAndMiss(t *testing.T) {
	t.Parallel()

	mock := &mockTool{
		result: NewResult("mock_tool", StatusOK, "test summary", map[string]string{"foo": "bar"}),
	}

	// 50ms TTL for testing expiration.
	cached := WithCache(50*time.Millisecond, mock)
	ctx := context.Background()
	args1 := json.RawMessage(`{"param":"value1"}`)
	args2 := json.RawMessage(`{"param":"value2"}`)

	// Call 1: Miss (empty cache)
	res1, err := cached.Execute(ctx, args1)
	if err != nil {
		t.Fatalf("execute 1 failed: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call, got %d", mock.callCount)
	}

	// Call 2: Hit (same args, within TTL)
	res2, err := cached.Execute(ctx, args1)
	if err != nil {
		t.Fatalf("execute 2 failed: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call (cache hit), got %d", mock.callCount)
	}

	// Ensure the result is a copy, not the same pointer (mutation protection)
	if res1 == res2 {
		t.Error("expected cached result to be a deep copy, got same pointer")
	}
	if string(res1.Data) != string(res2.Data) {
		t.Errorf("data mismatch: %s != %s", res1.Data, res2.Data)
	}

	// Call 3: Miss (different args)
	_, err = cached.Execute(ctx, args2)
	if err != nil {
		t.Fatalf("execute 3 failed: %v", err)
	}
	if mock.callCount != 2 {
		t.Errorf("expected 2 calls (args changed), got %d", mock.callCount)
	}

	// Call 4: Miss (args1 again, but args2 evicted args1 from O(1) cache)
	_, err = cached.Execute(ctx, args1)
	if err != nil {
		t.Fatalf("execute 4 failed: %v", err)
	}
	if mock.callCount != 3 {
		t.Errorf("expected 3 calls (args1 was evicted), got %d", mock.callCount)
	}

	// Call 5: Hit (args1 is now cached)
	_, err = cached.Execute(ctx, args1)
	if err != nil {
		t.Fatalf("execute 5 failed: %v", err)
	}
	if mock.callCount != 3 {
		t.Errorf("expected 3 calls (cache hit for args1), got %d", mock.callCount)
	}

	// Call 6: Miss (TTL expired)
	time.Sleep(60 * time.Millisecond) // Wait for TTL to expire
	_, err = cached.Execute(ctx, args1)
	if err != nil {
		t.Fatalf("execute 6 failed: %v", err)
	}
	if mock.callCount != 4 {
		t.Errorf("expected 4 calls (TTL expired), got %d", mock.callCount)
	}
}

func TestWithCache_OnlyCachesSuccess(t *testing.T) {
	t.Parallel()

	mock := &mockTool{
		result: NewResult("mock_tool", StatusError, "failed", nil),
	}

	cached := WithCache(1*time.Minute, mock)
	ctx := context.Background()
	args := json.RawMessage(`{}`)

	// Call 1: StatusError, should not be cached
	_, _ = cached.Execute(ctx, args)
	if mock.callCount != 1 {
		t.Errorf("expected 1 call, got %d", mock.callCount)
	}

	// Call 2: Should miss because previous was an error
	_, _ = cached.Execute(ctx, args)
	if mock.callCount != 2 {
		t.Errorf("expected 2 calls (error not cached), got %d", mock.callCount)
	}

	// Change to warning, which SHOULD be cached
	mock.result.Status = StatusWarning
	_, _ = cached.Execute(ctx, args)
	if mock.callCount != 3 {
		t.Errorf("expected 3 calls, got %d", mock.callCount)
	}

	// Call 4: Should hit (Warning was cached)
	_, _ = cached.Execute(ctx, args)
	if mock.callCount != 3 {
		t.Errorf("expected 3 calls (warning cached), got %d", mock.callCount)
	}
}

func BenchmarkWithCache_Hit(b *testing.B) {
	mock := &mockTool{
		result: NewResult("mock_tool", StatusOK, "test", map[string]string{"foo": "bar"}),
	}
	cached := WithCache(1*time.Minute, mock)
	ctx := context.Background()
	args := json.RawMessage(`{"param":"value1"}`)

	// Prime the cache
	_, _ = cached.Execute(ctx, args)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = cached.Execute(ctx, args)
	}
}

func BenchmarkWithCache_Miss(b *testing.B) {
	mock := &mockTool{
		result: NewResult("mock_tool", StatusOK, "test", map[string]string{"foo": "bar"}),
	}
	cached := WithCache(1*time.Minute, mock)
	ctx := context.Background()

	// Pre-allocate argument byte slices to isolate the cache logic from json string creation
	args1 := json.RawMessage(`{"param":"value1"}`)
	args2 := json.RawMessage(`{"param":"value2"}`)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			_, _ = cached.Execute(ctx, args1)
		} else {
			_, _ = cached.Execute(ctx, args2)
		}
	}
}
