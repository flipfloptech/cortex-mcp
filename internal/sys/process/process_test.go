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

func TestParseUintBytes(t *testing.T) {
	tests := []struct {
		input string
		want  uint64
	}{
		{"12345", 12345},
		{"0", 0},
		{"  42\n", 42}, // Handles leading/trailing non-digits implicitly in our loop
		{"9999999999", 9999999999},
		{"", 0},
		{"abc", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseUintBytes([]byte(tt.input))
			if got != tt.want {
				t.Errorf("parseUintBytes(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseState(t *testing.T) {
	tests := []struct {
		input []byte
		want  string
	}{
		{[]byte("S (sleeping)"), "S"},
		{[]byte("R (running)"), "R"},
		{[]byte("Z (zombie)"), "Z"},
		{[]byte("I (idle)"), "I"},
		{[]byte("Unknown"), "Unknown"},
		{[]byte("S"), "S"},
		{[]byte("R"), "R"},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			got := parseState(tt.input)
			if got != tt.want {
				t.Errorf("parseState(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
