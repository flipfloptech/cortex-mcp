---
name: tool-development-workflow
description: Formal workflow for implementing new diagnostic tools in the Cortex MCP registry, combining the strict TDD process with the registry.Tool architectural contract.
---

# Cortex MCP Tool Development Workflow

This document outlines the strict protocol for creating new diagnostic tools for the Cortex MCP registry. It merges the project's `tdd-development-workflow` with the architectural requirements of `registry.Tool`.

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
- **Native-First Implementation Strategy**: Tools must prioritize native, low-overhead methods (such as standard library Go functions, direct `/sys/` or `/proc/` filesystem reads, syscalls, or direct API/socket calls) over executing external third-party binaries. Launching subprocesses incurs substantial CPU/memory overhead and security risks. Third-party binaries (like `ip`, `smartctl`, `nvme-cli`, etc.) should only be utilized as a **fallback method** when native APIs/sysfs are unavailable or incomplete.
- **Error Encapsulation**: Tools must not return hard Go errors from `Execute()` unless the error is catastrophic to the mesh fabric itself. Instead, execution errors (e.g., file unreadable, parsing failed) must be gracefully encapsulated into the JSON payload via `registry.NewErrorResult(t.Name(), hostname, err.Error())` and returned alongside a `nil` Go error. This prevents localized tool failures from bubbling up and severing the MCP connection or halting the routing plane.
- **Auto-Registration**: Every tool package must contain an `init()` function that calls `registry.Register(New())` so the tool is automatically discovered via a blank import in `internal/mesh/tools_register.go`.
- **LLM-Centric Formatting**: Raw math is difficult for LLMs. If a calculation is required (e.g., Idle Percentage, Human-Readable Durations), the Go code *must* perform it and include it in the `ToolResult.Data`.
- **Tool Composition (Decouple Logic from Transport)**: An MCP Tool is fundamentally just a JSON-RPC adapter. When building tools that rely on other tools (like parsing device mappings or log files), we must **never** have Tools calling other Tools via the registry interface. Passing JSON strings back and forth internally is slow and loses type safety.
  - **The Pattern**: Extract the core diagnostic logic into reusable Go packages (e.g., standard Go functions returning strongly-typed structs or maps). Make the MCP Tools thin wrappers that invoke these Go functions directly. This keeps the codebase highly testable and ensures that performance improvements in base libraries automatically propagate to all composed tools without JSON parsing overhead.

## The Categorization Protocol

When adding a new tool to the MCP server, run it through this ruleset. The first YES determines the tool's category.

### 1. Hardware
**The Rule**: Does the tool query the physical silicon, chassis sensors, firmware, or out-of-band management controllers (BMC), bypassing the Linux kernel's logical abstractions?
- **Inclusions**: IPMI, DMI/SMBIOS decoding, core temperature sensors, ECC memory corrections (EDAC/MCE), physical DIMM inventory.
- **Exclusions**: Logical block devices or NVMe I/O stats (route to Storage).

### 2. Fabric (Network, InfiniBand, Mellanox)
**The Rule**: Does the tool measure data moving between nodes, the state of the network interface controllers (NICs/HCAs), or the topology of the cluster interconnect?
- **Inclusions**: TCP/UDP sockets, IP routing, ethtool stats, InfiniBand link states, RoCE congestion counters, Subnet Manager logs, MTU sizes.
- **Exclusions**: Distributed filesystem mount capacities (route to Storage).

### 3. Storage (Includes Lustre/NFS)
**The Rule**: Does the tool measure the persistence, capacity, queue depths, or input/output operations (I/O) of data on disk or over a storage-specific protocol?
- **Inclusions**: Block device mapping (lsblk), local disk I/O (iostat equivalents), NVMe SMART data, filesystem capacity/inodes, Lustre/NFS client RPC states.
- **Exclusions**: Pure RAM disks or page caches (route to Memory).

### 4. Memory
**The Rule**: Does the tool measure the allocation, fragmentation, swapping, or exhaustion of volatile system RAM?
- **Inclusions**: Free/Used stats, page caches, NUMA node allocation hits/misses, kernel slab usage, buddy allocator fragmentation, OOM killer logs.
- **Exclusions**: Physical DIMM slot speeds or ECC errors (route to Hardware).

### 5. Compute
**The Rule**: Does the tool measure what code is executing, how the kernel scheduler is handling it, or the physical topology of the processors it runs on?
- **Inclusions**: Process lists, thread wait channels (wchan), CPU/NUMA cache topologies, CPU power governors, interrupt (IRQ) balancing, cgroup limits.
- **Exclusions**: Overall system load averages (route to System).

