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
