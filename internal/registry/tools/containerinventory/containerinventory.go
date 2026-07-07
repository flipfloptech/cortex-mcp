// Package containerinventory implements the get_container_inventory tool.
//
// It discovers running containers natively by walking the cgroup v2 unified
// hierarchy for runtime-specific scope directories (docker, podman/libpod,
// containerd, CRI-O) and reads per-container process counts, memory usage,
// and CPU time straight from cgroup controller files. When the docker or
// podman CLIs are present, container names and images are enriched
// opportunistically.
package containerinventory

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// maxContainers caps the reported container list to bound LLM payloads.
const maxContainers = 100

// idShortLen is the canonical short container ID length used for
// cross-referencing cgroup scope names with runtime CLI output.
const idShortLen = 12

// Container is one discovered container with precomputed metrics.
type Container struct {
	// Runtime is the owning container runtime: docker|podman|containerd|crio.
	Runtime string `json:"runtime"`

	// IDShort is the first 12 hex chars of the container ID.
	IDShort string `json:"id_short"`

	// Name is the human container name (only when CLI enrichment succeeded).
	Name string `json:"name,omitempty"`

	// Image is the container image (only when CLI enrichment succeeded).
	Image string `json:"image,omitempty"`

	// Procs is the number of processes in the container cgroup.
	Procs int `json:"procs"`

	// MemoryMB is memory.current converted to MB (1 decimal place).
	MemoryMB float64 `json:"memory_mb"`

	// CPUUsageSeconds is cpu.stat usage_usec converted to seconds (1dp).
	CPUUsageSeconds float64 `json:"cpu_usage_seconds"`
}

// Output is the tool's data payload.
type Output struct {
	Containers      []Container    `json:"containers"`
	CountsByRuntime map[string]int `json:"counts_by_runtime"`
	Total           int            `json:"total"`
}

// fallbackPayload is returned on cgroup v1 hosts (unsupported hierarchy).
type fallbackPayload struct {
	IsSupported bool   `json:"is_supported"`
	Message     string `json:"message"`
}

// nameImage is a CLI-enriched (name, image) pair keyed by short ID.
type nameImage struct {
	Name  string
	Image string
}

// Tool implements registry.Tool for get_container_inventory.
type Tool struct {
	// cgroupRoot is the unified cgroup mount (normally /sys/fs/cgroup).
	// Injectable for tests.
	cgroupRoot string

	// execCommand runs a binary and returns its stdout. Injectable.
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)

	// execLookPath resolves a binary in PATH. Injectable.
	execLookPath func(file string) (string, error)
}

