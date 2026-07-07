package querydmesg

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestQueryDmesgTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "query_dmesg" {
		t.Errorf("expected Name() == 'query_dmesg', got %q", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("expected Category() == 'system', got %q", tool.Category())
	}
	params := tool.Parameters()
	if len(params) != 3 {
		t.Errorf("expected 3 parameters, got %d", len(params))
	}
}

func TestParseDmesgLine(t *testing.T) {
	t.Parallel()

	// Test full line with level and timestamp
	{
		input := "<4>[  261.273849] EXT4-fs (sda1): re-mounted. Opts: (null)"
		entry := parseDmesgLine(input)
		if entry.Level != "warn" || entry.Facility != "0" || entry.Timestamp != 261.273849 || entry.Message != "EXT4-fs (sda1): re-mounted. Opts: (null)" {
			t.Errorf("unexpected parsed entry: %+v", entry)
		}
	}

	// Test line with no level but timestamp
	{
		input := "[    0.000000] Linux version 6.8"
		entry := parseDmesgLine(input)
		if entry.Level != "" || entry.Timestamp != 0.0 || entry.Message != "Linux version 6.8" {
			t.Errorf("unexpected parsed entry: %+v", entry)
		}
	}

	// Test line with no level and no timestamp
	{
		input := "simple unstructured message"
		entry := parseDmesgLine(input)
		if entry.Level != "" || entry.Timestamp != 0.0 || entry.Message != "simple unstructured message" {
			t.Errorf("unexpected parsed entry: %+v", entry)
		}
	}
}

func TestQueryDmesgTool_Execute(t *testing.T) {
	t.Parallel()

	tool := &QueryDmesgTool{
		sysKlogctlSize: func(action int) (int, error) {
			return 256, nil
		},
		sysKlogctlRead: func(action int, buf []byte) (int, error) {
			copy(buf, []byte("<4>[  261.273849] EXT4-fs warning\n<3>[  262.123456] EXT4-fs error\n<6>[  263.000000] normal info message"))
			return 88, nil
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("should not be called as sysKlogctl succeeded")
		},
	}

	// 1. Regular dmesg query (no filtering, limit to 2 lines)
	{
		args, _ := json.Marshal(map[string]interface{}{
			"lines": 2,
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		if res.Status != registry.StatusOK {
			t.Fatalf("expected status OK, got %s", res.Status)
		}

		var data DmesgData
		_ = json.Unmarshal(res.Data, &data)

		// should get the last 2 entries
		if len(data.Entries) != 2 {
			t.Errorf("expected 2 entries, got %d", len(data.Entries))
		}
		if data.Entries[0].Level != "err" || data.Entries[1].Level != "info" {
			t.Errorf("unexpected levels in output: %+v", data.Entries)
		}
	}

	// 2. Grep regex filter
	{
		args, _ := json.Marshal(map[string]interface{}{
			"grep": "EXT4-fs",
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		var data DmesgData
		_ = json.Unmarshal(res.Data, &data)

		if len(data.Entries) != 2 {
			t.Errorf("expected 2 entries matching 'EXT4-fs', got %d", len(data.Entries))
		}
	}

	// 3. Level filter
	{
		args, _ := json.Marshal(map[string]interface{}{
			"level": "err",
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		var data DmesgData
		_ = json.Unmarshal(res.Data, &data)

		if len(data.Entries) != 1 || data.Entries[0].Level != "err" {
			t.Errorf("expected 1 entry with 'err' level, got %+v", data.Entries)
		}
	}
}

func TestQueryDmesgTool_Fallback(t *testing.T) {
	t.Parallel()

	tool := &QueryDmesgTool{
		sysKlogctlSize: func(action int) (int, error) {
			return 0, errors.New("permission denied")
		},
		sysKlogctlRead: func(action int, buf []byte) (int, error) {
			return 0, errors.New("permission denied")
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// verify fallback parameters
			if name != "dmesg" || len(args) != 1 || args[0] != "-r" {
				return nil, errors.New("unexpected command call")
			}
			return []byte("<4>[  261.273849] fallback warn message"), nil
		},
	}

	args, _ := json.Marshal(map[string]interface{}{})
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	var data DmesgData
	_ = json.Unmarshal(res.Data, &data)

	if len(data.Entries) != 1 || data.Entries[0].Message != "fallback warn message" {
		t.Errorf("expected fallback warn message, got %+v", data.Entries)
	}
}
