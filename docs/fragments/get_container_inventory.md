#### `get_container_inventory`
*Category: `compute` · Runs on: Every Linux node with /sys/fs/cgroup (cgroup v2)*

Discovers every running container on the node **without requiring any container runtime daemon**, by walking the cgroup v2 unified hierarchy for runtime scope directories. Per-container process counts, memory usage, and cumulative CPU time are read natively from cgroup controller files.

**Data Sources:**
- **Scope Discovery**: walk of `/sys/fs/cgroup` matching `docker-<id>.scope` (Docker, under system.slice), `libpod-<id>.scope` (Podman), and `cri-containerd-<id>.scope` / `crio-<id>.scope` (Kubernetes pods under `kubepods*.slice`). IDs must be ≥12-char hex, which naturally excludes helper scopes like `libpod-conmon-*`.
- **Per-Container Metrics**: `cgroup.procs` (line count → `procs`), `memory.current` (bytes), and `cpu.stat` `usage_usec` from each container's cgroup directory.
- **Opportunistic Enrichment**: `docker ps --format {{json .}}` and `podman ps --format json` map 12-char ID prefixes to container `name` and `image` when those CLIs are present — invoked only for runtimes actually observed in the cgroup tree.

**Mathematical Models / Formatting:**
- **Unit Conversion**: `memory_mb` = bytes / 1024² and `cpu_usage_seconds` = usage_usec / 10⁶, both rounded to 1 decimal place.
- **ID Normalization**: `id_short` is the canonical 12-char prefix, matching Docker CLI display convention and enabling cross-referencing.
- **Aggregation & Capping**: containers are sorted (runtime, id) for determinism and capped at 100 entries; `counts_by_runtime` and `total` are computed over ALL containers found.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/sys/fs/cgroup` is missing.
- **cgroup v1 hosts**: no `cgroup.controllers` at the root returns a `degraded` result with `{"is_supported": false, "message": "cgroup v1 not supported..."}` — only the v2 unified hierarchy is parsed (same contract as `get_cgroup_limits`).
- Zero container scopes is a valid empty `ok` result.
- CLI enrichment failures (daemon down, permission denied) are completely silent: containers are reported with IDs only.
- Unreadable metric files degrade to `0` values; unreadable subtrees are skipped during the walk.
