package listeningservices

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func mustMkdir(tb testing.TB, dir string) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWrite(tb testing.TB, path, content string) {
	tb.Helper()
	mustMkdir(tb, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		tb.Fatalf("write %s: %v", path, err)
	}
}

func mustSymlink(tb testing.TB, target, link string) {
	tb.Helper()
	mustMkdir(tb, filepath.Dir(link))
	if err := os.Symlink(target, link); err != nil {
		tb.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

const fixtureTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 10001 1 0000000000000000 100 0 0 10 0
   1: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 10002 1 0000000000000000 100 0 0 10 0
   2: 0100007F:A0F2 0100007F:1F90 01 00000000:00000000 00:00000000 00000000  1000        0 10099 1 0000000000000000 20 4 30 10 -1
garbage line that must be ignored
`

const fixtureTCP6 = `  sl  local_address                         rem_address                            st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 10003 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000001000000:1F91 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 10004 1 0000000000000000 100 0 0 10 0
   2: 00000000000000000000000001000000:A123 00000000000000000000000001000000:1F91 01 00000000:00000000 00:00000000 00000000     0        0 10097 1 0000000000000000 20 4 30 10 -1
`

const fixtureUDP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  100: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   101        0 10005 2 0000000000000000 0
  101: 00000000:0000 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 10098 2 0000000000000000 0
`

const fixtureUDP6 = `  sl  local_address                         rem_address                            st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  200: 00000000000000000000000000000000:0223 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 10006 2 0000000000000000 0
`

// writeFixture builds a fake procfs with four socket tables and fd trees:
//   - tcp:  127.0.0.1:8080 LISTEN (inode 10001), 0.0.0.0:22 LISTEN (inode
//     10002), one ESTABLISHED row that must be excluded, one garbage line.
//   - tcp6: [::]:3000 LISTEN (inode 10003), [::1]:8081 LISTEN (inode 10004),
//     one ESTABLISHED row that must be excluded.
//   - udp:  0.0.0.0:53 bound (inode 10005), one port-0 row that must be
//     excluded.
//   - udp6: [::]:547 bound (inode 10006) — deliberately unmapped to any fd.
//   - pid 1234 "nginx": fd 3 → socket:[10001], plus a non-socket fd.
//   - pid 5678 "sshd":  fd 3 → socket:[10002], fd 4 → socket:[10004].
//   - pid 4242 "resolved": fd 5 → socket:[10005], cmdline longer than 80.
//   - pid 9999 (no comm/cmdline files): fd 4 → socket:[10003].
//   - "self" and "notapid": non-numeric /proc entries that must be ignored.
func writeFixture(tb testing.TB) *Tool {
	tb.Helper()
	procRoot := filepath.Join(tb.TempDir(), "proc")

	mustWrite(tb, filepath.Join(procRoot, "net", "tcp"), fixtureTCP)
	mustWrite(tb, filepath.Join(procRoot, "net", "tcp6"), fixtureTCP6)
	mustWrite(tb, filepath.Join(procRoot, "net", "udp"), fixtureUDP)
	mustWrite(tb, filepath.Join(procRoot, "net", "udp6"), fixtureUDP6)

	mustSymlink(tb, "socket:[99999]", filepath.Join(procRoot, "self", "fd", "0"))
	mustMkdir(tb, filepath.Join(procRoot, "notapid", "fd"))

	mustWrite(tb, filepath.Join(procRoot, "1234", "comm"), "nginx\n")
	mustWrite(tb, filepath.Join(procRoot, "1234", "cmdline"), "nginx: master process /usr/sbin/nginx\x00-g\x00daemon on;\x00")
	mustSymlink(tb, "socket:[10001]", filepath.Join(procRoot, "1234", "fd", "3"))
	mustSymlink(tb, "/dev/null", filepath.Join(procRoot, "1234", "fd", "0"))

	mustWrite(tb, filepath.Join(procRoot, "5678", "comm"), "sshd\n")
	mustWrite(tb, filepath.Join(procRoot, "5678", "cmdline"), "/usr/sbin/sshd\x00-D\x00")
	mustSymlink(tb, "socket:[10002]", filepath.Join(procRoot, "5678", "fd", "3"))
	mustSymlink(tb, "socket:[10004]", filepath.Join(procRoot, "5678", "fd", "4"))

	longArg := strings.Repeat("a", 100)
	mustWrite(tb, filepath.Join(procRoot, "4242", "comm"), "resolved\n")
	mustWrite(tb, filepath.Join(procRoot, "4242", "cmdline"), "/usr/bin/resolved\x00--flag="+longArg+"\x00")
	mustSymlink(tb, "socket:[10005]", filepath.Join(procRoot, "4242", "fd", "5"))

	mustSymlink(tb, "socket:[10003]", filepath.Join(procRoot, "9999", "fd", "4"))

	return &Tool{procfsRoot: procRoot}
}

func executeJSON(tb testing.TB, tool *Tool, args string) (*registry.ToolResult, Data) {
	tb.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		tb.Fatalf("Execute returned hard error: %v", err)
	}
	var data Data
	if res.Status != registry.StatusError {
		if err := json.Unmarshal(res.Data, &data); err != nil {
			tb.Fatalf("unmarshal result data: %v", err)
		}
	}
	return res, data
}

func findListener(data Data, proto string, port int) (Listener, bool) {
	for _, l := range data.Listeners {
		if l.Proto == proto && l.Port == port {
			return l, true
		}
	}
	return Listener{}, false
}

func TestContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_listening_services" {
		t.Errorf("Name() = %q, want get_listening_services", tool.Name())
	}
	if tool.Category() != registry.CategoryNetwork {
		t.Errorf("Category() = %q, want network", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description() must be non-empty")
	}
	if tool.Hidden() {
		t.Error("Hidden() must be false")
	}
	help := tool.Help()
	for _, src := range []string{"/proc/net/tcp", "/proc/net/udp", "fd", "inode", "comm", "cmdline"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() missing data source reference %q", src)
		}
	}
	if params := tool.Parameters(); len(params) != 0 {
		t.Errorf("Parameters() len = %d, want 0 (tool takes no parameters)", len(params))
	}
	if tool.procfsRoot != "/proc" {
		t.Errorf("New() procfsRoot = %q, want /proc", tool.procfsRoot)
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	tool := writeFixture(t)
	if ok, reason := tool.IsSupported(); !ok {
		t.Errorf("IsSupported() = false (%s), want true with net/tcp present", reason)
	}

	missing := &Tool{procfsRoot: filepath.Join(t.TempDir(), "nope")}
	ok, reason := missing.IsSupported()
	if ok {
		t.Error("IsSupported() = true without net/tcp, want false")
	}
	if reason == "" {
		t.Error("unsupported verdict must carry a reason")
	}
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s (summary %q), want ok", res.Status, res.Summary)
	}
	if res.Metadata.FilteringMethod != "deterministic" {
		t.Errorf("FilteringMethod = %q, want deterministic", res.Metadata.FilteringMethod)
	}

	if len(data.Listeners) != 6 {
		t.Fatalf("listeners len = %d, want 6 (%+v)", len(data.Listeners), data.Listeners)
	}
	// Must be sorted ascending by port: 22, 53, 547, 3000, 8080, 8081.
	wantPorts := []int{22, 53, 547, 3000, 8080, 8081}
	for i, want := range wantPorts {
		if data.Listeners[i].Port != want {
			t.Errorf("listeners[%d].port = %d, want %d (sort by port)", i, data.Listeners[i].Port, want)
		}
	}

	ssh, ok := findListener(data, "tcp", 22)
	if !ok {
		t.Fatal("missing tcp:22 listener")
	}
	if ssh.Address != "0.0.0.0" || !ssh.Wildcard || ssh.UID != 0 {
		t.Errorf("tcp:22 = %+v, want wildcard 0.0.0.0 uid 0", ssh)
	}
	if ssh.PID != 5678 || ssh.Comm != "sshd" || !strings.Contains(ssh.Cmdline, "/usr/sbin/sshd -D") {
		t.Errorf("tcp:22 process = %+v, want pid 5678 sshd", ssh)
	}

	web, ok := findListener(data, "tcp", 8080)
	if !ok {
		t.Fatal("missing tcp:8080 listener")
	}
	if web.Address != "127.0.0.1" || web.Wildcard {
		t.Errorf("tcp:8080 = %+v, want non-wildcard 127.0.0.1", web)
	}
	if web.PID != 1234 || web.Comm != "nginx" {
		t.Errorf("tcp:8080 process = %+v, want pid 1234 nginx", web)
	}

	v6all, ok := findListener(data, "tcp6", 3000)
	if !ok {
		t.Fatal("missing tcp6:3000 listener")
	}
	if v6all.Address != "::" || !v6all.Wildcard || v6all.UID != 1000 {
		t.Errorf("tcp6:3000 = %+v, want wildcard :: uid 1000", v6all)
	}
	// pid 9999 has no comm/cmdline files: pid still resolved, names omitted.
	if v6all.PID != 9999 {
		t.Errorf("tcp6:3000 pid = %d, want 9999", v6all.PID)
	}
	if v6all.Comm != "" || v6all.Cmdline != "" {
		t.Errorf("tcp6:3000 comm/cmdline = %q/%q, want omitted for unreadable proc files", v6all.Comm, v6all.Cmdline)
	}

	v6lo, ok := findListener(data, "tcp6", 8081)
	if !ok {
		t.Fatal("missing tcp6:8081 listener")
	}
	if v6lo.Address != "::1" || v6lo.Wildcard {
		t.Errorf("tcp6:8081 = %+v, want non-wildcard ::1", v6lo)
	}
	if v6lo.PID != 5678 || v6lo.Comm != "sshd" {
		t.Errorf("tcp6:8081 process = %+v, want pid 5678 sshd", v6lo)
	}

	dns, ok := findListener(data, "udp", 53)
	if !ok {
		t.Fatal("missing udp:53 socket")
	}
	if dns.UID != 101 || dns.PID != 4242 || dns.Comm != "resolved" {
		t.Errorf("udp:53 = %+v, want uid 101 pid 4242 resolved", dns)
	}
	if len(dns.Cmdline) != 80 {
		t.Errorf("udp:53 cmdline len = %d, want truncated to 80", len(dns.Cmdline))
	}

	dhcp, ok := findListener(data, "udp6", 547)
	if !ok {
		t.Fatal("missing udp6:547 socket")
	}
	if dhcp.PID != 0 || dhcp.Comm != "" || dhcp.Cmdline != "" {
		t.Errorf("udp6:547 = %+v, want pid/comm/cmdline omitted (unresolved)", dhcp)
	}

	if len(data.Unresolved) != 1 {
		t.Fatalf("unresolved len = %d, want 1 (%+v)", len(data.Unresolved), data.Unresolved)
	}
	un := data.Unresolved[0]
	if un.Proto != "udp6" || un.Port != 547 || un.Inode != 10006 || un.UID != 0 {
		t.Errorf("unresolved[0] = %+v, want udp6:547 inode 10006 uid 0", un)
	}

	s := data.Summary
	if s.TCPListeners != 4 {
		t.Errorf("summary.tcp_listeners = %d, want 4", s.TCPListeners)
	}
	if s.UDPSockets != 2 {
		t.Errorf("summary.udp_sockets = %d, want 2", s.UDPSockets)
	}
	if s.Resolved != 5 {
		t.Errorf("summary.resolved = %d, want 5", s.Resolved)
	}
	if s.Unresolved != 1 {
		t.Errorf("summary.unresolved = %d, want 1", s.Unresolved)
	}
	if s.ProcessesScanned != 4 {
		t.Errorf("summary.processes_scanned = %d, want 4", s.ProcessesScanned)
	}
	if s.FDDirsSkipped != 0 {
		t.Errorf("summary.fd_dirs_skipped = %d, want 0", s.FDDirsSkipped)
	}
	if data.Truncated {
		t.Error("truncated = true, want false for 6 listeners")
	}
	if data.Note != "" {
		t.Errorf("note = %q, want empty when no fd dirs were skipped", data.Note)
	}
}

