package sysctltuning

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestSysctlTuningTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_sysctl_tuning_state" {
		t.Errorf("expected Name() == 'get_sysctl_tuning_state', got %q", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("expected Category() == 'system', got %q", tool.Category())
	}
	params := tool.Parameters()
	if len(params) != 1 {
		t.Errorf("expected 1 parameter, got %d", len(params))
	}
}

func TestResolveSysctlPath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		key      string
		expected string
	}{
		{"vm.dirty_ratio", "/proc/sys/vm/dirty_ratio"},
		{"net.ipv4.tcp_rmem", "/proc/sys/net/ipv4/tcp_rmem"},
		{"net.core.somaxconn", "/proc/sys/net/core/somaxconn"},
	}

	for _, tc := range testCases {
		res := resolveSysctlPath(tc.key)
		if res != tc.expected {
			t.Errorf("expected %q, got %q", tc.expected, res)
		}
	}
}

func TestSysctlTuningTool_Execute(t *testing.T) {
	t.Parallel()

	tool := &QuerySysctlTuningTool{
		readFile: func(path string) ([]byte, error) {
			if path == "/proc/sys/vm/dirty_ratio" {
				return []byte("20\n"), nil
			}
			if path == "/proc/sys/net/core/somaxconn" {
				return []byte("4096"), nil
			}
			return nil, errors.New("file not found")
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 2 && args[0] == "-n" {
				if args[1] == "net.ipv4.tcp_rmem" {
					return []byte("4096 131072 33554432\n"), nil
				}
			}
			return nil, errors.New("sysctl execution failed")
		},
	}

	// 1. Audit specific keys (one successful read, one command fallback, one error key)
	{
		args, _ := json.Marshal(map[string]interface{}{
			"keys": []string{"vm.dirty_ratio", "net.ipv4.tcp_rmem", "nonexistent.key"},
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		if res.Status != registry.StatusOK {
			t.Fatalf("expected status OK, got %s", res.Status)
		}

		var data SysctlData
		_ = json.Unmarshal(res.Data, &data)

		if data.Parameters["vm.dirty_ratio"] != "20" {
			t.Errorf("expected vm.dirty_ratio == '20', got %q", data.Parameters["vm.dirty_ratio"])
		}
		if data.Parameters["net.ipv4.tcp_rmem"] != "4096 131072 33554432" {
			t.Errorf("expected net.ipv4.tcp_rmem == '4096 131072 33554432', got %q", data.Parameters["net.ipv4.tcp_rmem"])
		}
		if data.Parameters["nonexistent.key"] != "error: file not found" {
			t.Errorf("expected nonexistent.key error message, got %q", data.Parameters["nonexistent.key"])
		}
	}

	// 2. Audit default critical HPC parameters
	{
		res, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}

		var data SysctlData
		_ = json.Unmarshal(res.Data, &data)

		// vm.dirty_ratio and net.core.somaxconn are mocked to succeed
		if data.Parameters["vm.dirty_ratio"] != "20" {
			t.Errorf("expected default vm.dirty_ratio == '20', got %q", data.Parameters["vm.dirty_ratio"])
		}
		if data.Parameters["net.core.somaxconn"] != "4096" {
			t.Errorf("expected default net.core.somaxconn == '4096', got %q", data.Parameters["net.core.somaxconn"])
		}
		// net.ipv4.conf.all.rp_filter should have error message
		if data.Parameters["net.ipv4.conf.all.rp_filter"] != "error: file not found" {
			t.Errorf("expected error for net.ipv4.conf.all.rp_filter, got %q", data.Parameters["net.ipv4.conf.all.rp_filter"])
		}
	}
}
