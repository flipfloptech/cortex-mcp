package process

import (
	"context"
	"regexp"
	"testing"
	"time"
)

// We will mock the file system in actual implementation to test calculation.
// For now, testing basic structure.

func TestGetList_Filters(t *testing.T) {
	// These tests will fail if not implemented.
	opts := FilterOptions{
		Limit:          10,
		SortBy:         "cpu",
		SampleDuration: 10 * time.Millisecond,
		UserRegex:      regexp.MustCompile("^root$"),
	}

	_, err := GetList(context.Background(), opts)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func BenchmarkGetList(b *testing.B) {
	opts := FilterOptions{
		Limit:          10,
		SortBy:         "cpu",
		SampleDuration: 1 * time.Millisecond,
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetList(ctx, opts)
	}
}