func TestExecuteOmitsUnresolvedFieldsInJSON(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	res, _ := executeJSON(t, tool, `{}`)
	// The raw JSON for the unresolved udp6:547 listener must not carry
	// pid/comm/cmdline keys at all (omitempty), not zero values.
	var raw struct {
		Listeners []map[string]interface{} `json:"listeners"`
	}
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	found := false
	for _, l := range raw.Listeners {
		if l["proto"] == "udp6" {
			found = true
			for _, key := range []string{"pid", "comm", "cmdline"} {
				if _, present := l[key]; present {
					t.Errorf("unresolved listener JSON carries key %q, want omitted", key)
				}
			}
		}
	}
	if !found {
		t.Fatal("udp6 listener not found in raw JSON")
	}
}

func TestExecuteMissingIPv6Tables(t *testing.T) {
	t.Parallel()
	// Only /proc/net/tcp and /proc/net/udp exist (no IPv6 support).
	procRoot := filepath.Join(t.TempDir(), "proc")
	mustWrite(t, filepath.Join(procRoot, "net", "tcp"), fixtureTCP)
	mustWrite(t, filepath.Join(procRoot, "net", "udp"), fixtureUDP)
	tool := &Tool{procfsRoot: procRoot}

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok when v6 tables are absent", res.Status)
	}
	if len(data.Listeners) != 3 {
		t.Errorf("listeners len = %d, want 3 (tcp:22, udp:53, tcp:8080)", len(data.Listeners))
	}
	if data.Summary.TCPListeners != 2 || data.Summary.UDPSockets != 1 {
		t.Errorf("summary = %+v, want 2 tcp / 1 udp", data.Summary)
	}
}

