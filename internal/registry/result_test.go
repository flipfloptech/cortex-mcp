package registry_test

import (
	"encoding/json"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// TestNewResult_PopulatesFields verifies that NewResult sets all fields correctly.
func TestNewResult_PopulatesFields(t *testing.T) {
	t.Parallel()

	data := map[string]int{"cpus": 64}
	result := registry.NewResult("system_info", "oss1", registry.StatusOK, "64 CPUs detected", data)

	if result.ToolName != "system_info" {
		t.Errorf("ToolName = %q, want %q", result.ToolName, "system_info")
	}
	if result.NodeID != "oss1" {
		t.Errorf("NodeID = %q, want %q", result.NodeID, "oss1")
	}
	if result.Status != registry.StatusOK {
		t.Errorf("Status = %q, want %q", result.Status, registry.StatusOK)
	}
	if result.Summary != "64 CPUs detected" {
		t.Errorf("Summary = %q, want %q", result.Summary, "64 CPUs detected")
	}
	if result.Metadata.Timestamp == "" {
		t.Error("Timestamp must be populated")
	}

	// Verify Data is valid JSON containing our data.
	var parsed map[string]int
	if err := json.Unmarshal(result.Data, &parsed); err != nil {
		t.Fatalf("Data is not valid JSON: %v", err)
	}
	if parsed["cpus"] != 64 {
		t.Errorf("Data[cpus] = %d, want 64", parsed["cpus"])
	}
}

// TestNewErrorResult_SetsErrorStatus verifies error result construction.
func TestNewErrorResult_SetsErrorStatus(t *testing.T) {
	t.Parallel()

	result := registry.NewErrorResult("broken_tool", "node1", "connection refused")

	if result.Status != registry.StatusError {
		t.Errorf("Status = %q, want %q", result.Status, registry.StatusError)
	}
	if result.ToolName != "broken_tool" {
		t.Errorf("ToolName = %q, want %q", result.ToolName, "broken_tool")
	}
	if result.Summary != "connection refused" {
		t.Errorf("Summary = %q, want %q", result.Summary, "connection refused")
	}

	// Verify Data contains the error message.
	var parsed map[string]string
	if err := json.Unmarshal(result.Data, &parsed); err != nil {
		t.Fatalf("Data is not valid JSON: %v", err)
	}
	if parsed["error"] != "connection refused" {
		t.Errorf("Data[error] = %q, want %q", parsed["error"], "connection refused")
	}
}

// TestResultStatus_Constants verifies the status constant values.
func TestResultStatus_Constants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status registry.ResultStatus
		want   string
	}{
		{registry.StatusOK, "ok"},
		{registry.StatusWarning, "warning"},
		{registry.StatusError, "error"},
		{registry.StatusDegraded, "degraded"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.want {
			t.Errorf("Status constant %v = %q, want %q", tt.status, string(tt.status), tt.want)
		}
	}
}

// TestToolResult_JSONRoundtrip verifies that ToolResult serializes and
// deserializes cleanly via JSON.
func TestToolResult_JSONRoundtrip(t *testing.T) {
	t.Parallel()

	original := registry.NewResult("test_tool", "node-1", registry.StatusWarning, "disk at 90%", map[string]float64{"usage": 0.9})
	original.Metadata.ExecutionTimeMs = 42
	original.Metadata.FilteringMethod = "deterministic"

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded registry.ToolResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ToolName != original.ToolName {
		t.Errorf("ToolName = %q, want %q", decoded.ToolName, original.ToolName)
	}
	if decoded.Status != original.Status {
		t.Errorf("Status = %q, want %q", decoded.Status, original.Status)
	}
	if decoded.Metadata.ExecutionTimeMs != 42 {
		t.Errorf("ExecutionTimeMs = %d, want 42", decoded.Metadata.ExecutionTimeMs)
	}
	if decoded.Metadata.FilteringMethod != "deterministic" {
		t.Errorf("FilteringMethod = %q, want %q", decoded.Metadata.FilteringMethod, "deterministic")
	}
}
