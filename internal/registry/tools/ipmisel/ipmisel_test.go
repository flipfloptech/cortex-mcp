package ipmisel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// selElistFixture mirrors real `ipmitool sel elist` output, including a
// Pre-Init timestamp row and Critical / Non-recoverable severities.
const selElistFixture = `   1 | 05/01/2026 | 13:02:11 | Temperature #0x30 | Upper Critical going high | Asserted
   2 | 05/01/2026 | 13:02:45 | Temperature #0x30 | Upper Critical going high | Deasserted
   3 | 05/02/2026 | 08:15:02 | Fan #0x41 | Lower Non-recoverable going low | Asserted
   4 |  Pre-Init  |Pre-Init   | Memory #0x53 | Correctable ECC | Asserted
   5 | 05/03/2026 | 21:44:19 | Power Supply #0x62 | Presence detected | Asserted
   6 | 05/04/2026 | 02:10:33 | Event Logging Disabled #0x07 | Log area reset/cleared | Asserted
`

// selInfoFixture mirrors real `ipmitool sel info` output.
const selInfoFixture = `SEL Information
Version          : 1.5 (v1.5, v2 compliant)
Entries          : 132
Free Space       : 8144 bytes 
Percent Used     : 20%
Last Add Time    : 05/04/2026 02:10:33
Last Del Time    : Not Available
Overflow         : false
Supported Cmds   : 'Reserve' 'Get Alloc Info' 
# of Alloc Units : 512
Alloc Unit Size  : 20
# Free Units     : 407
Largest Free Blk : 407
Max Record Size  : 20
`

type stubOpts struct {
	lookPaths map[string]bool
	euid      int
	devices   []string // relative to devRoot, e.g. "ipmi0", "ipmi/0"
	cmd       func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// stubEnv swaps the package-level test seams and restores them on cleanup.
// Tests that call it mutate shared package state and must NOT call t.Parallel().
func stubEnv(t *testing.T, opts stubOpts) {
	t.Helper()
	oldLook, oldCmd, oldEuid, oldDev := execLookPath, execCommand, geteuid, devRoot
	t.Cleanup(func() {
		execLookPath, execCommand, geteuid, devRoot = oldLook, oldCmd, oldEuid, oldDev
	})

	execLookPath = func(file string) (string, error) {
		if opts.lookPaths[file] {
			return "/mock/bin/" + file, nil
		}
		return "", errors.New("executable file not found in $PATH")
	}
	euid := opts.euid
	geteuid = func() int { return euid }

	devRoot = t.TempDir()
	for _, rel := range opts.devices {
		full := filepath.Join(devRoot, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if opts.cmd != nil {
		execCommand = opts.cmd
	} else {
		execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Errorf("unexpected execCommand invocation: %s %v", name, args)
			return nil, errors.New("unexpected invocation")
		}
	}
}

// selCmdMock dispatches on the ipmitool subcommand ("elist" vs "info").
func selCmdMock(elistOut string, elistErr error, infoOut string, infoErr error) func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "elist") {
			return []byte(elistOut), elistErr
		}
		if strings.Contains(joined, "info") {
			return []byte(infoOut), infoErr
		}
		return nil, fmt.Errorf("unexpected command: %s %v", name, args)
	}
}

