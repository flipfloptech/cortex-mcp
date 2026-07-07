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