### 6. System (The Fallback / Global State)
**The Rule**: Does the tool provide macroscopic context about the operating system as a whole, rather than diving into a specific hardware subsystem?
- **Inclusions**: Uptime, 1/5/15-minute load averages, kernel boot parameters, systemd daemon states, global sysctl limits, OS versioning, generic journalctl/dmesg queries.
- **Exclusions**: Anything that isolates a specific resource (CPU/RAM/Disk/NIC).

## Phase 0: Branching

Always start from the latest `dev` branch. Tools are isolated features.

```bash
git checkout dev
git pull origin dev
git checkout -b feat/my-new-tool
```

## Phase 0.5: Planning & Secondary Review

1. **Create/Update Implementation Plan**: Create or update the `implementation_plan.md` artifact outlining the design, data sources, degradation profiles, parameters, and fallback commands.
2. **Clarify Ambiguities**: If there are any unknowns (e.g., precise file paths, data formats), formulate and ask clarifying questions.
3. **Secondary Review Step**: Present the plan to the user with `request_feedback = true` and wait for explicit user approval. Do NOT proceed to writing tests or code until the user approves the implementation plan. If changes are requested, update the plan and repeat this review step.

## Phase 1: Tool Definition & Tests (TDD)

Write tests *before* writing the implementation (Tests First, Code Second). Create `internal/registry/tools/mytool/mytool_test.go`.

### Required Test Coverage & Scope:
1. **Contract Compliance Test**: Asserts the tool implements `registry.Tool`, has the correct Name, Category, and Help (which *must* reference data sources).
2. **Execute Test**: Mocks necessary files or skips the execution if `IsSupported()` returns false, then validates the JSON schema (`res.Data` unmarshals properly) and checks for required logical constraints (e.g., numbers > 0, percentages between 0 and 100).
3. **Helper Tests**: Any complex parsing or mathematical calculations (like `formatDuration` or parsing sysfs trees) must have their own table-driven tests.
4. **Happy Paths & Error Paths**: Test normal operation, boundary conditions (empty inputs, zero values), and proper error handling.
5. **Concurrency & Race Safety**: Verify parallel test execution (`t.Parallel()`) to detect potential data races.
6. **Timeouts & Cancellation**: Verify that execution respects `context.Context` cancellation and deadline propagation.
7. **Mandatory Benchmarking**: EVERY function implemented must have a corresponding benchmark (`func BenchmarkXxx(b *testing.B)`). This is rigidly enforced by the AST `benchcov` validator during integration.

### Test Quality Checklist:
- [ ] Each test has a clear, descriptive name.
- [ ] Tests are independent — no shared mutable state.
- [ ] Table-driven tests for parameterized cases.
- [ ] `t.Parallel()` utilized where safe to expose races.
- [ ] No `time.Sleep` — use channels, contexts, or sync primitives.

**Commit the tests:**
`git commit -m "test(registry): define behavior for mytool"`

## Phase 2: Implementation

Create `internal/registry/tools/mytool/mytool.go`. Write the minimum code required to satisfy the tests.

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

### Implementation Checklist:
- [ ] All tests pass cleanly (`go test -race` clean).
- [ ] Error messages wrap context properly (`fmt.Errorf("operation: %w", err)`).
- [ ] Resources (files, sockets) are properly closed via `defer`.
- [ ] Speculative abstractions are avoided; code stays simple and direct.

**Commit the implementation:**
`git commit -m "feat(registry): implement mytool"`

## Phase 3: Registration & Refinement

1. Open `internal/mesh/tools_register.go`.
2. Add a blank import for your new package: `_ "github.com/flipfloptech/cortex-mcp/internal/registry/tools/mytool"`.
3. Test locally: `CGO_ENABLED=0 go test -race -v ./internal/registry/...`.

## Phase 4: Integration Verification

Execute the strict CI gates locally:

```bash
task check
task bench
```
*If `task fmt-check` fails, run `task fmt` and re-run `task check`.*
*The `task bench` command must be run to ensure all newly added benchmarks are executed and pass the AST `benchcov` requirement.*

### Verification Checklist:
- [ ] Full project compiles clean: `go build ./...`.
- [ ] All project tests pass: `go test -race ./...`.
- [ ] Code is formatted: `task fmt-check`.
- [ ] Linter is clean: `golangci-lint run ./...` returns zero issues.

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
git rebase dev          # if dev has diverged
git merge --ff-only feat/my-new-tool
git branch -d feat/my-new-tool
```
- **Rebase First**: Keep history linear; avoid merge commits.
- **Delete Branch**: Delete the local feature branch immediately after a successful merge.
