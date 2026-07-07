# Cortex MCP Tool Catalog

This catalog documents every tool in the Cortex MCP Server, detailing their data sources, MCP visibility, and degradation profiles.

## MCP Exposure Model

The Cortex MCP gateway exposes tools to LLMs through a **meta-tool pattern**. Instead of registering hundreds of tools directly with the MCP protocol (which would overwhelm context windows), the gateway exposes exactly **4 meta-tools**:

| Meta-Tool | Purpose | MCP Exposed |
|---|---|---|
| `get_tool_list` | Discover available tools across the mesh | ✅ Direct |
| `get_tool_help` | Get JSON schema for a specific tool | ✅ Direct |
| `call_tool` | Invoke any tool (unicast, fan-out, auto-route) | ✅ Direct |
| `get_mesh_overview` | Aggregate fleet topology, roles, and Mermaid graph | ✅ Direct |

All other tools are **indirectly accessible** through `call_tool`. The LLM uses `get_tool_list` to discover them and `call_tool` to invoke them.

### Tool Visibility

Tools have a `Hidden` flag in their `ToolDefinition`. Hidden tools:
- **Do NOT appear** in `get_tool_list` output (invisible to the LLM's tool browsing)
- **Are still callable** via `call_tool` if the LLM (or gateway) knows their name
- **Are still documented** via `get_tool_help`

This is used for lifecycle management tools — they need to be callable by the gateway and test harness, but should not clutter the LLM's diagnostic tool namespace.

---

## Core Philosophy

### Graceful Degradation
Tools must prioritize availability over absolute completeness. If a tool expects secondary data (e.g., `/etc/os-release` for the OS name) but cannot find it, it should return `"unknown"` or an empty string rather than failing the execution. Hard failures (`IsSupported() = false`) should be reserved for scenarios where the fundamental operation of the tool is impossible (e.g., a required binary is missing, or the core sysfs tree does not exist).

### Role-Based Tool Activation
Tools are dynamically registered based on the specialized storage roles of the node they run on. The `internal/registry.DetectNodeRoles()` engine determines these capabilities by inspecting sysfs:

- **SFA Controllers**: Detected via the presence of `/sys/module/jsysdd`, `/sys/class/jsys`, `/sys/module/jnvme`, or `/sys/class/jnvme`.
- **Lustre MGS**: Detected via active instances in `/sys/fs/lustre/mgs/`.
- **Lustre MDS**: Detected via active targets in `/sys/fs/lustre/mdt/`.
- **Lustre OSS**: Detected via active targets in `/sys/fs/lustre/obdfilter/`.
- **Lustre Client**: Detected via active mounts in `/sys/fs/lustre/llite/`.

A single physical node can represent any combination of these roles (e.g., an SFA storage controller running hyperconverged MGS + MDS + OSS targets). If no specialized roles are detected, the node is classified as **`generic`** and only core diagnostic tools will be advertised.

---

## Visible Tools (LLM-Discoverable)

These tools appear in `get_tool_list` output and are the primary interface for LLM-driven diagnostics.

### Plugin Tools (registry.Tool)

These follow the `registry.Tool` interface and are auto-registered via `init()`.

#### `get_system_info`
*Category: `system` · Runs on: Every node*

Gathers foundational telemetry about the host system. This tool is designed to run universally on any Linux environment, including minimal containers and heavily stripped OS deployments.

**Data Sources:**
- **Hostname**: Retrieved via standard OS calls (`os.Hostname()`).
- **CPUs**: Number of logical cores reported by the Go runtime (`runtime.NumCPU()`).
- **Kernel Version**: Read directly from `/proc/sys/kernel/osrelease`.
- **OS Distribution**: Parsed from `/etc/os-release`, prioritizing the `PRETTY_NAME` field and falling back to `ID`.
- **Node Roles**: Detected via `registry.DetectNodeRoles()`, which inspects sysfs paths for SFA controllers, Lustre MGS/MDS/OSS targets, and mounted Lustre clients. Returns `["generic"]` when no specialized roles are detected.
- **Role Info**: Detailed `NodeRoleInfo` struct with per-target lists (e.g., active MDTs, OSTs, mounted filesystems).
- **Virtualization Context**: "Bulletproof" waterfall detection inspecting `/proc/self/cgroup`, `/proc/self/mountinfo`, `.dockerenv`, and DMI/hypervisor sysfs paths to identify container/VM runtimes.
- **Mellanox Context**: Inspects `/sys/class/infiniband` to detect Ethernet/InfiniBand presence and `/sys/module/mlx5_core/version` for MOFED driver versions vs Upstream kernel drivers.
- **Software Stack**: Detects installed Lustre versions via `/sys/fs/lustre/version`.
- **Node Scale**: Collects total memory capacity from `/proc/meminfo` and system boot time from `/proc/stat`.
- **Kernel Boot**: Retrieves boot parameters directly from `/proc/cmdline`.

**Degradation Profile:**
- `IsSupported()` will only return `false` if the host operating system is not Linux.
- If `/proc/sys/kernel/osrelease` or `/etc/os-release` are missing or unreadable, the tool degrades gracefully by returning `"unknown"` or empty strings. If no Lustre/SFA sysfs paths exist, `roles` returns `["generic"]` — the tool never errors.
- New telemetry data points default to empty strings, `0`, or `false` gracefully if their respective data sources are missing or unreadable.

#### `get_cpu_topology`
*Category: `compute` · Runs on: Every Linux node*

Provides a deterministic, pure-sysfs map of the hardware compute layout, enabling identification of thread-pinning violations, cross-socket latency bottlenecks, and SMT contention.

**Data Sources:**
- **NUMA Nodes**: Maps NUMA to logical threads via `/sys/devices/system/node/node*/cpulist`.
- **HW Topology**: Reads socket IDs, core IDs, and SMT siblings from `/sys/devices/system/cpu/cpu*/topology/`.
- **L3 Cache Locality**: Extracts cache domains from `/sys/devices/system/cpu/cpu*/cache/index3/shared_cpu_list`.

**Mathematical Models / Output Structuring:**
- **String Expansion**: Parses kernel list formats (e.g. `0-7,64-71`) into deterministic, sorted integer arrays.
- **Hierarchical Aggregation**: Groups results strictly by `NUMA Node -> L3 Cache Domain -> Physical Cores`, avoiding flat, repetitious lists of 100+ logical cores.

**Degradation Profile:**
- **UMA Fallback**: If `/sys/devices/system/node/` is missing or contains only one node, sets `is_numa: false` and buckets all cores safely under `numa_node_0`.
- **Virtualization Blindness**: In heavily abstracted VMs where L3 caches are unreadable, the tool avoids breaking the JSON shape by placing all cores into `l3_domain_0` and setting the `l3_topology_abstracted: true` safety flag on the system summary.
- **Context Timeouts**: Incorporates aggressive `5s` context timeouts around sysfs reads to prevent agent hangs on unresponsive virtual filesystems.

#### `get_irq_affinity`
*Category: `compute` · Runs on: Every Linux node*

Provides a consolidated map of hardware interrupt distribution, identifying the highest-volume devices and the specific CPU cores burdened by them.

**Data Sources:**
- Reads `/proc/interrupts` directly to map interrupts to logical CPU cores.

**Mathematical Models / Output Structuring:**
- **Active CPUs Filtering**: Strip the Zeroes logic to only include CPUs where the interrupt count represents >1% of the total hits for that IRQ.
- **Top N Limitations**: Filters out background noise by sorting IRQs by total volume and returning only the Top 20 highest volume IRQs, ensuring token-efficiency regardless of core count.
- **Burden Summary**: Aggregates the total hardware interrupts per CPU to return the top 5 most heavily burdened logical cores.

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux or if `/proc/interrupts` is missing.
- Handles missing device names by falling back to the IRQ number.
- Correctly parses and groups non-numeric IRQs (e.g., `NMI`, `LOC`, `IPI`) ensuring robust execution on edge environments.

#### `get_cpu_power_state`
*Category: `compute` · Runs on: Every Linux node*

Returns a highly aggregated map of CPU frequency profiles (P-states) and idle sleep limits (C-states). Groups logical CPUs by their governor and EPP. Crucial for diagnosing latency spikes or thermal throttling.

**Data Sources:**
- **P-States**: Reads `/sys/devices/system/cpu/cpu*/cpufreq/` for `scaling_governor`, `energy_performance_preference`, `scaling_cur_freq`, `scaling_max_freq`, and `scaling_min_freq`.
- **C-States**: Reads `/sys/devices/system/cpu/cpu0/cpuidle/` for `stateX/name` and `stateX/disable` to list enabled vs disabled idle states.
- **Drivers**: Reads `scaling_driver` and `current_driver` to identify the power management controllers.

**Mathematical Models / Output Structuring:**
- **Compound Keys**: Groups frequency profiles by `[governor]-[epp]` (e.g., `powersave-performance`) to avoid collisions on modern architectures.
- **Unit Conversion**: Averages the `scaling_cur_freq` across the group and converts all frequencies from KHz to MHz for human readability.
- **Hardware Bounds**: Calculates the absolute minimum and maximum hardware bounds across the group defensively.

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux.
- **Virtualization / BIOS Lockout**: If the OS does not have visibility into P-states (no `cpu0/cpufreq`), safely degrades by setting `power_management_managed_by_os: false` and omitting frequency profiles.
- **C-State Visibility**: If `cpuidle` is missing, drops the `c_state_limits` block entirely and flags `c_states_visible: false` to prevent misinterpretation.

#### `get_cgroup_limits`
*Category: `compute` · Runs on: Every Linux node*

Provides a targeted readout of Linux control group (cgroup) isolation policies for a specific workload, enabling the LLM to detect artificial CPU throttling, forced NUMA pinning, and cgroup-level Out-Of-Memory (OOM) events.

**Data Sources:**
- **Targeted Mode**: Reads `/proc/[target_pid]/cgroup` to locate the unified hierarchy path, then reads limits from `/sys/fs/cgroup/...`.
- **Global Mode**: Scans for common high-level workload manager parent slices (e.g., `slurm`, `docker`, `kubepods.slice`).
- **Cgroup v2 Data**: Reads `cpu.max`, `cpu.stat`, `cpuset.cpus`, `cpuset.mems`, `memory.max`, and `memory.events`.

**Mathematical Models / Output Structuring:**
- **Logical Cores**: Translates `cpu.max` quota strings (`MAX PERIOD`) into a clean float `allowed_logical_cores` (e.g., `max / period`).
- **Throttling & OOMs**: Extracts exact values for `throttled_time_ms` and `oom_kills_detected` to immediately identify resource starvation.
- **Flat Arrays**: Presents `allowed_cpus` and `allowed_numa_nodes` as flat arrays for easy topology cross-referencing.

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux or if `/sys/fs/cgroup` is missing.
- **Cgroup v1 Fallback**: Strictly enforces v2 parsing. If `/sys/fs/cgroup/cgroup.controllers` is missing, the tool aborts parsing and gracefully returns a fallback payload with `"is_supported": false` and an explicit message, preventing LLM hallucinations.


#### `get_uptime`
*Category: `system` · Runs on: Every Linux node*

Reads and calculates system uptime and CPU idle time. Formats the data into pre-processed human-readable strings to reduce the mathematical overhead for LLMs consuming the API.

**Data Sources:**
- **Uptime/Idle**: Read from `/proc/uptime`. The first value is the total system uptime, and the second is the total idle time across all CPUs.
- **CPU Count**: Number of logical cores reported by the Go runtime (`runtime.NumCPU()`).

**Mathematical Models:**
- **Idle Percentage**: Calculated dynamically as `(IdleSeconds / (UptimeSeconds * CPUCount)) * 100.0`.
- **Human Readable Format**: Converts seconds into a comma-separated duration string (e.g., `4 days, 1 hours, 25 minutes, 35 seconds`).

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux, or if `/proc/uptime` is unreadable/missing.

#### `get_loadavg`
*Category: `system` · Runs on: Every Linux node*

Reads the system load averages and scheduling entity statistics to provide a snapshot of system CPU and I/O pressure.

**Data Sources:**
- Read directly from `/proc/loadavg`.

**Mathematical Models:**
- Directly extracts the 1-minute, 5-minute, and 15-minute load averages.
- Parses the scheduling entity ratio (e.g., `1/863`) into two distinct integers: `RunnableEntities` and `TotalEntities`.

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux, or if `/proc/loadavg` is unreadable/missing.

#### `get_process_list`
*Category: `compute` · Runs on: Every Linux node*

Pulls a lightweight, top-N list of CPU/Mem consumers, with optional filtering via regex on user, process name, state, and cmdline. This operates completely in-memory using zero-allocation data plane iterators and avoids shelling out to `top` or `ps`.

**Data Sources:**
- Read directly from `/proc/uptime`, `/proc`, `/proc/[pid]/stat`, `/proc/[pid]/status`, and `/proc/[pid]/cmdline`.

**Mathematical Models:**
- Accurate CPU% calculations are derived by taking a rapid delta (default 100ms window, tunable via `sample_duration_ms`) of the process `utime` and `stime` against system uptime progression.
- Filters out non-active processes via strict regex matching before sorting and truncation to ensure top-N limit precision.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/stat` or `/proc/uptime` are missing.
- Safely ignores processes that terminate during the delta window.
- Returns `null` for cmdline if unreadable due to privileges or lack of initialization.

#### `get_process_tree`
*Category: `compute` · Runs on: Every Linux node*

Builds the execution hierarchy for a target process to identify workload origins. Returns the full ancestry path (up to PID 1) and all descendants of the target PID.

**Data Sources:**
- Read directly from `/proc/uptime`, `/proc`, `/proc/[pid]/stat`, `/proc/[pid]/status`, and `/proc/[pid]/cmdline`.

**Mathematical Models:**
- Maps PID to PPID using the 4th field in `/proc/[pid]/stat` to build a complete `O(N)` tree, then isolates the target process's ancestry and descendants to keep JSON payloads small.
- Accurate CPU% calculations are derived by taking a rapid delta (100ms window) of the process `utime` and `stime` against system uptime progression, measured across all processes simultaneously.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/stat` or `/proc/uptime` are missing.
- Returns an error if the `target_pid` does not exist or has died.
- Returns `null` for cmdline if unreadable due to privileges or lack of initialization.

#### `get_thread_wchan`
*Category: `compute` · Runs on: Every Linux node*

Diagnoses system hangs by showing exactly which kernel function threads are blocked on. Features two distinct diagnostic modes (Global and Targeted) to prevent token exhaustion at scale.

**Data Sources:**
- Reads `/proc/[pid]/wchan` for kernel wait channels.
- Reads `/proc/[pid]/status` for thread state (`State:`).
- Reads `/proc/[pid]/stat` to identify kernel threads (`PPID == 2`).
- Reads `/proc/[pid]/task/[tid]/comm` to map thread IDs to human-readable process names.

**Mathematical Models / Output Structuring:**
- **Mode 1 (Global Scan):** When `target_pid` is omitted, aggregates results into a nested structure separating `blocked_wchan` counts from standard `thread_states`. Threads are grouped by `wchan` and then by executable (`comm`). Truncates `example_pids` to a hard cap of 15 per executable, returning an `omitted_pids` count to maintain context without token explosion.
- **Mode 2 (Targeted Scan):** When a specific `target_pid` is provided, bypasses aggregation to return a flat, surgical list of all threads for that process (TID, Comm, Wchan, State).
- Identifies kernel threads and flags them by prefixing their names with `[kthread] ` in the output.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/1/wchan` is inaccessible.
- If the kernel restricts wchan visibility (value is `"0"` or unreadable), it safely degrades by grouping that thread's state from `/proc/[pid]/status` into the `thread_states` bucket, ensuring `blocked_wchan` only contains true signal.

#### `get_buddy_info`
*Category: `memory` · Runs on: Every Linux node*

Analyzes memory fragmentation, which is critical when a system has free RAM but still fails to allocate large contiguous pages.

**Data Sources:**
- Reads `/proc/buddyinfo` for contiguous memory block availability across NUMA nodes and zones.

**Mathematical Models:**
- Converts raw block counts into bytes using a standard 4KB base page size.
- Calculates a "fragmentation score" (0-100 index) using the formula: `(Free Bytes in Orders 0-3 / Total Free Bytes) * 100`. A score of 100 means highly fragmented (memory is shattered into unusable microscopic fragments).
- Aggregates the results into a flat array per NUMA node and zone, alongside a global `system_summary`.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/buddyinfo` is missing (non-Linux OS).

#### `get_memory_info`
*Category: `memory` · Runs on: Every Linux node*

A highly structured, math-free alternative to the `free` command, giving the LLM an instant read on starvation and swap thrashing by standardizing all memory metrics into Megabytes (MB) as raw integers.

**Data Sources:**
- Reads `/proc/meminfo`.

**Mathematical Models:**
- **True Used:** Calculates `MemTotal - MemAvailable` (or falls back to legacy calculation on older kernels) to give the exact amount of memory actively consumed by applications, preventing hallucination that Linux page caches are "wasted" memory.
- **Swap Ratio:** Calculates `(SwapTotal - SwapFree) / SwapTotal * 100` as a float rounded to two decimal places to detect early paging.
- **Swap Active:** Boolean flag instantly drawing attention to potential thrashing.

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux or if `/proc/meminfo` is missing.
- Older kernels (pre-3.14) do not expose `MemAvailable`. The tool degrades gracefully by calculating legacy math (Free + Buffers + Cached) and flags the payload with `"estimation_mode": "legacy"`. Otherwise, `"estimation_mode": "standard"`.

#### `get_hugepage_info`
*Category: `memory` · Runs on: Every Linux node*

Provides a comprehensive audit of memory paging efficiency by identifying the allocation status of static HugePages and the current operational mode of Transparent HugePages (THP) to diagnose memory allocation stalls.

**Data Sources:**
- **Static HugePages**: `/proc/meminfo`
- **Transparent HugePages (THP) Settings**: `/sys/kernel/mm/transparent_hugepage/enabled`, `/sys/kernel/mm/transparent_hugepage/defrag`
- **THP Performance Impact**: `/proc/vmstat`

**Mathematical Models:**
- **Capacity**: Translates page counts into Human-readable bytes via (`HugePages_Total` * `Hugepagesize`).
- **Utilization Ratio**: Calculates `usage_pct` (`((Total - Free) / Total) * 100.0`). Prevents divide-by-zero panics by returning `0.0` when total pages are zero.
- **Stalling Risk**: Analyzes `thp_enabled_mode` and `thp_defrag_policy` against `thp_fault_fallback` events to determine if synchronous background defragmentation is inducing IO latency (e.g., buffer overflows on NICs).

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/meminfo` is missing or inaccessible.
- **Missing THP Fallback**: If the kernel lacks THP support (`/sys/kernel/mm/transparent_hugepage` missing), degrades gracefully by omitting the `thp_stats` block entirely and reporting `thp_enabled_mode` as `unsupported`, while still returning accurate static hugepage telemetry.

#### `get_numa_stats`
*Category: `memory` · Runs on: Every Linux node*

Provides instant visibility into memory locality efficiency by translating cumulative NUMA allocation counters into actionable hit/miss ratios. This helps detect thread-pinning violations or kernel numad failures.

**Data Sources:**
- Reads `/sys/devices/system/node/node*/numastat` for NUMA allocation metrics (`numa_hit`, `numa_miss`, `numa_foreign`, `local_node`, `other_node`).

**Mathematical Models / Formatting:**
- **Node Miss Ratio:** Calculates `(numa_miss / (numa_hit + numa_miss)) * 100` per NUMA node as a float rounded to two decimal places.
- **System Miss Ratio:** Aggregates hits and misses across all nodes to provide a single `system_miss_ratio_pct`.
- Utilizes 64-bit unsigned integers natively to prevent counter overflow on high-throughput nodes.

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux or if `/sys/devices/system/node` is missing.
- **UMA Fallback:** If the system only has `node0` (Uniform Memory Access), cross-node misses are physically impossible. The tool safely degrades by setting `"is_numa": false`, ratios to `0.0`, and returns the available hit counters without failing.

#### `get_slab_info`
*Category: `memory` · Runs on: Every Linux node*

Provides a precise map of kernel object cache allocations by calculating the true memory footprint of each slab. Returns only the top consumers to help diagnose metadata exhaustion, dentry storms, or driver memory leaks.

**Data Sources:**
- Reads `/proc/slabinfo`. Requires Root privileges.

**Mathematical Models / Formatting:**
- **True Footprint:** Calculates `(num_objs * objsize)` and standardizes to Megabytes.
- **Fragmentation:** Calculates `((num_objs - active_objs) / num_objs) * 100` as a percentage.
- **Truncation:** Sorts by total size descending and returns only the top 15 consumers for O(1) token scaling.

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux or if `/proc/slabinfo` does not exist.
- **Permission Denied Fallback:** If the process lacks root privileges (EACCES), the tool gracefully degrades by returning an `error` status with a summary explaining the authorization requirement and omitting the data payload.


#### `get_numa_edac_errors`
*Category: `hardware` · Runs on: Every Linux node*

Extracts Error Detection and Correction (EDAC) statistics from physical RAM, mapping memory controllers to specific DIMMs to identify failing hardware before uncorrectable memory corruption causes a system crash.

**Data Sources:**
- Controller Stats: `/sys/devices/system/edac/mc/mc*/ce_count` and `ue_count`.
- DIMM Stats: Recursive extraction from `/sys/devices/system/edac/mc/mc*/dimm*` (or `rank*`/`csrow*` driver variations).
- Context Uptime: `/proc/uptime`.

**Mathematical Models / Formatting:**
- Evaluates system health as `warning` on correctable errors and `critical` on uncorrectable errors.
- Simplifies output by omitting `failing_dimms` sub-arrays on completely healthy (zero error) controllers, saving LLM token payload weight.

**Degradation Profile:**
- `IsSupported()` returns `true` (standard on Linux), but dynamically sets status to `degraded` with a warning message if the EDAC driver (`amd64_edac`, `sb_edac`) is missing or virtualization disables hardware ECC access.

---

#### `query_journalctl`
*Category: `system` · Runs on: Every Linux node*

Query the systemd journal logs with time-bounds and regex filtering.

**Data Sources:**
- **Primary**: `journalctl -o json` (logs payload query)
- **Primary Boots**: `journalctl --list-boots` (system boots list query)

**Mathematical Models / Formatting:**
- **JSON Parsing**: Directly maps structural keys to rules fields (`__REALTIME_TIMESTAMP`, `MESSAGE`, `SYSLOG_IDENTIFIER`, `_SYSTEMD_UNIT`, `PRIORITY`, `_HOSTNAME`, `_PID`).
- **Timestamp Formatting**: Parses microsecond Unix timestamps natively in Go to RFC3339 representation with microsecond precision.
- **Go Regex Engine**: Performs native, PCRE-compliant regexp matching on log payloads to ensure consistent filtering patterns.
- **Boots Parser**: Evaluates space-separated boot records, extracting boot index, ID, first entry timestamp, and last entry timestamp.

**Degradation Profile:**
- `IsSupported()` returns `false` if `journalctl` is not in `PATH`.
- If JSON logs parsing fails or query parameters are invalid, an error result is returned.

---

#### `query_dmesg`
*Category: `system` · Runs on: Every Linux node*

Inspect and filter the kernel ring buffer logs.

**Data Sources:**
- **Primary**: Native syslog `klogctl` syscalls (actions 10 and 3).
- **Fallback**: `dmesg -r` command (executed via shell).

**Mathematical Models / Formatting:**
- **Raw Buffer Parser**: Decodes logs line-by-line, matching syslog priority prefixes `<p>` to map facility values and level names (`emerg`, `alert`, `crit`, `err`, `warn`, `notice`, `info`, `debug`).
- **Timestamp Parsing**: Extracts decimal seconds offset string `[seconds]` into float values representing kernel runtime offsets.
- **Go Regex Engine**: Matches message payload substrings against compiled expressions.

**Degradation Profile:**
- `IsSupported()` returns `false` if `dmesg` binary is missing and raw `klogctl` returns permission errors.
- If both native reads and shell fallbacks are restricted by security policies, an execution error is returned.

---

#### `get_systemd_status`
*Category: `system` · Runs on: Every Linux node*

Inspect unit activation states for specific daemons, list all loaded services, and discover failed systemd units.

**Data Sources:**
- **Primary Show**: `systemctl show <unit>` (for specific units query)
- **Primary List**: `systemctl list-units` (for all/failed units query)

**Mathematical Models / Formatting:**
- **Properties Parser**: Decodes `Key=Value` properties blocks, mapping configuration variables like load/active/sub states, PIDs, and unit file states.
- **Table Tokenizer**: Evaluates tabular text rows, matching fields by position index and removing failed unit status indicators (`●`, `*`).

**Degradation Profile:**
- `IsSupported()` returns `false` if the `systemctl` binary is not in `PATH`.
- Omitted properties are returned as blank/empty values.

---

#### `get_sysctl_tuning_state`
*Category: `system` · Runs on: Every Linux node*

Inspect and audit kernel parameters related to network buffer limits, connection queues, virtual memory caching ratios, and asymmetric routing.

**Data Sources:**
- **Primary**: Native `/proc/sys/` files (resolved via path-translation).
- **Fallback**: `sysctl -n <key>` command execution.

**Mathematical Models / Formatting:**
- **Path Resolution**: Maps dot-notation sysctl parameter names to respective filesystem paths under `/proc/sys/`.
- **Predefined HPC Profile**: Audits 10 critical parameters (`net.ipv4.conf.all.rp_filter`, `net.ipv4.conf.default.rp_filter`, `net.ipv4.tcp_rmem`, `net.ipv4.tcp_wmem`, `net.core.rmem_max`, `net.core.wmem_max`, `net.core.somaxconn`, `net.core.netdev_max_backlog`, `vm.dirty_ratio`, `vm.dirty_background_ratio`) to evaluate latency and throughput capabilities.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/sys` is unreadable.
- If a parameter key does not exist or is protected by strict permission policies, its returned value contains the error message.

---

#### `get_kernel_modules`
*Category: `system` · Runs on: Every Linux node*

Inspect loaded Linux kernel modules/drivers, operational states, sizes, memory addresses, and dependency linkages.

**Data Sources:**
- **Primary**: Native `/proc/modules` file.
- **Fallback**: `lsmod` command (executed via shell).

**Mathematical Models / Formatting:**
- **Tokens Extractor**: Parses space-separated lists from raw modules data buffers, decoding module names, byte sizes, reference counts, dependencies list (splitting by commas), module states, and memory addresses.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/modules` file is missing and the `lsmod` binary is not in `PATH`.
- If permission denied errors prevent native file reads, the command execution fallback is evaluated.

---

#### `get_kernel_module_info`
*Category: `system` · Runs on: Every Linux node*

Inspect active operational properties and static packaging metadata for a specific Linux kernel module/driver.

**Data Sources:**
- **Primary Native (Live State)**: Reads `/sys/module/<name>/coresize`, `initsize`, `initstate`, `refcnt`, `taint`, and lists directories under `/sys/module/<name>/holders/` (to find referencing modules).
- **Supplement / Fallback (Static Properties)**: Runs the `modinfo` command to extract compiler headers and properties (author, description, license, dependencies, version, aliases, srcversion).

**Mathematical Models / Formatting:**
- **Modinfo Parser**: Decodes continuation lines and colon-delimited key-value maps from `modinfo` outputs.
- **Sysfs Mapper**: Parses integer values from sysfs entries representing core/init footprints and active references.

**Degradation Profile:**
- `IsSupported()` returns `false` if `modinfo` is not in `PATH`.
- If the module is not currently loaded, the `live_state` field is omitted from the JSON payload (returning static metadata).

---

#### `get_open_file_limits`
*Category: `system` · Runs on: Every Linux node*

Inspect system-wide file descriptor allocation limits and utilization percentage.

**Data Sources:**
- **Primary**: Native `/proc/sys/fs/file-nr` file.
- **Fallback**: `sysctl fs.file-nr` command execution.

**Mathematical Models / Formatting:**
- **Allocation Statistics**: Parses space-separated metrics, calculating system file descriptor capacity limit usage percentage as `(allocated / max) * 100`.

**Degradation Profile:**
- `IsSupported()` returns `false` if the proc file is missing and the `sysctl` command is not in `PATH`.
- If both read types fail, a tool execution error is returned.

---

#### `get_pressure_stall_info`
*Category: `system` · Runs on: Every Linux node with PSI enabled (kernel ≥ 4.20, not booted with `psi=0`)*

Reads the kernel's Pressure Stall Information (PSI) accounting to quantify how much wall-clock time tasks spend stalled waiting for CPU, memory, I/O, and IRQ. This is the canonical saturation signal: non-zero `full` pressure means every non-idle task was blocked simultaneously — pure lost throughput.

**Data Sources:**
- Read directly from `/proc/pressure/cpu`, `/proc/pressure/memory`, `/proc/pressure/io`.
- `/proc/pressure/irq` (optional; kernels ≥ 6.1 only).

**Mathematical Models / Formatting:**
- Decodes each `some`/`full` record into `avg10`/`avg60`/`avg300` percentages plus the cumulative stall total in microseconds (`total_usec`).
- Pre-evaluated `warning_reasons` on the 10-second averages: `cpu some avg10 > 40%` (CPU contention), `memory full avg10 > 10%` (reclaim/thrashing stalls), `io full avg10 > 10%` (storage saturation). Any breach flips the result status to `warning`.

**Degradation Profile:**
- `IsSupported()` returns `false` when `/proc/pressure/cpu` is missing ("PSI not available (kernel < 4.20 or psi=0)").
- A missing `irq` file is normal on kernels < 6.1 and is silently omitted; the `cpu` resource has no `full` record on kernels < 5.13.
- A malformed resource file is skipped and reported in `notes`; if no resource is readable at all, an error result is returned.

---

#### `get_time_sync_status`
*Category: `system` · Runs on: Every Linux node*

Audits system clock discipline: whether the clock is synchronized, by how much it drifts, which kernel clocksource is active, and which NTP daemon (if any) is steering it. Clock skew silently breaks TLS, Kerberos, distributed locks, log correlation, and lease logic.

**Data Sources:**
- Native `adjtimex(2)` syscall in read-only mode (`modes=0`): `STA_UNSYNC` synchronization flag, clock offset (nanoseconds when `STA_NANO` is set, microseconds otherwise), estimated/maximum error bounds.
- `/sys/devices/system/clocksource/clocksource0/current_clocksource` and `available_clocksource`.
- Enrichment via binaries when present: `chronyc -c tracking` (stratum, reference source, daemon offset, leap status) or `timedatectl show` (`NTP=`/`NTPSynchronized=` properties, attributed to `systemd-timesyncd`).

**Mathematical Models / Formatting:**
- Kernel offset normalized to milliseconds with 2 decimal places, sign preserved (`STA_NANO` unit handling).
- `warning_reasons`: clock not synchronized; absolute offset > 100 ms; current clocksource is not `tsc` on amd64 (caveat: paravirtual clocksources such as `kvm-clock`/`hyperv_clocksource` are normal on VMs).

**Degradation Profile:**
- `adjtimex` denied (`EPERM`, common in unprivileged containers): falls back to daemon queries only and reports `"kernel_status_available": false`; synchronization is then judged from the daemon (chrony leap status / `NTPSynchronized`).
- No `chronyc`/`timedatectl`: the `ntp_daemon` object is omitted; kernel + clocksource data still returned.
- Missing clocksource sysfs files: the `clocksource` object is omitted.

---

#### `get_kernel_security_state`
*Category: `system` · Runs on: Every Linux node*

Audits the kernel's integrity and hardening posture: taint state (is this kernel still trustworthy/supportable?), lockdown mode, active LSM (SELinux/AppArmor), and per-CPU-vulnerability mitigation state.

**Data Sources:**
- `/proc/sys/kernel/tainted` — decimal bitmask decoded bit-by-bit (bits 0–18) into flag letters and plain-language reasons (P proprietary module, F force loaded, O out-of-tree, E unsigned, L soft lockup, K live patched, ...).
- `/sys/kernel/security/lockdown` — the active mode is the bracketed word in `none [integrity] confidentiality`.
- SELinux: `/sys/fs/selinux/enforce` (`1` = enforcing, `0` = permissive, absent = `not_present`).
- AppArmor: `/sys/module/apparmor/parameters/enabled` (`Y`/`N`, absent = `not_present`).
- CPU vulnerabilities: `/sys/devices/system/cpu/vulnerabilities/*`.

**Mathematical Models / Formatting:**
- Each vulnerability's raw kernel string is classified deterministically: `Not affected` → `not_affected`, `Mitigation: ...` → `mitigated`, `Vulnerable...` → `vulnerable` (context-prefixed strings like `KVM: Mitigation: ...` handled); `vulnerable_count` aggregates the unmitigated ones.
- `warning_reasons`: any `vulnerable_count > 0` (with the affected names), and trust-relevant taint bits P (proprietary), F (force loaded), E (unsigned module).

**Degradation Profile:**
- Every source is independent: a missing file yields `not_present` (LSM), an omitted `lockdown_mode`, an empty vulnerability list, or an untainted default — never an execution error.
- An unparseable taint value degrades to untainted (`0`).

---

#### `get_scheduled_jobs`
*Category: `system` · Runs on: Linux nodes with systemd and/or cron*

Inventories all recurring background work on the node: systemd timers and cron entries. Essential for explaining periodic load spikes, tracing unexpected file changes, and auditing active automation.

**Data Sources:**
- systemd timers: `systemctl list-timers --all --no-pager --output=json`; on older systemd without JSON support, the plain-text table is parsed as a fallback (unit/activates always recovered, timestamps best-effort).
- System cron parsed natively: `/etc/crontab` and `/etc/cron.d/*` (`m h dom mon dow user command` format).
- User crontabs parsed natively: `/var/spool/cron/crontabs/*` (Debian) and `/var/spool/cron/*` (RHEL) — one file per user, no user column.

**Mathematical Models / Formatting:**
- Timer microsecond-epoch fields (`next`, `last`) converted to RFC3339 UTC (`next_iso`, `last_iso`), `null` preserved for never/none.
- Comments and environment lines skipped; `@reboot`/`@daily` specials preserved verbatim as the schedule.
- Token caps: timers limited to 50, cron entries to 100, commands truncated to 120 characters; `summary` always carries the total discovered counts (`timers`, `cron_entries`, `crontabs_skipped`).

**Degradation Profile:**
- `IsSupported()` returns `false` only when `systemctl` is absent from `$PATH` **and** no cron path exists.
- `systemctl` missing or failing: timers list empty, cron still reported.
- Unreadable user crontab files/dirs (running unprivileged): counted in `summary.crontabs_skipped`, execution still returns status `ok`.

---

#### `get_logged_in_sessions`
*Category: `system` · Runs on: Every Linux node with /run/utmp or systemd-logind*

Enumerates active interactive login sessions — local TTYs and remote SSH/PTY logins — revealing who is on the node, from where, and since when. Useful for correlating performance anomalies or configuration drift with human activity.

**Data Sources:**
- **Native**: `/run/utmp` binary session database. Each 384-byte record (little-endian x86_64 glibc layout) is decoded with `encoding/binary`; NUL-padded C strings are trimmed. Only `USER_PROCESS` (type 7) records — real interactive logins — are reported; reboot, runlevel, and dead-process records are filtered out.
- **Fallback**: `loginctl list-sessions --output=json` (systemd-logind) when the utmp database is missing or unreadable. This path carries no login timestamp or leader PID, so those fields degrade to empty/`0`.

**Mathematical Models / Formatting:**
- **Timestamp Normalization**: utmp `timeval` (32-bit sec/usec) is converted to RFC3339 UTC `login_time`.
- **Local vs Remote**: `remote_host` is the origin host/IP for remote logins and empty for local console sessions — no reverse-DNS heuristics are applied.
- **List Capping**: The session list is capped at 100 entries; `count` reflects the returned list. The `source` field (`utmp`|`loginctl`) tells the LLM which fidelity level was used.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/run/utmp` is absent AND no `loginctl` binary exists.
- Malformed or truncated utmp records are skipped individually, never fatal.
- Zero active sessions is a valid `ok` result (headless/compute nodes).
- loginctl execution or JSON parse failures return an encapsulated error result, never a hard Go error.

---

#### `get_package_audit`
*Category: `system` · Runs on: Any node with rpm or dpkg*

Audits whether specific packages are installed and at which exact version/release, straight from the native package manager database. Designed for fleet-wide verification of driver stacks, security patch levels, and dependency presence via a required `packages` array parameter (globs allowed, e.g. `kernel*`).

**Data Sources:**
- **rpm** (RHEL/Rocky/SUSE): `rpm -q --queryformat "%{NAME}\t%{VERSION}\t%{RELEASE}\t%{ARCH}\n" <pkg>`. Missing packages exit 1 with `package X is not installed`.
- **dpkg** (Debian/Ubuntu): `dpkg-query -W -f '${Package}\t${Version}\t${Architecture}\t${db:Status-Status}\n' <pkg>`. Only rows whose `db:Status-Status` is exactly `installed` count — config-file residue is treated as missing.
- **Manager Detection**: `rpm` is preferred when both binaries exist (rpm-based distros commonly ship dpkg shims).

**Mathematical Models / Formatting:**
- **Query Expansion**: One query may produce multiple entries when globs match several packages or multiple versions are installed side by side (multi-version kernels); each entry carries its originating `query` field.
- **Batch Capping**: The query list is capped at 50 names per call; truncation is flagged in the summary.
- **Precomputed Aggregates**: `installed_count` and a `missing[]` list of not-installed queries are precomputed so the LLM never has to re-derive them.

**Degradation Profile:**
- `IsSupported()` returns `false` only when neither `rpm` nor `dpkg-query` is in `$PATH`.
- A per-package query failure (even rpmdb corruption) degrades to an `installed: false` entry for that query; the batch is never aborted.
- Missing/empty `packages` parameter returns an encapsulated error result.

---

#### `query_coredumps`
*Category: `system` · Runs on: Nodes with systemd-coredump (coredumpctl or /var/lib/systemd/coredump)*

Queries recent application crashes captured by systemd-coredump, surfacing crash-looping executables and the fatal signals that killed them. Accepts an optional `last_n` parameter (default 20, capped at 100).

**Data Sources:**
- **Primary**: `coredumpctl list --json=short --no-pager` — a JSON array with µs-epoch `time`, `pid`, `uid`, `sig`, `corefile` state (`present`|`missing`|`none`), and executable path. Exit status 1 with `No coredumps found` is treated as a valid empty result, not a failure.
- **Fallback**: filename scan of `/var/lib/systemd/coredump`, parsing `core.<comm>.<uid>.<boot-id>.<pid>.<timestamp>[.zst]` entries when the `coredumpctl` binary is unavailable or fails. The comm segment may contain dots, so fixed fields are anchored from the right.

**Mathematical Models / Formatting:**
- **Signal Decoding**: Common fatal signals are decoded to names (4 → `SIGILL`, 6 → `SIGABRT`, 7 → `SIGBUS`, 8 → `SIGFPE`, 11 → `SIGSEGV`); anything else renders as `SIG<n>`. Filename-fallback entries omit the signal (not encoded in the name).
- **Temporal Ordering**: Dumps are sorted newest-first on the raw µs timestamp before the `last_n` cut; µs-epoch values are emitted as RFC3339 UTC.
- **Crash-Loop Aggregation**: `count_by_executable` is computed over ALL dumps found (not just the returned window) alongside `total`, so repeat offenders are visible even beyond the cap.

**Degradation Profile:**
- `IsSupported()` returns `false` only when there is no `coredumpctl` binary and no coredump spool directory.
- "No coredumps found" and an empty spool directory both return `ok` with zero dumps.
- Unparseable spool filenames are skipped individually, never fatal.
- A hard `coredumpctl` failure silently falls back to the directory scan when the spool directory exists.

---

#### `get_file_locks`
*Category: `system` · Runs on: Every Linux node*

Summarizes the kernel file-lock table to diagnose lock contention: counts by lock type and mode, the processes holding the most locks, and blocked waiters queued behind held locks.

**Data Sources:**
- **Lock Table**: `/proc/locks` text file, including `->` blocked-waiter lines (`ID: [->] TYPE MODE KIND PID MAJ:MIN:INO START END`).
- **Process Names**: `/proc/<pid>/comm` for holder and waiter identity resolution.

**Mathematical Models / Formatting:**
- **Aggregation**: `total_locks` counts held locks only (waiter lines excluded); `by_type` buckets POSIX / FLOCK / OFDLCK / LEASE and `by_mode` buckets READ / WRITE.
- **Top Holders**: held locks grouped per pid, ranked by lock count (ties broken by pid) and capped at the top 15, each with resolved `comm`.
- **Blocked Waiters**: exact `count` plus a detail list (`{pid, comm, type, mode}`) capped at 15 entries.
- **Warning Heuristic**: any blocked waiter appends a `warning_reasons` entry ("N process(es) blocked waiting on file locks") and flips the result status from `ok` to `warning`.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/locks` is missing.
- Dead pids (comm unreadable) degrade to `comm: "unknown"`; OFD locks carry pid `-1` and report `"OFD (no owner pid)"` instead of a process name.
- Malformed lock-table lines are skipped rather than failing the parse; an empty table is a valid `ok` result with zeroed aggregates.
- A missing locks file at execution time and cancelled contexts are encapsulated as error results (never hard Go errors).

---

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

---

#### `get_process_memory_detail`
*Category: `memory` · Runs on: Every Linux node*

Produces an accurate memory footprint for a single process (required `target_pid` parameter), distinguishing truly-owned memory (PSS, private pages) from shared library pages that naive RSS readings double-count, plus swap pressure, transparent huge page usage, and the rlimits bounding the process.

**Data Sources:**
- **Primary**: `/proc/<pid>/smaps_rollup` — kernel-precomputed totals for `Rss`, `Pss`, `Shared_Clean`, `Shared_Dirty`, `Private_Clean`, `Private_Dirty`, `Swap`, `SwapPss`, and `AnonHugePages` (all `kB` lines).
- **Fallback**: `/proc/<pid>/smaps` — the same keys summed across every mapping on kernels older than 4.14 that lack `smaps_rollup`.
- **Identity**: `/proc/<pid>/status` for `Name` (comm), `Threads`, and `VmSwap` (used only when smaps carries no Swap key).
- **Limits**: `/proc/<pid>/limits` rows `Max address space` and `Max locked memory` (soft limit reported; `unlimited` preserved as a string).

**Mathematical Models / Formatting:**
- **Unit Conversion**: all sizes converted from kB to MB, rounded to 1 decimal place.
- **Composite Fields**: `shared_mb = Shared_Clean + Shared_Dirty`; `private_mb = Private_Clean + Private_Dirty`; `thp_mb = AnonHugePages`.
- **Headroom Ratio**: `rss_pct_of_address_limit = RSS / soft address-space limit × 100` (1dp), emitted **only** when that limit is finite — never against `unlimited`.

**Degradation Profile:**
- `IsSupported()` returns `false` when `/proc/self/status` is unreadable (non-Linux).
- Nonexistent `target_pid` returns a `process not found` error result; missing/invalid `target_pid` returns a parameter error result.
- Missing `smaps_rollup` (old kernels) silently falls back to summing `smaps`.
- Permission denied on smaps data (other users' processes without root/CAP_SYS_PTRACE) returns an error result explicitly naming the privilege requirement.
- An unreadable `limits` file degrades to `"unknown"` limit values, never fatal.

---

#### `get_pci_link_status`
*Category: `hardware` · Runs on: Every Linux node with a PCI bus*

Audits PCIe link training and Advanced Error Reporting (AER) health for every PCI device, surfacing downtrained links (e.g. a x16 HCA silently renegotiated to x4 after a reseat) and devices accumulating correctable/nonfatal/fatal bus errors — the classic signatures of failed risers, retimers, and poorly seated cards.

**Data Sources:**
- Device Discovery: `/sys/bus/pci/devices/` directory iteration.
- Link Training: `current_link_speed`, `current_link_width`, `max_link_speed`, `max_link_width` per device.
- Identity: `class`, `vendor`, `device`, `numa_node` per device.
- AER Counters (when exposed by the kernel AER driver): `aer_dev_correctable`, `aer_dev_nonfatal`, `aer_dev_fatal` — multi-line `KEY N` files whose individual counters are summed, excluding `TOTAL_ERR_*` aggregate lines to avoid double counting.

**Mathematical Models / Formatting:**
- **Downtraining Detection**: Parses the numeric `GT/s` prefix of the speed strings (`"8.0 GT/s PCIe"` → `8.0`) and flags `is_downtrained` when current speed < max speed or current width < max width, emitting explicit reasons like `running x4 at max x8`.
- **Class Decoding**: Renders raw PCI class hex into LLM-friendly family names (`0x0108` → `nvme`, `0x0207`/`0x0c04` → `infiniband`, `0x02` → `ethernet`, `0x03` → `gpu`, `0x01` → `storage`); unmapped classes keep the raw hex as `other (0x...)`.
- **Signal-over-Noise Filtering**: By default only devices that are downtrained, have AER errors, or belong to an interesting class (NVMe, network/InfiniBand, GPU, fabric) are returned; `all=true` returns every device exposing link files. The devices list is capped at 64 entries with a `summary.truncated` flag.
- **Status Escalation**: Result status becomes `warning` when any link is downtrained or any device reports nonfatal/fatal AER errors.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/sys/bus/pci/devices` does not exist (no PCI bus exposed, e.g. some VMs/containers).
- Devices without link capability files (virtual functions, host bridges) are skipped silently.
- `numa_node` of `-1` (no affinity reported) omits the field entirely; missing AER files omit the `aer` block rather than reporting zeros.

---

#### `get_sensor_readings`
*Category: `hardware` · Runs on: Bare-metal Linux nodes exposing hwmon chips*

Collects every hardware monitoring sensor the kernel exposes — temperatures, fan tachometers, power draw, and voltage rails — normalized into human units and classified against hardware-defined thermal thresholds. Identifies overheating packages, dead fans, and sagging rails without requiring `lm-sensors` to be installed.

**Data Sources:**
- Chip Discovery: `/sys/class/hwmon/hwmon*/` iteration; attributes are resolved from the chip directory first, then the nested `device/` directory used by older kernel layouts.
- Chip Name: `hwmon*/name`.
- Temperatures: `temp<N>_input` (millidegrees C) with `temp<N>_label`, `temp<N>_max`, `temp<N>_crit`.
- Fans: `fan<N>_input` (RPM) with `fan<N>_label`.
- Power: `power<N>_average` preferred over `power<N>_input` (microwatts) with `power<N>_label`.
- Voltages: `in<N>_input` (millivolts) with `in<N>_label`.

**Mathematical Models / Formatting:**
- **Unit Normalization**: m°C → °C (1 decimal), µW → W (1 decimal), mV → V (3 decimals); labels fall back to the sysfs index (`temp1`) when no `_label` file exists.
- **Threshold Classification** (deterministic, per temperature): reading ≥ `crit` → `critical`, reading ≥ `max` → `warning`, otherwise `ok`; each breach is written to a top-level `warning_reasons[]` entry naming the chip, sensor, reading, and threshold.
- **Status Escalation**: any `critical` sensor → result status `error`, any `warning` sensor → `warning`, mirroring the EDAC tool's health mapping.
- **Payload Caps**: 64 sensors per type per chip and 20 warning reasons (overflow noted as `(+N more ...)`).

**Degradation Profile:**
- `IsSupported()` returns `false` with reason `no hwmon sensors exposed (common in VMs)` when `/sys/class/hwmon` is missing or contains no `hwmon*` directories.
- Individual unreadable or unparseable sensor files are skipped silently; chips exposing zero readable sensors are omitted from the output.
- Missing `temp<N>_max`/`temp<N>_crit` files omit the thresholds and leave the sensor status `ok` — never escalating on absent data.

---

#### `get_dmi_inventory`
*Category: `hardware` · Runs on: Every Linux node exposing SMBIOS/DMI*

Reports the physical identity of the machine — manufacturer, product, serial number, mainboard, firmware level, and chassis form factor — and, when possible, the exact physical DIMM population per slot. Essential for support cases, firmware audits, and correlating EDAC memory errors to the physical module that needs replacing.

**Data Sources:**
- **Native (pure sysfs)**: `/sys/class/dmi/id/` attributes — `sys_vendor`, `product_name`, `product_serial` (root-only, mode 0400), `product_uuid` (root-only), `board_vendor`, `board_name`, `bios_vendor`, `bios_version`, `bios_date`, `chassis_type` (numeric SMBIOS code).
- **Enrichment (binary fallback)**: `dmidecode -t memory`, wrapped in `sudo -n` when the agent runs without root. Parses `Memory Device` blocks for `Locator`, `Size`, `Speed`, `Type`, `Manufacturer`, and `Part Number` using exact key matching (so `Bank Locator`, `Type Detail`, and `Configured Memory Speed` never pollute the fields).

**Mathematical Models / Formatting:**
- **Chassis Decoding**: Maps the numeric `chassis_type` through the SMBIOS 7.4.1 enum (1..36, e.g. `1=Other`, `3=Desktop`, `17=Main Server Chassis`, `23=Rack Mount Chassis`); unmapped codes render as `type <n>`, unparseable values as `unknown`.
- **Slot Accounting**: DIMM blocks reporting `No Module Installed` are counted as `dimm_summary.empty_slots` instead of emitting empty entries; the populated list is capped at 64 modules.
- **Privilege Signaling**: `serial_available` explicitly reports whether `product_serial` was readable, letting an LLM distinguish "machine has no serial" from "agent needs root".

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/sys/class/dmi/id` does not exist (firmware exposes no SMBIOS, e.g. some containers/architectures).
- Unreadable root-only attributes (`product_serial`, `product_uuid`) are omitted with `serial_available: false` — the result stays `ok`.
- Missing identity attributes degrade to `"unknown"` per the catalog philosophy; if `dmidecode` is absent or fails (no passwordless sudo), the `dimms`/`dimm_summary` sections are omitted entirely without erroring.

---

#### `get_gpu_status`
*Category: `hardware` · Runs on: Nodes with NVIDIA or AMD GPU telemetry (vendor CLI or amdgpu sysfs)*

Reports a per-GPU health snapshot — utilization, VRAM pressure, temperature, power draw/limit and (NVIDIA only) volatile uncorrected ECC errors and performance state — so accelerator saturation, thermal throttling and silicon degradation can be spotted before jobs fail. Vendor CLIs are the justified primary source because GPU telemetry rides proprietary protocols (NVML / ROCm SMI); a native sysfs fallback keeps partial coverage when no tooling is installed.

**Data Sources:**
- **Primary (NVIDIA)**: `nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,power.limit,ecc.errors.uncorrected.volatile.total,pstate --format=csv,noheader,nounits`.
- **Secondary (AMD)**: `rocm-smi --showuse --showmemuse --showtemp --showpower --json`, parsed defensively since metric key labels vary between ROCm releases (edge temperature preferred, average package power preferred).
- **Native fallback (amdgpu sysfs)**: `/sys/class/drm/card<N>/device/` — `gpu_busy_percent`, `mem_info_vram_used`, `mem_info_vram_total`, plus `hwmon/hwmon*/temp1_input` (millidegrees) and `hwmon/hwmon*/power1_average` (microwatts). Connector children (`card0-eDP-1`) and render nodes (`renderD128`) are excluded.

**Mathematical Models / Formatting:**
- Pre-computes `memory_used_pct = memory_used_mb / memory_total_mb × 100`, rounded to one decimal place, so the LLM never divides.
- Scales raw sysfs units locally: millidegrees → °C, microwatts → W, bytes → MB.
- Heuristic per-GPU `warning_reasons` flag temperature > 85°C, uncorrected ECC errors > 0 and memory utilization > 95%; any flagged GPU elevates the result status to `warning`.
- The `backend` field records which source (`nvidia-smi`, `rocm-smi`, `sysfs`) produced the snapshot.

**Degradation Profile:**
- `IsSupported()` returns `false` only when neither `nvidia-smi` nor `rocm-smi` is in `PATH` and no `/sys/class/drm/card*/device/gpu_busy_percent` exists.
- Backends degrade in priority order: a failing `nvidia-smi` falls through to `rocm-smi`, then to the sysfs partial read; only when every source fails does the tool return an error result naming each failed backend.
- Values a backend reports as `[N/A]` / `[Not Supported]` (and sysfs files that are missing or unreadable) are omitted from the JSON entirely rather than zeroed; NVIDIA-only fields (`ecc_uncorrected`, `pstate`) are absent on other backends.
- Command execution respects context cancellation/timeouts and returns an encapsulated error result instead of a hard failure.

---

#### `query_ipmi_sel`
*Category: `hardware` · Runs on: Bare-metal nodes with a BMC exposing an IPMI character device*

Queries the baseboard management controller's System Event Log to surface out-of-band hardware events — temperature excursions, fan failures, ECC faults, PSU state changes — recorded by the BMC independently of the host OS, along with the SEL's remaining capacity. `ipmitool` is the justified primary source since SEL access requires the vendor IPMI protocol over the kernel's BMC character device.

**Data Sources:**
- **Primary records**: `ipmitool sel elist` (wrapped in `sudo -n` when the effective UID is not 0 and sudo is available), parsed row-by-row into `{id, timestamp, sensor, event, direction}`.
- **Capacity metadata**: `ipmitool sel info`, parsed tolerantly (`Entries`, `Free Space`, `Percent Used` labels vary slightly between BMC firmwares; the first integer in each value is extracted).
- **Device gate**: a BMC character device must exist at `/dev/ipmi0`, `/dev/ipmi/0` or `/dev/ipmidev/0`.

**Mathematical Models / Formatting:**
- Converts `MM/DD/YYYY HH:MM:SS` date/time columns to RFC3339; events logged before BMC clock initialization keep their raw `Pre-Init` marker.
- Aggregates the entire SEL into `counts_by_sensor_type` (e.g. `Temperature: 3`) by stripping the `#0xNN` sensor suffix, while `records` returns only the `last_n` most recent rows (default 25, capped at 200) to bound token payload.
- Heuristic `warning_reasons` flag any record whose event contains `Critical` or `Non-recoverable` (case-sensitive, so IPMI's warning-level `Non-critical` does not false-positive) and SEL usage above 75%; itemized critical warnings are capped at 10 with an aggregate overflow entry.

**Degradation Profile:**
- `IsSupported()` returns `false` when `ipmitool` is missing from `PATH` or no BMC character device exists — the reason string distinguishes which prerequisite failed.
- Permission failures (sudo password required, device permission denied, insufficient privilege level) return the encapsulated error result `Unauthorized: Root or passwordless sudo privileges required for BMC access.` instead of a hard Go error.
- A failing `sel info` degrades gracefully: records are still returned and `sel_info` is omitted from the payload. An empty SEL ("SEL has no entries") yields an `ok` result with an empty records list.

---

### Storage Tools

#### `get_block_topology`
*Category: `storage` · Runs on: Every Linux node*

Constructs a hierarchical map of the storage stack, tracing physical block devices through virtual layers (MDADM, LVM, Device Mapper) to their final mount points and swap areas. Enables the LLM to diagnose whether a Lustre block timeout is a single NVMe failure or an entire Volume Group degradation.

**Data Sources:**
- Physical Devices & Partitions: `/sys/class/block/*/` (device existence, `dev`, `size`, `device/model`).
- Device Relationships: `/sys/class/block/*/holders/` and `/sys/class/block/*/slaves/` directories.
- RAID Status: `/proc/mdstat` for MDADM array states.
- Mount Points: `/proc/self/mountinfo` (uses `major:minor` numbers for precise device-to-mount mapping).
- Swap Areas: `/proc/swaps`.

**Stitching Strategy:**
- Uses `major:minor` device numbers as the primary key to link block devices to mount points.
- Follows `holders`/`slaves` directories to trace `physical→partition→DM/MD→mount` relationships without invoking `lsblk`.

**Mathematical Models / Formatting:**
- **Size (GiB):** Calculated as `(sectors * 512) / (1024³)`, rounded to 1 decimal place. Uses binary GiB to align with standard Linux tools (`df -h`, `lsblk`).
- **Transport Detection:** Best-effort heuristic: NVMe device names → `pcie`; symlink path inspection for `virtio`, `usb`, `sata`, `sas`.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/sys/class/block/` is missing (non-Linux or container without sysfs).
- **Missing `/proc/mdstat`:** `md_arrays` returns `[]` (no software RAID is a valid state).
- **Unreadable `/proc/self/mountinfo`:** `mount_point` fields are empty strings.
- **Unreadable `/proc/swaps`:** `swap_devices` returns `[]`.

#### `get_disk_io_stats`
*Category: `storage` · Runs on: Every Linux node*

Provides high-resolution I/O performance metrics by parsing kernel block-layer counters, translating raw sector counts into MB/s and IOPS to identify storage bottlenecks. Uses an internal 500ms sleep to calculate instantaneous rate metrics.

**Data Sources:**
- **I/O Counters**: `/proc/diskstats` (sectors read/written, operations completed, time spent doing I/O, and current queue depth).
- **Queue Limits**: `/sys/block/<dev>/queue/nr_requests` to provide context for queue depth saturation.

**Mathematical Models / Formatting:**
- **Device Filtering**: Excludes virtual devices that clutter the payload (e.g., `loop`, `ram`, `zram`, `nbd`), but includes `dm-` (LVM/Multipath) devices to show virtual layer latency.
- **Delta Calculation**: Takes a 500ms synchronous delta of cumulative kernel counters to provide instantaneous rates (`read_mbs`, `write_mbs`, `read_iops`, `write_iops`).
- **Utilization & Latency**: Calculates average I/O latency in milliseconds and device utilization percentage (clamped to 100%).
- **Aggregation**: Truncates the output to devices exceeding a tunable latency threshold (default 20.0ms), but calculates a global `system_summary` representing total combined throughput and the peak latency across all devices.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/diskstats` is missing or inaccessible.
- If `/sys/block/<dev>/queue/nr_requests` is missing (common for NVMe namespaces or certain DM devices), safely omits `queue_depth_max` via JSON `omitempty` rather than failing the parse.
- Context cancellation during the 500ms measurement window causes an immediate return, ensuring tool responsiveness under timeout conditions.


#### `get_block_scheduler_info`
*Category: `storage` · Runs on: Every Linux node*

Audits block device I/O scheduler and read-ahead settings to identify software-induced latency (schedulers) and inefficient caching (read-ahead) that throttle high-performance storage hardware. Essential for Lustre OSS nodes where NVMe drives must use the `none` scheduler and read-ahead must not double-cache against Lustre's own algorithms.

**Data Sources:**
- **Scheduler Status**: `/sys/block/<dev>/queue/scheduler` — kernel returns space-separated available schedulers with the active one in brackets.
- **Read-Ahead**: `/sys/block/<dev>/queue/read_ahead_kb` — integer in kilobytes.
- **Device Discovery**: `/sys/block/` directory iteration with partition exclusion via sysfs `partition` marker file.

**Device Filtering:**
- Includes physical disks (NVMe, SATA, SAS, VirtIO), Device Mapper (`dm-*`), and MD RAID (`md*`) logical volumes.
- Excludes `loop`, `ram`, `zram`, `nbd` pseudo-devices and partitions (which inherit queue settings from parent devices).

**Mathematical Models / Formatting:**
- **Scheduler Parsing**: Regex-free bracket extraction from kernel format `[active] avail1 avail2`. Handles edge cases: `none` without brackets (DM devices), empty files, whitespace-only content.
- **Tuning Warnings**: Two heuristic rules producing a `warning_reasons []string` array:
  1. **NVMe Scheduler Trap**: NVMe devices using schedulers other than `none` — PCIe NVMe drives have massive internal parallel queues; OS-level scheduling wastes CPU time re-ordering I/O the firmware handles natively.
  2. **High Read-Ahead**: `read_ahead_kb >= 4096` — causes cache thrashing, especially on Lustre OSS nodes that implement their own read-ahead algorithms.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/sys/block` is missing (non-Linux or container without sysfs).
- **Missing scheduler file**: `active_scheduler: "none"`, `available_schedulers: ["none"]`.
- **Missing read_ahead_kb file**: `read_ahead_kb: 0`.
- **DM/MD "none" without brackets**: Handled gracefully as `active_scheduler: "none"`.


#### `get_mount_usage`
*Category: `storage` · Runs on: Every Linux node*

Provides instantaneous capacity and inode exhaustion metrics for all active, physical, and network filesystems. Utilizes the statfs syscall with strict timeout protection to prevent the agent from hanging on dead network mounts.

**Data Sources:**
- **Mount Identification**: Parses `/proc/self/mountinfo` instead of `/etc/mtab` to bypass namespace spoofing and guarantee kernel ground truth.
- **Capacity & Inodes**: Uses the `statfs` syscall mapped directly to each mount path.

**Mathematical Models / Formatting:**
- **Device Filtering**: Eliminates pseudo-filesystems by using a Source-Device Heuristic. Excludes paths lacking `/dev/`, `:` or `@`, and explicitly blacklists `tmpfs` and `devtmpfs`.
- **Capacity**: Converts total, used, and free blocks into GiB. Calculates `usage_pct` dynamically based on available blocks.
- **Inode Metrics**: Returns exact `total`, `used`, and `free` inode counts with a `usage_pct`, crucial for detecting MDT metadata exhaustion in Lustre/NFS which can cause "disk full" errors even with 99% capacity free.
- **Hung Mount Trap Protection**: Wraps every `statfs` syscall in a goroutine with a strict 2000ms context timeout. If a network filesystem (e.g., Lustre OSS) goes offline, the `statfs` call blocks in an uninterruptible sleep (D-state). The tool safely abandons the stalled goroutine and sets the mount status to `hung`, instantly doubling as an availability health check for remote file systems.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/self/mountinfo` is missing or inaccessible.
- Returns `status: "hung"` and populates the `error` field if the `statfs` system call exceeds the 2-second timeout, rather than returning a generic error and failing the execution.

---

#### `get_nvme_smart_log`
*Category: `storage` · Runs on: Nodes with `nvme-cli` or `smartctl` installed*

Audits the physical health, thermal state, and silicon degradation of NVMe storage media by querying controller SMART logs, identifying thermal throttling risk and imminent drive failure before it corrupts data. Accepts an optional `target_device` parameter (e.g., `nvme1`) to audit a single controller; audits all discovered controllers when omitted.

**Data Sources:**
- **Device Discovery**: Enumerates NVMe controllers (`nvme0`, `nvme1`, …) from `/sys/class/nvme/` — no external binary needed for discovery.
- **Primary**: `nvme smart-log /dev/<dev> -o json` (nvme-cli).
- **Fallback**: `smartctl -a -j /dev/<dev>`, parsing the `nvme_smart_health_information_log` block.
- **Privilege Escalation**: NVMe admin ioctls require root; when running unprivileged, commands are automatically wrapped in non-interactive `sudo -n` if `sudo` is in `PATH`.

**Mathematical Models / Formatting:**
- **Temperature Unit Heuristic**: Controllers report temperature in Kelvin or Celsius depending on tooling; values `> 200` are treated as Kelvin and converted (`value − 273`) to a normalized `temperature_c`.
- **Per-Drive Warning Heuristics**: Populates a `warning_reasons []string` array from four rules — temperature `> 75°C` (thermal throttling likely), `percent_used > 90` (rated lifespan nearly consumed), `media_errors > 0` (unrecovered data-integrity failures), and a non-zero controller `critical_warning` bitmask. Any warning flips the drive's `status` from `healthy` to `critical`.
- **System Summary**: Aggregates `drives_audited`, `critical_warnings_detected` (count of drives with a critical-warning bitmask), and `media_errors_detected` (summed across drives).

**Degradation Profile:**
- `IsSupported()` returns `false` only if **neither** `nvme` nor `smartctl` is in `PATH`.
- **No NVMe Hardware**: An empty `/sys/class/nvme/` is a valid state — returns `drives_audited: 0` with empty results rather than failing.
- **Permission Lockout**: If the ioctl is refused (`permission denied`, `operation not permitted`, or sudo demanding a password), returns an explicit `Unauthorized` error instructing that root or passwordless sudo is required — rather than silently returning partial data.
- **Per-Device Fallback Chain**: If nvme-cli output fails to parse for a device, the tool retries via smartctl; if both fail non-fatally, the device is skipped and remaining drives are still audited.

---

#### `get_lustre_client_stats`
*Category: `storage` · Runs on: Lustre Client nodes*

Collects Lustre client stats, read-ahead performance, and active MDT/OST connections.

**Data Sources:**
- **Lustre Version**: `/sys/fs/lustre/version` (or `/proc/fs/lustre/version`).
- **Filesystem Stats**: `/sys/fs/lustre/llite/<client>/stats` (or `/proc/...`).
- **Read-Ahead Stats**: `/sys/fs/lustre/llite/<client>/read_ahead_stats` (or `/proc/...`).
- **Readahead Tunables**: `max_read_ahead_mb`, `max_read_ahead_per_file_mb`, and `max_read_ahead_whole_mb` files in `/sys/fs/lustre/llite/<client>/`.
- **Connections & Timeouts**: `/sys/fs/lustre/{osc,mdc}/*/import`, `/sys/fs/lustre/{osc,mdc}/*/active`, and `/sys/fs/lustre/{osc,mdc}/*/timeouts`.

**Mathematical Models / Formatting:**
- **Hit Rate**: Computes read-ahead cache hit rate: `hits / (hits + misses) * 100`.
- **Active Connections Check**: Computes ratio of active/total MDT and OST connections based on `state` (must be `FULL`) and `active` status (must be `1`).
- **RPC Timeouts**: Extracts worst-case and current adaptive timeouts (`cur`, `worst`), alongside raw RPC timeouts from import files.

**Degradation Profile:**
- `IsSupported()` returns `false` if neither `/sys/fs/lustre` nor `/proc/fs/lustre` exists.
- If specific files (like `timeouts` or readahead parameters) are missing, the tool degrades gracefully by omitting them or reporting defaults, returning a partial results structure instead of failing.

---

#### `get_nfs_client_stats`
*Category: `storage` · Runs on: NFS Client nodes*

Collects NFS client statistics, active server connections, mounted volumes, and RPC transport / backlog metrics.

**Data Sources:**
- **NFS Servers**: `/proc/fs/nfsfs/servers` (if available).
- **NFS Volumes**: `/proc/fs/nfsfs/volumes` (if available).
- **Mount Statistics**: `/proc/self/mountstats` (or `/proc/mountstats`).

**Mathematical Models / Formatting:**
- **RPC Backlog**: Sums cumulative backlog metrics across all mounted NFS instances.
- **Protocol & Transport**: Extracts protocol type (`tcp`/`udp`), ports, age, active requests, max slots, sends, receives, and bad transaction IDs (`bad_xids`).

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/self/mountstats` and `/proc/mountstats` are both missing.
- If `/proc/fs/nfsfs/` files do not exist (e.g. NFS client module is not loaded or there are no active NFS connections), the tool degrades gracefully by reporting empty servers/volumes list and parsing mount stats only.

---

#### `get_raid_health`
*Category: `storage` · Runs on: Nodes with Linux software RAID (md driver loaded)*

Audits Linux software RAID (md) array health natively from the kernel — no mdadm dependency — reporting per-array state, degradation, rebuild/resync progress, mismatch counts, and per-member device states, and flagging faulty members and arrays running at reduced redundancy.

**Data Sources:**
- **Support Gate**: `/proc/mdstat` presence indicates the md driver is loaded.
- **Array Discovery**: Entries under `/sys/block/md*` that contain an `md/` subdirectory (the definitive array marker, which also excludes partitions like `md0p1`).
- **Per-Array Attributes**: `/sys/block/md*/md/{array_state,degraded,sync_action,sync_completed,mismatch_cnt,raid_disks,level}`.
- **Per-Member State**: `/sys/block/md*/md/dev-*/state` (`in_sync`, `faulty`, `spare`, ...).

**Mathematical Models / Formatting:**
- **Rebuild Progress**: `rebuild_pct` is computed from the `sync_completed` fraction (`N / M` sectors) as `N / M × 100`, rounded to 1 decimal place; `none`, a zero denominator, or unparseable content yield `0`.
- **Per-Array Warning Heuristics**: Populates `warning_reasons []string` from four rules — `degraded=1`, any member whose state contains `faulty` (also promoted into `failed_members[]`), an active `sync_action` of `recover`/`resync` (redundancy not at full strength; `check`/`repair` scrubs are not warnings), and `mismatch_cnt > 0` (blocks inconsistent between mirrors/parity).
- **Summary Aggregation**: `summary` counts `total`, `degraded_count`, and `rebuilding_count` (arrays with `sync_action` of `recover`/`resync`); any warning flips the result status from `ok` to `warning`.
- **Determinism**: Arrays and members are sorted by name for stable, diff-friendly output.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/proc/mdstat` is missing (md driver not loaded).
- **No Arrays**: A node with the md driver loaded but zero arrays is a valid state — returns `arrays: []` with an `ok` status.
- **Missing Attributes**: Individual missing/unreadable sysfs attributes degrade to zero values (`""`, `0`, `false`) instead of failing the array or the execution.
- **Cancellation**: A cancelled context returns an encapsulated error result, never a hard Go error.

---

#### `get_multipath_status`
*Category: `storage` · Runs on: SAN-attached nodes with dm-multipath maps or multipath-tools installed*

Audits device-mapper multipath (dm-multipath) maps and their individual SAN paths, flagging offline/failed paths and — critically — maps that have lost every active path (all I/O to that LUN fails).

**Data Sources:**
- **Map Discovery (native)**: Scans `/sys/block/dm-*/dm/uuid` for the `mpath-` prefix; LVM, dm-crypt, and other dm targets are excluded. Friendly names come from `dm/name`.
- **Path Membership (native)**: The map's `slaves/` directory lists its component path devices.
- **Per-Path State (native)**: `/sys/block/<slave>/device/state` (`running` / `offline`).
- **Enrichment (optional)**: `multipathd show maps raw format "%n %w %N %t"` adds the device-mapper state (`dm_state`, e.g. `active`/`suspend`) per map when the daemon is reachable.

**Mathematical Models / Formatting:**
- **Path Accounting**: `active_paths` counts paths in the `running` state; `failed_paths = total_paths − active_paths`. A missing per-path state file degrades to `unknown` and is counted as failed.
- **Per-Map Warning Heuristics**: Populates `warning_reasons []string` with one entry per non-running path, plus an explicit `CRITICAL: no active paths remaining` entry when a map with paths has zero active ones.
- **Status Escalation**: Any map with zero active paths drives the result status to `error`; any failed path (with redundancy remaining) drives `warning`; otherwise `ok`.
- **Summary Aggregation**: `summary` precomputes `total_maps`, `maps_with_failed_paths`, and `maps_with_no_active_paths`. Maps and paths are sorted by name for stable output.

**Degradation Profile:**
- `IsSupported()` returns `false` only when there is no `mpath-` dm device in sysfs **and** neither `multipath` nor `multipathd` is in `PATH` (reason: "no multipath devices and multipathd not installed").
- **No Maps**: Zero multipath maps is a valid state — returns `maps: []` with an `ok` status.
- **Daemon Failures Ignored**: multipathd socket/permission errors never affect results — native sysfs is the source of truth and enrichment is best-effort (`dm_state` is simply omitted).
- **Cancellation**: A cancelled context returns an encapsulated error result, never a hard Go error.

---

#### `get_smart_health`
*Category: `storage` · Runs on: Nodes with `smartctl` (smartmontools) installed*

Audits the SMART health of SATA/SAS drives (`sd*`) to catch failing spinning disks and SSDs before data loss, reducing full smartctl output to the failure-predictive core: overall self-assessment, temperature, power-on hours, and the four canonical defect/link counters. Accepts an optional `target_device` parameter (e.g., `sda`) to audit a single drive; audits all discovered drives when omitted. NVMe devices are excluded — they are covered by `get_nvme_smart_log`.

**Data Sources:**
- **Device Discovery**: Enumerates `/sys/class/block` entries matching `^sd[a-z]+$` that have a physical `device/` entry (partitions and virtual devices excluded) — no external binary needed for discovery. `device/vendor` + `device/model` provide the identity fallback when smartctl reports no `model_name`.
- **Primary**: `smartctl -a -j /dev/<dev>`, parsing `smart_status.passed`, `temperature.current`, `power_on_time.hours`, and `ata_smart_attributes.table[]` raw values for IDs 5 (Reallocated_Sector_Ct), 197 (Current_Pending_Sector), 198 (Offline_Uncorrectable), and 199 (UDMA_CRC_Error_Count).
- **Privilege Escalation**: SMART ioctls require root; when running unprivileged, commands are automatically wrapped in non-interactive `sudo -n` if `sudo` is in `PATH` (same contract as `get_nvme_smart_log`).

**Mathematical Models / Formatting:**
- **Exit-Bitmask Tolerance**: smartctl exits non-zero when SMART checks fail even though its JSON is valid, so output is parsed regardless of exit status; only unparseable output counts as a per-drive failure.
- **Per-Drive Warning Heuristics**: Populates `warning_reasons []string` from six rules — failed self-assessment, `reallocated_sectors > 0` (grown defects), `pending_sectors > 0` (unstable sectors), `uncorrectable_sectors > 0`, `crc_errors > 0` (cabling/backplane link integrity), and temperature `> 60°C`. Any warning flips the drive's `status` from `healthy` to `critical`.
- **Status Escalation**: A failed SMART self-assessment drives the result status to `error`; attribute-level findings alone drive `warning`; otherwise `ok`.
- **System Summary**: Aggregates `drives_audited`, `drives_failing` (drives in `critical` status), and `total_reallocated` (summed across drives).

**Degradation Profile:**
- `IsSupported()` returns `false` only if `smartctl` is not in `PATH`.
- **No SATA/SAS Hardware**: Zero matching drives is a valid state — returns `drives: []` with `drives_audited: 0` rather than failing.
- **Permission Lockout**: If the command is refused (`permission denied`, `operation not permitted`, or sudo demanding a password), returns an explicit `Unauthorized` error instructing that root or passwordless sudo is required — rather than silently returning partial data.
- **Per-Drive Skip**: A drive whose output fails to parse (USB bridge without SAT, dead device) is skipped and the remaining drives are still audited.
- **Cancellation**: A cancelled context returns an encapsulated error result, never a hard Go error.

---

#### `get_zfs_status`
*Category: `storage` · Runs on: Nodes with the ZFS kernel module loaded or `zpool` installed*

Audits the ZFS storage stack: per-pool health, capacity, fragmentation, scrub/resilver progress, and device error counters, plus ARC cache efficiency — combining native kernel counters with the zpool CLI's stable parseable output.

**Data Sources:**
- **ARC (native)**: `/proc/spl/kstat/zfs/arcstats` 3-column kstat rows (`size`, `c_max`, `hits`, `misses`).
- **Module Version (native)**: `/sys/module/zfs/version`.
- **Pool Inventory**: `zpool list -Hp -o name,size,alloc,free,frag,cap,health` (script-friendly: no header, exact byte values). `zpool` JSON output requires ZFS ≥ 2.3, so the stable text/parseable formats are used instead.
- **Pool Detail**: `zpool status <pool>` text — the `scan:` line for scrub/resilver state and the config table for per-device READ/WRITE/CKSUM counters.

**Mathematical Models / Formatting:**
- **ARC Efficiency**: `hit_ratio_pct = hits / (hits + misses) × 100`, rounded to 2 decimal places (0 when there are no lookups); `size_mb`/`max_mb` converted from bytes (MiB).
- **Capacity Conversion**: `size_gb`/`alloc_gb` converted from exact bytes to GiB, rounded to 1 decimal place; `frag_pct`/`capacity_pct` taken from zpool's precomputed percentages.
- **Scan Classification**: The `scan:` line is normalized to `scrub in progress`, `resilver in progress`, `scrub repaired`, `resilvered`, or `none`; in-progress scans include `scan_pct` extracted from the `NN.NN% done` progress figure.
- **Error Accounting**: READ/WRITE/CKSUM counters are summed across every row of the config tree (pool, vdevs, and leaf devices) with human-readable magnitude suffixes (`1.05K`, `3M`, ...) expanded; dashes and garbage degrade to 0.
- **Per-Pool Warning Heuristics**: Populates `warning_reasons []string` from three rules — health ≠ `ONLINE`, any error counter > 0, and `capacity_pct > 90` (ZFS performance degrades when nearly full). Any warning flips the result status to `warning`.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/sys/module/zfs` is absent **and** `zpool` is not in `PATH`.
- **No Pools**: Zero imported pools is a valid state — returns `pools: []` with an `ok` status.
- **ARC Missing**: If `arcstats` is unreadable (module unloaded, procfs restricted), the `arc` block is omitted entirely rather than reporting zeros.
- **Binary Missing / CLI Failure**: If the kernel module is loaded but `zpool` is missing or `zpool list` fails, the tool degrades to an ARC-only report with an explanatory `note` and a `degraded` status. A per-pool `zpool status` failure keeps the list-derived health/capacity data (scan state falls back to `none`).
- **Cancellation**: A cancelled context returns an encapsulated error result, never a hard Go error.

---

#### `get_open_files`
*Category: `storage` · Runs on: Every Linux node*

Finds every process holding open file descriptors under a path prefix — the native (binary-free) answer to "why can't this filesystem unmount?" and "what is still writing to this mount?".

**Data Sources:**
- **FD Tables**: `/proc/<pid>/fd/*` symlinks resolved via `readlink` for every numeric `/proc` entry (no `lsof`/`fuser` binaries).
- **Process Names**: `/proc/<pid>/comm`.
- **Deleted Files**: readlink targets ending in `" (deleted)"` mark files that hold disk space until closed.

**Mathematical Models / Formatting:**
- **Prefix Matching**: fd targets are matched with a plain prefix comparison against the required `path` parameter (so `/mnt/lustre` also matches `/mnt/lustre2`; pass a trailing slash to bind to a directory). The deleted suffix is stripped before matching.
- **Ranking & Caps**: processes are ranked by matching `fd_count` (ties broken by pid) and truncated to `limit` (default 20, cap 100); each entry carries up to 3 `example_paths` and a `has_deleted` flag.
- **Totals**: `total_matching_fds` and `deleted_open_count` cover the full scan, not just the truncated process list.

**Degradation Profile:**
- `IsSupported()` returns `false` off-Linux or when `<procfs>/self/fd` is not readable.
- fd directories unreadable due to permissions (other users' processes when unprivileged) are counted in `processes_skipped` — never an error.
- Processes that vanish mid-scan (fd dir or individual fds gone) are skipped silently; dead-pid comm reads degrade to `"unknown"`.
- Missing/empty `path`, malformed arguments, and cancelled contexts (checked between pid iterations) are encapsulated as error results (never hard Go errors).

---

#### `get_lustre_server_stats`
*Category: `storage` · Runs on: Lustre Server nodes (OSS/MDS/MGS)*

Collects per-target statistics for Lustre server roles: OSS (`obdfilter`), MDS (`mdt`), and MGS (`mgs`) — export counts, capacity/inode utilization, top RPC/IO counters, and the OSS disk-I/O-size distribution.

**Data Sources:**
- **Target Discovery & Roles**: subdirectories of `/sys/fs/lustre/{obdfilter,mdt,mgs}/` (or `/proc/fs/lustre/...`); each file is resolved sysfs-first with a per-file procfs fallback, matching the split layout of real Lustre releases.
- **OSS Targets**: `obdfilter/<target>/{num_exports,kbytestotal,kbytesfree,filestotal,filesfree,stats,brw_stats}`.
- **MDS Targets**: `mdt/<target>/{num_exports,stats,md_stats}` (open/close/getattr/setattr counters), plus capacity/inode files when exposed.
- **MGS Target**: `mgs/MGS/{num_exports,stats}`.

**Mathematical Models / Formatting:**
- **Utilization**: precomputes `used_pct = (total - free) / total * 100` (rounded to 2 decimals) for both capacity (`kbytestotal`/`kbytesfree`) and inodes (`filestotal`/`filesfree`).
- **Top Counters**: parses `name count samples [unit] min max sum` lines from `stats` (merged with `md_stats` on MDTs) and keeps the top-10 counters by count, ties broken by name for deterministic output.
- **brw_stats Condensation** (OSS only): parses only the `disk I/O size` histogram section and emits the top-5 non-zero buckets per direction as `{size, pct}` (e.g. `1M` / `43%`), dropping zero-sample buckets.
- **Role Summary**: `role_summary {is_oss, is_mds, is_mgs}` derived from which target-type directories contain entries.
- **Target Filter**: optional `target` parameter restricts output to a single target name; an unknown name returns an error result listing the requested target.

**Degradation Profile:**
- `IsSupported()` returns `false` via `registry.DetectNodeRoles()` when the node has no OSS/MDS/MGS role ("node has no Lustre server targets").
- Missing per-target files (e.g. `num_exports`, capacity counters, `stats`) degrade to zero values / empty `top_stats` — a partial entry is always returned instead of failing.
- Missing or unparseable `brw_stats` omits the `brw_io_sizes` block entirely.
- Zero discovered targets returns a `warning` status ("no Lustre server targets found") rather than an error; cancelled contexts and malformed arguments are encapsulated as error results.

---

### Network & Fabric Tools

#### `get_network_interfaces`
*Category: `network` · Runs on: Every Linux node*

Collects network interface information including MAC addresses, IP configurations (IPv4/IPv6), MTU values, and L2 link operational states.

**Data Sources:**
- **Primary**: `/sys/class/net/` sysfs directory (excluding loopback `lo`) and Go's standard library `net` package interfaces.
- **Fallback**: `ip -j addr` command output (executed via shell).

**Mathematical Models / Formatting:**
- **State Normalization**: Operational states (`operstate`) are normalized to lowercase (e.g. `"up"`, `"down"`, `"unknown"`).
- **Metric Merging**: Merges speed (Mbps), carrier state, and duplex configuration read from sysfs files with IP configuration returned by the Go runtime or the command fallback.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/sys/class/net` does not exist and the `ip` binary is not in `PATH`.
- If standard Go `net` package address resolution fails or misses an interface, the tool falls back to running `ip -j addr` to resolve IPs. Loopback interface `lo` is filtered out by default, but virtual bridges (`virbr*`) and tunnels (`vnet*`) are included.

---

#### `get_routing_table`
*Category: `network` · Runs on: Every Linux node*

Collects the active IPv4 and IPv6 system routing table entries and default gateway targets.

**Data Sources:**
- **Primary**: `/proc/net/route` (IPv4 routing table) and `/proc/net/ipv6_route` (IPv6 routing table) text files.
- **Fallback**: `ip -j route` (IPv4) and `ip -6 -j route` (IPv6) command outputs.

**Mathematical Models / Formatting:**
- **Hex Decoding**: Decodes destination, gateway, and mask addresses from kernel little-endian host hex representation (IPv4) and big-endian 32-character hex strings (IPv6).
- **Prefix Masks**: Converts IPv4 hexadecimal masks into unified prefix length integers.
- **Gateway Count**: Identifies default route targets (IPv4 destination `0.0.0.0` or IPv6 destination `::` with a non-zero gateway).

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/net` does not exist and the `ip` binary is not in `PATH`.
- If native `/proc` reads fail or return empty routes, the tool runs command fallbacks `ip -j route` and `ip -6 -j route` respectively to gather routing configurations.

---

#### `get_socket_stats`
*Category: `network` · Runs on: Every Linux node*

Collects system network socket statistics for active and listening ports.

**Data Sources:**
- **Primary**: `/proc/net/tcp` & `/proc/net/udp` (IPv4) and `/proc/net/tcp6` & `/proc/net/udp6` (IPv6) text files.
- **Fallback**: `ss -t -u -a -n -H` command output (executed via shell).

**Mathematical Models / Formatting:**
- **Hex Decoding**: Decodes hex IP addresses and ports. Swaps bytes of 32-bit segments to reconstruct big-endian representations from host-endian (little-endian on x86_64) formatted kernel strings in IPv6 procfs records.
- **State Mappings**: Maps numerical socket states to standard protocol connection names (e.g. `ESTABLISHED`, `LISTEN`, `CLOSE_WAIT`, `TIME_WAIT`).
- **Connection Counts**: Aggregates total socket counts, including statistics grouped by active states.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/net` does not exist and the `ss` binary is not in `PATH`.
- If native procfs reads fail or return no active sockets, the tool runs fallback command `ss -t -u -a -n -H` and parses the text response.

---

#### `get_nic_ethtool_stats`
*Category: `network` · Runs on: Every Linux node*

Queries network interface card (NIC) buffer rings, queue allocations, and driver telemetry.

**Data Sources:**
- **Primary**: `/sys/class/net/<iface>/statistics/` files for drop/error counters.
- **Fallback**: `ethtool -g`, `ethtool -l`, and `ethtool -S` command outputs (executed via shell).

**Mathematical Models / Formatting:**
- **Native Reads**: Directly parses sysfs `/sys/class/net/` counters to obtain network errors, stack drops, and FIFO overruns without subprocess overhead.
- **Ethtool Parsers**: Decodes ring settings (`ethtool -g`), queue channels configuration (`ethtool -l`), and granular driver statistics (`ethtool -S`) into structured configurations.
- **Drops Count**: Aggregates total RX and TX drops across all monitored interfaces.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/sys/class/net` does not exist and the `ethtool` binary is not in `PATH`.
- If native statistics directories are unreadable, standard metrics default to `0`. If ethtool commands are blocked, fail, or return `n/a`, the respective config maps/structs are gracefully omitted from the final payload.

---

#### `get_eth_hardware_stats`
*Category: `network` · Runs on: Every Linux node*

Deep audit of ethernet hardware configurations, queue limits, coalescing states, and driver stats.

**Data Sources:**
- **Primary**: `/sys/class/net/<iface>/statistics/` files for drop/error counters.
- **Fallback**: `ethtool -g`, `ethtool -c`, and `ethtool -S` command outputs (executed via shell).

**Mathematical Models / Formatting:**
- **Native Reads**: Directly parses sysfs `/sys/class/net/` counters to obtain network errors, stack drops, and FIFO overruns without subprocess overhead.
- **Ethtool Parsers**: Decodes ring configurations (`ethtool -g`), interrupt coalescing delay and frame limits (`ethtool -c`), and custom PHY driver statistics (`ethtool -S`).
- **Drops Count**: Aggregates total RX and TX drops across all monitored interfaces.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/sys/class/net` does not exist and the `ethtool` binary is not in `PATH`.
- If native statistics directories are unreadable, standard metrics default to `0`. If ethtool commands are blocked, fail, or return `n/a`, the respective config maps/structs are gracefully omitted from the final payload.

---

#### `get_routing_rules`
*Category: `network` · Runs on: Every Linux node*

Query the system Routing Policy Database (RPDB) rules (i.e. policy routing).

**Data Sources:**
- **Primary**: `ip -j rule` (JSON output)
- **Fallback**: `ip rule` (plain text output)

**Mathematical Models / Formatting:**
- **JSON Parsing**: Directly maps structural keys to rules fields (priority, src, dst, table, proto, iif, oif, fwmark, tos).
- **Text Parser**: Falls back to tokenizing space-separated `ip rule` outputs, mapping matching rule attributes.

**Degradation Profile:**
- `IsSupported()` returns `false` if the `ip` binary is not in `PATH`.
- If JSON execution fails, fallback text output is evaluated.

---

#### `get_infiniband_status`
*Category: `network` · Runs on: Nodes with InfiniBand/RoCE HCAs (sysfs `class/infiniband` present)*

Audits every InfiniBand/RoCE HCA port natively from sysfs, decoding link state, physical state, rate, LID, and per-port error counters, and raising deterministic warnings for non-ACTIVE links or nonzero error counters.

**Data Sources:**
- **Primary**: `/sys/class/infiniband/<hca>/ports/<n>/{state,phys_state,rate,lid,link_layer}` sysfs attributes.
- **Primary**: `/sys/class/infiniband/<hca>/ports/<n>/counters/` error counter files: `symbol_error`, `link_downed`, `link_error_recovery`, `port_rcv_errors`, `port_xmit_discards`.
- No external binaries are executed.

**Mathematical Models / Formatting:**
- **State Decoding**: Strips numeric prefixes from kernel state strings (`"4: ACTIVE"` → `ACTIVE`, `"5: LinkUp"` → `LinkUp`).
- **Rate Decoding**: Preserves the raw rate string (`"100 Gb/sec (4X EDR)"`) and precomputes a numeric `rate_gbps` field for the leading speed value.
- **LID Decoding**: Hex-decodes the sysfs `lid` attribute (`0x3` → `3`) into an integer.
- **Warning Reasons**: Per-port deterministic warnings when `state != ACTIVE`, `phys_state != LinkUp`, or any error counter is nonzero.
- **System Summary**: Precomputes `hca_count`, `total_ports`, `active_ports`, and `ports_with_errors`; the result status escalates to `warning` when any warning reason exists.

**Degradation Profile:**
- `IsSupported()` returns `false` with reason "no InfiniBand devices (sysfs path missing)" when `<sysfs>/class/infiniband` does not exist.
- Absent counter files (common on RoCE ports) are omitted from the `counters` map rather than zero-filled; a fully absent `counters/` directory omits the map entirely.
- RoCE ports (`link_layer: Ethernet`) are reported factually with the same fields and warning logic; no special-casing.
- HCAs without a readable `ports/` directory and non-numeric entries in `ports/` are skipped; missing attribute files degrade to empty/zero fields instead of failing.
- A canceled context or an unreadable `class/infiniband` directory returns an encapsulated `error` result (never a hard Go error).

---

#### `get_lnet_status`
*Category: `network` · Runs on: Nodes with the LNet kernel module loaded or `lnetctl` installed (Lustre servers, routers, and clients)*

Reports the health of the LNet fabric layer used by Lustre: local network interfaces (NIs) with up/down status and tx credit levels, known peer count, and global message counters (send/recv/route/drop/errors).

**Data Sources:**
- **Primary**: `/sys/kernel/debug/lnet/nis` (NI status, refs, max/tx/min credits), `/sys/kernel/debug/lnet/peers` (peer count), and `/sys/kernel/debug/lnet/stats` (positional message counters). debugfs typically requires root.
- **Fallback**: `lnetctl net show` and `lnetctl stats show` command outputs, parsed by simple `key: value` indentation scanning (no YAML library), wrapped with `sudo -n` when not running as root.

**Mathematical Models / Formatting:**
- **Positional Decoding**: The single-line `stats` file is decoded into named counters (`msgs_alloc msgs_max errors send_count recv_count route_count drop_count send_length recv_length route_length drop_length`).
- **Credit Semantics**: `max/tx/min` credits are emitted as optional fields; a negative minimum tx credit means messages had to queue waiting for credits (fabric congestion). Credits are debugfs-only and omitted on the `lnetctl` path instead of emitting misleading zeros.
- **Warning Reasons**: Deterministic triggers — any NI whose status is not `up` (NID lists capped at 5 entries), negative `min` tx credits (credit starvation), and `drop_count > 0`. Any trigger flips the result status to `warning`.
- **Source Attribution**: The `source` field records whether `debugfs` or `lnetctl` served the data.

**Degradation Profile:**
- `IsSupported()` returns `false` with reason `"LNet not loaded and lnetctl not found"` if `/sys/module/lnet` does not exist and `lnetctl` is not in `PATH`.
- If `/sys/kernel/debug/lnet/nis` is unreadable (non-root debugfs), the tool falls back to `lnetctl net show` / `lnetctl stats show`. Peer count is debugfs-only and reported with `peers_available: false` on the fallback path.
- If debugfs is unreadable and `lnetctl` is missing or denied, the tool returns an error result explaining the root / passwordless-sudo requirement. Missing `peers`/`stats` files degrade gracefully (`peers_available` / `stats_available` set to `false`) rather than failing.

---

#### `get_bond_status`
*Category: `network` · Runs on: Nodes with the Linux bonding module loaded (`/proc/net/bonding` present)*

Audits every configured bonding (link aggregation) device by parsing the kernel bonding driver's procfs report, extracting bond mode, MII status, the currently active slave, per-slave link health, and 802.3ad (LACP) aggregator details when present.

**Data Sources:**
- **Primary**: `/proc/net/bonding/<bond>` text files (one per configured bond) exposed by the bonding kernel module.
- Bond-level fields parsed: `Bonding Mode:`, `MII Status:`, `Currently Active Slave:`, and 802.3ad info (`LACP rate:`, Active Aggregator `Partner Mac Address:`).
- Per-slave sections parsed: `Slave Interface:`, `MII Status:`, `Speed:`, `Duplex:`, `Link Failure Count:`.
- No external binaries are executed.

**Mathematical Models / Formatting:**
- **Speed Decoding**: Converts the kernel `Speed:` field (`"25000 Mbps"`) into an integer `speed_mbps`; `Unknown`, negative, or malformed speeds decode to `0`.
- **Active Slave Normalization**: `Currently Active Slave: None` is decoded to an empty field instead of the literal string `None`.
- **Warning Reasons**: Per-bond deterministic warnings when the bond MII status is not `up`, any slave MII status is not `up`, or any slave has a nonzero `Link Failure Count`.
- **System Summary**: Precomputes `total_bonds`, `bonds_up`, `total_slaves`, `slaves_up`, and `bonds_with_warnings`; the result status escalates to `warning` when any warning reason exists.

**Degradation Profile:**
- `IsSupported()` returns `false` when `<procfs>/net/bonding` does not exist (bonding module not loaded).
- Unparseable or missing fields degrade to empty strings / zero values — parsing never fails on garbage content.
- Indented per-slave LACP PDU detail lines (e.g. `system mac address:`) are deliberately not matched, so partner data cannot leak into slave fields.
- An empty bonding directory is a valid result (`bonds: []`, status `ok`); unreadable individual bond files are skipped.
- A canceled context or an unreadable bonding directory returns an encapsulated `error` result (never a hard Go error).

---

#### `get_arp_neighbors`
*Category: `network` · Runs on: Every Linux node (procfs ARP table or `ip` binary)*

Collects the kernel neighbor (ARP/NDP) tables, classifies every entry by resolution state, and surfaces broken address resolution first: FAILED and INCOMPLETE neighbors are prioritized ahead of healthy ones, and every FAILED neighbor is always listed in full.

**Data Sources:**
- **Primary**: `/proc/net/arp` text table (IPv4; columns IP / HWtype / Flags / HWaddress / Mask / Device).
- **Fallback/Enrichment**: `ip -j neigh` JSON output (adds IPv6 neighbors and granular NUD states: `reachable`, `stale`, `failed`, `incomplete`, `permanent`, `delay`, `probe`).

**Mathematical Models / Formatting:**
- **Flag Decoding**: procfs hex flags decode by bit — `ATF_PERM` (`0x4`, thus also `0x6`) → `permanent`, `ATF_COM` (`0x2`) → `reachable`, `0x0` → `incomplete`; unparseable flags → `unknown`.
- **Enrichment Merge**: `ip neigh` entries are overlaid onto procfs entries keyed by `(ip, device)`; granular NUD states win over coarse flag mappings, MACs are filled in when missing, and unmatched entries (e.g., IPv6) are appended.
- **MAC Normalization**: The all-zero placeholder MAC (`00:00:00:00:00:00`) of incomplete entries is blanked.
- **Prioritized Capping**: Entries are stably sorted `failed` → `incomplete` → all other states, then capped at 50 (`entries_shown`, `truncated`); `total_entries` and `counts_by_state` always reflect the full table, and the `failed` list is never truncated.
- **Warning Reasons**: A warning is raised (status `warning`) when any neighbor is in the FAILED state.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/proc/net/arp` is missing **and** the `ip` binary is not in `PATH`.
- If `ip` is missing, fails to execute, or returns unparseable JSON, the tool degrades to procfs-only IPv4 data, sets `ipv6_included: false`, and records the reason in a `note` field.
- If `/proc/net/arp` is unreadable but `ip -j neigh` works, the ip output alone is used.
- When neither source is usable, or the context is canceled, an encapsulated `error` result is returned (never a hard Go error).

---

#### `get_conntrack_summary`
*Category: `network` · Runs on: Every Linux node with the nf_conntrack module loaded*

Summarizes netfilter connection-tracking table pressure: current entry count vs table capacity with a precomputed usage percentage, plus per-CPU failure counters summed across all CPUs. This is the go-to check when a busy node starts logging `nf_conntrack: table full, dropping packet`.

**Data Sources:**
- **Primary**: `/proc/sys/net/netfilter/nf_conntrack_count` (current tracked connections), `/proc/sys/net/netfilter/nf_conntrack_max` (table capacity), and `/proc/net/stat/nf_conntrack` (header line plus one row of hexadecimal counters per CPU).
- **Fallback**: None required — all sources are world-readable procfs files.

**Mathematical Models / Formatting:**
- **Usage Percentage**: `usage_pct = round(count / max × 100, 2dp)`, guarded to `0` when `max` is unavailable.
- **Header-Driven Hex Decoding**: `/proc/net/stat/nf_conntrack` columns vary across kernel versions (`searched`/`delete_list` vs `clashres`/`chainlength`), so columns are resolved by header name rather than position. Each per-CPU row is parsed as base-16 and summed column-wise; the row count is reported as `cpu_count`.
- **Counter Selection**: Only failure-relevant sums are emitted (`invalid`, `insert_failed`, `drop`, `early_drop`, `search_restart`) instead of the full raw matrix.
- **Warning Reasons**: Deterministic triggers — `usage_pct > 80`, and non-zero `drop`, `early_drop`, or `insert_failed` sums. Any trigger flips the result status to `warning`.

**Degradation Profile:**
- `IsSupported()` returns `false` with reason `"conntrack not loaded"` if `nf_conntrack_count` does not exist (conntrack module absent).
- If `/proc/net/stat/nf_conntrack` is missing or unparsable, the tool degrades to counts only and sets `counters_available: false` (status stays `ok`).
- If `nf_conntrack_max` is unreadable, `max` and `usage_pct` are reported as `0` rather than failing. Only an unreadable `nf_conntrack_count` produces an error result.

---

#### `get_firewall_summary`
*Category: `network` · Runs on: Every Linux node with `nft`, `iptables-save`, or `iptables` in `PATH`*

Summarizes the host packet-filter configuration without dumping the full ruleset: the active backend, every table and chain with its policy, per-chain rule counts, and packet/byte counters. A factual tool — it reports state and emits no warnings by default. When both `table` and `chain` parameters are provided, the matching chain's individual rules are rendered compactly (capped at 100).

**Data Sources:**
- **Primary**: `nft -j list ruleset` JSON output (`nftables[]` array of `{table}`, `{chain}`, and `{rule}` objects; `metainfo`, sets, and named counters are ignored).
- **Fallback**: `iptables-save -c` text output (`*table` headers, `:CHAIN POLICY [pkts:bytes]` policy counters, `-A` rule lines). As a last resort, `iptables -S` is parsed (filter table only, no counters). Commands are wrapped with `sudo -n` when not running as root.

**Mathematical Models / Formatting:**
- **Counter Semantics**: `packets`/`bytes` per chain are the policy counters from `:CHAIN POLICY [pkts:bytes]` for iptables, but the **sum of anonymous rule counter expressions** for nftables (nftables chains carry no implicit counters). Named counter references (`{"counter": "name"}`) are skipped.
- **Activity Heuristic**: `firewall_active = total_rules > 0`; an empty ruleset is a valid `ok` result with `firewall_active: false`.
- **Rule Rendering**: With `table` + `chain` given, nftables rules are rendered as `family table chain handle N: <compact expr JSON>` (plus comment); iptables rules are the raw `-A ...` lines. Output is capped at 100 rules with `rules_truncated: true` beyond that, and `requested_chain_found` reports whether the chain exists. Chain names are matched case-sensitively (nftables lowercase, iptables uppercase).

**Degradation Profile:**
- `IsSupported()` returns `false` if none of `nft`, `iptables-save`, or `iptables` are in `PATH`.
- If `nft` fails for a non-permission reason (e.g. no nf_tables kernel support), the tool falls back to `iptables-save -c`, then `iptables -S`.
- Permission-denied output from any backend (`permission denied`, `operation not permitted`, sudo password prompts) produces an error result explaining the root / passwordless-sudo requirement instead of a partial answer.

---

### Mesh Infrastructure Tools

#### `get_mesh_topology`
*Category: `mesh` · Runs on: Every node*

Returns a point-in-time snapshot of the mesh topology **as seen by the node it runs on**. All data is sourced locally — zero network traffic. When fanned out to `*`, the union of all nodes' direct-peer relationships produces the complete mesh graph.

**Data Sources:**
- **Gradient Routing Table**: `node.MeshTopology()` reads `AllRoutes()` for all known paths with cost and next-hop.
- **Peer Manager**: Identifies directly connected peers via active yamux sessions.
- **Capability Index**: Aggregates tool/capability registrations learned via gossip.
- **Resolver Cache**: Reports the number of hostname-to-address mappings.

**Output Fields:**
- `node_id`: The identity of the reporting node.
- `direct_peers`: Count of currently connected peers.
- `known_nodes`: Total number of distinct nodes in the routing table (direct + gossip-learned).
- `resolver_entries`: Number of hosts in the resolver cache.
- `node_details[]`: Per-node detail including `node_id`, `impedance` (total path cost), `next_hop` (routing next hop), `capabilities`, and `is_direct` (whether this is a direct peer).

**Degradation Profile:**
- Always available on any mesh node. Returns empty `node_details` if the mesh has no peers.

---

## Hidden Tools (Gateway/Harness Only)

These tools have `Hidden: true` — they do **not** appear in `get_tool_list` and are invisible to the LLM during tool browsing. They remain callable via `call_tool` by the gateway, test harness, and any component that knows their name.

### Lifecycle Management Tools

> **⚠️ Operational Impact**: These tools have real infrastructure side effects. They are hidden to prevent accidental LLM invocation during diagnostic sessions.

#### `node_install`
*Category: `lifecycle` · Hidden: ✅*

Converts an ephemeral node (running from `/tmp`) into persistent infrastructure.

**Operations:**
1. Copies the running binary to `/opt/cortex-mcp/bin/cortex-mcp`
2. Writes a systemd unit file (`cortex-mcp.service`)
3. Runs `systemctl daemon-reload`, `enable`, and `start`

#### `node_uninstall`
*Category: `lifecycle` · Hidden: ✅*

Removes the node — handles both persistent (systemd) and ephemeral (`/tmp`) nodes.

**Operations:**
- **Persistent nodes**: Stops and disables the systemd service, removes the unit file and installed binary.
- **Ephemeral nodes**: Removes the `/tmp` binary and exits the process.

#### `node_restart`
*Category: `lifecycle` · Hidden: ✅*

Restarts the local cortex-mcp systemd service (`systemctl restart cortex-mcp`).

#### `node_stop`
*Category: `lifecycle` · Hidden: ✅*

Gracefully stops the cortex-mcp systemd service without uninstalling (`systemctl stop cortex-mcp`).

#### `node_upgrade`
*Category: `lifecycle` · Hidden: ✅*

Upgrades the node binary and restarts the service.

**Parameters:**
- `path` (string, required): Path to the new binary on the local filesystem.

**Operations:**
1. Copies the new binary from the specified path over `/opt/cortex-mcp/bin/cortex-mcp`
2. Runs `systemctl daemon-reload` and `restart`

#### `node_deploy`
*Category: `lifecycle` · Hidden: ✅*

Deploys the mesh binary to another host via SSH. Any node in the fabric can act as a jumphost.

**Parameters:**
- `target` (string, required): Target host address (e.g., `10.0.1.5` or `host:port`).

---

## Meta-Tools (MCP Gateway Level)

These tools operate at the MCP gateway, not on individual nodes. They are **directly MCP-exposed** — the LLM calls them without going through `call_tool`.

### `get_tool_list`
*Category: `meta` · MCP Access: Direct*

Discovers all available **visible** tools across the mesh. Hidden tools are excluded. Returns name, description, and category for each tool.

**Parameters:**
- `category` (string, optional): Only list tools in this category. Case-insensitive. Valid values: `system`, `compute`, `memory`, `network`, `storage`, `hardware`, `lifecycle`. Invalid values return an error listing the valid categories.

Categories are a closed enum (`registry.Category`) — tools declare them as typed constants, so a misspelled or miscased category cannot compile.

### `get_tool_help`
*Category: `meta` · MCP Access: Direct*

Returns the full JSON schema, parameters, and long description for a specific tool. Works for both visible and hidden tools.

### `call_tool`
*Category: `meta` · MCP Access: Direct*

Invokes any tool in the mesh (visible or hidden). Supports three dispatch modes:
- **Auto-route** (no `node_name`): Routes to the lowest-impedance node offering the tool.
- **Unicast** (exact `node_name`): Sends to a specific node.
- **Fan-out** (pattern `node_name`): Executes on all matching nodes. Supports `*`, nodeset ranges (`node[1-10]`), exclusions (`oss[01-72]!oss[10-15]`), and groups (`@storage`).

### `get_mesh_overview`
*Category: `meta` · MCP Access: Direct*

Provides a complete cluster topology in a single call. Fans out `get_system_info` and `get_mesh_topology` to every node in the mesh, then aggregates the results at the gateway.

**Algorithm:**
1. **Fan-out `get_system_info`** to `*` (all nodes) — collects hostname, OS, arch, CPUs, kernel, distro, and detected storage roles from every node.
2. **Fan-out `get_mesh_topology`** to `*` (all nodes) — collects each node's direct peers, impedance costs, next-hop routing, and capabilities.
3. **Edge deduplication**: Unions all `IsDirect=true` relationships across all nodes to produce the complete mesh graph.
4. **Role count aggregation**: Counts occurrences of each role (SFA, MGS, MDS, OSS, Client, Generic) across all nodes.
5. **Mermaid rendering**: Generates a `graph TD` diagram from the real edge set, with nodes colored by primary role.

**Output includes:**
- `total_nodes`: Number of nodes in the fleet.
- `role_counts`: Map of role → count (e.g., `{"mgs": 4, "mds": 10, "oss": 100, "client": 1000, "generic": 2}`).
- `nodes[]`: Per-node detail (hostname, roles, OS, CPUs, impedance from gateway, direct/transitive status, next-hop, tools).
- `edges[]`: Deduplicated direct connections between nodes.
- `mermaid_graph`: Pre-rendered Mermaid diagram string.

**Degradation Profile:**
- Requires a live mesh with at least one connected node to produce meaningful results.
- Nodes that fail to respond to the fan-out are omitted from the topology (no error propagation).
- Returns sensible defaults (`total_nodes: 0`, empty arrays) for an empty mesh.
