package kernelmodules

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestKernelModulesTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_kernel_modules" {
		t.Errorf("expected Name() == 'get_kernel_modules', got %q", tool.Name())
	}
	if tool.Category() != "system" {
		t.Errorf("expected Category() == 'system', got %q", tool.Category())
	}
	params := tool.Parameters()
	if len(params) != 1 {
		t.Errorf("expected 1 parameter, got %d", len(params))
	}
}

func TestParseModulesBuffer(t *testing.T) {
	t.Parallel()

	// 1. Test /proc/modules format parsing
	{
		input := `inet_diag 20480 2 tcp_diag,udp_diag, Live 0xffffffffc04c5000
vhost_net 36864 12 - Live 0xffffffffc04ab000`
		modules := parseModulesBuffer([]byte(input))
		if len(modules) != 2 {
			t.Fatalf("expected 2 modules, got %d", len(modules))
		}

		m0 := modules[0]
		if m0.Name != "inet_diag" || m0.Size != 20480 || m0.RefCount != 2 || len(m0.UsedBy) != 2 || m0.UsedBy[0] != "tcp_diag" || m0.UsedBy[1] != "udp_diag" || m0.State != "Live" || m0.Address != "0xffffffffc04c5000" {
			t.Errorf("unexpected parsed module 0: %+v", m0)
		}

		m1 := modules[1]
		if m1.Name != "vhost_net" || m1.Size != 36864 || m1.RefCount != 12 || len(m1.UsedBy) != 0 || m1.State != "Live" || m1.Address != "0xffffffffc04ab000" {
			t.Errorf("unexpected parsed module 1: %+v", m1)
		}
	}

	// 2. Test lsmod format parsing
	{
		input := `Module                  Size  Used by
uinput                 28672  1 
tcp_diag               20480  0 `
		modules := parseModulesBuffer([]byte(input))
		if len(modules) != 2 {
			t.Fatalf("expected 2 modules, got %d", len(modules))
		}

		m0 := modules[0]
		if m0.Name != "uinput" || m0.Size != 28672 || m0.RefCount != 1 || len(m0.UsedBy) != 0 {
			t.Errorf("unexpected parsed module 0 from lsmod: %+v", m0)
		}
	}
}

func TestKernelModulesTool_Execute(t *testing.T) {
	t.Parallel()

	tool := &QueryKernelModulesTool{
		readFile: func(path string) ([]byte, error) {
			if path == "/proc/modules" {
				return []byte(`inet_diag 20480 2 tcp_diag,udp_diag, Live 0xffffffffc04c5000
vhost_net 36864 12 - Live 0xffffffffc04ab000`), nil
			}
			return nil, errors.New("file not found")
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("should not be called as file read succeeded")
		},
	}

	// 1. Execute without filter
	{
		res, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}

		if res.Status != registry.StatusOK {
			t.Fatalf("expected status OK, got %s", res.Status)
		}

		var data KernelModulesData
		_ = json.Unmarshal(res.Data, &data)

		if len(data.Modules) != 2 {
			t.Errorf("expected 2 modules, got %d", len(data.Modules))
		}
	}

	// 2. Execute with filter
	{
		args, _ := json.Marshal(map[string]interface{}{
			"filter": "vhost",
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		var data KernelModulesData
		_ = json.Unmarshal(res.Data, &data)

		if len(data.Modules) != 1 || data.Modules[0].Name != "vhost_net" {
			t.Errorf("expected only vhost_net module, got %+v", data.Modules)
		}
	}
}

func TestKernelModulesTool_Fallback(t *testing.T) {
	t.Parallel()

	tool := &QueryKernelModulesTool{
		readFile: func(path string) ([]byte, error) {
			return nil, errors.New("permission denied")
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "lsmod" {
				return []byte(`Module                  Size  Used by
uinput                 28672  1 `), nil
			}
			return nil, errors.New("cmd failed")
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var data KernelModulesData
	_ = json.Unmarshal(res.Data, &data)

	if len(data.Modules) != 1 || data.Modules[0].Name != "uinput" {
		t.Errorf("expected fallback uinput module, got %+v", data.Modules)
	}
}
