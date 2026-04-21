package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cortex-mesh/cortex-mesh/testutil/benchtrack"
)

func main() {
	// Reads from os.Stdin
	results, err := benchtrack.ParseBenchmarkOutput(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing benchmark output: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No benchmarks found in input.")
		os.Exit(0)
	}

	// Create .benchmarks directory if it doesn't exist
	outDir := ".benchmarks"
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
		os.Exit(1)
	}

	// Output filename: .benchmarks/YYYYMMDD-HHMMSS.json
	timestamp := time.Now().Format("20060102-150405")
	outPath := filepath.Join(outDir, fmt.Sprintf("%s-benchmarks.json", timestamp))

	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(outPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully recorded %d benchmarks to %s\n", len(results), outPath)
}
