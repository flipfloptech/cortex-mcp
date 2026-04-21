package benchcov

import (
	"path/filepath"
	"testing"
)

func TestAnalyzePackage(t *testing.T) {
	// Our testdata/pkg1 has:
	// - ExportedFunc1
	// - unexportedFunc2
	// - funcWithoutBenchmark
	// And tests for the first two.

	pkgPath := filepath.Join("testdata", "pkg1")
	report, err := AnalyzePackage(pkgPath)
	if err != nil {
		t.Fatalf("AnalyzePackage failed: %v", err)
	}

	if report.TotalFuncs != 3 {
		t.Errorf("expected 3 total funcs, got %d", report.TotalFuncs)
	}

	if report.CoveredFuncs != 2 {
		t.Errorf("expected 2 covered funcs, got %d", report.CoveredFuncs)
	}

	if len(report.Missing) != 1 || report.Missing[0] != "funcWithoutBenchmark" {
		t.Errorf("expected missing to be [funcWithoutBenchmark], got %v", report.Missing)
	}
}
