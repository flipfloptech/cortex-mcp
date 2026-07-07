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
