package loadavg

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

func TestLoadAvgTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_loadavg" {
		t.Errorf("expected Name() == 'loadavg', got %q", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("expected Category() == 'system', got %q", tool.Category())
	}
	if !strings.Contains(tool.Help(), "/proc/loadavg") {
		t.Errorf("Help() missing source reference, got: %s", tool.Help())
	}
	if tool.Parameters() != nil {
		t.Errorf("expected Parameters() to be nil")
	}
}

func TestParseLoadAvg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
		want    LoadAvgData
	}{
		{
			name:    "valid",
			input:   "0.68 0.69 0.74 1/863 12345\n",
			wantErr: false,
			want: LoadAvgData{
				Load1:            0.68,
				Load5:            0.69,
				Load15:           0.74,
				RunnableEntities: 1,
				TotalEntities:    863,
				LastPID:          12345,
			},
		},
		{
			name:    "invalid format short",
			input:   "0.68 0.69 0.74 1/863",
			wantErr: true,
		},
		{
			name:    "invalid numbers",
			input:   "foo 0.69 0.74 1/863 12345\n",
			wantErr: true,
		},
		{
			name:    "invalid entities",
			input:   "0.68 0.69 0.74 foo 12345\n",
			wantErr: true,
		},
		{
			name:    "invalid entities ratio",
			input:   "0.68 0.69 0.74 1-863 12345\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseLoadAvg([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseLoadAvg() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseLoadAvg() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadAvgTool_Execute(t *testing.T) {
	t.Parallel()

	// Write mock /proc/loadavg
	dir := t.TempDir()
	procFile := filepath.Join(dir, "get_loadavg")
	err := os.WriteFile(procFile, []byte("0.68 0.69 0.74 2/1000 54321\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write mock loadavg: %v", err)
	}

	tool := New()

	// For the integration execution, if we are not on Linux or don't have access
	// to the real /proc/loadavg, we skip the live test.
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

	var data LoadAvgData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal result data: %v", err)
	}

	// We can't assert the exact values of the live system, but we can assert they are non-negative
	if data.Load1 < 0 || data.Load5 < 0 || data.Load15 < 0 {
		t.Errorf("loads should be >= 0, got 1:%.2f 5:%.2f 15:%.2f", data.Load1, data.Load5, data.Load15)
	}
	if data.TotalEntities <= 0 {
		t.Errorf("TotalEntities should be > 0, got %d", data.TotalEntities)
	}
	if data.RunnableEntities <= 0 {
		t.Errorf("RunnableEntities should be > 0, got %d", data.RunnableEntities)
	}
}
