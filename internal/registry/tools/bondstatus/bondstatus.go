// Package bondstatus implements the get_bond_status diagnostic tool. It
// parses Linux bonding driver state natively from /proc/net/bonding/<bond>
// files (one per bond), requiring no external binaries.
package bondstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func init() {
	registry.Register(New())
}

// Tool implements registry.Tool for get_bond_status.
type Tool struct {
	procfsRoot string
}

// New returns a new instance of the Tool rooted at the real procfs.
func New() *Tool {
	return &Tool{procfsRoot: "/proc"}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string {
	return "get_bond_status"
}

// Description provides a concise summary of what this tool does.
func (t *Tool) Description() string {
	return "Audit Linux bonding (link aggregation) health: bond/slave MII status, active slave, speeds, and link failure counts."
}

// Help provides detailed documentation.
func (t *Tool) Help() string {
	return `Parses every bond file under /proc/net/bonding/ (one file per configured bond).

For each bond it extracts the bonding mode, bond-level MII status, and the currently
active slave. For each slave interface it extracts MII status, negotiated speed
(decoded to an integer speed_mbps, 0 when Unknown), duplex, and the link failure count.
When the bond runs IEEE 802.3ad (LACP), the LACP rate and active aggregator partner
MAC address are included.

Warnings are raised deterministically when the bond or any slave reports MII status
"down", or when a slave has a nonzero link failure count. Unparseable fields degrade
to empty/zero values instead of failing; an empty bonding directory is a valid result
(bonds: []).`
}

// Category organizes the tool within the registry.
func (t *Tool) Category() registry.Category {
	return registry.CategoryNetwork
}

// Hidden hides the tool from general LLM discovery if true.
func (t *Tool) Hidden() bool {
	return false
}

// Parameters defines the expected input schema (none for this tool).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// IsSupported checks whether the bonding module exposes its procfs directory.
func (t *Tool) IsSupported() (bool, string) {
	dir := filepath.Join(t.procfsRoot, "net", "bonding")
	if !registry.PathExists(dir) {
		return false, fmt.Sprintf("bonding module not loaded (%s missing)", dir)
	}
	return true, ""
}

// Slave is the per-slave-interface diagnostic entry.
type Slave struct {
	Name             string `json:"name"`
	MIIStatus        string `json:"mii_status"`
	SpeedMbps        int64  `json:"speed_mbps"`
	Duplex           string `json:"duplex"`
	LinkFailureCount int64  `json:"link_failure_count"`
}

// Bond is the per-bond diagnostic entry.
type Bond struct {
	Name           string   `json:"name"`
	Mode           string   `json:"mode"`
	MIIStatus      string   `json:"mii_status"`
	ActiveSlave    string   `json:"active_slave,omitempty"`
	LACPRate       string   `json:"lacp_rate,omitempty"`
	PartnerMAC     string   `json:"partner_mac,omitempty"`
	Slaves         []Slave  `json:"slaves"`
	WarningReasons []string `json:"warning_reasons,omitempty"`
}

// SystemSummary aggregates bond health for fast LLM triage.
type SystemSummary struct {
	TotalBonds        int `json:"total_bonds"`
	BondsUp           int `json:"bonds_up"`
	TotalSlaves       int `json:"total_slaves"`
	SlavesUp          int `json:"slaves_up"`
	BondsWithWarnings int `json:"bonds_with_warnings"`
}

// Output is the tool's data payload.
type Output struct {
	SystemSummary SystemSummary `json:"system_summary"`
	Bonds         []Bond        `json:"bonds"`
}

// Execute reads and parses every bond file under /proc/net/bonding.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled before execution: %v", err)), nil
	}

	dir := filepath.Join(t.procfsRoot, "net", "bonding")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("cannot read %s: %v", dir, err)), nil
	}

	bonds := []Bond{}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled during procfs walk: %v", err)), nil
		}
		if e.IsDir() {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			// Degrade: skip unreadable bond files instead of failing.
			continue
		}
		bonds = append(bonds, parseBondFile(e.Name(), content))
	}

	summary := SystemSummary{TotalBonds: len(bonds)}
	warningCount := 0
	for _, b := range bonds {
		if b.MIIStatus == "up" {
			summary.BondsUp++
		}
		summary.TotalSlaves += len(b.Slaves)
		for _, s := range b.Slaves {
			if s.MIIStatus == "up" {
				summary.SlavesUp++
			}
		}
		if len(b.WarningReasons) > 0 {
			summary.BondsWithWarnings++
			warningCount += len(b.WarningReasons)
		}
	}

	status := registry.StatusOK
	if warningCount > 0 {
		status = registry.StatusWarning
	}

	summaryText := fmt.Sprintf("Bonding: %d bond(s) (%d up), %d slave(s) (%d up), %d bond(s) with warnings",
		summary.TotalBonds, summary.BondsUp, summary.TotalSlaves, summary.SlavesUp, summary.BondsWithWarnings)

	res := registry.NewResult(t.Name(), status, summaryText, Output{SystemSummary: summary, Bonds: bonds})
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// parseBondFile parses the content of a single /proc/net/bonding/<bond> file
// into a Bond entry, including deterministic warning reasons. Unparseable
// fields degrade to empty/zero values; this function never fails.
func parseBondFile(name string, content []byte) Bond {
	bond := Bond{Name: name, Slaves: []Slave{}}

	var current *Slave
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		if v, ok := fieldValue(line, "Slave Interface:"); ok {
			bond.Slaves = append(bond.Slaves, Slave{Name: v})
			current = &bond.Slaves[len(bond.Slaves)-1]
			continue
		}

		if current == nil {
			// Bond-level header section (including 802.3ad info).
			if v, ok := fieldValue(line, "Bonding Mode:"); ok {
				bond.Mode = v
			} else if v, ok := fieldValue(line, "MII Status:"); ok {
				bond.MIIStatus = v
			} else if v, ok := fieldValue(line, "Currently Active Slave:"); ok {
				if v != "None" {
					bond.ActiveSlave = v
				}
			} else if v, ok := fieldValue(line, "Partner Mac Address:"); ok {
				bond.PartnerMAC = v
			} else if strings.HasPrefix(strings.ToLower(line), "lacp rate:") {
				bond.LACPRate = strings.TrimSpace(line[len("lacp rate:"):])
			}
			continue
		}

		// Per-slave section. Indented LACP PDU detail lines (e.g.
		// "system mac address:") match none of these prefixes.
		if v, ok := fieldValue(line, "MII Status:"); ok {
			current.MIIStatus = v
		} else if v, ok := fieldValue(line, "Speed:"); ok {
			current.SpeedMbps = parseSpeedMbps(v)
		} else if v, ok := fieldValue(line, "Duplex:"); ok {
			current.Duplex = v
		} else if v, ok := fieldValue(line, "Link Failure Count:"); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
				current.LinkFailureCount = n
			}
		}
	}

	if bond.MIIStatus != "up" {
		bond.WarningReasons = append(bond.WarningReasons,
			fmt.Sprintf("bond MII status is %q (expected up)", bond.MIIStatus))
	}
	for _, s := range bond.Slaves {
		if s.MIIStatus != "up" {
			bond.WarningReasons = append(bond.WarningReasons,
				fmt.Sprintf("slave %s MII status is %q (expected up)", s.Name, s.MIIStatus))
		}
		if s.LinkFailureCount > 0 {
			bond.WarningReasons = append(bond.WarningReasons,
				fmt.Sprintf("slave %s has %d link failure(s)", s.Name, s.LinkFailureCount))
		}
	}
	return bond
}

// fieldValue returns the trimmed value after prefix when line starts with
// prefix (e.g., fieldValue("MII Status: up", "MII Status:") -> "up", true).
func fieldValue(line, prefix string) (string, bool) {
	if strings.HasPrefix(line, prefix) {
		return strings.TrimSpace(line[len(prefix):]), true
	}
	return "", false
}

// parseSpeedMbps decodes a bonding Speed field ("25000 Mbps") into an integer
// megabit value. "Unknown", negative, or malformed speeds decode to 0.
func parseSpeedMbps(raw string) int64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}
