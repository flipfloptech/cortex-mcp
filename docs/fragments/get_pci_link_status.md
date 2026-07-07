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
