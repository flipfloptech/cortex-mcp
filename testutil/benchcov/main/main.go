package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortex-mesh/cortex-mesh/testutil/benchcov"
)

func main() {
	var missing []string
	totalFuncs := 0
	coveredFuncs := 0

	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}

		// skip vendor, testutil, proto, and hidden dirs
		if (strings.HasPrefix(info.Name(), ".") && info.Name() != ".") || info.Name() == "vendor" || info.Name() == "testdata" || info.Name() == "proto" || info.Name() == "testutil" {
			return filepath.SkipDir
		}

		report, err := benchcov.AnalyzePackage(path)
		if err != nil {
			// ignore errors (like no go files)
			return nil
		}

		totalFuncs += report.TotalFuncs
		coveredFuncs += report.CoveredFuncs
		for _, m := range report.Missing {
			missing = append(missing, fmt.Sprintf("%s:%s", path, m))
		}

		return nil
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error walking directory: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Total Functions: %d\n", totalFuncs)
	fmt.Printf("Covered Functions: %d\n", coveredFuncs)

	if len(missing) > 0 {
		fmt.Printf("\nMissing Benchmarks (%d):\n", len(missing))
		for _, m := range missing {
			fmt.Printf("  - %s\n", m)
		}
		os.Exit(1)
	}

	fmt.Println("100% Benchmark Coverage!")
}
