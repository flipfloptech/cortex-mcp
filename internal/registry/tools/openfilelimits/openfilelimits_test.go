package openfilelimits

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestOpenFileLimitsTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_open_file_limits" {
		t.Errorf("expected Name() == 'get_open_file_limits', got %q", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("expected Category() == 'system', got %q", tool.Category())
	}
	params := tool.Parameters()
	if len(params) != 0 {
		t.Errorf("expected 0 parameters, got %d", len(params))
	}
}

func TestParseFileNr(t *testing.T) {
	t.Parallel()

	// 1. Proc format
	{
		input := "18120\t0\t2097152\n"
		data, err := parseFileNr([]byte(input))
		if err != nil {
			t.Fatal(err)
		}

		if data.Allocated != 18120 || data.Unused != 0 || data.Max != 2097152 || data.UsagePercent != (18120.0*100.0)/2097152.0 {
			t.Errorf("unexpected parsed proc data: %+v", data)
		}
	}

	// 2. Sysctl format
	{
		input := "fs.file-nr = 18120\t0\t2097152"
		data, err := parseFileNr([]byte(input))
		if err != nil {
			t.Fatal(err)
		}

		if data.Allocated != 18120 || data.Unused != 0 || data.Max != 2097152 {
			t.Errorf("unexpected parsed sysctl data: %+v", data)
		}
	}

	// 3. Error case
	{
		_, err := parseFileNr([]byte("invalid format"))
		if err == nil {
			t.Errorf("expected error on invalid format")
		}
	}
}

func TestOpenFileLimitsTool_Execute(t *testing.T) {
	t.Parallel()

	tool := &QueryOpenFileLimitsTool{
		readFile: func(path string) ([]byte, error) {
			if path == "/proc/sys/fs/file-nr" {
				return []byte("1000\t0\t100000"), nil
			}
			return nil, errors.New("file not found")
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("should not be called as file read succeeded")
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if res.Status != registry.StatusOK {
		t.Fatalf("expected status OK, got %s", res.Status)
	}

	var data FileLimitsData
	_ = json.Unmarshal(res.Data, &data)

	if data.Allocated != 1000 || data.Max != 100000 || data.UsagePercent != 1.0 {
		t.Errorf("unexpected data: %+v", data)
	}
}

func TestOpenFileLimitsTool_Fallback(t *testing.T) {
	t.Parallel()

	tool := &QueryOpenFileLimitsTool{
		readFile: func(path string) ([]byte, error) {
			return nil, errors.New("permission denied")
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 1 && args[0] == "fs.file-nr" {
				return []byte("fs.file-nr = 5000\t0\t100000"), nil
			}
			return nil, errors.New("cmd failed")
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var data FileLimitsData
	_ = json.Unmarshal(res.Data, &data)

	if data.Allocated != 5000 || data.Max != 100000 || data.UsagePercent != 5.0 {
		t.Errorf("expected fallback data, got %+v", data)
	}
}
