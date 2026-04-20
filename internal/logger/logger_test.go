package logger

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestInitLogger_JSON(t *testing.T) {
	t.Parallel()
	log, err := InitLogger(Config{
		Level:    "debug",
		Encoding: "json",
	})
	if err != nil {
		t.Fatalf("InitLogger failed: %v", err)
	}
	if log == nil {
		t.Fatal("expected a configured zap.Logger, got nil")
	}
	if !log.Core().Enabled(zapcore.DebugLevel) {
		t.Error("expected debug level to be enabled")
	}
}

func TestInitLogger_Console(t *testing.T) {
	t.Parallel()
	log, err := InitLogger(Config{
		Level:    "info",
		Encoding: "console",
	})
	if err != nil {
		t.Fatalf("InitLogger failed: %v", err)
	}
	if log.Core().Enabled(zapcore.DebugLevel) {
		t.Error("expected debug level to be disabled for info level")
	}
	if !log.Core().Enabled(zapcore.InfoLevel) {
		t.Error("expected info level to be enabled")
	}
}

func TestInitLogger_InvalidLevel(t *testing.T) {
	t.Parallel()
	_, err := InitLogger(Config{
		Level:    "invalid",
		Encoding: "json",
	})
	if err == nil {
		t.Error("expected error for invalid log level, got nil")
	}
}

func TestContextLogger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Initial FromContext should return a non-nil no-op (or global) logger
	defaultLog := FromContext(ctx)
	if defaultLog == nil {
		t.Fatal("expected non-nil default logger from empty context")
	}

	// Create a test logger
	testLog := zap.NewNop().With(zap.String("test", "value"))
	ctx = WithContext(ctx, testLog)

	// Fetch it back
	fetched := FromContext(ctx)
	if fetched == nil {
		t.Fatal("expected non-nil fetched logger")
	}
}
