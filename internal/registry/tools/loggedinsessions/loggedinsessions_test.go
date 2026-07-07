package loggedinsessions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

// putUtmpRecord builds one raw 384-byte little-endian x86_64 glibc utmp
// record. The layout is constructed manually (by byte offset, not via the
// production struct) so the tests independently pin the wire format:
//
//	off   0: int32  ut_type      (short + 2 bytes alignment padding)
//	off   4: int32  ut_pid
//	off   8: [32]byte ut_line
//	off  40: [4]byte  ut_id
//	off  44: [32]byte ut_user
//	off  76: [256]byte ut_host
//	off 332: 2x int16 ut_exit
//	off 336: int32  ut_session
//	off 340: int32  tv_sec
//	off 344: int32  tv_usec
//	off 348: [4]int32 ut_addr_v6
//	off 364: [20]byte reserved
//	total: 384
func putUtmpRecord(typ, pid int32, line, user, host string, sec, usec int32) []byte {
	buf := make([]byte, 384)
	binary.LittleEndian.PutUint32(buf[0:], uint32(typ))
	binary.LittleEndian.PutUint32(buf[4:], uint32(pid))
	copy(buf[8:40], line)
	copy(buf[40:44], "ts/0")
	copy(buf[44:76], user)
	copy(buf[76:332], host)
	binary.LittleEndian.PutUint32(buf[340:], uint32(sec))
	binary.LittleEndian.PutUint32(buf[344:], uint32(usec))
	return buf
}

