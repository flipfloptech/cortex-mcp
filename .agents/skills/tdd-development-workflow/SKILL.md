---
name: tdd-development-workflow
description: Test-Driven Development workflow for cortex-mesh. Tests are written first to define correct behavior, then code is written to satisfy them. Every logical unit of work is committed. Edge cases are not optional.
---

# TDD Development Workflow

A disciplined, test-first development workflow for building industrial-grade systems. Tests define the contract. Code fulfills it.

## Core Principle: Tests First, Code Second

Every feature, bugfix, or refactor follows the same cycle:

1. **Define** — What should the correct behavior be?
2. **Test** — Write tests that assert that behavior (they will fail).
3. **Implement** — Write the minimum code to make the tests pass.
4. **Refine** — Refactor for clarity and performance while tests stay green.
5. **Commit** — Each logical step gets its own git commit.

## Phase 0: Branch from `dev`

All work happens on feature/fix branches off `dev`. Never commit directly to `dev` or `main`.

```bash
git checkout dev
git pull origin dev
git checkout -b feat/short-description   # or fix/short-description
```

- **Branch naming**: `feat/`, `fix/`, `refactor/`, `perf/`, `docs/` prefixes matching conventional commit types.
- **One branch per logical unit of work**: a feature, a bugfix, or a hardening pass.

## Phase 1: Understand & Plan

Before touching any code:

- State the goal in one sentence.
- Identify the package(s) and file(s) that will be affected.
- List the behaviors that need to be correct when you're done.
- Identify edge cases, error conditions, and concurrency concerns upfront.

Produce a brief execution plan:
```
Goal: [one sentence]
Affected: [packages/files]

Behaviors:
1. [Expected behavior] → verify: [how]
2. [Expected behavior] → verify: [how]
3. [Edge case] → verify: [how]
```

## Phase 2: Write Tests

Write the test file(s) BEFORE any implementation:

- **Happy path**: The expected, normal-operation case.
- **Error paths**: What happens when inputs are invalid, connections drop, contexts cancel?
- **Boundary conditions**: Empty inputs, maximum values, zero-length streams, single-node mesh.
- **Concurrency**: Race conditions under parallel access. Multiple goroutines hitting the same state.
- **Timeouts & Cancellation**: Context deadlines, slow peers, hung connections.

### Test Quality Checklist

- [ ] Each test has a clear, descriptive name (`TestSonar_BroadcastStorm_UUIDDedup`)
- [ ] Tests are independent — no shared mutable state between test cases
- [ ] Table-driven tests for parameterized scenarios
- [ ] `t.Parallel()` where safe to expose race conditions
- [ ] Assertions include meaningful failure messages
- [ ] No `time.Sleep` — use channels, contexts, or sync primitives for coordination

### Commit: Tests

```
test(package): define behavior for [feature]

- Happy path: [describe]
- Error cases: [describe]  
- Edge cases: [describe]
- All tests currently FAIL (no implementation yet)
```

## Phase 3: Implement

Write the minimum code to make all tests pass:

- Don't add features beyond what the tests require.
- Don't add abstractions for hypothetical future needs.
- Don't optimize prematurely — correctness first.
- Run `go test -race ./package/...` after every meaningful change.

### Implementation Checklist

- [ ] All tests pass
- [ ] `go test -race` clean
- [ ] `go vet` clean
- [ ] Error messages include package and operation context
- [ ] Resources are cleaned up via `defer`
- [ ] No TODO/FIXME left without a tracking issue

### Commit: Implementation

```
feat(package): implement [feature]

- All tests passing
- Race-free under -race flag
- [Brief note on approach taken]
```

## Phase 4: Refine & Harden

With green tests as your safety net:

- Refactor for clarity if the implementation is messy.
- Add benchmarks for hot paths (`func BenchmarkXxx`).
- Profile allocations for data-plane code (`-benchmem`).
- Add any additional edge case tests discovered during implementation.

### Commit: Refinements

```
refactor(package): [what changed and why]
```
or
```
perf(package): optimize [hot path] - [result]
```

## Phase 5: Integration Verification

Before considering work complete:

1. `go build ./...` — full project compiles.
2. `go test -race ./...` — all tests pass project-wide.
3. `go vet ./...` — no static analysis warnings.
4. `golangci-lint run ./...` — zero lint issues.
5. Review the diff: every changed line traces to the original goal.

If a `task check` target exists, run it — it is the authoritative quality gate.

## Phase 6: Merge to `dev` & Cleanup

After all checks pass, merge the feature branch back into `dev` and delete it:

```bash
git checkout dev
git merge --ff-only feat/short-description
git branch -d feat/short-description
```

- **Always `--ff-only`**: If `dev` has diverged, rebase the feature branch first (`git rebase dev`). No merge commits for linear history.
- **Delete the branch**: Once merged, the branch serves no purpose. Delete it immediately.
- **Push**: `git push origin dev` to sync remote.

## Git Discipline

- **`dev` is the integration branch**: All feature/fix branches start from and merge back into `dev`. `main` is reserved for releases.
- **Atomic commits**: One logical change per commit. Tests and implementation can be separate commits.
- **Conventional format**: `type(scope): description` — types: `feat`, `fix`, `test`, `refactor`, `perf`, `docs`, `chore`.
- **No direct commits to `dev` or `main`**: Always use a branch.
- **Commit messages explain WHY**, not just what. The diff shows what changed; the message explains the reasoning.
- **Delete branches after merge**: Stale branches are clutter. Merge → delete → move on.

## Anti-Patterns to Avoid

| Anti-Pattern | Correct Approach |
|---|---|
| Writing code first, tests after | Write tests first — they define the contract |
| Testing only the happy path | Cover errors, boundaries, concurrency, cancellation |
| Giant commits with tests + code + refactor | Separate commits for tests, implementation, refinement |
| "Improving" unrelated code while fixing a bug | Touch only what the goal requires |
| Skipping `-race` because "it's simple" | Always run with `-race`. Simple code has races too |
| Using `time.Sleep` in tests | Use channels, contexts, `sync.WaitGroup`, or mutexes |
| Speculative abstractions | Build what's needed now. Refactor when a real pattern emerges |