// New creates the tool with production data sources.
func New() *Tool {
	return &Tool{
		cgroupRoot: "/sys/fs/cgroup",
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
	return "get_container_inventory"
}

// Description returns a one-line summary for get_tool_list.
func (t *Tool) Description() string {
	return "Inventory running containers with per-container procs, memory, and CPU from cgroups"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Discovers every running container on the node without requiring any container
runtime daemon, by walking the cgroup v2 unified hierarchy under /sys/fs/cgroup
for runtime scope directories:
- docker-<id>.scope            (Docker, under system.slice)
- libpod-<id>.scope            (Podman; conmon monitor scopes are excluded)
- cri-containerd-<id>.scope    (Kubernetes containerd, under kubepods*.slice)
- crio-<id>.scope              (Kubernetes CRI-O, under kubepods*.slice)

Data Sources:
- Native per-container metrics from the container's cgroup directory:
  cgroup.procs (process count), memory.current (bytes -> memory_mb, 1dp),
  cpu.stat usage_usec (-> cpu_usage_seconds, 1dp).
- Opportunistic enrichment: 'docker ps --format {{json .}}' and
  'podman ps --format json' map 12-char ID prefixes to container names and
  images when those CLIs exist.

Output: {containers[] {runtime, id_short, name?, image?, procs, memory_mb,
cpu_usage_seconds} capped at 100, counts_by_runtime, total}.

Degradation Profile:
- cgroup v1 hosts (no /sys/fs/cgroup/cgroup.controllers) return a degraded
  {"is_supported": false} payload — only the v2 unified hierarchy is parsed.
- Zero container scopes is a valid empty OK result.
- CLI enrichment failures are silent: containers are reported with IDs only.
- Unreadable metric files degrade to 0 values, never fatal.`
}

// Category classifies the tool.
func (t *Tool) Category() registry.Category {
	return registry.CategoryCompute
}

// Parameters returns the parameter schema (none).
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{}
}

// Hidden reports whether the tool is hidden from discovery.
func (t *Tool) Hidden() bool { return false }

// IsSupported checks that the cgroup filesystem exists.
func (t *Tool) IsSupported() (bool, string) {
	if !registry.PathExists(t.cgroupRoot) {
		return false, fmt.Sprintf("%s is missing", t.cgroupRoot)
	}
	return true, ""
}

// Execute inventories running containers.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context cancelled: %v", err)), nil
	}

	// Only the cgroup v2 unified hierarchy is parsed (same contract as
	// get_cgroup_limits).
	if !registry.PathExists(filepath.Join(t.cgroupRoot, "cgroup.controllers")) {
		res := registry.NewResult(t.Name(), registry.StatusDegraded,
			"cgroup v1 detected (unsupported)", fallbackPayload{
				IsSupported: false,
				Message:     "cgroup v1 not supported. Only the v2 unified hierarchy is parsed.",
			})
		res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
		res.Metadata.FilteringMethod = "deterministic"
		return res, nil
	}

	containers := scanContainers(t.cgroupRoot)

	sort.Slice(containers, func(i, j int) bool {
		if containers[i].Runtime != containers[j].Runtime {
			return containers[i].Runtime < containers[j].Runtime
		}
		return containers[i].IDShort < containers[j].IDShort
	})

	counts := map[string]int{}
	hasDocker, hasPodman := false, false
	for _, c := range containers {
		counts[c.Runtime]++
		if c.Runtime == "docker" {
			hasDocker = true
		}
		if c.Runtime == "podman" {
			hasPodman = true
		}
	}

	// Opportunistic name/image enrichment; silent on any failure.
	if enriched := t.enrichmentMap(ctx, hasDocker, hasPodman); len(enriched) > 0 {
		for i := range containers {
			if e, ok := enriched[containers[i].IDShort]; ok {
				containers[i].Name = e.Name
				containers[i].Image = e.Image
			}
		}
	}

	total := len(containers)
	if len(containers) > maxContainers {
		containers = containers[:maxContainers]
	}

	out := Output{
		Containers:      containers,
		CountsByRuntime: counts,
		Total:           total,
	}

	res := registry.NewResult(t.Name(), registry.StatusOK,
		fmt.Sprintf("%d running containers discovered across %d runtimes", total, len(counts)), out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// scanContainers walks the cgroup v2 tree collecting container scopes.
func scanContainers(root string) []Container {
	containers := []Container{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtrees are skipped, not fatal
		}
		if !d.IsDir() || path == root {
			return nil
		}
		runtime, id, ok := classifyScope(d.Name())
		if !ok {
			return nil
		}
		procs, memMB, cpuSec := readContainerMetrics(path)
		containers = append(containers, Container{
			Runtime:         runtime,
			IDShort:         id[:idShortLen],
			Procs:           procs,
			MemoryMB:        memMB,
			CPUUsageSeconds: cpuSec,
		})
		return filepath.SkipDir // do not descend into container-internal cgroups
	})
	return containers
}

// classifyScope matches a cgroup directory name against known container
// runtime scope patterns and extracts the container ID. The ID must be a
// hex string of at least 12 chars, which naturally excludes helper scopes
// such as libpod-conmon-<id>.scope.
func classifyScope(dirName string) (runtime, id string, ok bool) {
	if !strings.HasSuffix(dirName, ".scope") {
		return "", "", false
	}
	base := strings.TrimSuffix(dirName, ".scope")

	switch {
	case strings.HasPrefix(base, "docker-"):
		runtime, id = "docker", strings.TrimPrefix(base, "docker-")
	case strings.HasPrefix(base, "libpod-"):
		runtime, id = "podman", strings.TrimPrefix(base, "libpod-")
	case strings.HasPrefix(base, "cri-containerd-"):
		runtime, id = "containerd", strings.TrimPrefix(base, "cri-containerd-")
	case strings.HasPrefix(base, "crio-"):
		runtime, id = "crio", strings.TrimPrefix(base, "crio-")
	default:
		return "", "", false
	}

	if len(id) < idShortLen {
		return "", "", false
	}
	for _, r := range id {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			return "", "", false
		}
	}
	return runtime, id, true
}

// readContainerMetrics reads process count, memory (MB), and cumulative CPU
// time (seconds) from a container's cgroup directory. Missing or unreadable
// files degrade to zero values.
func readContainerMetrics(dir string) (procs int, memMB, cpuSec float64) {
	if data, err := os.ReadFile(filepath.Join(dir, "cgroup.procs")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) != "" {
				procs++
			}
		}
	}

	if data, err := os.ReadFile(filepath.Join(dir, "memory.current")); err == nil {
		if bytes, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
			memMB = round1dp(float64(bytes) / (1024 * 1024))
		}
	}

	if data, err := os.ReadFile(filepath.Join(dir, "cpu.stat")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "usage_usec" {
				if usec, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					cpuSec = round1dp(float64(usec) / 1e6)
				}
				break
			}
		}
	}
	return procs, memMB, cpuSec
}

// enrichmentMap builds a shortID -> (name, image) map from the docker and
// podman CLIs. Every failure path returns whatever was collected so far —
// enrichment is strictly best-effort.
func (t *Tool) enrichmentMap(ctx context.Context, wantDocker, wantPodman bool) map[string]nameImage {
	enriched := map[string]nameImage{}

	if wantDocker {
		if _, err := t.execLookPath("docker"); err == nil {
			if out, err := t.execCommand(ctx, "docker", "ps", "--format", "{{json .}}"); err == nil {
				for k, v := range parseDockerPS(out) {
					enriched[k] = v
				}
			}
		}
	}

	if wantPodman {
		if _, err := t.execLookPath("podman"); err == nil {
			if out, err := t.execCommand(ctx, "podman", "ps", "--format", "json"); err == nil {
				for k, v := range parsePodmanPS(out) {
					enriched[k] = v
				}
			}
		}
	}
	return enriched
}

// parseDockerPS parses `docker ps --format {{json .}}` output: one JSON
// object per line with ID (12 chars), Names, and Image. Malformed lines
// are skipped.
func parseDockerPS(out []byte) map[string]nameImage {
	m := map[string]nameImage{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			ID    string `json:"ID"`
			Names string `json:"Names"`
			Image string `json:"Image"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil || row.ID == "" {
			continue
		}
		id := row.ID
		if len(id) > idShortLen {
			id = id[:idShortLen]
		}
		m[id] = nameImage{Name: row.Names, Image: row.Image}
	}
	return m
}

// parsePodmanPS parses `podman ps --format json` output: a JSON array with
// full 64-char Id, Names list, and Image. Malformed output yields an empty
// map.
func parsePodmanPS(out []byte) map[string]nameImage {
	m := map[string]nameImage{}
	var rows []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return m
	}
	for _, row := range rows {
		if row.ID == "" {
			continue
		}
		id := row.ID
		if len(id) > idShortLen {
			id = id[:idShortLen]
		}
		name := ""
		if len(row.Names) > 0 {
			name = row.Names[0]
		}
		m[id] = nameImage{Name: name, Image: row.Image}
	}
	return m
}

// round1dp rounds to one decimal place for LLM-friendly output.
func round1dp(v float64) float64 {
	return math.Round(v*10) / 10
}
