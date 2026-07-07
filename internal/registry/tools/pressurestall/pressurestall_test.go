package pressurestall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// writePressureTree creates a fake <root>/pressure directory populated with
// the given resource files. Returns the fake procfs root.
func writePressureTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "pressure")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir pressure: %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func TestPressureStallTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_pressure_stall_info" {
		t.Errorf("Name() = %q, want get_pressure_stall_info", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("Category() = %q, want system", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	if !strings.Contains(tool.Help(), "/proc/pressure") {
		t.Errorf("Help() must reference /proc/pressure data source, got: %s", tool.Help())
	}
	// Thresholds must be documented for the LLM.
	if !strings.Contains(tool.Help(), "40") || !strings.Contains(tool.Help(), "10") {
		t.Errorf("Help() must document warning thresholds, got: %s", tool.Help())
	}
	if tool.Parameters() != nil {
		t.Error("Parameters() must be nil (no parameters)")
	}
	if tool.Hidden() {
		t.Error("Hidden() must be false")
	}
}

func TestPressureStallTool_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("supported when pressure/cpu exists", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.procfsRoot = writePressureTree(t, map[string]string{
			"cpu": "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		})
		ok, reason := tool.IsSupported()
		if !ok {
			t.Errorf("IsSupported() = false (%s), want true", reason)
		}
	})

	t.Run("unsupported when PSI missing", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.procfsRoot = t.TempDir()
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("IsSupported() = true, want false")
		}
		if !strings.Contains(reason, "PSI not available") {
			t.Errorf("reason = %q, want mention of PSI not available", reason)
		}
	})
}

func TestParsePSILine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		line     string
		wantKind string
		want     PSILine
		wantErr  bool
	}{
		{
			name:     "valid some line",
			line:     "some avg10=0.12 avg60=1.50 avg300=0.03 total=123456",
			wantKind: "some",
			want:     PSILine{Avg10: 0.12, Avg60: 1.50, Avg300: 0.03, TotalUsec: 123456},
		},
		{
			name:     "valid full line",
			line:     "full avg10=42.00 avg60=10.10 avg300=9.99 total=999999999999",
			wantKind: "full",
			want:     PSILine{Avg10: 42.00, Avg60: 10.10, Avg300: 9.99, TotalUsec: 999999999999},
		},
		{
			name:    "garbage line",
			line:    "this is not psi",
			wantErr: true,
		},
		{
			name:    "empty line",
			line:    "",
			wantErr: true,
		},
		{
			name:    "missing fields",
			line:    "some avg10=0.00",
			wantErr: true,
		},
		{
			name:    "non numeric avg",
			line:    "some avg10=abc avg60=0.00 avg300=0.00 total=0",
			wantErr: true,
		},
		{
			name:    "non numeric total",
			line:    "some avg10=0.00 avg60=0.00 avg300=0.00 total=xyz",
			wantErr: true,
		},
		{
			name:    "unknown kind",
			line:    "half avg10=0.00 avg60=0.00 avg300=0.00 total=0",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			kind, got, err := parsePSILine(tt.line)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parsePSILine(%q) error = %v, wantErr %v", tt.line, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", kind, tt.wantKind)
			}
			if got != tt.want {
				t.Errorf("line = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParsePressureFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  string
		wantSome *PSILine
		wantFull *PSILine
		wantErr  bool
	}{
		{
			name:     "some and full",
			content:  "some avg10=1.00 avg60=2.00 avg300=3.00 total=100\nfull avg10=4.00 avg60=5.00 avg300=6.00 total=200\n",
			wantSome: &PSILine{Avg10: 1.00, Avg60: 2.00, Avg300: 3.00, TotalUsec: 100},
			wantFull: &PSILine{Avg10: 4.00, Avg60: 5.00, Avg300: 6.00, TotalUsec: 200},
		},
		{
			name:     "some only (cpu on older kernels)",
			content:  "some avg10=0.55 avg60=0.66 avg300=0.77 total=42\n",
			wantSome: &PSILine{Avg10: 0.55, Avg60: 0.66, Avg300: 0.77, TotalUsec: 42},
		},
		{
			name:    "malformed line",
			content: "some avg10=oops avg60=0.00 avg300=0.00 total=0\n",
			wantErr: true,
		},
		{
			name:    "empty file",
			content: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parsePressureFile([]byte(tt.content))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parsePressureFile() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if (got.Some == nil) != (tt.wantSome == nil) {
				t.Fatalf("Some presence = %v, want %v", got.Some != nil, tt.wantSome != nil)
			}
			if tt.wantSome != nil && *got.Some != *tt.wantSome {
				t.Errorf("Some = %+v, want %+v", *got.Some, *tt.wantSome)
			}
			if (got.Full == nil) != (tt.wantFull == nil) {
				t.Fatalf("Full presence = %v, want %v", got.Full != nil, tt.wantFull != nil)
			}
			if tt.wantFull != nil && *got.Full != *tt.wantFull {
				t.Errorf("Full = %+v, want %+v", *got.Full, *tt.wantFull)
			}
		})
	}
}