func TestToolContract(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "query_ipmi_sel" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "query_ipmi_sel")
	}
	if tool.Category() != registry.CategoryHardware {
		t.Errorf("Category() = %q, want %q", tool.Category(), registry.CategoryHardware)
	}
	if tool.Hidden() {
		t.Error("Hidden() = true, want false")
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}

	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("Parameters() = %v, want exactly last_n", params)
	}
	p := params[0]
	if p.Name != "last_n" || p.Type != "integer" || p.Required || p.Default != "25" {
		t.Errorf("last_n param = %+v", p)
	}

	help := tool.Help()
	for _, src := range []string{"ipmitool sel elist", "sel info", "/dev/ipmi0"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

func TestIsSupported(t *testing.T) {
	tests := []struct {
		name        string
		ipmitool    bool
		devices     []string
		want        bool
		reasonParts []string
	}{
		{name: "binary and /dev/ipmi0", ipmitool: true, devices: []string{"ipmi0"}, want: true},
		{name: "binary and /dev/ipmi/0", ipmitool: true, devices: []string{"ipmi/0"}, want: true},
		{name: "binary and /dev/ipmidev/0", ipmitool: true, devices: []string{"ipmidev/0"}, want: true},
		{name: "binary missing", ipmitool: false, devices: []string{"ipmi0"}, want: false, reasonParts: []string{"ipmitool"}},
		{name: "device missing", ipmitool: true, want: false, reasonParts: []string{"BMC"}},
		{name: "both missing", ipmitool: false, want: false, reasonParts: []string{"ipmitool", "BMC"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubEnv(t, stubOpts{
				lookPaths: map[string]bool{"ipmitool": tc.ipmitool},
				devices:   tc.devices,
				cmd: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return nil, errors.New("IsSupported must not execute commands")
				},
			})

			got, reason := New().IsSupported()
			if got != tc.want {
				t.Fatalf("IsSupported() = %v, want %v (reason %q)", got, tc.want, reason)
			}
			for _, part := range tc.reasonParts {
				if !strings.Contains(reason, part) {
					t.Errorf("reason %q must mention %q", reason, part)
				}
			}
		})
	}
}

func TestExecute_HappyPath(t *testing.T) {
	stubEnv(t, stubOpts{
		lookPaths: map[string]bool{"ipmitool": true},
		euid:      0,
		devices:   []string{"ipmi0"},
		cmd:       selCmdMock(selElistFixture, nil, selInfoFixture, nil),
	})

	res, err := New().Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (encapsulated)", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning (Critical/Non-recoverable records present); summary: %s", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("Data unmarshal: %v", err)
	}

	if out.SELInfo == nil {
		t.Fatal("sel_info missing")
	}
	if out.SELInfo.Entries == nil || *out.SELInfo.Entries != 132 {
		t.Errorf("entries = %v, want 132", out.SELInfo.Entries)
	}
	if out.SELInfo.FreeSpaceBytes == nil || *out.SELInfo.FreeSpaceBytes != 8144 {
		t.Errorf("free_space_bytes = %v, want 8144", out.SELInfo.FreeSpaceBytes)
	}
	if out.SELInfo.PercentUsed == nil || *out.SELInfo.PercentUsed != 20 {
		t.Errorf("percent_used = %v, want 20", out.SELInfo.PercentUsed)
	}

	if len(out.Records) != 6 {
		t.Fatalf("got %d records, want 6", len(out.Records))
	}
	r0 := out.Records[0]
	if r0.ID != "1" || r0.Sensor != "Temperature #0x30" || r0.Event != "Upper Critical going high" || r0.Direction != "Asserted" {
		t.Errorf("record 0 = %+v", r0)
	}
	if r0.Timestamp != "2026-05-01T13:02:11Z" {
		t.Errorf("record 0 timestamp = %q, want RFC3339 2026-05-01T13:02:11Z", r0.Timestamp)
	}
	if out.Records[1].Direction != "Deasserted" {
		t.Errorf("record 1 direction = %q, want Deasserted", out.Records[1].Direction)
	}
	if out.Records[3].Timestamp != "Pre-Init" {
		t.Errorf("record 3 timestamp = %q, want raw Pre-Init preserved", out.Records[3].Timestamp)
	}

	wantCounts := map[string]int{
		"Temperature":            2,
		"Fan":                    1,
		"Memory":                 1,
		"Power Supply":           1,
		"Event Logging Disabled": 1,
	}
	if len(out.CountsBySensorType) != len(wantCounts) {
		t.Errorf("counts_by_sensor_type = %v, want %v", out.CountsBySensorType, wantCounts)
	}
	for k, v := range wantCounts {
		if out.CountsBySensorType[k] != v {
			t.Errorf("counts[%q] = %d, want %d", k, out.CountsBySensorType[k], v)
		}
	}

	// Records 1, 2 (Critical) and 3 (Non-recoverable) must be flagged; 20% SEL
	// usage must not be.
	if len(out.WarningReasons) != 3 {
		t.Errorf("warning_reasons = %v, want 3", out.WarningReasons)
	}
}

