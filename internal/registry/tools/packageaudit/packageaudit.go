// Package packageaudit implements the get_package_audit tool.
//
// It verifies the installation state and exact versions of a caller-supplied
// list of packages by querying the native package manager database:
// rpm (RHEL/Rocky/SUSE) or dpkg-query (Debian/Ubuntu).
package packageaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// maxQueries caps the number of package names audited per call so a single
// invocation stays bounded in runtime and token cost.
const maxQueries = 50

// PackageStatus is the audit outcome for one package (one manager DB row).
// A single query may expand to several entries when globs match multiple
// packages or multiple versions are installed side by side (e.g. kernels).
type PackageStatus struct {
	// Query is the caller-supplied name/glob that produced this entry.
	Query string `json:"query"`

	// Installed reports whether the manager considers the package installed.
	Installed bool `json:"installed"`

	// Name is the resolved package name (installed entries only).
	Name string `json:"name,omitempty"`

	// Version is the installed version (installed entries only).
	Version string `json:"version,omitempty"`

	// Release is the distro release tag (rpm only).
	Release string `json:"release,omitempty"`

	// Arch is the package architecture (installed entries only).
	Arch string `json:"arch,omitempty"`
}

// Output is the tool's data payload.
type Output struct {
	Manager        string          `json:"manager"`
	Packages       []PackageStatus `json:"packages"`
	InstalledCount int             `json:"installed_count"`
	Missing        []string        `json:"missing"`
}

// Tool implements registry.Tool for get_package_audit.
type Tool struct {
	// execCommand runs a binary and returns its stdout (stdout is still
	// returned alongside the error for non-zero exits). Injectable.
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)

	// execLookPath resolves a binary in PATH. Injectable.
	execLookPath func(file string) (string, error)
}

