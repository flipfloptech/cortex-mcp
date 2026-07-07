package lustreclientstats

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestLustreClientStatsTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_lustre_client_stats" {
		t.Errorf("expected Name() == 'get_lustre_client_stats', got %q", tool.Name())
	}
	if tool.Category() != registry.CategoryStorage {
		t.Errorf("expected Category() == 'storage', got %q", tool.Category())
	}
	if !strings.Contains(tool.Help(), "/sys/fs/lustre") && !strings.Contains(tool.Help(), "/proc/fs/lustre") {
		t.Errorf("Help() missing data source reference, got: %s", tool.Help())
	}
	if tool.Parameters() != nil {
		t.Errorf("expected Parameters() to be nil")
	}
}

func TestParseLliteStats(t *testing.T) {
	t.Parallel()

	input := `
read_bytes                3340631 samples [bytes] 4096 1048576 14421705314304
write_bytes               3290112 samples [bytes] 4096 1048576 144217053
open                      12345 samples [reqs]
close                     12340 samples [reqs]
`
	stats, err := parseLliteStats([]byte(input))
	if err != nil {
		t.Fatalf("failed to parse llite stats: %v", err)
	}

	rb, ok := stats["read_bytes"]
	if !ok {
		t.Fatalf("missing read_bytes in stats")
	}
	if rb.Samples != 3340631 {
		t.Errorf("read_bytes.Samples = %d, want 3340631", rb.Samples)
	}
	if rb.Unit != "bytes" {
		t.Errorf("read_bytes.Unit = %q, want 'bytes'", rb.Unit)
	}
	if rb.Min == nil || *rb.Min != 4096 {
		t.Errorf("read_bytes.Min mismatch")
	}
	if rb.Max == nil || *rb.Max != 1048576 {
		t.Errorf("read_bytes.Max mismatch")
	}
	if rb.Sum == nil || *rb.Sum != 14421705314304 {
		t.Errorf("read_bytes.Sum mismatch")
	}

	open, ok := stats["open"]
	if !ok {
		t.Fatalf("missing open in stats")
	}
	if open.Samples != 12345 {
		t.Errorf("open.Samples = %d, want 12345", open.Samples)
	}
	if open.Unit != "reqs" {
		t.Errorf("open.Unit = %q, want 'reqs'", open.Unit)
	}
	if open.Min != nil || open.Max != nil || open.Sum != nil {
		t.Errorf("expected min/max/sum to be nil for open")
	}
}

func TestParseReadAheadStats(t *testing.T) {
	t.Parallel()

	input := `
hits 500 samples [pages]
misses 100 samples [pages]
readpage_backwards 15 samples [pages]
`
	ra := parseReadAheadStats([]byte(input))
	if ra.Hits != 500 {
		t.Errorf("Hits = %d, want 500", ra.Hits)
	}
	if ra.Misses != 100 {
		t.Errorf("Misses = %d, want 100", ra.Misses)
	}
	if ra.HitRatePct == nil || *ra.HitRatePct != 83.33333333333334 {
		t.Errorf("HitRatePct = %f (raw: %v), want 83.33333333333334", *ra.HitRatePct, *ra.HitRatePct)
	}
	if val, ok := ra.Other["readpage_backwards"]; !ok || val != 15 {
		t.Errorf("readpage_backwards mismatch: got %d (ok: %t)", val, ok)
	}
}

func TestParseImport(t *testing.T) {
	t.Parallel()

	input := `
import:
    name: lustre-OST0000-osc-ffff880123456780
    target: lustre-OST0000_UUID
    state: FULL
    connect_count: 5
    connection:
        failover_nids: [ 10.0.0.1@o2ib ]
        current_connection: 10.0.0.1@o2ib
    rpcs:
        inflight: 3
        queued: 0
        timeouts: 12
`
	imp := parseImport("lustre-OST0000-osc-ffff880123456780", []byte(input))
	if imp.Name != "lustre-OST0000-osc-ffff880123456780" {
		t.Errorf("Name = %q, want 'lustre-OST0000-osc-ffff880123456780'", imp.Name)
	}
	if imp.Target != "lustre-OST0000_UUID" {
		t.Errorf("Target = %q, want 'lustre-OST0000_UUID'", imp.Target)
	}
	if imp.State != "FULL" {
		t.Errorf("State = %q, want 'FULL'", imp.State)
	}
	if imp.ConnectCount != 5 {
		t.Errorf("ConnectCount = %d, want 5", imp.ConnectCount)
	}
	if imp.Inflight != 3 {
		t.Errorf("Inflight = %d, want 3", imp.Inflight)
	}
	if imp.Timeouts != 12 {
		t.Errorf("Timeouts = %d, want 12", imp.Timeouts)
	}
}

func TestParseAdaptiveTimeout(t *testing.T) {
	t.Parallel()

	input := "service : cur 1 worst 30 (at 1681257150, 85d23h58m54s ago) 1 1 1 1"
	at, err := parseAdaptiveTimeout([]byte(input))
	if err != nil {
		t.Fatalf("failed to parse adaptive timeout: %v", err)
	}

	if at == nil {
		t.Fatal("expected non-nil adaptive timeout struct")
	}
	if at.Cur != 1 {
		t.Errorf("Cur = %d, want 1", at.Cur)
	}
	if at.Worst != 30 {
		t.Errorf("Worst = %d, want 30", at.Worst)
	}
}