func TestExecute_LastNTruncationAndCap(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&sb, "%4d | 05/01/2026 | 13:%02d:%02d | Temperature #0x30 | Upper Non-critical going high | Asserted\n", i, (i/60)%60, i%60)
	}
	bigElist := sb.String()

	tests := []struct {
		name        string
		args        string
		wantRecords int
		wantFirstID string
	}{
		{name: "default 25 when omitted", args: ``, wantRecords: 25, wantFirstID: "226"},
		{name: "default 25 when zero", args: `{"last_n": 0}`, wantRecords: 25, wantFirstID: "226"},
		{name: "explicit last_n", args: `{"last_n": 5}`, wantRecords: 5, wantFirstID: "246"},
		{name: "capped at 200", args: `{"last_n": 500}`, wantRecords: 200, wantFirstID: "51"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubEnv(t, stubOpts{
				lookPaths: map[string]bool{"ipmitool": true},
				euid:      0,
				devices:   []string{"ipmi0"},
				cmd:       selCmdMock(bigElist, nil, selInfoFixture, nil),
			})

			var args json.RawMessage
			if tc.args != "" {
				args = json.RawMessage(tc.args)
			}
			res, err := New().Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != registry.StatusOK {
				t.Fatalf("Status = %q, want ok (Non-critical must not trip the Critical heuristic)", res.Status)
			}

			var out Output
			if err := json.Unmarshal(res.Data, &out); err != nil {
				t.Fatal(err)
			}
			if len(out.Records) != tc.wantRecords {
				t.Fatalf("got %d records, want %d", len(out.Records), tc.wantRecords)
			}
			if out.Records[0].ID != tc.wantFirstID {
				t.Errorf("first returned record id = %q, want %q (most recent tail)", out.Records[0].ID, tc.wantFirstID)
			}
			if out.Records[len(out.Records)-1].ID != "250" {
				t.Errorf("last record id = %q, want 250", out.Records[len(out.Records)-1].ID)
			}
			// Sensor-type counts must cover the full SEL, not just the returned window.
			if out.CountsBySensorType["Temperature"] != 250 {
				t.Errorf("counts[Temperature] = %d, want 250", out.CountsBySensorType["Temperature"])
			}
		})
	}
}

func TestExecute_SudoWrapping(t *testing.T) {
	tests := []struct {
		name     string
		euid     int
		sudo     bool
		wantName string
		wantArgs []string
	}{
		{name: "non-root with sudo wraps sudo -n", euid: 1000, sudo: true, wantName: "sudo", wantArgs: []string{"-n", "ipmitool", "sel", "elist"}},
		{name: "non-root without sudo runs plain", euid: 1000, sudo: false, wantName: "ipmitool", wantArgs: []string{"sel", "elist"}},
		{name: "root runs plain", euid: 0, sudo: true, wantName: "ipmitool", wantArgs: []string{"sel", "elist"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotName string
			var gotArgs []string
			stubEnv(t, stubOpts{
				lookPaths: map[string]bool{"ipmitool": true, "sudo": tc.sudo},
				euid:      tc.euid,
				devices:   []string{"ipmi0"},
				cmd: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					if gotName == "" {
						gotName = name
						gotArgs = args
					}
					if strings.Contains(strings.Join(args, " "), "info") {
						return []byte(selInfoFixture), nil
					}
					return []byte(selElistFixture), nil
				},
			})

			if _, err := New().Execute(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if gotName != tc.wantName {
				t.Errorf("invoked %q, want %q", gotName, tc.wantName)
			}
			if len(gotArgs) != len(tc.wantArgs) {
				t.Fatalf("args = %v, want %v", gotArgs, tc.wantArgs)
			}
			for i := range tc.wantArgs {
				if gotArgs[i] != tc.wantArgs[i] {
					t.Fatalf("args = %v, want %v", gotArgs, tc.wantArgs)
				}
			}
		})
	}
}

