package numaedacerrors

import (
	"context"
	"encoding/json"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/hardware"
)

func init() {
	registry.Register(New())
}

// Tool implements the registry.Tool interface for getting NUMA EDAC errors.
type Tool struct{}

// New returns a new instance of the Tool.
func New() *Tool {
	return &Tool{}
}

// Name returns the unique identifier for this tool in the registry.
func (t *Tool) Name() string {
	return "get_numa_edac_errors"
}

// Description provides a brief, one-line summary of the tool.
func (t *Tool) Description() string {
	return "Physical health audit of system RAM via EDAC ECC error counts."
}

// Help provides a detailed explanation of the tool.
func (t *Tool) Help() string {
	return `Extracts Correctable and Uncorrectable memory error counts from the EDAC subsystem.
Maps errors to specific memory controllers and physical DIMMs.
Useful for identifying failing RAM sticks before they cause a system crash.`
}

// Category returns the functional group for this tool.
func (t *Tool) Category() string {
	return "hardware"
}

// Parameters returns the expected arguments schema (empty for this tool).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden indicates whether the tool should be excluded from automatic LLM context.
func (t *Tool) Hidden() bool {
	return false
}

// IsSupported determines if the base OS supports EDAC scanning.
// We always return true on Linux, and use graceful degradation if the EDAC driver is missing.
func (t *Tool) IsSupported() (bool, string) {
	return true, ""
}

// Execute performs the diagnostic routine and encapsulates the output.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	// 1. Gather Data
	info, err := hardware.GetEDACErrorsInfo()
	if err != nil {
		// Encapsulate execution errors instead of blowing up the MCP stream
		return registry.NewErrorResult(t.Name(), "localhost", err.Error()), nil
	}

	// 2. Set Status based on the hardware health logic
	var status registry.ResultStatus
	switch info.SystemSummary.HealthStatus {
	case "degraded":
		status = registry.StatusDegraded
	case "warning":
		status = registry.StatusWarning
	case "critical":
		status = registry.StatusError
	default:
		status = registry.StatusOK
	}

	// 3. Serialize Data
	dataBytes, err := json.Marshal(info)
	if err != nil {
		return registry.NewErrorResult(t.Name(), "localhost", "failed to marshal response: "+err.Error()), nil
	}
	var data map[string]interface{}
	_ = json.Unmarshal(dataBytes, &data)

	// 4. Determine Summary Message
	summary := "EDAC error counts successfully queried."
	switch status {
	case registry.StatusDegraded:
		summary = "EDAC drivers not loaded; hardware ECC monitoring unavailable."
	case registry.StatusWarning:
		summary = "System RAM is generating correctable ECC errors."
	case registry.StatusError:
		summary = "System RAM has uncorrectable ECC errors. High risk of crash."
	}

	return registry.NewResult(t.Name(), "localhost", status, summary, data), nil
}
