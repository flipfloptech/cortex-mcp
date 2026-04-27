package tools

import (
	"encoding/json"
	"testing"
)

// --- ToolDefinition ---

func TestToolDefinition_Fields(t *testing.T) {
	t.Parallel()

	def := ToolDefinition{
		Name:            "lustre_health",
		Description:     "Check Lustre filesystem health",
		LongDescription: "Detailed check of Lustre filesystem...",
		Category:        "lustre",
		Parameters: []ToolParam{
			{Name: "verbose", Type: "boolean", Description: "Enable verbose output", Required: false, Default: "false"},
			{Name: "target", Type: "string", Description: "Target filesystem", Required: true},
		},
	}

	if def.Name != "lustre_health" {
		t.Fatal("Name mismatch")
	}
	if def.Category != "lustre" {
		t.Fatal("Category mismatch")
	}
	if len(def.Parameters) != 2 {
		t.Fatalf("expected 2 params, got %d", len(def.Parameters))
	}

}

// --- ToolDefinition JSON Schema auto-generation ---

func TestToolDefinition_GenerateSchema(t *testing.T) {
	t.Parallel()

	def := ToolDefinition{
		Name: "test_tool",
		Parameters: []ToolParam{
			{Name: "verbose", Type: "boolean", Description: "Enable verbose output", Required: false, Default: "false"},
			{Name: "target", Type: "string", Description: "Target filesystem", Required: true},
			{Name: "count", Type: "integer", Description: "Number of items", Required: false},
		},
	}

	schema := def.GenerateSchema()

	// Should be valid JSON.
	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("GenerateSchema produced invalid JSON: %v", err)
	}

	// Should be type "object".
	if parsed["type"] != "object" {
		t.Fatalf("schema type = %v, want 'object'", parsed["type"])
	}

	// Properties should contain all params.
	props, ok := parsed["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing 'properties'")
	}
	if _, ok := props["verbose"]; !ok {
		t.Fatal("missing 'verbose' property")
	}
	if _, ok := props["target"]; !ok {
		t.Fatal("missing 'target' property")
	}

	// Required should contain only required params.
	required, ok := parsed["required"].([]interface{})
	if !ok {
		t.Fatal("schema missing 'required'")
	}
	if len(required) != 1 || required[0] != "target" {
		t.Fatalf("required = %v, want [target]", required)
	}
}

func TestToolDefinition_GenerateSchema_NoParams(t *testing.T) {
	t.Parallel()

	def := ToolDefinition{Name: "simple_tool"}
	schema := def.GenerateSchema()

	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("GenerateSchema produced invalid JSON: %v", err)
	}
	if parsed["type"] != "object" {
		t.Fatalf("schema type = %v, want 'object'", parsed["type"])
	}
}

func TestToolDefinition_InputSchema_Override(t *testing.T) {
	t.Parallel()

	custom := json.RawMessage(`{"type": "object", "custom": true}`)
	def := ToolDefinition{
		Name:        "custom_tool",
		InputSchema: custom,
	}

	// When InputSchema is set, it should be used as-is.
	schema := def.EffectiveSchema()
	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid schema: %v", err)
	}
	if parsed["custom"] != true {
		t.Fatal("custom schema should be used when InputSchema is set")
	}
}

// --- ToolResult ---

func TestToolResult_Success(t *testing.T) {
	t.Parallel()

	result := &ToolResult{
		Content: json.RawMessage(`{"status": "ok"}`),
		IsError: false,
	}

	if result.IsError {
		t.Fatal("result should not be error")
	}

	var content map[string]interface{}
	if err := json.Unmarshal(result.Content, &content); err != nil {
		t.Fatalf("invalid content: %v", err)
	}
	if content["status"] != "ok" {
		t.Fatalf("status = %v, want 'ok'", content["status"])
	}
}

func TestToolResult_Error(t *testing.T) {
	t.Parallel()

	result := NewErrorResult("something went wrong")
	if !result.IsError {
		t.Fatal("error result should have IsError=true")
	}

	var content map[string]interface{}
	if err := json.Unmarshal(result.Content, &content); err != nil {
		t.Fatalf("invalid content: %v", err)
	}
	if content["error"] != "something went wrong" {
		t.Fatalf("error message mismatch: %v", content["error"])
	}
}

func TestToolResult_JSON(t *testing.T) {
	t.Parallel()

	result := NewJSONResult(map[string]string{"key": "value"})
	if result.IsError {
		t.Fatal("JSON result should not be error")
	}

	var content map[string]interface{}
	if err := json.Unmarshal(result.Content, &content); err != nil {
		t.Fatalf("invalid content: %v", err)
	}
	if content["key"] != "value" {
		t.Fatalf("content mismatch: %v", content)
	}
}

func TestToolResult_TextContent(t *testing.T) {
	t.Parallel()

	result := NewTextResult("hello world")
	if result.IsError {
		t.Fatal("text result should not be error")
	}

	var content map[string]interface{}
	if err := json.Unmarshal(result.Content, &content); err != nil {
		t.Fatalf("invalid content: %v", err)
	}
	if content["text"] != "hello world" {
		t.Fatalf("text mismatch: %v", content)
	}
}

// --- ToolInfo ---

func TestToolInfo_SortedByImpedance(t *testing.T) {
	t.Parallel()

	info := ToolInfo{
		Definition: ToolDefinition{Name: "test_tool"},
		Nodes: []AgentInfo{
			{NodeID: "node-heavy", Impedance: 80.0},
			{NodeID: "node-idle", Impedance: 1.0},
			{NodeID: "node-mid", Impedance: 30.0},
		},
	}

	info.SortNodesByImpedance()

	if info.Nodes[0].NodeID != "node-idle" {
		t.Fatalf("first node should be lowest impedance, got %q", info.Nodes[0].NodeID)
	}
	if info.Nodes[2].NodeID != "node-heavy" {
		t.Fatalf("last node should be highest impedance, got %q", info.Nodes[2].NodeID)
	}
}

// --- NodeResult ---

func TestNodeResult_Fields(t *testing.T) {
	t.Parallel()

	result := NodeResult{
		NodeID: "node-1",
		Result: &ToolResult{
			Content: json.RawMessage(`{"ok": true}`),
			IsError: false,
		},
		Error: nil,
	}

	if result.NodeID != "node-1" {
		t.Fatal("NodeID mismatch")
	}
	if result.Error != nil {
		t.Fatal("Error should be nil")
	}
	if result.Result.IsError {
		t.Fatal("Result should not be error")
	}
}