func TestExecute_PermissionDenied(t *testing.T) {
	tests := []struct {
		name string
		out  string
		err  error
	}{
		{name: "sudo password required", out: "sudo: a password is required", err: errors.New("exit status 1")},
		{name: "device permission denied", out: "Could not open device at /dev/ipmi0: Permission denied", err: errors.New("exit status 1")},
		{name: "insufficient privilege", out: "Insufficient privilege level", err: errors.New("exit status 1")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubEnv(t, stubOpts{
				lookPaths: map[string]bool{"ipmitool": true, "sudo": true},
				euid:      1000,
				devices:   []string{"ipmi0"},
				cmd:       selCmdMock(tc.out, tc.err, "", errors.New("should not reach info")),
			})

			res, err := New().Execute(context.Background(), nil)
			if err != nil {
				t.Fatalf("Execute() error = %v, want nil (encapsulated)", err)
			}
			if res.Status != registry.StatusError {
				t.Fatalf("Status = %q, want error", res.Status)
			}
			want := "Unauthorized: Root or passwordless sudo privileges required for BMC access."
			if res.Summary != want {
				t.Errorf("Summary = %q, want %q", res.Summary, want)
			}
		})
	}
}

func TestExecute_SelInfoFailureDegradesToRecordsOnly(t *testing.T) {
	stubEnv(t, stubOpts{
		lookPaths: map[string]bool{"ipmitool": true},
		euid:      0,
		devices:   []string{"ipmi0"},
		cmd:       selCmdMock(selElistFixture, nil, "Get SEL Info command failed", errors.New("exit status 1")),
	})

	res, err := New().Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning (records still analyzed)", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.SELInfo != nil {
		t.Errorf("sel_info = %+v, want omitted on sel info failure", out.SELInfo)
	}
	if len(out.Records) != 6 {
		t.Errorf("got %d records, want 6", len(out.Records))
	}

	var generic map[string]any
	if err := json.Unmarshal(res.Data, &generic); err != nil {
		t.Fatal(err)
	}
	if _, present := generic["sel_info"]; present {
		t.Error("sel_info key must be absent from JSON when sel info fails")
	}
}

func TestExecute_SELNearlyFull(t *testing.T) {
	fullInfo := "Entries          : 900\nFree Space       : 512 bytes\nPercent Used     : 82%\n"
	benignElist := "   1 | 05/01/2026 | 13:02:11 | Power Supply #0x62 | Presence detected | Asserted\n"

	stubEnv(t, stubOpts{
		lookPaths: map[string]bool{"ipmitool": true},
		euid:      0,
		devices:   []string{"ipmi0"},
		cmd:       selCmdMock(benignElist, nil, fullInfo, nil),
	})

	res, err := New().Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning (82%% > 75%%)", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.WarningReasons) != 1 || !strings.Contains(out.WarningReasons[0], "82") {
		t.Errorf("warning_reasons = %v, want single SEL-capacity warning mentioning 82", out.WarningReasons)
	}
}

func TestExecute_ElistFailure(t *testing.T) {
	stubEnv(t, stubOpts{
		lookPaths: map[string]bool{"ipmitool": true},
		euid:      0,
		devices:   []string{"ipmi0"},
		cmd:       selCmdMock("", errors.New("BMC timeout"), selInfoFixture, nil),
	})

	res, err := New().Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("Status = %q, want error", res.Status)
	}
	if !strings.Contains(res.Summary, "sel elist") {
		t.Errorf("Summary %q should mention the failed command", res.Summary)
	}
}

func TestExecute_EmptySEL(t *testing.T) {
	stubEnv(t, stubOpts{
		lookPaths: map[string]bool{"ipmitool": true},
		euid:      0,
		devices:   []string{"ipmi0"},
		cmd:       selCmdMock("SEL has no entries\n", nil, selInfoFixture, nil),
	})

	res, err := New().Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok; summary: %s", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Records == nil || len(out.Records) != 0 {
		t.Errorf("records = %v, want empty non-nil slice", out.Records)
	}
	if len(out.CountsBySensorType) != 0 {
		t.Errorf("counts = %v, want empty", out.CountsBySensorType)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("warnings = %v, want none", out.WarningReasons)
	}
}

