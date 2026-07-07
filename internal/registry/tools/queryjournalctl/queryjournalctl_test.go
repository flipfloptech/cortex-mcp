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
	if len(params) != 8 {
		t.Errorf("expected 8 parameters, got %d", len(params))
	}
}

func TestFormatRealtimeTimestamp(t *testing.T) {
	t.Parallel()

	res := formatRealtimeTimestamp("1783433504008977")
	expected := "2026-07-07T14:11:44.008977Z"
	if res != expected {
		t.Errorf("expected %q, got %q", expected, res)
	}

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

func TestParseListBoots(t *testing.T) {
	t.Parallel()

	input := `IDX BOOT ID                          FIRST ENTRY                 LAST ENTRY
 -2 a35f46f9fd7a4357809c6f15d2044c0f Mon 2026-07-06 13:01:35 EDT Mon 2026-07-06 17:51:27 EDT
  0 c39ab38571934210860c3709c82a791e Mon 2026-07-06 18:55:20 EDT Tue 2026-07-07 10:12:44 EDT
`
	boots := parseListBoots([]byte(input))
	if len(boots) != 2 {
		t.Fatalf("expected 2 boots, got %d", len(boots))
	}

	if boots[0].Index != -2 || boots[0].BootID != "a35f46f9fd7a4357809c6f15d2044c0f" || boots[0].FirstEntry != "Mon 2026-07-06 13:01:35 EDT" || boots[0].LastEntry != "Mon 2026-07-06 17:51:27 EDT" {
		t.Errorf("unexpected boot 0: %+v", boots[0])
	}
	if boots[1].Index != 0 || boots[1].BootID != "c39ab38571934210860c3709c82a791e" {
		t.Errorf("unexpected boot 1: %+v", boots[1])
	}
}

func TestQueryJournalctlTool_Execute(t *testing.T) {
	t.Parallel()

	tool := &QueryJournalctlTool{
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			hasJSON := false
			hasLines := false
			hasBoot := false
			hasListBoots := false

			for _, arg := range args {
				if arg == "json" {
					hasJSON = true
				}
				if arg == "5" {
					hasLines = true
				}
				if arg == "-b" {
					hasBoot = true
				}
				if arg == "--list-boots" {
					hasListBoots = true
				}
			}

			if hasListBoots {
				return []byte(`IDX BOOT ID                          FIRST ENTRY                 LAST ENTRY
 -1 1ca8e302d5ba4b66ad92a12f28d14115 Mon 2026-07-06 17:59:18 EDT Mon 2026-07-06 18:55:03 EDT`), nil
			}

			if !hasJSON {
				return nil, errors.New("expected json output formatting")
			}
			if !hasLines {
				return nil, errors.New("expected lines parameter limit")
			}
			if hasBoot {
				return []byte(`{"__REALTIME_TIMESTAMP":"1783433504008977","MESSAGE":"boot filtered message","SYSLOG_IDENTIFIER":"kernel","PRIORITY":"4","_HOSTNAME":"strixhalo"}`), nil
			}

			return []byte(`{"__REALTIME_TIMESTAMP":"1783433504008977","MESSAGE":"split lock detection","SYSLOG_IDENTIFIER":"kernel","PRIORITY":"4","_HOSTNAME":"strixhalo"}
{"__REALTIME_TIMESTAMP":"1783433504008992","MESSAGE":"unrelated message","SYSLOG_IDENTIFIER":"systemd","PRIORITY":"6","_HOSTNAME":"strixhalo"}`), nil
		},
	}

	// 1. Regular logs query
	{
		args, _ := json.Marshal(map[string]interface{}{
			"lines": 5,
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		var data JournalctlData
		_ = json.Unmarshal(res.Data, &data)

		if len(data.Entries) != 2 || len(data.Boots) != 0 {
			t.Errorf("expected 2 entries and 0 boots, got entries %d, boots %d", len(data.Entries), len(data.Boots))
		}
	}

	// 2. Query with boot ID filter
	{
		args, _ := json.Marshal(map[string]interface{}{
			"lines": 5,
			"boot":  "-1",
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		var data JournalctlData
		_ = json.Unmarshal(res.Data, &data)

		if len(data.Entries) != 1 || data.Entries[0].Message != "boot filtered message" {
			t.Errorf("expected 1 boot-filtered entry, got %v", data.Entries)
		}
	}

	// 3. List boots
	{
		args, _ := json.Marshal(map[string]interface{}{
			"list_boots": true,
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}

		var data JournalctlData
		_ = json.Unmarshal(res.Data, &data)

		if len(data.Boots) != 1 || data.Boots[0].BootID != "1ca8e302d5ba4b66ad92a12f28d14115" {
			t.Errorf("expected 1 listed boot, got %v", data.Boots)
		}
	}
}
