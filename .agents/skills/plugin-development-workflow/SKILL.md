---
name: plugin-development-workflow
description: Formal workflow for implementing new diagnostic tools (plugins) in the Cortex MCP registry, combining the strict TDD process with the registry.Tool architectural contract.
---

# Cortex MCP Plugin Development Workflow

This document outlines the strict protocol for creating new diagnostic tools (plugins) for the Cortex MCP registry. It merges the project's `tdd-development-workflow` with the architectural requirements of `registry.Tool`.

## Core Architectural Contract

Every tool in Cortex MCP must implement the `registry.Tool` interface:

```go
type Tool interface {
	Name() string
	Description() string
	Help() string
	Category() string
	Parameters() []ToolParam
	IsSupported() (bool, string)
	Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error)
}
```

- **Graceful Degradation**: Tools must not return `IsSupported() = false` or throw errors simply because non-critical telemetry is missing. If a core dependency (e.g., a required binary or base sysfs path) is missing, `IsSupported()` returns false. If secondary data is missing, the tool returns partial JSON output.
- **Auto-Registration**: Every tool package must contain an `init()` function that calls `registry.Register(New())` so the tool is automatically discovered via a blank import in `internal/mesh/tools_register.go`.
- **LLM-Centric Formatting**: Raw math is difficult for LLMs. If a calculation is required (e.g., Idle Percentage, Human-Readable Durations), the Go code *must* perform it and include it in the `ToolResult.Data`.
- **Composition & Reusability**: Tools should act as thin, strongly-typed JSON-RPC wrappers around reusable core business logic. If a tool parses a file or extracts data (e.g., extracting device mappings), that logic must be exposed as public Go functions so other tools can import and use it natively, avoiding expensive cross-tool RPC calls or duplication of effort.

## Phase 0: Branching

Always start from the latest `dev` branch. Tools are isolated features.

```bash
git checkout dev
git pull origin dev
git checkout -b feat/my-new-tool
```

## Phase 1: Tool Definition & Tests (TDD)

Write tests *before* writing the implementation. Create `internal/registry/tools/mytool/mytool_test.go`.

### Required Test Coverage:
1. **Contract Compliance Test**: Asserts the tool implements `registry.Tool`, has the correct Name, Category, and Help (which *must* reference data sources).
2. **Execute Test**: Mocks necessary files or skips the execution if `IsSupported()` returns false, then validates the JSON schema (`res.Data` unmarshals properly) and checks for required logical constraints (e.g., numbers > 0, percentages between 0 and 100).
3. **Helper Tests**: Any complex parsing or mathematical calculations (like `formatDuration` or parsing sysfs trees) must have their own table-driven tests.
4. **Mandatory Benchmarking**: EVERY function implemented must have a corresponding benchmark (`func BenchmarkXxx(b *testing.B)`). This is rigidly enforced by the AST `benchcov` validator during integration.

**Commit the tests:**
`git commit -m "test(registry): define behavior for mytool"`

## Phase 2: Implementation

Create `internal/registry/tools/mytool/mytool.go`.

1. Implement the `registry.Tool` methods.
2. Ensure `Parameters()` accurately maps to any input the tool expects (or returns `nil` for diagnostic tools with no parameters).
3. Use `registry.NewResult` and `registry.NewErrorResult` correctly.
4. Perform calculations and human-readable string formatting locally.
5. Create the `init()` block for registration:
   ```go
   func init() {
       registry.Register(New())
   }
   ```

## Phase 3: Registration & Refinement

1. Open `internal/mesh/tools_register.go`.
2. Add a blank import for your new package: `_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/mytool"`.
3. Test locally: `CGO_ENABLED=0 go test -race -v ./internal/registry/...`.

**Commit the implementation:**
`git commit -m "feat(registry): implement mytool"`

## Phase 4: Integration Verification

Execute the strict CI gates locally:

```bash
task check
task bench
```
*If `task fmt-check` fails, run `task fmt` and re-run `task check`.*
*The `task bench` command must be run to ensure all newly added benchmarks are executed and pass the AST `benchcov` requirement.*

## Phase 5: Documentation

1. Open `TOOL_CATALOG.md`.
2. Append the new tool under the appropriate category.
3. Clearly define:
   - **Data Sources**: The exact sysfs paths, `/proc` files, or binaries invoked.
   - **Mathematical Models**: Any pre-processing or ratios calculated.
   - **Degradation Profile**: When it fails vs when it returns partial truth.

**Commit the documentation:**
`git commit -m "docs(registry): add mytool to tool catalog"`

## Phase 6: Merge & Cleanup

Always merge via fast-forward and clean up the branch to maintain a linear history.

```bash
git checkout dev
git merge --ff-only feat/my-new-tool
git branch -d feat/my-new-tool
```