func TestExecutePrimaryTableMissing(t *testing.T) {
	t.Parallel()
	// procfs root exists but net/tcp does not: Execute must return an
	// encapsulated error result, never a hard Go error.
	procRoot := filepath.Join(t.TempDir(), "proc")
	mustMkdir(t, filepath.Join(procRoot, "net"))
	tool := &Tool{procfsRoot: procRoot}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute must not return hard error, got %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error when net/tcp is unreadable", res.Status)
	}
}

func TestExecuteNoListeners(t *testing.T) {
	t.Parallel()
	procRoot := filepath.Join(t.TempDir(), "proc")
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	mustWrite(t, filepath.Join(procRoot, "net", "tcp"), header)
	tool := &Tool{procfsRoot: procRoot}

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok for empty tables", res.Status)
	}
	if len(data.Listeners) != 0 || data.Summary.TCPListeners != 0 {
		t.Errorf("expected empty result, got %+v", data)
	}
}

func TestExecutePermissionSkipped(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}

	tool := writeFixture(t)
	lockedFD := filepath.Join(tool.procfsRoot, "777", "fd")
	mustSymlink(t, "socket:[10006]", filepath.Join(lockedFD, "0"))
	if err := os.Chmod(lockedFD, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedFD, 0o755) })

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok despite unreadable fd dir", res.Status)
	}
	if data.Summary.FDDirsSkipped != 1 {
		t.Errorf("fd_dirs_skipped = %d, want 1", data.Summary.FDDirsSkipped)
	}
	// udp6:547 socket belongs to the unreadable pid: must stay unresolved.
	if data.Summary.Unresolved != 1 {
		t.Errorf("unresolved = %d, want 1", data.Summary.Unresolved)
	}
	if data.Note == "" || !strings.Contains(data.Note, "root") {
		t.Errorf("note = %q, want root hint when fd dirs were skipped", data.Note)
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error on cancelled context", res.Status)
	}
}