func TestLustreClientStatsTool_Execute(t *testing.T) {
	t.Parallel()

	// Write mock Lustre client directory structure
	dir := t.TempDir()
	sysfsRoot := filepath.Join(dir, "sys_fs_lustre")

	versionPath := filepath.Join(sysfsRoot, "version")
	if err := os.MkdirAll(sysfsRoot, 0755); err != nil {
		t.Fatalf("failed to create sysfs root: %v", err)
	}
	if err := os.WriteFile(versionPath, []byte("lustre: 2.15.3\n"), 0644); err != nil {
		t.Fatalf("failed to write version: %v", err)
	}

	lliteDir := filepath.Join(sysfsRoot, "llite", "lustre-client-1")
	if err := os.MkdirAll(lliteDir, 0755); err != nil {
		t.Fatalf("failed to create llite client dir: %v", err)
	}

	statsData := `
read_bytes                1000 samples [bytes] 4096 4096 4096000
write_bytes               2000 samples [bytes] 4096 4096 8192000
`
	if err := os.WriteFile(filepath.Join(lliteDir, "stats"), []byte(statsData), 0644); err != nil {
		t.Fatalf("failed to write stats: %v", err)
	}

	raData := `
hits 500 samples [pages]
misses 100 samples [pages]
`
	if err := os.WriteFile(filepath.Join(lliteDir, "read_ahead_stats"), []byte(raData), 0644); err != nil {
		t.Fatalf("failed to write read_ahead_stats: %v", err)
	}

	// Write readahead parameters
	if err := os.WriteFile(filepath.Join(lliteDir, "max_read_ahead_mb"), []byte("1024\n"), 0644); err != nil {
		t.Fatalf("failed to write max_read_ahead_mb: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lliteDir, "max_read_ahead_per_file_mb"), []byte("256\n"), 0644); err != nil {
		t.Fatalf("failed to write max_read_ahead_per_file_mb: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lliteDir, "max_read_ahead_whole_mb"), []byte("64\n"), 0644); err != nil {
		t.Fatalf("failed to write max_read_ahead_whole_mb: %v", err)
	}

	oscDir := filepath.Join(sysfsRoot, "osc", "osc-OST0000")
	if err := os.MkdirAll(oscDir, 0755); err != nil {
		t.Fatalf("failed to create osc dir: %v", err)
	}

	importData := `
target: lustre-OST0000_UUID
state: FULL
connect_count: 1
rpcs:
    inflight: 3
    queued: 0
    timeouts: 12
`
	if err := os.WriteFile(filepath.Join(oscDir, "import"), []byte(importData), 0644); err != nil {
		t.Fatalf("failed to write import: %v", err)
	}

	if err := os.WriteFile(filepath.Join(oscDir, "active"), []byte("1\n"), 0644); err != nil {
		t.Fatalf("failed to write active: %v", err)
	}

	timeoutsData := "service : cur 1 worst 30 (at 1681257150, 85d23h58m54s ago) 1 1 1 1"
	if err := os.WriteFile(filepath.Join(oscDir, "timeouts"), []byte(timeoutsData), 0644); err != nil {
		t.Fatalf("failed to write timeouts: %v", err)
	}

	tool := &LustreClientStatsTool{
		sysfsPath: sysfsRoot,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := tool.Execute(ctx, []byte(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != registry.StatusOK {
		t.Errorf("expected StatusOK, got %s. Summary: %s", res.Status, res.Summary)
	}

	var data LustreClientStatsData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal result data: %v", err)
	}

	if data.LustreVersion != "lustre: 2.15.3" {
		t.Errorf("LustreVersion = %q, want 'lustre: 2.15.3'", data.LustreVersion)
	}

	if len(data.Filesystems) != 1 {
		t.Fatalf("expected 1 filesystem, got %d", len(data.Filesystems))
	}

	fs := data.Filesystems[0]
	if fs.Name != "lustre-client-1" {
		t.Errorf("filesystem name = %q, want 'lustre-client-1'", fs.Name)
	}

	if fs.ReadAhead.Hits != 500 {
		t.Errorf("Hits = %d, want 500", fs.ReadAhead.Hits)
	}
	if fs.ReadAhead.HitRatePct == nil || *fs.ReadAhead.HitRatePct != 83.33333333333334 {
		t.Errorf("HitRatePct = %f (raw: %v), want 83.33333333333334", *fs.ReadAhead.HitRatePct, *fs.ReadAhead.HitRatePct)
	}
	if fs.ReadAhead.MaxReadaheadMB == nil || *fs.ReadAhead.MaxReadaheadMB != 1024 {
		t.Errorf("MaxReadaheadMB = %v, want 1024", fs.ReadAhead.MaxReadaheadMB)
	}
	if fs.ReadAhead.MaxPerFileMB == nil || *fs.ReadAhead.MaxPerFileMB != 256 {
		t.Errorf("MaxPerFileMB = %v, want 256", fs.ReadAhead.MaxPerFileMB)
	}
	if fs.ReadAhead.MaxWholeMB == nil || *fs.ReadAhead.MaxWholeMB != 64 {
		t.Errorf("MaxWholeMB = %v, want 64", fs.ReadAhead.MaxWholeMB)
	}

	if len(data.Connections.OST) != 1 {
		t.Fatalf("expected 1 OST connection, got %d", len(data.Connections.OST))
	}

	ost := data.Connections.OST[0]
	if ost.Target != "lustre-OST0000_UUID" || ost.State != "FULL" {
		t.Errorf("OST target/state mismatch: %+v", ost)
	}
	if ost.Timeouts != 12 {
		t.Errorf("OST Timeouts = %d, want 12", ost.Timeouts)
	}
	if ost.Active != 1 {
		t.Errorf("OST Active = %d, want 1", ost.Active)
	}
	if ost.Adaptive == nil || ost.Adaptive.Cur != 1 || ost.Adaptive.Worst != 30 {
		t.Errorf("OST Adaptive timeout mismatch: %+v", ost.Adaptive)
	}
}
