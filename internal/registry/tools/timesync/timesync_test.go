package timesync

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// writeClocksourceTree builds a fake sysfs tree exposing the given
// clocksource files and returns the fake sysfs root.
func writeClocksourceTree(t *testing.T, current, available string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "devices", "system", "clocksource", "clocksource0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir clocksource: %v", err)
	}
	if current != "" {
		if err := os.WriteFile(filepath.Join(dir, "current_clocksource"), []byte(current), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if available != "" {
		if err := os.WriteFile(filepath.Join(dir, "available_clocksource"), []byte(available), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// testTool returns a tool with every external dependency neutralized:
// no daemon binaries, empty sysfs, and a synced adjtimex reporting zero offset.
func testTool(t *testing.T) *Tool {
	t.Helper()
	tool := New()
	tool.sysfsRoot = t.TempDir()
	tool.goarch = "arm64" // neutral: no tsc expectation
	tool.adjtimex = func(tx *syscall.Timex) (int, error) {
		tx.Status = 0
		return 0, nil
	}
	tool.execLookPath = func(file string) (string, error) {
		return "", fmt.Errorf("%s: not found", file)
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("unexpected exec of %s", name)
	}
	return tool
}

func TestTimeSyncTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_time_sync_status" {
		t.Errorf("Name() = %q, want get_time_sync_status", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("Category() = %q, want system", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	help := tool.Help()
	for _, src := range []string{"adjtimex", "clocksource", "chronyc", "timedatectl"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
	if tool.Parameters() != nil {
		t.Error("Parameters() must be nil (no parameters)")
	}
	if tool.Hidden() {
		t.Error("Hidden() must be false")
	}
}

func TestTimeSyncTool_IsSupported(t *testing.T) {
	t.Parallel()
	tool := New()
	ok, reason := tool.IsSupported()
	if registry.IsLinux() && !ok {
		t.Errorf("IsSupported() = false (%s) on Linux, want true", reason)
	}
}

func TestTimeSyncTool_Execute_KernelMicroseconds(t *testing.T) {
	t.Parallel()

	tool := testTool(t)
	tool.sysfsRoot = writeClocksourceTree(t, "tsc\n", "tsc hpet acpi_pm\n")
	tool.goarch = "amd64"
	tool.adjtimex = func(tx *syscall.Timex) (int, error) {
		if tx.Modes != 0 {
			t.Errorf("adjtimex Modes = %d, want 0 (read-only)", tx.Modes)
		}
		tx.Offset = 50000 // µs (no STA_NANO) => 50 ms
		tx.Status = 0     // synchronized
		tx.Maxerror = 500000
		tx.Esterror = 200
		return 0, nil
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if !out.Synchronized {
		t.Error("Synchronized = false, want true")
	}
	if !out.KernelStatusAvailable {
		t.Error("KernelStatusAvailable = false, want true")
	}
	if out.OffsetMs == nil || *out.OffsetMs != 50.00 {
		t.Errorf("OffsetMs = %v, want 50.00", out.OffsetMs)
	}
	if out.EstErrorUs != 200 || out.MaxErrorUs != 500000 {
		t.Errorf("est/max error = %d/%d, want 200/500000", out.EstErrorUs, out.MaxErrorUs)
	}
	if out.Clocksource == nil || out.Clocksource.Current != "tsc" {
		t.Fatalf("Clocksource = %+v, want current=tsc", out.Clocksource)
	}
	if len(out.Clocksource.Available) != 3 {
		t.Errorf("Available = %v, want 3 entries", out.Clocksource.Available)
	}
	if out.NTPDaemon != nil {
		t.Errorf("NTPDaemon = %+v, want omitted with no daemon", out.NTPDaemon)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("WarningReasons = %v, want none", out.WarningReasons)
	}
}

func TestTimeSyncTool_Execute_KernelNanoseconds(t *testing.T) {
	t.Parallel()

	tool := testTool(t)
	tool.adjtimex = func(tx *syscall.Timex) (int, error) {
		tx.Offset = -1500000 // ns with STA_NANO => -1.5 ms
		tx.Status = staNano
		return 0, nil
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.OffsetMs == nil || *out.OffsetMs != -1.5 {
		t.Errorf("OffsetMs = %v, want -1.5 (STA_NANO nanosecond offset, sign preserved)", out.OffsetMs)
	}
	if !out.Synchronized {
		t.Error("Synchronized = false, want true (STA_UNSYNC clear)")
	}
}

func TestTimeSyncTool_Execute_Warnings(t *testing.T) {
	t.Parallel()

	tool := testTool(t)
	tool.sysfsRoot = writeClocksourceTree(t, "hpet\n", "tsc hpet\n")
	tool.goarch = "amd64"
	tool.adjtimex = func(tx *syscall.Timex) (int, error) {
		tx.Offset = 250000 // 250 ms
		tx.Status = staUnsync
		return 5, nil // TIME_ERROR
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.Synchronized {
		t.Error("Synchronized = true, want false (STA_UNSYNC set)")
	}
	if len(out.WarningReasons) != 3 {
		t.Fatalf("WarningReasons = %v, want 3 (unsync, offset, clocksource)", out.WarningReasons)
	}
	joined := strings.Join(out.WarningReasons, " | ")
	for _, want := range []string{"not synchronized", "offset", "clocksource"} {
		if !strings.Contains(strings.ToLower(joined), want) {
			t.Errorf("warnings %q missing %q", joined, want)
		}
	}
}

func TestTimeSyncTool_Execute_TSCOnAmd64NoWarning(t *testing.T) {
	t.Parallel()

	tool := testTool(t)
	tool.sysfsRoot = writeClocksourceTree(t, "tsc\n", "tsc hpet\n")
	tool.goarch = "amd64"

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("WarningReasons = %v, want none for tsc on amd64", out.WarningReasons)
	}
}

func TestTimeSyncTool_Execute_NonTSCOnArmNoWarning(t *testing.T) {
	t.Parallel()

	tool := testTool(t)
	tool.sysfsRoot = writeClocksourceTree(t, "arch_sys_counter\n", "arch_sys_counter\n")
	tool.goarch = "arm64"

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("WarningReasons = %v, want none (tsc check is amd64-only)", out.WarningReasons)
	}
}

func TestTimeSyncTool_Execute_EPERMFallsBackToChrony(t *testing.T) {
	t.Parallel()

	tool := testTool(t)
	tool.adjtimex = func(tx *syscall.Timex) (int, error) {
		return 0, syscall.EPERM
	}
	tool.execLookPath = func(file string) (string, error) {
		if file == "chronyc" {
			return "/usr/bin/chronyc", nil
		}
		return "", fmt.Errorf("%s: not found", file)
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "chronyc" {
			return nil, fmt.Errorf("unexpected command %s", name)
		}
		return []byte("A29FC87B,203.0.113.5,2,1723805518.224495338,0.000145339,-0.000004312,0.000035612,-0.696,0.021,3.7,0.000206396,0.000572906,64.2,Normal\n"), nil
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.KernelStatusAvailable {
		t.Error("KernelStatusAvailable = true, want false on EPERM")
	}
	if out.OffsetMs != nil {
		t.Errorf("kernel OffsetMs = %v, want omitted on EPERM", out.OffsetMs)
	}
	if out.NTPDaemon == nil {
		t.Fatal("NTPDaemon missing, want chrony data")
	}
	if out.NTPDaemon.Name != "chrony" {
		t.Errorf("daemon name = %q, want chrony", out.NTPDaemon.Name)
	}
	if out.NTPDaemon.Stratum != 2 {
		t.Errorf("stratum = %d, want 2", out.NTPDaemon.Stratum)
	}
	if out.NTPDaemon.RefSource != "203.0.113.5" {
		t.Errorf("ref_source = %q, want 203.0.113.5", out.NTPDaemon.RefSource)
	}
	if out.NTPDaemon.OffsetMs == nil || *out.NTPDaemon.OffsetMs != 0.15 {
		t.Errorf("daemon offset_ms = %v, want 0.15 (0.000145339 s)", out.NTPDaemon.OffsetMs)
	}
	if !out.Synchronized {
		t.Error("Synchronized = false, want true (chrony leap status Normal)")
	}
}

func TestTimeSyncTool_Execute_TimedatectlFallback(t *testing.T) {
	t.Parallel()

	tool := testTool(t)
	tool.adjtimex = func(tx *syscall.Timex) (int, error) {
		return 0, syscall.EPERM
	}
	tool.execLookPath = func(file string) (string, error) {
		if file == "timedatectl" {
			return "/usr/bin/timedatectl", nil
		}
		return "", fmt.Errorf("%s: not found", file)
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "timedatectl" {
			return nil, fmt.Errorf("unexpected command %s", name)
		}
		return []byte("Timezone=Etc/UTC\nLocalRTC=no\nCanNTP=yes\nNTP=yes\nNTPSynchronized=yes\nTimeUSec=Mon 2024-08-19 09:01:50 UTC\n"), nil
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.NTPDaemon == nil || out.NTPDaemon.Name != "systemd-timesyncd" {
		t.Fatalf("NTPDaemon = %+v, want systemd-timesyncd", out.NTPDaemon)
	}
	if !out.Synchronized {
		t.Error("Synchronized = false, want true (NTPSynchronized=yes)")
	}
	if out.KernelStatusAvailable {
		t.Error("KernelStatusAvailable = true, want false")
	}
}

func TestTimeSyncTool_Execute_NoKernelNoDaemon(t *testing.T) {
	t.Parallel()

	tool := testTool(t)
	tool.adjtimex = func(tx *syscall.Timex) (int, error) {
		return 0, syscall.EPERM
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning (nothing proves sync)", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.Synchronized {
		t.Error("Synchronized = true, want false with no evidence of sync")
	}
	if out.KernelStatusAvailable {
		t.Error("KernelStatusAvailable = true, want false")
	}
}

func TestParseChronyTracking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantErr    bool
		wantStrat  int
		wantRef    string
		wantOffset float64
		wantLeap   string
	}{
		{
			name:       "full modern output",
			input:      "A29FC87B,203.0.113.5,2,1723805518.224495338,0.000145339,-0.000004312,0.000035612,-0.696,0.021,3.7,0.000206396,0.000572906,64.2,Normal\n",
			wantStrat:  2,
			wantRef:    "203.0.113.5",
			wantOffset: 0.15,
			wantLeap:   "Normal",
		},
		{
			name:       "minimal five fields",
			input:      "7F7F0101,,10,1723805518.0,-0.5\n",
			wantStrat:  10,
			wantRef:    "7F7F0101",
			wantOffset: -500.0,
			wantLeap:   "",
		},
		{
			name:    "too few fields",
			input:   "A29FC87B,203.0.113.5,2\n",
			wantErr: true,
		},
		{
			name:    "bad stratum",
			input:   "A29FC87B,203.0.113.5,two,1723805518.0,0.0001,x,x,x,x,x,x,x,x,Normal\n",
			wantErr: true,
		},
		{
			name:    "bad offset",
			input:   "A29FC87B,203.0.113.5,2,1723805518.0,oops,x,x,x,x,x,x,x,x,Normal\n",
			wantErr: true,
		},
		{
			name:    "empty",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			daemon, leap, err := parseChronyTracking([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseChronyTracking() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if daemon.Name != "chrony" {
				t.Errorf("name = %q, want chrony", daemon.Name)
			}
			if daemon.Stratum != tt.wantStrat {
				t.Errorf("stratum = %d, want %d", daemon.Stratum, tt.wantStrat)
			}
			if daemon.RefSource != tt.wantRef {
				t.Errorf("ref_source = %q, want %q", daemon.RefSource, tt.wantRef)
			}
			if daemon.OffsetMs == nil || math.Abs(*daemon.OffsetMs-tt.wantOffset) > 1e-9 {
				t.Errorf("offset_ms = %v, want %v", daemon.OffsetMs, tt.wantOffset)
			}
			if leap != tt.wantLeap {
				t.Errorf("leap = %q, want %q", leap, tt.wantLeap)
			}
		})
	}
}

func TestParseTimedatectlShow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{
			name:  "typical output",
			input: "Timezone=Etc/UTC\nNTP=yes\nNTPSynchronized=no\n",
			want:  map[string]string{"Timezone": "Etc/UTC", "NTP": "yes", "NTPSynchronized": "no"},
		},
		{
			name:  "garbage lines ignored",
			input: "not a kv line\nNTP=yes\n\n",
			want:  map[string]string{"NTP": "yes"},
		},
		{
			name:  "empty",
			input: "",
			want:  map[string]string{},
		},
		{
			name:  "value containing equals",
			input: "TimeUSec=Mon 2024-08-19 09:01:50 UTC\nRTCTimeUSec=a=b\n",
			want:  map[string]string{"TimeUSec": "Mon 2024-08-19 09:01:50 UTC", "RTCTimeUSec": "a=b"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseTimedatectlShow([]byte(tt.input))
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestRound2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   float64
		want float64
	}{
		{145.339, 145.34},
		{-0.4312, -0.43},
		{50.0, 50.0},
		{0.005, 0.01},
		{-1.499999, -1.5},
	}
	for _, tt := range tests {
		if got := round2(tt.in); got != tt.want {
			t.Errorf("round2(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestTimeSyncTool_Execute_ContextCanceled(t *testing.T) {
	t.Parallel()

	tool := testTool(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on canceled context", res.Status)
	}
}
