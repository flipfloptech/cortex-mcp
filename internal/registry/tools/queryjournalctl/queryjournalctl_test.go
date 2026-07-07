package queryjournalctl

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestQueryJournalctlTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "query_journalctl" {
		t.Errorf("expected Name() == 'query_journalctl', got %q", tool.Name())
	}
	if tool.Category() != "system" {
		t.Errorf("expected Category() == 'system', got %q", tool.Category())
	}
	params := tool.Parameters()
	if len(params) != 6 {
		t.Errorf("expected 6 parameters, got %d", len(params))
	}
}

func TestFormatRealtimeTimestamp(t *testing.T) {
	t.Parallel()

	// 1783433504008977 is a timestamp in microseconds
	// 1783433504 seconds is 2026-07-07 14:11:44 UTC
	res := formatRealtimeTimestamp("1783433504008977")
	expected := "2026-07-07T14:11:44.008977Z"
	if res != expected {
		t.Errorf("expected %q, got %q", expected, res)
	}

	// Test invalid timestamp strings
	if formatRealtimeTimestamp("invalid") != "" {
		t.Errorf("expected empty string for invalid timestamp")
	}
}

func TestParseJournalLine(t *testing.T) {
	t.Parallel()

	input := `{"__REALTIME_TIMESTAMP":"1783433504008977","MESSAGE":"split lock detection","SYSLOG_IDENTIFIER":"kernel","PRIORITY":"4","_HOSTNAME":"strixhalo","_PID":"1234"}`
	entry, err := parseJournalLine([]byte(input))
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	if entry.Message != "split lock detection" || entry.Timestamp != "2026-07-07T14:11:44.008977Z" || entry.Identifier != "kernel" || entry.Priority != "4" || entry.Hostname != "strixhalo" || entry.Pid != "1234" {
		t.Errorf("unexpected parsed entry: %+v", entry)
	}
}

func TestQueryJournalctlTool_Execute(t *testing.T) {
	t.Parallel()

	tool := &QueryJournalctlTool{
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// verify query options
			hasJSON := false
			hasLines := false
			for _, arg := range args {
				if arg == "json" {
					hasJSON = true
				}
				if arg == "5" {
					hasLines = true
				}
			}
			if !hasJSON {
				return nil, errors.New("expected json output formatting")
			}
			if !hasLines {
				return nil, errors.New("expected lines parameter limit")
			}

			return []byte(`{"__REALTIME_TIMESTAMP":"1783433504008977","MESSAGE":"split lock detection","SYSLOG_IDENTIFIER":"kernel","PRIORITY":"4","_HOSTNAME":"strixhalo"}
{"__REALTIME_TIMESTAMP":"1783433504008992","MESSAGE":"unrelated message","SYSLOG_IDENTIFIER":"systemd","PRIORITY":"6","_HOSTNAME":"strixhalo"}`), nil
		},
	}

	// Execute without grep filter
	{
		args, _ := json.Marshal(map[string]interface{}{
			"lines": 5,
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		if res.Status != registry.StatusOK {
			t.Fatalf("expected status OK, got %s. Summary: %s", res.Status, res.Summary)
		}

		var data JournalctlData
		if err := json.Unmarshal(res.Data, &data); err != nil {
			t.Fatal(err)
		}

		if len(data.Entries) != 2 {
			t.Errorf("expected 2 entries, got %d", len(data.Entries))
		}
	}

	// Execute with regex grep filter
	{
		args, _ := json.Marshal(map[string]interface{}{
			"lines": 5,
			"grep":  "split lock",
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		var data JournalctlData
		if err := json.Unmarshal(res.Data, &data); err != nil {
			t.Fatal(err)
		}

		if len(data.Entries) != 1 || data.Entries[0].Message != "split lock detection" {
			t.Errorf("expected only 1 matching entry, got %d entries: %+v", len(data.Entries), data.Entries)
		}
	}
}
