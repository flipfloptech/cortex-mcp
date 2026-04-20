package stream_test

import (
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry/stream"
)

// TestThresholdEmitter_Classification verifies severity classification.
func TestThresholdEmitter_Classification(t *testing.T) {
	t.Parallel()

	te := stream.NewThresholdEmitter(80.0, 95.0)

	tests := []struct {
		value float64
		want  stream.Severity
	}{
		{50.0, stream.SeverityOK},
		{79.9, stream.SeverityOK},
		{80.0, stream.SeverityWarning},
		{90.0, stream.SeverityWarning},
		{94.9, stream.SeverityWarning},
		{95.0, stream.SeverityCritical},
		{100.0, stream.SeverityCritical},
	}

	for _, tt := range tests {
		result := te.Evaluate(tt.value)
		if result.Severity != tt.want {
			t.Errorf("Evaluate(%v).Severity = %q, want %q", tt.value, result.Severity, tt.want)
		}
		if result.Value != tt.value {
			t.Errorf("Evaluate(%v).Value = %v, want %v", tt.value, result.Value, tt.value)
		}
	}
}

// TestThresholdEmitter_Stats verifies aggregate statistics tracking.
func TestThresholdEmitter_Stats(t *testing.T) {
	t.Parallel()

	te := stream.NewThresholdEmitter(50.0, 90.0)

	values := []float64{10, 20, 55, 60, 91, 95, 30}
	for _, v := range values {
		te.Evaluate(v)
	}

	stats := te.Stats()
	if stats.TotalSeen != 7 {
		t.Errorf("TotalSeen = %d, want 7", stats.TotalSeen)
	}
	if stats.TotalOK != 3 {
		t.Errorf("TotalOK = %d, want 3", stats.TotalOK)
	}
	if stats.TotalWarning != 2 {
		t.Errorf("TotalWarning = %d, want 2", stats.TotalWarning)
	}
	if stats.TotalCritical != 2 {
		t.Errorf("TotalCritical = %d, want 2", stats.TotalCritical)
	}
}

// TestThresholdEmitter_SwappedThresholds verifies that swapped thresholds
// are auto-corrected.
func TestThresholdEmitter_SwappedThresholds(t *testing.T) {
	t.Parallel()

	// Pass critical < warn — should auto-swap.
	te := stream.NewThresholdEmitter(95.0, 80.0)

	result := te.Evaluate(85.0)
	if result.Severity != stream.SeverityWarning {
		t.Errorf("Severity = %q, want %q (auto-swapped thresholds)", result.Severity, stream.SeverityWarning)
	}
}

// TestThresholdEmitter_ZeroValues verifies behavior with zero values.
func TestThresholdEmitter_ZeroValues(t *testing.T) {
	t.Parallel()

	te := stream.NewThresholdEmitter(0.0, 0.0)

	// Everything should be critical when thresholds are both 0.
	result := te.Evaluate(0.0)
	if result.Severity != stream.SeverityCritical {
		t.Errorf("Severity = %q, want %q", result.Severity, stream.SeverityCritical)
	}
}

// TestSeverity_Constants verifies severity constant values.
func TestSeverity_Constants(t *testing.T) {
	t.Parallel()

	if string(stream.SeverityOK) != "ok" {
		t.Errorf("SeverityOK = %q, want %q", stream.SeverityOK, "ok")
	}
	if string(stream.SeverityWarning) != "warning" {
		t.Errorf("SeverityWarning = %q, want %q", stream.SeverityWarning, "warning")
	}
	if string(stream.SeverityCritical) != "critical" {
		t.Errorf("SeverityCritical = %q, want %q", stream.SeverityCritical, "critical")
	}
}
