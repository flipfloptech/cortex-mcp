package process

import (
	"context"
	"testing"
	"time"
)

func TestGetTree_Contract(t *testing.T) {
	// Simple contract test to ensure GetTree exists and compiles
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try to get tree for PID 1, which should always exist and have children
	tree, err := GetTree(ctx, 1)
	if err != nil {
		t.Fatalf("GetTree(1) failed: %v", err)
	}

	if tree == nil {
		t.Fatal("Expected non-nil tree for PID 1")
	}

	if tree.PID != 1 {
		t.Errorf("Expected root PID 1, got %d", tree.PID)
	}

	if len(tree.Children) == 0 {
		t.Error("Expected PID 1 to have children")
	}

	// Verify fields
	if tree.Name == "" {
		t.Error("Expected root process to have a name")
	}
}

func TestGetTree_InvalidPID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 9999999 is typically invalid for default linux pid_max
	tree, err := GetTree(ctx, 9999999)
	if err == nil {
		t.Error("Expected error for non-existent PID")
	}
	if tree != nil {
		t.Error("Expected nil tree for non-existent PID")
	}
}

func BenchmarkGetTree(b *testing.B) {
	ctx := context.Background()
	// Run once to warm up any caches/buffers
	_, _ = GetTree(ctx, 1)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = GetTree(ctx, 1)
	}
}
