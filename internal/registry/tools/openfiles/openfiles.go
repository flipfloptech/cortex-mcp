// Package openfiles implements the get_open_files tool.
//
// It answers "who has files open under this path?" — the question behind
// unmountable filesystems, full-but-empty disks (deleted-but-open files),
// and busy Lustre/NFS mounts. It scans /proc/<pid>/fd/* symlinks natively
// (no lsof/fuser binaries) and matches their targets against a required
// path prefix.
package openfiles

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

const (
	defaultLimit    = 20
	maxLimit        = 100
	examplePathsCap = 3
	deletedSuffix   = " (deleted)"
)

// ProcessOpenFiles reports one process's open fds under the requested path.
type ProcessOpenFiles struct {
	PID          int      `json:"pid"`
	Comm         string   `json:"comm"`
	FDCount      int      `json:"fd_count"`
	ExamplePaths []string `json:"example_paths,omitempty"`
	HasDeleted   bool     `json:"has_deleted"`
}

// Data is the tool's structured output payload.
type Data struct {
	Path             string             `json:"path"`
	TotalMatchingFDs int                `json:"total_matching_fds"`
	Processes        []ProcessOpenFiles `json:"processes,omitempty"`
	DeletedOpenCount int                `json:"deleted_open_count"`
	ProcessesSkipped int                `json:"processes_skipped"`
}

// Tool implements registry.Tool for get_open_files.
type Tool struct {
	procfsRoot string
}

// New returns a Tool wired to the real procfs root.
func New() *Tool {
	return &Tool{procfsRoot: "/proc"}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_open_files"
}

// Description returns a one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Find which processes hold files open under a path prefix (native lsof for mounts)"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Finds every process holding open file descriptors under a path prefix — the native answer to "why can't this filesystem unmount?" or "what is still writing to this mount?".

Scans /proc/<pid>/fd/* symlink targets (via readlink, no lsof/fuser binary)
and matches them against the required "path" prefix (plain prefix match, so
"/mnt/lustre" also matches "/mnt/lustre2"; pass a trailing slash to bind to a
directory). Deleted-but-open files (readlink targets ending in " (deleted)")
are flagged per process and counted globally — they hold disk space until
closed. Process names come from /proc/<pid>/comm. Filtering is deterministic.

Output: processes ranked by fd_count (top "limit", default 20, cap 100) with
up to 3 example paths each; totals cover the full scan, not just the ranked
list. fd directories unreadable due to permissions (other users' processes
when unprivileged) are counted in processes_skipped rather than failing.

Parameters:
- path (required string): mount point or path prefix to match.
- limit (optional integer): max processes returned (default 20, cap 100).`
}

// Category returns the tool taxonomy classification.
func (t *Tool) Category() registry.Category {
	return registry.CategoryStorage
}

// Parameters returns the parameter schema.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "path",
			Type:        "string",
			Description: "Required. Mount point or path prefix to match open file descriptors against (e.g. '/mnt/lustre').",
			Required:    true,
		},
		{
			Name:        "limit",
			Type:        "integer",
			Description: "Optional. Maximum number of processes to return, ranked by open fd count (default 20, cap 100).",
			Required:    false,
			Default:     "20",
		},
	}
}

// Hidden returns false; this is a public diagnostic tool.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires Linux procfs with a readable self/fd directory.
func (t *Tool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux procfs"
	}
	selfFD := filepath.Join(t.procfsRoot, "self", "fd")
	if _, err := os.ReadDir(selfFD); err != nil {
		return false, fmt.Sprintf("%s is not readable: %v", selfFD, err)
	}
	return true, ""
}

// Execute scans all pid fd tables for descriptors under the requested path.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	path, limit, err := parseArgs(args)
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	entries, err := os.ReadDir(t.procfsRoot)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to read %s: %v", t.procfsRoot, err)), nil
	}

	data := Data{Path: path}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), "cancelled: "+err.Error()), nil
		}
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue // not a process directory
		}

		scan, err := scanFDDir(filepath.Join(t.procfsRoot, entry.Name(), "fd"), path)
		if err != nil {
			if os.IsPermission(err) {
				data.ProcessesSkipped++
			}
			continue // vanished pid or unreadable fd table — never fatal
		}
		if scan.count == 0 {
			continue
		}

		data.TotalMatchingFDs += scan.count
		data.DeletedOpenCount += scan.deleted
		data.Processes = append(data.Processes, ProcessOpenFiles{
			PID:          pid,
			Comm:         t.readComm(entry.Name()),
			FDCount:      scan.count,
			ExamplePaths: scan.examples,
			HasDeleted:   scan.deleted > 0,
		})
	}

	sort.Slice(data.Processes, func(i, j int) bool {
		if data.Processes[i].FDCount != data.Processes[j].FDCount {
			return data.Processes[i].FDCount > data.Processes[j].FDCount
		}
		return data.Processes[i].PID < data.Processes[j].PID
	})
	matchingProcs := len(data.Processes)
	if matchingProcs > limit {
		data.Processes = data.Processes[:limit]
	}

	summary := fmt.Sprintf("%d open fd(s) under %s across %d process(es), %d deleted-but-open",
		data.TotalMatchingFDs, path, matchingProcs, data.DeletedOpenCount)

	res := registry.NewResult(t.Name(), registry.StatusOK, summary, data)
	res.Metadata.FilteringMethod = "deterministic"
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// parseArgs validates the tool arguments: path is required, limit defaults
// to defaultLimit and is capped at maxLimit.
func parseArgs(args json.RawMessage) (string, int, error) {
	var params struct {
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return "", 0, fmt.Errorf("failed to parse arguments: %v", err)
		}
	}
	if params.Path == "" {
		return "", 0, fmt.Errorf("missing required parameter: path (mount point or path prefix)")
	}

	limit := params.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return params.Path, limit, nil
}

// fdScan accumulates matches from one process's fd directory.
type fdScan struct {
	count    int
	deleted  int
	examples []string
}

// scanFDDir readlinks every entry of an fd directory and matches targets
// against the path prefix. The returned error is the ReadDir failure
// (ENOENT for vanished pids, EACCES for other users' processes).
func scanFDDir(fdDir, prefix string) (fdScan, error) {
	var scan fdScan
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return scan, err
	}

	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil {
			continue // fd closed mid-scan
		}
		deleted := strings.HasSuffix(target, deletedSuffix)
		target = strings.TrimSuffix(target, deletedSuffix)
		if !strings.HasPrefix(target, prefix) {
			continue
		}

		scan.count++
		if deleted {
			scan.deleted++
		}
		if len(scan.examples) < examplePathsCap {
			scan.examples = append(scan.examples, target)
		}
	}
	return scan, nil
}

// readComm resolves a pid directory name to its process name, degrading to
// "unknown" for processes that died mid-scan.
func (t *Tool) readComm(pidDir string) string {
	raw, err := os.ReadFile(filepath.Join(t.procfsRoot, pidDir, "comm"))
	if err != nil {
		return "unknown"
	}
	comm := strings.TrimSpace(string(raw))
	if comm == "" {
		return "unknown"
	}
	return comm
}
