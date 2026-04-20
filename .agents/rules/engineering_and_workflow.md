---
trigger: always_on
description: Coding standards, security protocols, and development workflows for the cortex-mcp project.
---

# Engineering & Workflow

## Directives

- **Application Mindset**: This is the consumer application for `cortex-mesh`. Your focus is on clean CLI UX, solid daemon orchestration, and seamless MCP integration.
- **Test-Driven Development**: Tests are written FIRST. Code is written to make tests pass. No exceptions.
- **No Shortcuts**: Never bypass linters, tests, or quality gates via configuration suppression.

## Go Coding Standards

- **Version**: Go 1.25+ is the project standard (see `go.mod` for the exact minimum).
- **Use the Absolute Best Tools**: Always select the absolute best, most performant, and reliable tool for the job. For structured logging, use high-end frameworks like `go.uber.org/zap`.
- **Concurrency**: Respect `context.Context` cancellation everywhere.
- **Error Handling**: Wrap errors with context (`fmt.Errorf("config parse: %w", err)`).
- **Lint Compliance**: All code must pass `golangci-lint run ./...` with zero issues.

## Project Structure

```
cortex-mcp/
├── cmd/
│   └── cortex-mcp/   # Minimal main() wrapper calling into internal
├── internal/
│   ├── config/       # TOML configuration loading and validation
│   ├── mesh/         # Application lifecycle and `cortex-mesh` initialization
│   ├── mcp/          # Model Context Protocol server implementation
│   └── registry/     # Actual tools and handlers deployed across the fleet
```

## Development Workflow

- **Conventional Commits**: Use structured commit messages (e.g., `feat(registry): ...`, `fix(config): ...`).
- **Git Strategy**: Use `feat/` and `fix/` branches. Keep a clean, rebased history on `main`.
- **Pre-Commit Checks** (`task check`):
  1. `task fmt-check` — all files formatted.
  2. `task vet` — go vet clean.
  3. `task lint` — zero golangci-lint issues.
  4. `task test` — tests pass.

## Testing Standards

- **TDD Mandate**: Write tests BEFORE implementation. Define what correct behavior looks like, then code to satisfy it.
- **Integration Tests**: Focus heavily on integration between the CLI commands and the expected mesh states.
- **Coverage Target**: 90%+ line coverage across all packages. Coverage gaps must be justified.
