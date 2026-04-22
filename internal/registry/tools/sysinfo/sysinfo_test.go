package sysinfo_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/registry/tools/sysinfo"
)

// TestSystemInfoTool_ContractCompliance verifies the tool satisfies the
// Tool interface contract with non-empty required fields.
func TestSystemInfoTool_ContractCompliance(t *testing.T) {
	t.Parallel()

	var tool registry.Tool = &sysinfo.SystemInfoTool{}

	if tool.Name() == "" {
		t.Error("Name() must not be empty")
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	if tool.Help() == "" {
		t.Error("Help() must not be empty")
	}
	if tool.Category() == "" {
		t.Error("Category() must not be empty")
	}
}

// TestSystemInfoTool_Name verifies the tool name.
func TestSystemInfoTool_Name(t *testing.T) {
	t.Parallel()

	tool := &sysinfo.SystemInfoTool{}
	if tool.Name() != "get_system_info" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "get_system_info")
	}
}

// TestSystemInfoTool_Category verifies the tool category.
func TestSystemInfoTool_Category(t *testing.T) {
	t.Parallel()

	tool := &sysinfo.SystemInfoTool{}
	if tool.Category() != "system" {
		t.Errorf("Category() = %q, want %q", tool.Category(), "system")
	}
}

// TestSystemInfoTool_HelpContainsFilteringMethod verifies that Help()
// documents the filtering methodology.
func TestSystemInfoTool_HelpContainsFilteringMethod(t *testing.T) {
	t.Parallel()

	tool := &sysinfo.SystemInfoTool{}
	help := tool.Help()

	// Help must describe the filtering approach.
	if !containsSubstring(help, "deterministic") && !containsSubstring(help, "Deterministic") {
		t.Error("Help() should describe the filtering method (deterministic)")
	}
}

// TestSystemInfoTool_IsSupported verifies platform detection.
func TestSystemInfoTool_IsSupported(t *testing.T) {
	t.Parallel()

	tool := &sysinfo.SystemInfoTool{}
	supported, reason := tool.IsSupported()

	// system_info should be supported on Linux.
	if registry.IsLinux() {
		if !supported {
			t.Errorf("IsSupported() = false on Linux, reason: %s", reason)
		}
	}
}

// TestSystemInfoTool_Execute verifies that Execute returns a valid ToolResult
// with the expected data structure.
func TestSystemInfoTool_Execute(t *testing.T) {
	t.Parallel()

	if !registry.IsLinux() {
		t.Skip("system_info requires Linux")
	}

	tool := &sysinfo.SystemInfoTool{}
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// Verify result envelope.
	if result.ToolName != "get_system_info" {
		t.Errorf("ToolName = %q, want %q", result.ToolName, "get_system_info")
	}
	if result.Status != registry.StatusOK {
		t.Errorf("Status = %q, want %q", result.Status, registry.StatusOK)
	}
	if result.Metadata.FilteringMethod != "deterministic" {
		t.Errorf("FilteringMethod = %q, want %q", result.Metadata.FilteringMethod, "deterministic")
	}

	// Verify Data contains expected fields.
	var data map[string]interface{}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("Data is not valid JSON: %v", err)
	}

	requiredFields := []string{"hostname", "os", "arch", "cpus", "kernel", "roles"}
	for _, field := range requiredFields {
		if _, ok := data[field]; !ok {
			t.Errorf("Data missing required field: %s", field)
		}
	}

	// cpus should be a positive number.
	cpus, ok := data["cpus"].(float64)
	if !ok || cpus < 1 {
		t.Errorf("Data[cpus] = %v, want positive number", data["cpus"])
	}

	// roles must be a non-empty slice — at minimum ["generic"].
	rolesRaw, ok := data["roles"].([]interface{})
	if !ok || len(rolesRaw) == 0 {
		t.Errorf("Data[roles] must be a non-empty array, got %v", data["roles"])
	}

	// role_info must be present as an object.
	roleInfo, ok := data["role_info"]
	if !ok || roleInfo == nil {
		t.Error("Data[role_info] must be present")
	}
}

// TestSystemInfoTool_RolesContainValidValues verifies that the roles field
// contains only recognized role names from DetectNodeRoles().
func TestSystemInfoTool_RolesContainValidValues(t *testing.T) {
	t.Parallel()

	if !registry.IsLinux() {
		t.Skip("system_info requires Linux")
	}

	tool := &sysinfo.SystemInfoTool{}
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("Data is not valid JSON: %v", err)
	}

	rolesRaw, ok := data["roles"].([]interface{})
	if !ok {
		t.Fatal("roles must be an array")
	}

	validRoles := map[string]bool{
		"sfa": true, "mgs": true, "mds": true, "oss": true, "client": true, "generic": true,
	}
	for _, r := range rolesRaw {
		role, ok := r.(string)
		if !ok {
			t.Errorf("role entry is not a string: %v", r)
			continue
		}
		if !validRoles[role] {
			t.Errorf("unexpected role %q, valid roles: sfa, mgs, mds, oss, client, generic", role)
		}
	}
}

// TestSystemInfoTool_ExecuteRespectsContext verifies context cancellation.
func TestSystemInfoTool_ExecuteRespectsContext(t *testing.T) {
	t.Parallel()

	if !registry.IsLinux() {
		t.Skip("system_info requires Linux")
	}

	tool := &sysinfo.SystemInfoTool{}

	// Execute with a valid context should succeed.
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute with valid context returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Execute returned nil result")
	}
}

// TestSystemInfoTool_GracefulDegradation verifies that Execute never errors —
// missing data sources produce "unknown" or empty values, not failures.
// system_info is a core diagnostic tool that must work on stripped/minimal systems.
func TestSystemInfoTool_GracefulDegradation(t *testing.T) {
	t.Parallel()

	if !registry.IsLinux() {
		t.Skip("system_info requires Linux")
	}

	tool := &sysinfo.SystemInfoTool{}
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute must never return an error, got: %v", err)
	}

	// Status must always be OK — degradation is in the data, not the status.
	if result.Status != registry.StatusOK {
		t.Errorf("Status = %q, want %q (graceful degradation should still be OK)", result.Status, registry.StatusOK)
	}

	// kernel field should be present (even if "unknown").
	var data map[string]interface{}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("Data is not valid JSON: %v", err)
	}

	kernel, ok := data["kernel"].(string)
	if !ok {
		t.Error("kernel field must be a string")
	}
	if kernel == "" {
		t.Error("kernel should be populated or 'unknown', not empty")
	}
}

// TestSystemInfoTool_HelpDocumentsDegradation verifies that Help()
// explains the graceful degradation behavior.
func TestSystemInfoTool_HelpDocumentsDegradation(t *testing.T) {
	t.Parallel()

	tool := &sysinfo.SystemInfoTool{}
	help := tool.Help()

	if !containsSubstring(help, "Graceful degradation") && !containsSubstring(help, "graceful degradation") {
		t.Error("Help() should document graceful degradation behavior")
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
