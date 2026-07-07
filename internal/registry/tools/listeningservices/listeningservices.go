// Package listeningservices implements the get_listening_services tool.
//
// It answers "what is listening on this node, and which process owns it?" —
// the question behind port conflicts, unexpected exposure audits, and
// "is the daemon actually up?" checks. It parses /proc/net/tcp[6] (LISTEN
// rows) and /proc/net/udp[6] (bound sockets) natively — no ss/netstat
// binaries — and joins socket inodes to processes via /proc/[pid]/fd
// symlink targets.
package listeningservices

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

const (
	// maxListeners caps the listeners list so LLM payloads stay small.
	// Sorting by port happens before capping, so well-known low ports win.
	maxListeners = 200

	// cmdlineMaxLen truncates process command lines in the output.
	cmdlineMaxLen = 80

	// tcpListenState is the kernel's hex st column value for LISTEN.
	tcpListenState = "0A"

	// rootHintNote is attached whenever fd directories were unreadable.
	rootHintNote = "run as root for complete socket→process mapping"
)

// Listener is one listening TCP socket or bound UDP socket. PID, Comm and
// Cmdline are omitted when the socket inode could not be joined to a process.
type Listener struct {
	Proto    string `json:"proto"` // tcp | tcp6 | udp | udp6
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Wildcard bool   `json:"wildcard"` // bound to 0.0.0.0 or ::
	UID      int    `json:"uid"`
	PID      int    `json:"pid,omitempty"`
	Comm     string `json:"comm,omitempty"`
	Cmdline  string `json:"cmdline,omitempty"`
}

// Unresolved is the compact digest of a socket whose inode had no matching
// /proc/[pid]/fd entry (kernel-owned, or fd dirs unreadable when unprivileged).
type Unresolved struct {
	Proto string `json:"proto"`
	Port  int    `json:"port"`
	Inode int    `json:"inode"`
	UID   int    `json:"uid"`
}

// Summary carries the aggregate counters for the full scan (unaffected by
// the listeners cap).
type Summary struct {
	TCPListeners     int `json:"tcp_listeners"`
	UDPSockets       int `json:"udp_sockets"`
	Resolved         int `json:"resolved"`
	Unresolved       int `json:"unresolved"`
	ProcessesScanned int `json:"processes_scanned"`
	FDDirsSkipped    int `json:"fd_dirs_skipped"`
}

// Data is the tool's structured output payload.
type Data struct {
	Listeners  []Listener   `json:"listeners"`
	Unresolved []Unresolved `json:"unresolved,omitempty"`
	Summary    Summary      `json:"summary"`
	Truncated  bool         `json:"truncated,omitempty"`
	Note       string       `json:"note,omitempty"`
}

// sockEntry is one decoded row from a /proc/net socket table.
type sockEntry struct {
	proto   string
	address string
	port    int
	uid     int
	inode   int
}

// scanStats counts the outcome of the /proc/[pid]/fd walk.
type scanStats struct {
	processesScanned int
	fdDirsSkipped    int
}

// Tool implements registry.Tool for get_listening_services.
type Tool struct {
	procfsRoot string
}

// New returns a Tool wired to the real procfs root.
func New() *Tool {
	return &Tool{procfsRoot: "/proc"}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_listening_services"
}

// Description returns a one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Map listening TCP ports and bound UDP sockets to their owning processes (native ss/netstat)"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Lists every listening TCP socket and bound UDP socket with the process that owns it — the native answer to "what is exposed on this node?" and "which daemon holds this port?".

