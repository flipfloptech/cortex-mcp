package uptime

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

func TestUptimeTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "uptime" {
		t.Errorf("expected Name() == 'uptime', got %q", tool.Name())
	}
	if tool.Category() != "system" {
		t.Errorf("expected Category() == 'system', got %q", tool.Category())
	}
	if !strings.Contains(tool.Help(), "/proc/uptime") {
		t.Errorf("Help() missing source reference, got: %s", tool.Help())
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		seconds float64
		want    string
	}{
		{0.0, "0 seconds"},
		{45.5, "45 seconds"},
		{65.0, "1 minutes, 5 seconds"},
		{3600.0, "1 hours"},
		{3665.0, "1 hours, 1 minutes, 5 seconds"},
		{90000.0, "1 days, 1 hours"},
		{350735.47, "4 days, 1 hours, 25 minutes, 35 seconds"},
	}

	for _, tt := range tests {
		got := formatDuration(tt.seconds)
		if got != tt.want {
			t.Errorf("formatDuration(%.2f) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestUptimeTool_Execute(t *testing.T) {
	t.Parallel()

	// Create a temporary mock /proc/uptime file
	dir := t.TempDir()
	procFile := filepath.Join(dir, "uptime")
	err := os.WriteFile(procFile, []byte("350735.47 234388.90\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write mock uptime: %v", err)
	}

	tool := New()

	// Skip execution on non-linux or if /proc/uptime is missing
	supported, _ := tool.IsSupported()
	if !supported {
		t.Skip("skipping execution test on unsupported system")
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

	var data UptimeData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal result data: %v", err)
	}

	if data.UptimeSeconds <= 0 {
		t.Errorf("UptimeSeconds should be > 0, got %.2f", data.UptimeSeconds)
	}
	if data.IdlePercent < 0 || data.IdlePercent > 100 {
		t.Errorf("IdlePercent should be between 0 and 100, got %.2f", data.IdlePercent)
	}
	if data.HumanReadable == "" {
		t.Errorf("HumanReadable should not be empty")
	}
}