// writeUtmpFile writes the given records into <dir>/utmp and returns dir as
// the injectable runRoot.
func writeUtmpFile(t testing.TB, records ...[]byte) string {
	t.Helper()
	dir := t.TempDir()
	var data []byte
	for _, r := range records {
		data = append(data, r...)
	}
	if err := os.WriteFile(filepath.Join(dir, "utmp"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func lookPathMissing(string) (string, error) { return "", errors.New("not found") }

func lookPathFound(file string) (string, error) { return "/mock/bin/" + file, nil }

// ---------------------------------------------------------------------------
// Contract compliance
// ---------------------------------------------------------------------------

func TestLoggedInSessionsTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_logged_in_sessions" {
		t.Errorf("expected name get_logged_in_sessions, got %q", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("expected category system, got %q", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected non-empty description")
	}
	if tool.Hidden() {
		t.Error("expected Hidden() == false")
	}
	if len(tool.Parameters()) != 0 {
		t.Errorf("expected no parameters, got %d", len(tool.Parameters()))
	}

	help := tool.Help()
	if help == "" {
		t.Fatal("expected non-empty help")
	}
	for _, src := range []string{"utmp", "loginctl"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

// ---------------------------------------------------------------------------
// IsSupported
// ---------------------------------------------------------------------------

func TestLoggedInSessionsTool_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("utmp present", func(t *testing.T) {
		t.Parallel()
		dir := writeUtmpFile(t, putUtmpRecord(7, 100, "tty1", "root", "", 1, 0))
		tool := &Tool{runRoot: dir, execLookPath: lookPathMissing}
		ok, _ := tool.IsSupported()
		if !ok {
			t.Error("expected supported when <runRoot>/utmp exists")
		}
	})

	t.Run("loginctl only", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{runRoot: t.TempDir(), execLookPath: lookPathFound}
		ok, _ := tool.IsSupported()
		if !ok {
			t.Error("expected supported when loginctl is in PATH")
		}
	})

	t.Run("neither source", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{runRoot: t.TempDir(), execLookPath: lookPathMissing}
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("expected unsupported when no utmp and no loginctl")
		}
		if reason == "" {
			t.Error("expected a reason when unsupported")
		}
	})
}

// ---------------------------------------------------------------------------
// Execute: native utmp path
// ---------------------------------------------------------------------------

func TestLoggedInSessionsTool_Execute_Utmp(t *testing.T) {
	t.Parallel()

	loginSec := int32(1751888000)
	dir := writeUtmpFile(t,
		putUtmpRecord(2, 0, "~", "reboot", "6.1.0", 1751880000, 0), // BOOT_TIME: filtered out
		putUtmpRecord(7, 4242, "tty2", "alice", "", loginSec, 0),   // local session
		putUtmpRecord(7, 5353, "pts/0", "bob", "10.0.0.9", loginSec+60, 0), // remote SSH session
		putUtmpRecord(8, 999, "pts/9", "gone", "", loginSec, 0),    // DEAD_PROCESS: filtered out
	)

	tool := &Tool{
		runRoot:      dir,
		execLookPath: lookPathMissing,
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Error("execCommand must not be called when utmp is readable")
			return nil, errors.New("unexpected")
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected status ok, got %q (%s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("failed to unmarshal data: %v", err)
	}

	if out.Source != "utmp" {
		t.Errorf("expected source utmp, got %q", out.Source)
	}
	if out.Count != 2 || len(out.Sessions) != 2 {
		t.Fatalf("expected 2 USER_PROCESS sessions, got count=%d len=%d", out.Count, len(out.Sessions))
	}

	local := out.Sessions[0]
	if local.User != "alice" || local.TTY != "tty2" || local.PID != 4242 {
		t.Errorf("unexpected local session: %+v", local)
	}
	if local.RemoteHost != "" {
		t.Errorf("expected empty remote_host for local session, got %q", local.RemoteHost)
	}
	wantTime := time.Unix(int64(loginSec), 0).UTC().Format(time.RFC3339)
	if local.LoginTime != wantTime {
		t.Errorf("expected login_time %q, got %q", wantTime, local.LoginTime)
	}

	remote := out.Sessions[1]
	if remote.User != "bob" || remote.TTY != "pts/0" || remote.RemoteHost != "10.0.0.9" || remote.PID != 5353 {
		t.Errorf("unexpected remote session: %+v", remote)
	}
}

func TestLoggedInSessionsTool_Execute_UtmpZeroSessionsValid(t *testing.T) {
	t.Parallel()
	// Only non-USER_PROCESS records: zero sessions is a valid, OK result.
	dir := writeUtmpFile(t,
		putUtmpRecord(1, 0, "~", "runlevel", "", 1751880000, 0),
		putUtmpRecord(2, 0, "~", "reboot", "", 1751880000, 0),
	)
	tool := &Tool{runRoot: dir, execLookPath: lookPathMissing}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected ok for zero sessions, got %q", res.Status)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 0 || len(out.Sessions) != 0 {
		t.Errorf("expected zero sessions, got %+v", out)
	}
}

func TestLoggedInSessionsTool_Execute_MalformedTrailingRecordSkipped(t *testing.T) {
	t.Parallel()
	full := putUtmpRecord(7, 77, "tty7", "carol", "", 1751888000, 0)
	dir := writeUtmpFile(t, full, full[:100]) // trailing partial record
	tool := &Tool{runRoot: dir, execLookPath: lookPathMissing}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 {
		t.Errorf("expected 1 session (partial record skipped), got %d", out.Count)
	}
}

// ---------------------------------------------------------------------------
// Execute: loginctl fallback path
// ---------------------------------------------------------------------------

func TestLoggedInSessionsTool_Execute_LoginctlFallback(t *testing.T) {
	t.Parallel()
	tool := &Tool{
		runRoot:      t.TempDir(), // no utmp file
		execLookPath: lookPathFound,
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "loginctl" {
				return nil, fmt.Errorf("unexpected binary %q", name)
			}
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "list-sessions") || !strings.Contains(joined, "--output=json") {
				return nil, fmt.Errorf("unexpected args %q", joined)
			}
			return []byte(`[{"session":"3","uid":1000,"user":"justin","seat":"seat0","tty":"tty2"},{"session":"c7","uid":42,"user":"gdm","seat":"","tty":"tty1"}]`), nil
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Source != "loginctl" {
		t.Errorf("expected source loginctl, got %q", out.Source)
	}
	if out.Count != 2 {
		t.Fatalf("expected 2 sessions, got %d", out.Count)
	}
	if out.Sessions[0].User != "justin" || out.Sessions[0].TTY != "tty2" {
		t.Errorf("unexpected session[0]: %+v", out.Sessions[0])
	}
	if out.Sessions[0].RemoteHost != "" {
		t.Errorf("loginctl sessions carry no remote host, got %q", out.Sessions[0].RemoteHost)
	}
}

func TestLoggedInSessionsTool_Execute_LoginctlErrors(t *testing.T) {
	t.Parallel()

	t.Run("command failure", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{
			runRoot:      t.TempDir(),
			execLookPath: lookPathFound,
			execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return nil, errors.New("dbus unavailable")
			},
		}
		res, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatalf("errors must be encapsulated, got hard error: %v", err)
		}
		if res.Status != registry.StatusError {
			t.Errorf("expected error status, got %q", res.Status)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{
			runRoot:      t.TempDir(),
			execLookPath: lookPathFound,
			execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("this is not json"), nil
			},
		}
		res, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatalf("errors must be encapsulated, got hard error: %v", err)
		}
		if res.Status != registry.StatusError {
			t.Errorf("expected error status, got %q", res.Status)
		}
	})

	t.Run("no source at all", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{runRoot: t.TempDir(), execLookPath: lookPathMissing}
		res, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatalf("errors must be encapsulated, got hard error: %v", err)
		}
		if res.Status != registry.StatusError {
			t.Errorf("expected error status, got %q", res.Status)
		}
	})
}

