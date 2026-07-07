// Package arpneighbors implements the get_arp_neighbors diagnostic tool.
// It reads the IPv4 ARP table natively from /proc/net/arp and, when the
// 'ip' binary is available, enriches it with 'ip -j neigh' output for IPv6
// neighbors and granular NUD states (REACHABLE/STALE/FAILED/...).
package arpneighbors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func init() {
	registry.Register(New())
}

// maxEntries caps the entries list so LLM payloads stay small; FAILED and
// INCOMPLETE neighbors are prioritized ahead of healthy ones before capping.
const maxEntries = 50

// Tool implements registry.Tool for get_arp_neighbors.
type Tool struct {
	procfsRoot  string
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookPath    func(file string) (string, error)
}

// New returns a new instance of the Tool wired to the real procfs and PATH.
func New() *Tool {
	return &Tool{
		procfsRoot: "/proc",
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		lookPath: exec.LookPath,
	}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string {
	return "get_arp_neighbors"
}

// Description provides a concise summary of what this tool does.
func (t *Tool) Description() string {
	return "Audit ARP/NDP neighbor tables: per-state counts with FAILED/INCOMPLETE neighbors surfaced first."
}

// Help provides detailed documentation.
func (t *Tool) Help() string {
	return `Collects the kernel neighbor (ARP/NDP) tables and classifies every entry by state.

Data sources: /proc/net/arp is parsed natively (IPv4; flags decoded 0x0=incomplete,
0x2=reachable/complete, 0x4|0x6=permanent). When the 'ip' binary is in PATH, 'ip -j neigh'
JSON output enriches the table with IPv6 neighbors and granular NUD states
(reachable/stale/failed/incomplete/permanent/delay/probe).

Output precomputes counts_by_state and total_entries, caps the entries list at 50
(FAILED and INCOMPLETE neighbors are prioritized first), and always lists every FAILED
neighbor in full. A warning is raised when any neighbor is in the FAILED state.
If 'ip' is unavailable the tool degrades to procfs-only IPv4 data (ipv6_included: false).`
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

// IsSupported checks that at least one neighbor data source is available.
func (t *Tool) IsSupported() (bool, string) {
	if registry.PathExists(filepath.Join(t.procfsRoot, "net", "arp")) {
		return true, ""
	}
	if _, err := t.lookPath("ip"); err == nil {
		return true, ""
	}
	return false, "neither /proc/net/arp nor the 'ip' binary is available"
}

// Neighbor is a single ARP/NDP table entry.
type Neighbor struct {
	IP     string `json:"ip"`
	Device string `json:"device"`
	MAC    string `json:"mac,omitempty"`
	State  string `json:"state"`
	Family string `json:"family"`
}

// Output is the tool's data payload.
type Output struct {
	TotalEntries   int            `json:"total_entries"`
	EntriesShown   int            `json:"entries_shown"`
	Truncated      bool           `json:"truncated"`
	IPv6Included   bool           `json:"ipv6_included"`
	CountsByState  map[string]int `json:"counts_by_state"`
	Entries        []Neighbor     `json:"entries"`
	Failed         []Neighbor     `json:"failed,omitempty"`
	WarningReasons []string       `json:"warning_reasons,omitempty"`
	Note           string         `json:"note,omitempty"`
}

// Execute gathers neighbor entries from procfs and (optionally) ip neigh.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled before execution: %v", err)), nil
	}

	entries := []Neighbor{}
	procOK := false
	if data, err := os.ReadFile(filepath.Join(t.procfsRoot, "net", "arp")); err == nil {
		entries = parseProcArp(data)
		procOK = true
	}

	ipv6Included := false
	note := ""
	if _, err := t.lookPath("ip"); err == nil {
		out, execErr := t.execCommand(ctx, "ip", "-j", "neigh")
		if execErr != nil {
			note = "'ip -j neigh' execution failed; procfs data only (IPv4)"
		} else if extra, parseErr := parseIPNeighJSON(out); parseErr != nil {
			note = "'ip -j neigh' output unparseable; procfs data only (IPv4)"
		} else {
			entries = mergeNeighbors(entries, extra)
			ipv6Included = true
		}
	} else {
		note = "'ip' binary not found; procfs data only (IPv4)"
	}

	if !procOK && !ipv6Included {
		return registry.NewErrorResult(t.Name(),
			"no neighbor data available: /proc/net/arp unreadable and 'ip neigh' unusable"), nil
	}

	counts := map[string]int{}
	failed := []Neighbor{}
	for _, e := range entries {
		counts[e.State]++
		if e.State == "failed" {
			failed = append(failed, e)
		}
	}

	// FAILED first, then INCOMPLETE, then everything else (stable order).
	sort.SliceStable(entries, func(i, j int) bool {
		return statePriority(entries[i].State) < statePriority(entries[j].State)
	})
	shown := entries
	truncated := false
	if len(shown) > maxEntries {
		shown = shown[:maxEntries]
		truncated = true
	}

	var warnings []string
	status := registry.StatusOK
	if len(failed) > 0 {
		warnings = append(warnings,
			fmt.Sprintf("%d neighbor(s) in FAILED state — address resolution is broken for those hosts", len(failed)))
		status = registry.StatusWarning
	}

	out := Output{
		TotalEntries:   len(entries),
		EntriesShown:   len(shown),
		Truncated:      truncated,
		IPv6Included:   ipv6Included,
		CountsByState:  counts,
		Entries:        shown,
		Failed:         failed,
		WarningReasons: warnings,
		Note:           note,
	}

	summaryText := fmt.Sprintf("ARP/NDP: %d neighbor(s) (%d failed, %d incomplete, %d reachable), ipv6_included=%v",
		len(entries), counts["failed"], counts["incomplete"], counts["reachable"], ipv6Included)

	res := registry.NewResult(t.Name(), status, summaryText, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// parseProcArp parses /proc/net/arp content. Header and malformed lines are
// skipped; the all-zero MAC placeholder of incomplete entries is blanked.
func parseProcArp(content []byte) []Neighbor {
	neighbors := []Neighbor{}
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "IP address") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		mac := fields[3]
		if mac == "00:00:00:00:00:00" {
			mac = ""
		}
		neighbors = append(neighbors, Neighbor{
			IP:     fields[0],
			Device: fields[5],
			MAC:    mac,
			State:  flagsToState(fields[2]),
			Family: "ipv4",
		})
	}
	return neighbors
}