func TestExecuteCapsListeners(t *testing.T) {
	t.Parallel()
	procRoot := filepath.Join(t.TempDir(), "proc")

	var sb strings.Builder
	sb.WriteString("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n")
	for i := 0; i < 250; i++ {
		sb.WriteString(fmt.Sprintf("   %d: 00000000:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 %d 1 0000000000000000 100 0 0 10 0\n", i, 1000+i, 20000+i))
	}
	mustWrite(t, filepath.Join(procRoot, "net", "tcp"), sb.String())
	tool := &Tool{procfsRoot: procRoot}

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok", res.Status)
	}
	if len(data.Listeners) != 200 {
		t.Errorf("listeners len = %d, want cap 200", len(data.Listeners))
	}
	if !data.Truncated {
		t.Error("truncated = false, want true for 250 listeners")
	}
	// Cap keeps the lowest ports (sorted before truncation).
	if data.Listeners[0].Port != 1000 || data.Listeners[199].Port != 1199 {
		t.Errorf("cap window = [%d..%d], want [1000..1199]", data.Listeners[0].Port, data.Listeners[199].Port)
	}
	// Summary still reflects the full scan.
	if data.Summary.TCPListeners != 250 {
		t.Errorf("summary.tcp_listeners = %d, want 250 despite cap", data.Summary.TCPListeners)
	}
}

