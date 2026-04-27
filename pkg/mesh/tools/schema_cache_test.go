package tools

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"
)

// --- EffectiveSchema caching behavior ---

// TestEffectiveSchema_CachesGeneratedSchema verifies that EffectiveSchema
// generates the schema once and returns the cached result on subsequent calls.
func TestEffectiveSchema_CachesGeneratedSchema(t *testing.T) {
	t.Parallel()

	def := &ToolDefinition{
		Name: "cached_tool",
		Parameters: []ToolParam{
			{Name: "verbose", Type: "boolean", Description: "Verbose output", Required: false},
			{Name: "target", Type: "string", Description: "Target host", Required: true},
		},
	}

	// First call — generates and caches.
	s1 := def.EffectiveSchema()
	if s1 == nil {
		t.Fatal("first EffectiveSchema returned nil")
	}

	// Second call — should return cached (same bytes).
	s2 := def.EffectiveSchema()
	if s2 == nil {
		t.Fatal("second EffectiveSchema returned nil")
	}

	// Must be byte-identical.
	if !bytes.Equal(s1, s2) {
		t.Fatalf("cached schema differs:\n  s1: %s\n  s2: %s", s1, s2)
	}
}

// TestEffectiveSchema_ReturnsExplicitInputSchema verifies that when
// InputSchema is pre-set, EffectiveSchema returns it without generating.
func TestEffectiveSchema_ReturnsExplicitInputSchema(t *testing.T) {
	t.Parallel()

	custom := json.RawMessage(`{"type":"object","custom":true}`)
	def := &ToolDefinition{
		Name:        "custom_tool",
		InputSchema: custom,
		Parameters: []ToolParam{
			{Name: "ignored", Type: "string", Description: "Should not appear"},
		},
	}

	schema := def.EffectiveSchema()
	if !bytes.Equal(schema, custom) {
		t.Fatalf("schema = %s, want %s", schema, custom)
	}
}

// TestEffectiveSchema_NoParams_ReturnsEmptyObject verifies that a tool
// with no parameters and no InputSchema returns a valid empty schema.
func TestEffectiveSchema_NoParams_ReturnsEmptyObject(t *testing.T) {
	t.Parallel()

	def := &ToolDefinition{Name: "bare_tool"}

	schema := def.EffectiveSchema()
	if schema == nil {
		t.Fatal("EffectiveSchema returned nil for bare tool")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["type"] != "object" {
		t.Fatalf("type = %v, want 'object'", parsed["type"])
	}
}

// TestEffectiveSchema_EmptyParams_ReturnsEmptyObject verifies that a tool
// with an empty Parameters slice produces a valid schema with no properties.
func TestEffectiveSchema_EmptyParams_ReturnsEmptyObject(t *testing.T) {
	t.Parallel()

	def := &ToolDefinition{
		Name:       "empty_params_tool",
		Parameters: []ToolParam{},
	}

	schema := def.EffectiveSchema()

	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["type"] != "object" {
		t.Fatalf("type = %v, want 'object'", parsed["type"])
	}
	// Should not have properties key.
	if _, ok := parsed["properties"]; ok {
		t.Fatal("empty params should not produce properties")
	}
}

// TestEffectiveSchema_ConcurrentAccess verifies that multiple goroutines
// can call EffectiveSchema concurrently without races or corruption.
func TestEffectiveSchema_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	def := &ToolDefinition{
		Name: "concurrent_tool",
		Parameters: []ToolParam{
			{Name: "target", Type: "string", Description: "Target", Required: true},
			{Name: "count", Type: "integer", Description: "Count", Required: false},
		},
	}

	const goroutines = 100
	results := make([]json.RawMessage, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = def.EffectiveSchema()
		}(i)
	}
	wg.Wait()

	// All results must be identical.
	for i := 1; i < goroutines; i++ {
		if !bytes.Equal(results[0], results[i]) {
			t.Fatalf("goroutine %d got different schema:\n  [0]: %s\n  [%d]: %s",
				i, results[0], i, results[i])
		}
	}
}

// TestEffectiveSchema_GeneratedSchemaIsValid verifies the cached schema
// contains correct properties and required fields.
func TestEffectiveSchema_GeneratedSchemaIsValid(t *testing.T) {
	t.Parallel()

	def := &ToolDefinition{
		Name: "validated_tool",
		Parameters: []ToolParam{
			{Name: "host", Type: "string", Description: "Target host", Required: true},
			{Name: "port", Type: "integer", Description: "Port number", Required: false, Default: "22"},
		},
	}

	schema := def.EffectiveSchema()

	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify structure.
	if parsed["type"] != "object" {
		t.Fatalf("type = %v, want 'object'", parsed["type"])
	}

	props, ok := parsed["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("missing properties")
	}
	if _, ok := props["host"]; !ok {
		t.Fatal("missing 'host' property")
	}
	if _, ok := props["port"]; !ok {
		t.Fatal("missing 'port' property")
	}

	// Only "host" is required.
	required, ok := parsed["required"].([]interface{})
	if !ok {
		t.Fatal("missing required array")
	}
	if len(required) != 1 || required[0] != "host" {
		t.Fatalf("required = %v, want [host]", required)
	}
}

// --- Benchmark ---

// BenchmarkEffectiveSchema_Cached measures the cost of EffectiveSchema
// when the schema is already cached.
func BenchmarkEffectiveSchema(b *testing.B) {
	def := &ToolDefinition{
		Name: "bench_tool",
		Parameters: []ToolParam{
			{Name: "host", Type: "string", Description: "Host", Required: true},
			{Name: "port", Type: "integer", Description: "Port", Required: false},
			{Name: "verbose", Type: "boolean", Description: "Verbose", Required: false},
			{Name: "timeout", Type: "number", Description: "Timeout", Required: false},
		},
	}

	// Prime the cache.
	_ = def.EffectiveSchema()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = def.EffectiveSchema()
	}
}

// BenchmarkEffectiveSchema_Uncached measures the cost without caching
// (calling GenerateSchema directly).
func BenchmarkEffectiveSchema_Uncached(b *testing.B) {
	def := &ToolDefinition{
		Name: "bench_tool",
		Parameters: []ToolParam{
			{Name: "host", Type: "string", Description: "Host", Required: true},
			{Name: "port", Type: "integer", Description: "Port", Required: false},
			{Name: "verbose", Type: "boolean", Description: "Verbose", Required: false},
			{Name: "timeout", Type: "number", Description: "Timeout", Required: false},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = def.GenerateSchema()
	}
}
