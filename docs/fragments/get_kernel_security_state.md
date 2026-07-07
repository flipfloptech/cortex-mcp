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