// flagsToState decodes /proc/net/arp hex flags into a neighbor state:
// ATF_PERM (0x4) -> permanent, ATF_COM (0x2) -> reachable, 0x0 -> incomplete.
func flagsToState(flags string) string {
	s := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(flags)), "0x")
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return "unknown"
	}
	switch {
	case v&0x4 != 0:
		return "permanent"
	case v&0x2 != 0:
		return "reachable"
	default:
		return "incomplete"
	}
}

// ipNeighEntry mirrors one element of 'ip -j neigh' JSON output.
type ipNeighEntry struct {
	Dst    string   `json:"dst"`
	Dev    string   `json:"dev"`
	Lladdr string   `json:"lladdr"`
	State  []string `json:"state"`
}

// parseIPNeighJSON decodes 'ip -j neigh' output into Neighbor entries with
// lowercased granular NUD states. Entries without a destination are skipped.
func parseIPNeighJSON(out []byte) ([]Neighbor, error) {
	var raw []ipNeighEntry
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("cannot parse ip neigh JSON: %w", err)
	}
	neighbors := []Neighbor{}
	for _, e := range raw {
		if e.Dst == "" {
			continue
		}
		state := "unknown"
		if len(e.State) > 0 {
			state = strings.ToLower(e.State[0])
		}
		family := "ipv4"
		if strings.Contains(e.Dst, ":") {
			family = "ipv6"
		}
		neighbors = append(neighbors, Neighbor{
			IP:     e.Dst,
			Device: e.Dev,
			MAC:    e.Lladdr,
			State:  state,
			Family: family,
		})
	}
	return neighbors, nil
}

// mergeNeighbors overlays granular 'ip neigh' entries onto the procfs base
// set, keyed by (ip, device). Matching entries adopt the granular state (and
// MAC when missing); unmatched entries (e.g., IPv6) are appended.
func mergeNeighbors(base, extra []Neighbor) []Neighbor {
	merged := make([]Neighbor, len(base))
	copy(merged, base)

	index := make(map[string]int, len(merged))
	for i, n := range merged {
		index[n.IP+"%"+n.Device] = i
	}
	for _, n := range extra {
		key := n.IP + "%" + n.Device
		if i, ok := index[key]; ok {
			merged[i].State = n.State
			if merged[i].MAC == "" {
				merged[i].MAC = n.MAC
			}
		} else {
			merged = append(merged, n)
			index[key] = len(merged) - 1
		}
	}
	return merged
}

// statePriority orders neighbor states for display: failed first, then
// incomplete, then all healthy/other states.
func statePriority(state string) int {
	switch state {
	case "failed":
		return 0
	case "incomplete":
		return 1
	default:
		return 2
	}
}
