package logger

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

func BenchmarkInitLogger(b *testing.B) {
	cfg := Config{
		Level:    "info",
		Encoding: "json",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = InitLogger(cfg)
	}
}

func BenchmarkWithContext(b *testing.B) {
	ctx := context.Background()
	logger := zap.NewNop()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = WithContext(ctx, logger)
	}
}

func BenchmarkFromContext(b *testing.B) {
	logger := zap.NewNop()
	ctx := WithContext(context.Background(), logger)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = FromContext(ctx)
	}
}