func TestParseHexIPv4(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		hex     string
		want    string
		wantErr bool
	}{
		{"loopback little-endian", "0100007F", "127.0.0.1", false},
		{"wildcard", "00000000", "0.0.0.0", false},
		{"192.168.1.10", "0A01A8C0", "192.168.1.10", false},
		{"10.0.0.1", "0100000A", "10.0.0.1", false},
		{"broadcast", "FFFFFFFF", "255.255.255.255", false},
		{"too short", "007F", "", true},
		{"too long", "0100007F00", "", true},
		{"not hex", "ZZZZZZZZ", "", true},
		{"empty", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseHexIPv4(tc.hex)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("parseHexIPv4(%q) = %q, want %q", tc.hex, got, tc.want)
			}
		})
	}
}

func TestParseHexIPv6(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		hex     string
		want    string
		wantErr bool
	}{
		{"unspecified", "00000000000000000000000000000000", "::", false},
		{"loopback segment swap", "00000000000000000000000001000000", "::1", false},
		{"link-local fe80::1", "000080FE000000000000000001000000", "fe80::1", false},
		{"2001:db8::5", "B80D0120000000000000000005000000", "2001:db8::5", false},
		{"too short", "0000000000000000", "", true},
		{"not hex", "GGGG0000000000000000000000000000", "", true},
		{"empty", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseHexIPv6(tc.hex)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("parseHexIPv6(%q) = %q, want %q", tc.hex, got, tc.want)
			}
		})
	}
}

func TestParseHexPort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		hex     string
		want    int
		wantErr bool
	}{
		{"ssh", "0016", 22, false},
		{"http-alt big-endian", "1F90", 8080, false},
		{"zero", "0000", 0, false},
		{"max", "FFFF", 65535, false},
		{"not hex", "ZZZZ", 0, true},
		{"empty", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseHexPort(tc.hex)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("parseHexPort(%q) = %d, want %d", tc.hex, got, tc.want)
			}
		})
	}
}

func TestParseSocketLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		line     string
		proto    string
		wantOK   bool
		wantAddr string
		wantPort int
		wantUID  int
		wantIno  int
	}{
		{
			"tcp listen",
			"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 10001 1 0000000000000000 100 0 0 10 0",
			"tcp", true, "127.0.0.1", 8080, 0, 10001,
		},
		{
			"tcp established excluded",
			"   2: 0100007F:A0F2 0100007F:1F90 01 00000000:00000000 00:00000000 00000000  1000        0 10099 1 0000000000000000 20 4 30 10 -1",
			"tcp", false, "", 0, 0, 0,
		},
		{
			"tcp6 listen wildcard",
			"   0: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 10003 1 0000000000000000 100 0 0 10 0",
			"tcp6", true, "::", 3000, 1000, 10003,
		},
		{
			"udp bound",
			"  100: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   101        0 10005 2 0000000000000000 0",
			"udp", true, "0.0.0.0", 53, 101, 10005,
		},
		{
			"udp port zero excluded",
			"  101: 00000000:0000 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 10098 2 0000000000000000 0",
			"udp", false, "", 0, 0, 0,
		},
		{
			"header line",
			"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
			"tcp", false, "", 0, 0, 0,
		},
		{"garbage", "garbage line", "tcp", false, "", 0, 0, 0},
		{"empty", "", "tcp", false, "", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entry, ok := parseSocketLine(tc.line, tc.proto)
			if ok != tc.wantOK {
				t.Fatalf("ok = %t, want %t", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if entry.address != tc.wantAddr || entry.port != tc.wantPort || entry.uid != tc.wantUID || entry.inode != tc.wantIno {
				t.Errorf("entry = %+v, want addr=%s port=%d uid=%d inode=%d",
					entry, tc.wantAddr, tc.wantPort, tc.wantUID, tc.wantIno)
			}
		})
	}
}

func TestParseSocketFile(t *testing.T) {
	t.Parallel()

	entries := parseSocketFile([]byte(fixtureTCP), "tcp")
	if len(entries) != 2 {
		t.Fatalf("tcp entries = %d, want 2 (LISTEN rows only)", len(entries))
	}
	entries6 := parseSocketFile([]byte(fixtureTCP6), "tcp6")
	if len(entries6) != 2 {
		t.Fatalf("tcp6 entries = %d, want 2 (LISTEN rows only)", len(entries6))
	}
	udp := parseSocketFile([]byte(fixtureUDP), "udp")
	if len(udp) != 1 {
		t.Fatalf("udp entries = %d, want 1 (port-0 row excluded)", len(udp))
	}
	if empty := parseSocketFile(nil, "tcp"); len(empty) != 0 {
		t.Errorf("nil content entries = %d, want 0", len(empty))
	}
}

func TestBuildInodePIDMap(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	inodeToPID, stats, err := tool.buildInodePIDMap(context.Background())
	if err != nil {
		t.Fatalf("buildInodePIDMap: %v", err)
	}
	want := map[int]int{10001: 1234, 10002: 5678, 10004: 5678, 10003: 9999, 10005: 4242}
	for inode, pid := range want {
		if got := inodeToPID[inode]; got != pid {
			t.Errorf("inode %d → pid %d, want %d", inode, got, pid)
		}
	}
	if _, present := inodeToPID[10006]; present {
		t.Error("inode 10006 must be unmapped")
	}
	if stats.processesScanned != 4 {
		t.Errorf("processesScanned = %d, want 4", stats.processesScanned)
	}
	if stats.fdDirsSkipped != 0 {
		t.Errorf("fdDirsSkipped = %d, want 0", stats.fdDirsSkipped)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := tool.buildInodePIDMap(ctx); err == nil {
		t.Error("buildInodePIDMap must fail on cancelled context")
	}
}

func TestReadComm(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	if got := tool.readComm(1234); got != "nginx" {
		t.Errorf("readComm(1234) = %q, want nginx", got)
	}
	if got := tool.readComm(9999); got != "" {
		t.Errorf("readComm(9999) = %q, want empty for missing comm", got)
	}
}

func TestReadCmdline(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	got := tool.readCmdline(5678)
	if got != "/usr/sbin/sshd -D" {
		t.Errorf("readCmdline(5678) = %q, want %q (NULs → spaces, trimmed)", got, "/usr/sbin/sshd -D")
	}
	if long := tool.readCmdline(4242); len(long) != 80 {
		t.Errorf("readCmdline(4242) len = %d, want 80 (truncated)", len(long))
	}
	if got := tool.readCmdline(9999); got != "" {
		t.Errorf("readCmdline(9999) = %q, want empty for missing cmdline", got)
	}
}
