package buddyinfo

import (
	"context"
	"encoding/json"
	"testing"
)

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Category()
	}
}

func BenchmarkHidden(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Hidden()
	}
}

func BenchmarkParameters(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Parameters()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	t := New()
	supported, _ := t.IsSupported()
	if !supported {
		b.Skip("Tool not supported on this environment")
	}

	ctx := context.Background()
	args := json.RawMessage(`{}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.Execute(ctx, args)
	}
}
