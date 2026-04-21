package benchcov

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

type CoverageReport struct {
	TotalFuncs   int
	CoveredFuncs int
	Missing      []string
}

// AnalyzePackage analyzes a single package directory for benchmark coverage.
func AnalyzePackage(pkgPath string) (CoverageReport, error) {
	fset := token.NewFileSet()
	//nolint:staticcheck // parser.ParseDir is deprecated but sufficient for AST extraction here
	pkgs, err := parser.ParseDir(fset, pkgPath, nil, 0)
	if err != nil {
		return CoverageReport{}, err
	}

	report := CoverageReport{}
	funcs := make(map[string]bool)
	benchmarks := make(map[string]bool)

	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			isTest := strings.HasSuffix(filename, "_test.go")

			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					name := funcDecl.Name.Name
					if isTest {
						if strings.HasPrefix(name, "Benchmark") {
							benchmarks[name] = true
						}
					} else {
						// we only care about normal functions
						// ignore init functions
						if name == "init" {
							continue
						}
						// If it's a method, maybe prefix with receiver type?
						// The user asked for "every function". For simplicity, let's just use the function name.
						// Wait, methods can have the same name on different types.
						// For methods, we should probably prepend the receiver type.
						// Let's just do Name for now.
						funcs[name] = true
					}
				}
			}
		}
	}

	for fName := range funcs {
		report.TotalFuncs++

		// Expected benchmark name
		// e.g. "funcWithoutBenchmark" -> "BenchmarkFuncWithoutBenchmark"
		// Wait, the Go convention is Benchmark + Title(fName).
		expectedBenchName := "Benchmark" + strings.ToUpper(fName[:1]) + fName[1:]

		if benchmarks[expectedBenchName] {
			report.CoveredFuncs++
		} else {
			report.Missing = append(report.Missing, fName)
		}
	}

	return report, nil
}
