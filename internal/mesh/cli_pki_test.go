package mesh

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPKICLI_GenerateAndShow(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	var out bytes.Buffer

	// 1. Generate Mesh CA
	err := runPKIGenerate(&out, false)
	if err != nil {
		t.Fatalf("runPKIGenerate failed: %v", err)
	}

	if !strings.Contains(out.String(), "Mesh CA successfully generated") {
		t.Errorf("unexpected output from generate: %s", out.String())
	}

	// 2. Generate again without --force should fail
	out.Reset()
	err = runPKIGenerate(&out, false)
	if err == nil {
		t.Fatalf("expected error generating without --force, got nil")
	}

	// 3. Show Mesh CA
	out.Reset()
	err = runPKIShow(&out)
	if err != nil {
		t.Fatalf("runPKIShow failed: %v", err)
	}

	if !strings.Contains(out.String(), "Mesh CA Status: Active") {
		t.Errorf("unexpected output from show: %s", out.String())
	}
}

func TestPKICLI_Issue(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Issue before CA exists should fail
	var out bytes.Buffer
	err := runPKIIssue(&out, "test-node", "")
	if err == nil {
		t.Fatalf("expected error issuing without CA, got nil")
	}

	// Generate CA
	if err := runPKIGenerate(&out, true); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// Issue cert to stdout
	out.Reset()
	err = runPKIIssue(&out, "test-node", "")
	if err != nil {
		t.Fatalf("runPKIIssue failed: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, `"node_id": "test-node"`) || !strings.Contains(outStr, "cert_pem") {
		t.Errorf("unexpected output from issue: %s", outStr)
	}

	// Issue cert to file
	outFile := filepath.Join(tmpHome, "test_node_identity.json")
	err = runPKIIssue(&out, "test-node-file", outFile)
	if err != nil {
		t.Fatalf("runPKIIssue to file failed: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read issued file failed: %v", err)
	}

	if !strings.Contains(string(data), `"node_id": "test-node-file"`) {
		t.Errorf("unexpected content in issued file: %s", string(data))
	}
}

func BenchmarkRunPKIShow(b *testing.B) {
	tmpHome := b.TempDir()
	b.Setenv("HOME", tmpHome)
	var out bytes.Buffer
	_ = runPKIGenerate(&out, false)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		_ = runPKIShow(&out)
	}
}

func BenchmarkRunPKIGenerate(b *testing.B) {
	tmpHome := b.TempDir()
	b.Setenv("HOME", tmpHome)
	var out bytes.Buffer

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		_ = runPKIGenerate(&out, true)
	}
}

func BenchmarkRunPKIIssue(b *testing.B) {
	tmpHome := b.TempDir()
	b.Setenv("HOME", tmpHome)
	var out bytes.Buffer
	_ = runPKIGenerate(&out, false)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		_ = runPKIIssue(&out, "bench-node", "")
	}
}

func BenchmarkNewPKICmd(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = newPKICmd()
	}
}