func TestExecute_InvalidArgs(t *testing.T) {
	stubEnv(t, stubOpts{
		lookPaths: map[string]bool{"ipmitool": true},
		euid:      0,
		devices:   []string{"ipmi0"},
	})

	res, err := New().Execute(context.Background(), json.RawMessage(`{"last_n": "abc"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("Status = %q, want error", res.Status)
	}
	if !strings.Contains(res.Summary, "invalid arguments") {
		t.Errorf("Summary = %q, want invalid arguments", res.Summary)
	}
}

func TestExecute_MissingPrerequisites(t *testing.T) {
	t.Run("ipmitool not in PATH", func(t *testing.T) {
		stubEnv(t, stubOpts{lookPaths: map[string]bool{}, devices: []string{"ipmi0"}})
		res, err := New().Execute(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != registry.StatusError || !strings.Contains(res.Summary, "ipmitool") {
			t.Errorf("res = %q %q, want error mentioning ipmitool", res.Status, res.Summary)
		}
	})

	t.Run("BMC device missing", func(t *testing.T) {
		stubEnv(t, stubOpts{lookPaths: map[string]bool{"ipmitool": true}})
		res, err := New().Execute(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != registry.StatusError || !strings.Contains(res.Summary, "BMC") {
			t.Errorf("res = %q %q, want error mentioning BMC device", res.Status, res.Summary)
		}
	})
}

func TestExecute_ContextCancelled(t *testing.T) {
	invoked := false
	stubEnv(t, stubOpts{
		lookPaths: map[string]bool{"ipmitool": true},
		euid:      0,
		devices:   []string{"ipmi0"},
		cmd: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			invoked = true
			return []byte(selElistFixture), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := New().Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error for cancelled context", res.Status)
	}
	if invoked {
		t.Error("execCommand must not be invoked after context cancellation")
	}
}

func TestParseSELRecord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     string
		wantOK bool
		want   Record
	}{
		{
			name:   "standard six column row",
			in:     "   1 | 05/01/2026 | 13:02:11 | Temperature #0x30 | Upper Critical going high | Asserted",
			wantOK: true,
			want:   Record{ID: "1", Timestamp: "2026-05-01T13:02:11Z", Sensor: "Temperature #0x30", Event: "Upper Critical going high", Direction: "Asserted"},
		},
		{
			name:   "five columns without direction",
			in:     "   2 | 05/01/2026 | 13:03:00 | Session Audit #0xff | Session activated",
			wantOK: true,
			want:   Record{ID: "2", Timestamp: "2026-05-01T13:03:00Z", Sensor: "Session Audit #0xff", Event: "Session activated"},
		},
		{
			name:   "seven columns keeps direction ignores extra",
			in:     "   3 | 05/01/2026 | 13:04:00 | Temperature #0x30 | Upper Critical going high | Asserted | Reading 95 > Threshold 85 degrees C",
			wantOK: true,
			want:   Record{ID: "3", Timestamp: "2026-05-01T13:04:00Z", Sensor: "Temperature #0x30", Event: "Upper Critical going high", Direction: "Asserted"},
		},
		{
			name:   "pre-init timestamp preserved raw",
			in:     "   4 |  Pre-Init  |Pre-Init   | Memory #0x53 | Correctable ECC | Asserted",
			wantOK: true,
			want:   Record{ID: "4", Timestamp: "Pre-Init", Sensor: "Memory #0x53", Event: "Correctable ECC", Direction: "Asserted"},
		},
		{name: "informational line skipped", in: "SEL has no entries", wantOK: false},
		{name: "empty line skipped", in: "   ", wantOK: false},
		{name: "missing id skipped", in: " | 05/01/2026 | 13:02:11 | A #1 | B | Asserted", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseSELRecord(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.wantOK, got)
			}
			if ok && got != tc.want {
				t.Errorf("record = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseSELList(t *testing.T) {
	t.Parallel()

	records := parseSELList([]byte(selElistFixture))
	if len(records) != 6 {
		t.Fatalf("parsed %d records, want 6", len(records))
	}
	if records[5].ID != "6" || records[5].Sensor != "Event Logging Disabled #0x07" {
		t.Errorf("record 5 = %+v", records[5])
	}

	if got := parseSELList(nil); len(got) != 0 {
		t.Errorf("empty input parsed %d records", len(got))
	}
}

func TestParseSELInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		in          string
		wantNil     bool
		wantEntries *int64
		wantFree    *int64
		wantPct     *int64
	}{
		{
			name:        "standard output",
			in:          selInfoFixture,
			wantEntries: ptrInt64(132),
			wantFree:    ptrInt64(8144),
			wantPct:     ptrInt64(20),
		},
		{
			name:        "tight spacing variant",
			in:          "Entries:132\nFree Space:8144 bytes\nPercent Used:20%\n",
			wantEntries: ptrInt64(132),
			wantFree:    ptrInt64(8144),
			wantPct:     ptrInt64(20),
		},
		{
			name:        "unspecified values degrade per field",
			in:          "Entries          : unspecified\nFree Space       : 8144 bytes\nPercent Used     : unspecified\n",
			wantEntries: nil,
			wantFree:    ptrInt64(8144),
			wantPct:     nil,
		},
		{name: "no recognizable fields", in: "IPMI over LAN disabled\n", wantNil: true},
		{name: "empty input", in: "", wantNil: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseSELInfo([]byte(tc.in))
			if tc.wantNil {
				if got != nil {
					t.Fatalf("parseSELInfo = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("parseSELInfo = nil, want value")
			}
			checkInt64Ptr(t, "entries", got.Entries, tc.wantEntries)
			checkInt64Ptr(t, "free_space_bytes", got.FreeSpaceBytes, tc.wantFree)
			checkInt64Ptr(t, "percent_used", got.PercentUsed, tc.wantPct)
		})
	}
}

func TestParseTimestamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		date, tm, want string
	}{
		{"05/01/2026", "13:02:11", "2026-05-01T13:02:11Z"},
		{"12/31/1999", "23:59:59", "1999-12-31T23:59:59Z"},
		{"Pre-Init", "Pre-Init", "Pre-Init"},
		{"Pre-Init", "10:00:00", "Pre-Init 10:00:00"},
		{"garbage", "worse", "garbage worse"},
	}
	for _, tc := range tests {
		if got := parseTimestamp(tc.date, tc.tm); got != tc.want {
			t.Errorf("parseTimestamp(%q, %q) = %q, want %q", tc.date, tc.tm, got, tc.want)
		}
	}
}

func TestSensorType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"Temperature #0x30", "Temperature"},
		{"Event Logging Disabled #0x07", "Event Logging Disabled"},
		{"Power Supply #0x62", "Power Supply"},
		{"NoHashSensor", "NoHashSensor"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := sensorType(tc.in); got != tc.want {
			t.Errorf("sensorType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFirstInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want *int64
	}{
		{"8144 bytes", ptrInt64(8144)},
		{"20%", ptrInt64(20)},
		{"132", ptrInt64(132)},
		{"unspecified", nil},
		{"", nil},
	}
	for _, tc := range tests {
		got := firstInt(tc.in)
		checkInt64Ptr(t, fmt.Sprintf("firstInt(%q)", tc.in), got, tc.want)
	}
}

func TestIsPermissionError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{name: "nil error is never permission", out: "Permission denied", err: nil, want: false},
		{name: "sudo password prompt", out: "sudo: a password is required", err: errors.New("exit status 1"), want: true},
		{name: "device permission denied", out: "Could not open device at /dev/ipmi0: Permission denied", err: errors.New("exit status 1"), want: true},
		{name: "operation not permitted", out: "", err: errors.New("fork/exec: operation not permitted"), want: true},
		{name: "insufficient privilege", out: "Insufficient privilege level", err: errors.New("exit status 1"), want: true},
		{name: "unrelated failure", out: "BMC timeout", err: errors.New("exit status 1"), want: false},
	}
	for _, tc := range tests {
		if got := isPermissionError([]byte(tc.out), tc.err); got != tc.want {
			t.Errorf("%s: isPermissionError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAnalyzeRecords(t *testing.T) {
	t.Parallel()

	t.Run("counts and critical detection", func(t *testing.T) {
		t.Parallel()
		records := parseSELList([]byte(selElistFixture))
		counts, warnings := analyzeRecords(records)
		if counts["Temperature"] != 2 || counts["Fan"] != 1 {
			t.Errorf("counts = %v", counts)
		}
		if len(warnings) != 3 {
			t.Fatalf("warnings = %v, want 3", warnings)
		}
		if !strings.Contains(warnings[0], "Critical") {
			t.Errorf("warning[0] = %q, want the Critical event echoed", warnings[0])
		}
	})

	t.Run("non-critical does not trip heuristic", func(t *testing.T) {
		t.Parallel()
		records := []Record{{ID: "1", Sensor: "Temperature #0x30", Event: "Upper Non-critical going high"}}
		_, warnings := analyzeRecords(records)
		if len(warnings) != 0 {
			t.Errorf("warnings = %v, want none for Non-critical", warnings)
		}
	})

	t.Run("critical flood is capped", func(t *testing.T) {
		t.Parallel()
		var records []Record
		for i := 0; i < 12; i++ {
			records = append(records, Record{ID: fmt.Sprint(i + 1), Sensor: "Temperature #0x30", Event: "Upper Critical going high"})
		}
		_, warnings := analyzeRecords(records)
		if len(warnings) != 11 {
			t.Fatalf("got %d warnings, want 10 itemized + 1 aggregate", len(warnings))
		}
		if !strings.Contains(warnings[10], "2") {
			t.Errorf("aggregate warning = %q, want remaining count 2", warnings[10])
		}
	})

	t.Run("empty records", func(t *testing.T) {
		t.Parallel()
		counts, warnings := analyzeRecords(nil)
		if len(counts) != 0 || len(warnings) != 0 {
			t.Errorf("counts=%v warnings=%v, want empty", counts, warnings)
		}
		if counts == nil {
			t.Error("counts must be non-nil")
		}
	})
}

func TestBmcDevicePath(t *testing.T) {
	stubEnv(t, stubOpts{devices: []string{"ipmidev/0"}})

	path, ok := bmcDevicePath()
	if !ok {
		t.Fatal("bmcDevicePath() = not found, want found")
	}
	if !strings.HasSuffix(path, filepath.Join("ipmidev", "0")) {
		t.Errorf("path = %q, want ipmidev/0 suffix", path)
	}

	stubEnv(t, stubOpts{})
	if _, ok := bmcDevicePath(); ok {
		t.Error("bmcDevicePath() = found, want not found for empty devRoot")
	}
}

func TestBuildIpmitoolCommand(t *testing.T) {
	stubEnv(t, stubOpts{lookPaths: map[string]bool{"sudo": true}, euid: 1000})
	name, args := buildIpmitoolCommand("sel", "info")
	if name != "sudo" || len(args) != 4 || args[0] != "-n" || args[1] != "ipmitool" || args[2] != "sel" || args[3] != "info" {
		t.Errorf("buildIpmitoolCommand = %q %v, want sudo [-n ipmitool sel info]", name, args)
	}

	stubEnv(t, stubOpts{lookPaths: map[string]bool{"sudo": true}, euid: 0})
	name, args = buildIpmitoolCommand("sel", "info")
	if name != "ipmitool" || len(args) != 2 || args[0] != "sel" || args[1] != "info" {
		t.Errorf("buildIpmitoolCommand = %q %v, want ipmitool [sel info]", name, args)
	}
}

func checkInt64Ptr(t *testing.T, field string, got, want *int64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %d, want nil", field, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %d", field, *want)
	case want != nil && got != nil && *got != *want:
		t.Errorf("%s = %d, want %d", field, *got, *want)
	}
}

func ptrInt64(v int64) *int64 { return &v }
