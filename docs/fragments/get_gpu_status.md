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
