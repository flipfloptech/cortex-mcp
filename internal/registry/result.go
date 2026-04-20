package registry

import (
	"encoding/json"
	"time"
)

// ToolResult is the standardized output envelope for all tools.
// Every tool returns this format, ensuring consistent structure
// across the mesh regardless of the specific tool's data schema.
type ToolResult struct {
	// ToolName identifies which tool produced this result.
	ToolName string `json:"tool_name"`

	// NodeID identifies which node the result came from.
	// Set by the registry at invocation time, not by the tool itself.
	NodeID string `json:"node_id"`

	// Status is the outcome classification: "ok", "warning", "error", or "degraded".
	Status ResultStatus `json:"status"`

	// Summary is a one-line human-readable summary of the result.
	Summary string `json:"summary"`

	// Data contains the tool-specific structured output.
	// Each tool defines its own data schema, but it's always JSON.
	Data json.RawMessage `json:"data"`

	// Metadata contains execution metadata.
	Metadata ResultMetadata `json:"metadata"`
}

// ResultStatus classifies the outcome of a tool execution.
type ResultStatus string

const (
	StatusOK       ResultStatus = "ok"
	StatusWarning  ResultStatus = "warning"
	StatusError    ResultStatus = "error"
	StatusDegraded ResultStatus = "degraded"
)

// ResultMetadata provides execution context for a tool result.
type ResultMetadata struct {
	// ExecutionTimeMs is how long the tool took to execute.
	ExecutionTimeMs int64 `json:"execution_time_ms"`

	// Timestamp is when the result was produced (RFC3339).
	Timestamp string `json:"timestamp"`

	// FilteringMethod describes the analysis approach:
	//   "deterministic" — exact values, no estimation
	//   "heuristic"     — pattern matching, thresholds, estimation
	//   "none"          — raw data, no filtering applied
	FilteringMethod string `json:"filtering_method"`
}

// NewResult creates a ToolResult with standard metadata populated.
func NewResult(toolName, nodeID string, status ResultStatus, summary string, data interface{}) *ToolResult {
	raw, _ := json.Marshal(data)
	return &ToolResult{
		ToolName: toolName,
		NodeID:   nodeID,
		Status:   status,
		Summary:  summary,
		Data:     raw,
		Metadata: ResultMetadata{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
}

// NewErrorResult creates a ToolResult representing a tool-level error.
func NewErrorResult(toolName, nodeID, message string) *ToolResult {
	return NewResult(toolName, nodeID, StatusError, message, map[string]string{"error": message})
}
