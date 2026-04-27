package benchtrack

import (
	"bytes"
	"testing"
)

func TestParseBenchmarkOutput(t *testing.T) {
	input := `
goos: linux
goarch: amd64
pkg: github.com/flipfloptech/cortex-mcp/pkg/mesh/routing
cpu: AMD Ryzen 9 5950X 16-Core Processor
BenchmarkBufPool_GetPut-32        	10000000	       125.4 ns/op	      24 B/op	       1 allocs/op
BenchmarkBufPool_GetPut_Contended-32    10000000	       234.5 ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/flipfloptech/cortex-mcp/pkg/mesh/routing	3.141s
`
	results, err := ParseBenchmarkOutput(bytes.NewReader([]byte(input)))
	if err != nil {
		t.Fatalf("ParseBenchmarkOutput failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 benchmark results, got %d", len(results))
	}

	if results[0].Name != "BenchmarkBufPool_GetPut" {
		t.Errorf("expected BenchmarkBufPool_GetPut, got %s", results[0].Name)
	}
	if results[0].NsPerOp != 125.4 {
		t.Errorf("expected 125.4 ns/op, got %f", results[0].NsPerOp)
	}
	if results[0].AllocsPerOp != 1 {
		t.Errorf("expected 1 allocs/op, got %d", results[0].AllocsPerOp)
	}

	if results[1].Name != "BenchmarkBufPool_GetPut_Contended" {
		t.Errorf("expected BenchmarkBufPool_GetPut_Contended, got %s", results[1].Name)
	}
	if results[1].BytesPerOp != 0 {
		t.Errorf("expected 0 B/op, got %d", results[1].BytesPerOp)
	}
}