func TestPressureStallTool_Execute_HappyPath(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writePressureTree(t, map[string]string{
		"cpu":    "some avg10=1.23 avg60=0.50 avg300=0.10 total=1000\n",
		"memory": "some avg10=0.00 avg60=0.00 avg300=0.00 total=10\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=5\n",
		"io":     "some avg10=2.00 avg60=1.00 avg300=0.50 total=999\nfull avg10=1.50 avg60=0.75 avg300=0.25 total=500\n",
		"irq":    "full avg10=0.00 avg60=0.00 avg300=0.00 total=1\n",
	})

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

	cpu, ok := out.Resources["cpu"]
	if !ok || cpu.Some == nil {
		t.Fatal("missing cpu.some in output")
	}
	if cpu.Some.Avg10 != 1.23 || cpu.Some.TotalUsec != 1000 {
		t.Errorf("cpu.some = %+v, want avg10=1.23 total=1000", cpu.Some)
	}

	mem, ok := out.Resources["memory"]
	if !ok || mem.Some == nil || mem.Full == nil {
		t.Fatal("missing memory some/full in output")
	}
	if mem.Full.TotalUsec != 5 {
		t.Errorf("memory.full.total_usec = %d, want 5", mem.Full.TotalUsec)
	}

	io, ok := out.Resources["io"]
	if !ok || io.Full == nil {
		t.Fatal("missing io.full in output")
	}
	if io.Full.Avg10 != 1.50 {
		t.Errorf("io.full.avg10 = %v, want 1.50", io.Full.Avg10)
	}

	irq, ok := out.Resources["irq"]
	if !ok || irq.Full == nil {
		t.Fatal("missing irq.full in output")
	}

	if len(out.WarningReasons) != 0 {
		t.Errorf("WarningReasons = %v, want none", out.WarningReasons)
	}
}

func TestPressureStallTool_Execute_Warnings(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writePressureTree(t, map[string]string{
		"cpu":    "some avg10=55.00 avg60=20.00 avg300=5.00 total=1000\n",
		"memory": "some avg10=30.00 avg60=1.00 avg300=0.00 total=10\nfull avg10=12.50 avg60=1.00 avg300=0.00 total=5\n",
		"io":     "some avg10=90.00 avg60=1.00 avg300=0.00 total=999\nfull avg10=25.00 avg60=1.00 avg300=0.00 total=500\n",
	})

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
	if len(out.WarningReasons) != 3 {
		t.Fatalf("WarningReasons = %v, want exactly 3 (cpu some, memory full, io full)", out.WarningReasons)
	}
	joined := strings.Join(out.WarningReasons, " | ")
	for _, want := range []string{"cpu", "memory", "io"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q missing resource %q", joined, want)
		}
	}
}

func TestPressureStallTool_Execute_MissingIRQIsFine(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writePressureTree(t, map[string]string{
		"cpu":    "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		"memory": "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		"io":     "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
	})

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
	if _, present := out.Resources["irq"]; present {
		t.Error("irq must be omitted when /proc/pressure/irq is absent")
	}
}

func TestPressureStallTool_Execute_MalformedResourceSkippedWithNote(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writePressureTree(t, map[string]string{
		"cpu":    "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		"memory": "complete garbage here\n",
		"io":     "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
	})

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
	if _, present := out.Resources["memory"]; present {
		t.Error("malformed memory resource must be skipped")
	}
	if len(out.Notes) == 0 || !strings.Contains(strings.Join(out.Notes, " "), "memory") {
		t.Errorf("Notes = %v, want a note mentioning skipped memory resource", out.Notes)
	}
}

func TestPressureStallTool_Execute_NoPSIAtAll(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = t.TempDir() // no pressure/ dir at all

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute must encapsulate errors, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error", res.Status)
	}
}

func TestPressureStallTool_Execute_ContextCanceled(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writePressureTree(t, map[string]string{
		"cpu": "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
	})

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
