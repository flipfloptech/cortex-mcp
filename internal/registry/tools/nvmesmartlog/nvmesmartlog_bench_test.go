package nvmesmartlog

import (
	"context"
	"testing"
)

func BenchmarkExecute(b *testing.B) {
	tool := New()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, []byte(`{}`))
	}
}