Data sources: /proc/net/tcp and /proc/net/tcp6 (rows in state 0A = LISTEN)
plus /proc/net/udp and /proc/net/udp6 (bound sockets with a non-zero local
port). Hex addresses are decoded natively: IPv4 is little-endian
(0100007F → 127.0.0.1), IPv6 is four 32-bit groups each byte-swapped, and
ports are big-endian hex. Socket inodes are joined to processes by walking
/proc/[pid]/fd/* symlinks (readlink targets of the form "socket:[inode]"),
then process names come from /proc/[pid]/comm and /proc/[pid]/cmdline
(NUL-separated argv joined with spaces, truncated to 80 chars). Filtering
is deterministic.

Output: listeners sorted by port (capped at 200 with a truncated flag;
lowest ports are kept) with proto/address/port/wildcard/uid and, when the
inode→pid join succeeded, pid/comm/cmdline. Sockets that could not be
joined keep their listener entry (process fields omitted) and are also
digested into unresolved[] with their inode. The summary precomputes
tcp_listeners, udp_sockets, resolved/unresolved counts, processes_scanned
and fd_dirs_skipped.

Caveats: without root, /proc/[pid]/fd of other users' processes is
unreadable — those sockets land in unresolved[] and fd_dirs_skipped is
counted, with a note suggesting to run as root. Missing tcp6/udp6 tables
(IPv6 disabled) degrade gracefully to IPv4-only output.`
}

// Category returns the tool taxonomy classification.
func (t *Tool) Category() registry.Category {
	return registry.CategoryNetwork
}

// Parameters returns the parameter schema (none for this tool).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden returns false; this is a public diagnostic tool.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires the primary IPv4 TCP socket table in procfs.
func (t *Tool) IsSupported() (bool, string) {
	path := filepath.Join(t.procfsRoot, "net", "tcp")
	if !registry.PathExists(path) {
		return false, path + " is missing (Linux procfs required)"
	}
	return true, ""
}

// Execute collects socket tables, joins them to processes, and reports
// listeners sorted by port.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	sockets, err := t.collectSockets()
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	inodeToPID, stats, err := t.buildInodePIDMap(ctx)
	if err != nil {
		return registry.NewErrorResult(t.Name(), "cancelled: "+err.Error()), nil
	}

	data := Data{
		Listeners: make([]Listener, 0, len(sockets)),
		Summary: Summary{
			ProcessesScanned: stats.processesScanned,
			FDDirsSkipped:    stats.fdDirsSkipped,
		},
	}

	for _, s := range sockets {
		if strings.HasPrefix(s.proto, "tcp") {
			data.Summary.TCPListeners++
		} else {
			data.Summary.UDPSockets++
		}

		l := Listener{
			Proto:    s.proto,
			Address:  s.address,
			Port:     s.port,
			Wildcard: s.address == "0.0.0.0" || s.address == "::",
			UID:      s.uid,
		}
		if pid, ok := inodeToPID[s.inode]; ok {
			data.Summary.Resolved++
			l.PID = pid
			l.Comm = t.readComm(pid)
			l.Cmdline = t.readCmdline(pid)
		} else {
			data.Summary.Unresolved++
			data.Unresolved = append(data.Unresolved, Unresolved{
				Proto: s.proto,
				Port:  s.port,
				Inode: s.inode,
				UID:   s.uid,
			})
		}
		data.Listeners = append(data.Listeners, l)
	}

	sort.Slice(data.Listeners, func(i, j int) bool {
		a, b := data.Listeners[i], data.Listeners[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.Proto != b.Proto {
			return a.Proto < b.Proto
		}
		return a.Address < b.Address
	})
	if len(data.Listeners) > maxListeners {
		data.Listeners = data.Listeners[:maxListeners]
		data.Truncated = true
	}
	if data.Summary.FDDirsSkipped > 0 {
		data.Note = rootHintNote
	}

	summary := fmt.Sprintf("%d TCP listener(s), %d bound UDP socket(s): %d resolved to processes, %d unresolved",
		data.Summary.TCPListeners, data.Summary.UDPSockets, data.Summary.Resolved, data.Summary.Unresolved)

	res := registry.NewResult(t.Name(), registry.StatusOK, summary, data)
	res.Metadata.FilteringMethod = "deterministic"
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// collectSockets parses the four /proc/net socket tables. The IPv4 TCP
// table is mandatory (its absence or unreadability is an error); the
// others degrade silently when missing (e.g. IPv6 disabled).
func (t *Tool) collectSockets() ([]sockEntry, error) {
	primary := filepath.Join(t.procfsRoot, "net", "tcp")
	content, err := os.ReadFile(primary)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %v", primary, err)
	}
	sockets := parseSocketFile(content, "tcp")

	for _, proto := range []string{"tcp6", "udp", "udp6"} {
		content, err := os.ReadFile(filepath.Join(t.procfsRoot, "net", proto))
		if err != nil {
			continue // table absent (IPv6 disabled) — degrade to what exists
		}
		sockets = append(sockets, parseSocketFile(content, proto)...)
	}
	return sockets, nil
}

// buildInodePIDMap walks every numeric /proc/[pid]/fd directory once and
// maps socket inodes to pids. Unreadable fd directories (other users'
// processes when unprivileged) are counted, never fatal. The context is
// checked between pid iterations.
func (t *Tool) buildInodePIDMap(ctx context.Context) (map[int]int, scanStats, error) {
	var stats scanStats
	inodeToPID := make(map[int]int)

	entries, err := os.ReadDir(t.procfsRoot)
	if err != nil {
		return nil, stats, fmt.Errorf("failed to read %s: %v", t.procfsRoot, err)
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue // not a process directory
		}

		fdDir := filepath.Join(t.procfsRoot, entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			if os.IsPermission(err) {
				stats.fdDirsSkipped++
			}
			continue // vanished pid or unreadable fd table — never fatal
		}
		stats.processesScanned++

		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue // fd closed mid-scan
			}
			if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				continue
			}
			inode, err := strconv.Atoi(target[len("socket:[") : len(target)-1])
			if err != nil {
				continue
			}
			inodeToPID[inode] = pid
		}
	}
	return inodeToPID, stats, nil
}

// readComm resolves a pid to its process name, degrading to "" (omitted
// from JSON) for processes that died mid-scan or unreadable proc files.
func (t *Tool) readComm(pid int) string {
	raw, err := os.ReadFile(filepath.Join(t.procfsRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// readCmdline resolves a pid to its command line: NUL separators become
// spaces and the result is truncated to cmdlineMaxLen characters. Returns
// "" (omitted from JSON) when unreadable.
func (t *Tool) readCmdline(pid int) string {
	raw, err := os.ReadFile(filepath.Join(t.procfsRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	cmdline := strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " "))
	if len(cmdline) > cmdlineMaxLen {
		cmdline = cmdline[:cmdlineMaxLen]
	}
	return cmdline
}

// parseSocketFile decodes every data row of one /proc/net socket table,
// keeping LISTEN rows for tcp/tcp6 and non-zero-port rows for udp/udp6.
// Malformed rows are skipped, never fatal.
func parseSocketFile(content []byte, proto string) []sockEntry {
	var entries []sockEntry
	for _, line := range strings.Split(string(content), "\n") {
		if entry, ok := parseSocketLine(line, proto); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// parseSocketLine decodes a single socket table row. Columns:
// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode.
// Returns ok=false for headers, malformed rows, non-LISTEN TCP states,
// and port-0 UDP rows.
func parseSocketLine(line, proto string) (sockEntry, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 10 || fields[0] == "sl" {
		return sockEntry{}, false
	}

	state := fields[3]
	if strings.HasPrefix(proto, "tcp") && state != tcpListenState {
		return sockEntry{}, false
	}

	local := strings.Split(fields[1], ":")
	if len(local) != 2 {
		return sockEntry{}, false
	}

	port, err := parseHexPort(local[1])
	if err != nil {
		return sockEntry{}, false
	}
	if strings.HasPrefix(proto, "udp") && port == 0 {
		return sockEntry{}, false // unbound UDP socket
	}

	var address string
	if strings.HasSuffix(proto, "6") {
		address, err = parseHexIPv6(local[0])
	} else {
		address, err = parseHexIPv4(local[0])
	}
	if err != nil {
		return sockEntry{}, false
	}

	uid, err := strconv.Atoi(fields[7])
	if err != nil {
		return sockEntry{}, false
	}
	inode, err := strconv.Atoi(fields[9])
	if err != nil {
		return sockEntry{}, false
	}

	return sockEntry{proto: proto, address: address, port: port, uid: uid, inode: inode}, true
}

// parseHexIPv4 decodes the kernel's 8-hex-digit little-endian IPv4
// representation (e.g. "0100007F" → "127.0.0.1").
func parseHexIPv4(hexStr string) (string, error) {
	if len(hexStr) != 8 {
		return "", fmt.Errorf("invalid IPv4 hex length %d (want 8)", len(hexStr))
	}
	val, err := strconv.ParseUint(hexStr, 16, 32)
	if err != nil {
		return "", err
	}
	return net.IPv4(byte(val), byte(val>>8), byte(val>>16), byte(val>>24)).String(), nil
}

// parseHexIPv6 decodes the kernel's 32-hex-digit IPv6 representation:
// four 32-bit groups, each stored byte-swapped (little-endian words).
func parseHexIPv6(hexStr string) (string, error) {
	if len(hexStr) != 32 {
		return "", fmt.Errorf("invalid IPv6 hex length %d (want 32)", len(hexStr))
	}
	ip := make(net.IP, net.IPv6len)
	for i := 0; i < 4; i++ {
		word, err := strconv.ParseUint(hexStr[i*8:(i+1)*8], 16, 32)
		if err != nil {
			return "", err
		}
		ip[i*4] = byte(word)
		ip[i*4+1] = byte(word >> 8)
		ip[i*4+2] = byte(word >> 16)
		ip[i*4+3] = byte(word >> 24)
	}
	return ip.String(), nil
}

// parseHexPort decodes the kernel's big-endian 16-bit hex port field.
func parseHexPort(hexStr string) (int, error) {
	port, err := strconv.ParseUint(hexStr, 16, 16)
	if err != nil {
		return 0, err
	}
	return int(port), nil
}