// ---------------------------------------------------------------------------
// Context cancellation
// ---------------------------------------------------------------------------

func TestLoggedInSessionsTool_Execute_ContextCancelled(t *testing.T) {
	t.Parallel()
	dir := writeUtmpFile(t, putUtmpRecord(7, 1, "tty1", "root", "", 1751888000, 0))
	tool := &Tool{runRoot: dir, execLookPath: lookPathMissing}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("cancellation must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("expected error status for cancelled context, got %q", res.Status)
	}
}

// ---------------------------------------------------------------------------
// Parser tables
// ---------------------------------------------------------------------------

func TestParseUtmp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      []byte
		wantUsers []string
	}{
		{
			name:      "empty file",
			data:      nil,
			wantUsers: []string{},
		},
		{
			name:      "single user process",
			data:      putUtmpRecord(7, 10, "tty1", "root", "", 100, 0),
			wantUsers: []string{"root"},
		},
		{
			name: "mixed record types",
			data: append(
				putUtmpRecord(2, 0, "~", "reboot", "", 100, 0),
				putUtmpRecord(7, 10, "tty1", "root", "", 100, 0)...),
			wantUsers: []string{"root"},
		},
		{
			name:      "user process with empty user skipped",
			data:      putUtmpRecord(7, 10, "tty1", "", "", 100, 0),
			wantUsers: []string{},
		},
		{
			name:      "truncated record skipped",
			data:      putUtmpRecord(7, 10, "tty1", "root", "", 100, 0)[:200],
			wantUsers: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sessions := parseUtmp(tc.data)
			if len(sessions) != len(tc.wantUsers) {
				t.Fatalf("expected %d sessions, got %d (%+v)", len(tc.wantUsers), len(sessions), sessions)
			}
			for i, u := range tc.wantUsers {
				if sessions[i].User != u {
					t.Errorf("session %d: expected user %q, got %q", i, u, sessions[i].User)
				}
			}
		})
	}
}

func TestParseLoginctlJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{"empty array", `[]`, 0, false},
		{"two sessions", `[{"session":"1","uid":1000,"user":"a","seat":"seat0","tty":"tty2"},{"session":"2","uid":1001,"user":"b","seat":"","tty":"pts/1"}]`, 2, false},
		{"invalid json", `{nope`, 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sessions, err := parseLoginctlJSON([]byte(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(sessions) != tc.want {
				t.Errorf("expected %d sessions, got %d", tc.want, len(sessions))
			}
		})
	}
}

func TestCString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   []byte
		want string
	}{
		{[]byte{'a', 'b', 0, 0}, "ab"},
		{[]byte{0, 0, 0}, ""},
		{[]byte{'x'}, "x"},
		{[]byte{}, ""},
		{[]byte{'a', 0, 'b'}, "a"}, // stops at first NUL
	}
	for _, tc := range tests {
		if got := cString(tc.in[:]); got != tc.want {
			t.Errorf("cString(%v): expected %q, got %q", tc.in, tc.want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Benchmarks (benchcov: one per non-test function)
// ---------------------------------------------------------------------------

func benchTool(b *testing.B) *Tool {
	b.Helper()
	dir := writeUtmpFile(b,
		putUtmpRecord(7, 4242, "tty2", "alice", "", 1751888000, 0),
		putUtmpRecord(7, 5353, "pts/0", "bob", "10.0.0.9", 1751888060, 0),
	)
	return &Tool{runRoot: dir, execLookPath: lookPathMissing}
}

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := benchTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := benchTool(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkParseUtmp(b *testing.B) {
	data := append(
		putUtmpRecord(7, 4242, "tty2", "alice", "", 1751888000, 0),
		putUtmpRecord(7, 5353, "pts/0", "bob", "10.0.0.9", 1751888060, 0)...)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseUtmp(data)
	}
}

func BenchmarkParseLoginctlJSON(b *testing.B) {
	data := []byte(`[{"session":"3","uid":1000,"user":"justin","seat":"seat0","tty":"tty2"}]`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseLoginctlJSON(data)
	}
}

func BenchmarkCString(b *testing.B) {
	in := []byte{'a', 'l', 'i', 'c', 'e', 0, 0, 0}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cString(in)
	}
}
