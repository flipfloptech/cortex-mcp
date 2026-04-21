package benchtrack

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

type BenchmarkResult struct {
	Name        string  `json:"name"`
	Iterations  int     `json:"iterations"`
	NsPerOp     float64 `json:"ns_per_op"`
	BytesPerOp  int64   `json:"bytes_per_op"`
	AllocsPerOp int64   `json:"allocs_per_op"`
}

func ParseBenchmarkOutput(r io.Reader) ([]BenchmarkResult, error) {
	scanner := bufio.NewScanner(r)
	var results []BenchmarkResult

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// Example: BenchmarkBufPool_GetPut-32        	10000000	       125.4 ns/op	      24 B/op	       1 allocs/op

		nameWithProcs := fields[0]
		name := nameWithProcs
		if idx := strings.LastIndex(nameWithProcs, "-"); idx != -1 {
			name = nameWithProcs[:idx]
		}

		iters, _ := strconv.Atoi(fields[1])

		res := BenchmarkResult{
			Name:       name,
			Iterations: iters,
		}

		// Parse the rest of the fields
		for i := 2; i < len(fields)-1; i += 2 {
			valStr := fields[i]
			unit := fields[i+1]

			switch unit {
			case "ns/op":
				res.NsPerOp, _ = strconv.ParseFloat(valStr, 64)
			case "B/op":
				res.BytesPerOp, _ = strconv.ParseInt(valStr, 10, 64)
			case "allocs/op":
				res.AllocsPerOp, _ = strconv.ParseInt(valStr, 10, 64)
			}
		}

		results = append(results, res)
	}

	return results, scanner.Err()
}