// New creates the tool with production data sources.
func New() *Tool {
	return &Tool{
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		execLookPath: exec.LookPath,
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_package_audit"
}

// Description returns a one-line summary for get_tool_list.
func (t *Tool) Description() string {
	return "Audit installation state and versions of specific packages via rpm or dpkg"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Audits whether specific packages are installed and at which exact version/release,
using the node's native package manager database. Useful for verifying driver
stacks, security patch levels, and dependency presence across a fleet.

Data Sources:
- rpm:  'rpm -q --queryformat "%{NAME}\t%{VERSION}\t%{RELEASE}\t%{ARCH}\n" <pkg>'
  (RHEL/Rocky/SUSE family). Missing packages report "package X is not installed".
- dpkg: 'dpkg-query -W -f ..." <pkg>' (Debian/Ubuntu family), checking the
  db:Status-Status field so config-file residue is NOT counted as installed.
Manager detection prefers rpm when both binaries exist.

Parameters:
- packages: REQUIRED array of package names. Shell-style globs are passed
  through to the manager (e.g. 'kernel*'). Capped at 50 names per call.

Output: {manager, packages[] {query, installed, name, version, release (rpm),
arch}, installed_count, missing[]}. A query matching multiple installed
packages (globs, multi-version kernels) produces one entry per match.

Degradation Profile:
- A per-package query failure degrades to an installed=false entry for that
  query; the batch is never aborted.
- Deterministic: values come straight from the package database.`
}

// Category classifies the tool.
func (t *Tool) Category() registry.Category {
	return registry.CategorySystem
}

// Parameters returns the parameter schema.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "packages",
			Type:        "array",
			Description: "Package names to audit (globs allowed, e.g. 'kernel*'). Max 50 per call.",
			Required:    true,
		},
	}
}

// Hidden reports whether the tool is hidden from discovery.
func (t *Tool) Hidden() bool { return false }

// IsSupported checks that a supported package manager exists.
func (t *Tool) IsSupported() (bool, string) {
	if t.detectManager() == "" {
		return false, "neither 'rpm' nor 'dpkg-query' binary found in $PATH"
	}
	return true, ""
}

// detectManager returns "rpm", "dpkg", or "" — preferring rpm when both
// binaries are present (rpm-based distros often ship a dpkg shim, not the
// other way around).
func (t *Tool) detectManager() string {
	if _, err := t.execLookPath("rpm"); err == nil {
		return "rpm"
	}
	if _, err := t.execLookPath("dpkg-query"); err == nil {
		return "dpkg"
	}
	return ""
}

// Execute audits the requested packages.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context cancelled: %v", err)), nil
	}

	var req struct {
		Packages []string `json:"packages"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}
	if len(req.Packages) == 0 {
		return registry.NewErrorResult(t.Name(), "'packages' is required and must be a non-empty array of package names"), nil
	}

	manager := t.detectManager()
	if manager == "" {
		return registry.NewErrorResult(t.Name(), "neither 'rpm' nor 'dpkg-query' binary found in $PATH"), nil
	}

	queries := req.Packages
	truncated := false
	if len(queries) > maxQueries {
		queries = queries[:maxQueries]
		truncated = true
	}

	out := Output{
		Manager:  manager,
		Packages: []PackageStatus{},
		Missing:  []string{},
	}

	for _, query := range queries {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("context cancelled: %v", err)), nil
		}

		var raw []byte
		if manager == "rpm" {
			raw, _ = t.execCommand(ctx, "rpm", "-q", "--queryformat",
				"%{NAME}\t%{VERSION}\t%{RELEASE}\t%{ARCH}\n", query)
		} else {
			raw, _ = t.execCommand(ctx, "dpkg-query", "-W",
				"-f", "${Package}\t${Version}\t${Architecture}\t${db:Status-Status}\n", query)
		}
		// Exit status 1 means "not installed" for both managers; the
		// parsers below decide from the payload alone, so a command
		// error (including a genuinely broken manager) degrades to an
		// installed=false entry instead of aborting the batch.

		var entries []PackageStatus
		if manager == "rpm" {
			entries = parseRPMOutput(query, raw)
		} else {
			entries = parseDpkgOutput(query, raw)
		}

		if len(entries) == 0 {
			out.Packages = append(out.Packages, PackageStatus{Query: query, Installed: false})
			out.Missing = append(out.Missing, query)
			continue
		}
		out.Packages = append(out.Packages, entries...)
		out.InstalledCount += len(entries)
	}

	summary := fmt.Sprintf("%d installed / %d missing across %d queries (manager: %s)",
		out.InstalledCount, len(out.Missing), len(queries), manager)
	if truncated {
		summary += fmt.Sprintf(" — query list truncated to %d", maxQueries)
	}

	res := registry.NewResult(t.Name(), registry.StatusOK, summary, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// parseRPMOutput parses `rpm -q --queryformat "%{NAME}\t%{VERSION}\t%{RELEASE}\t%{ARCH}\n"`
// output. Lines like "package X is not installed" or anything that does not
// have the 4 tab-separated fields are skipped.
func parseRPMOutput(query string, out []byte) []PackageStatus {
	var entries []PackageStatus
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "is not installed") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			continue
		}
		entries = append(entries, PackageStatus{
			Query:     query,
			Installed: true,
			Name:      fields[0],
			Version:   fields[1],
			Release:   fields[2],
			Arch:      fields[3],
		})
	}
	return entries
}

// parseDpkgOutput parses `dpkg-query -W -f '${Package}\t${Version}\t${Architecture}\t${db:Status-Status}\n'`
// output. Only rows whose status is exactly "installed" count — config-file
// residue ("config-files") and "not-installed" rows are treated as missing.
func parseDpkgOutput(query string, out []byte) []PackageStatus {
	var entries []PackageStatus
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 || fields[3] != "installed" {
			continue
		}
		entries = append(entries, PackageStatus{
			Query:     query,
			Installed: true,
			Name:      fields[0],
			Version:   fields[1],
			Arch:      fields[2],
		})
	}
	return entries
}
