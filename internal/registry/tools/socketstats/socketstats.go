package socketstats

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

type SocketEntry struct {
	Protocol   string `json:"protocol"`
	LocalIP    string `json:"local_ip"`
	LocalPort  int    `json:"local_port"`
	RemoteIP   string `json:"remote_ip"`
	RemotePort int    `json:"remote_port"`
	State      string `json:"state"`
	RecvQueue  int    `json:"recv_queue"`
	SendQueue  int    `json:"send_queue"`
	Inode      int    `json:"inode,omitempty"`
	Uid        int    `json:"uid,omitempty"`
}

type SocketStatsData struct {
	Sockets []SocketEntry `json:"sockets"`
}

type SocketStatsTool struct {
	procnetRoot string
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *SocketStatsTool {
	return &SocketStatsTool{
		procnetRoot: "/proc/net",
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func init() {
	registry.Register(registry.WithCache(5*time.Second, New()))
}

func (t *SocketStatsTool) Name() string {
	return "get_socket_stats"
}

func (t *SocketStatsTool) Category() registry.Category {
	return registry.CategoryNetwork
}

func (t *SocketStatsTool) Help() string {
	return `Collects system network socket statistics for active and listening ports.

Exposes local and remote IPs, ports, protocols (TCP/UDP), socket queue depths, owner UIDs, and socket inodes.

Data Sources:
- /proc/net/tcp & /proc/net/udp (IPv4 sockets)
- /proc/net/tcp6 & /proc/net/udp6 (IPv6 sockets)
- ss (via ss -t -u -a -n -H fallback execution)`
}

func (t *SocketStatsTool) Description() string {
	return "Active connections, listening ports, and socket states"
}

func (t *SocketStatsTool) Parameters() []registry.ToolParam {
	return nil
}

func (t *SocketStatsTool) Hidden() bool { return false }

func (t *SocketStatsTool) IsSupported() (bool, string) {
	if !registry.PathExists(t.procnetRoot) {
		_, err := exec.LookPath("ss")
		if err != nil {
			return false, "Socket statistics not supported (missing /proc/net and ss binary)"
		}
	}
	return true, ""
}

func (t *SocketStatsTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var sockets []SocketEntry
	hasNativeData := false

	// Try reading natively from procfs files
	protocols := []string{"tcp", "udp", "tcp6", "udp6"}
	for _, proto := range protocols {
		path := filepath.Join(t.procnetRoot, proto)
		if registry.PathExists(path) {
			content, err := os.ReadFile(path)
			if err == nil {
				parsed, parseErr := parseProcSockets(content, proto)
				if parseErr == nil {
					sockets = append(sockets, parsed...)
					hasNativeData = true
				}
			}
		}
	}

	// Fallback to ss command if native reads produced no data
	if !hasNativeData || len(sockets) == 0 {
		output, cmdErr := t.execCommand(ctx, "ss", "-t", "-u", "-a", "-n", "-H")
		if cmdErr == nil {
			parsed, parseErr := parseSsFallback(output)
			if parseErr == nil {
				sockets = parsed
			}
		}
	}

	// State counting
	estabCount := 0
	listenCount := 0
	timeWaitCount := 0
	for _, s := range sockets {
		state := strings.ToUpper(s.State)
		if state == "ESTAB" || state == "ESTABLISHED" {
			estabCount++
		} else if state == "LISTEN" || state == "LISTENING" {
			listenCount++
		} else if state == "TIME-WAIT" || state == "TIME_WAIT" {
			timeWaitCount++
		}
	}

	summaryStr := fmt.Sprintf("Socket Statistics: %d active sockets (established: %d, listening: %d, time-wait: %d)",
		len(sockets),
		estabCount,
		listenCount,
		timeWaitCount)

	data := SocketStatsData{Sockets: sockets}
	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func parseIPv4HexIPPort(hexStr string) (string, int, error) {
	parts := strings.Split(hexStr, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid hex IP:port format: %s", hexStr)
	}

	val, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return "", 0, err
	}
	b0 := byte(val & 0xFF)
	b1 := byte((val >> 8) & 0xFF)
	b2 := byte((val >> 16) & 0xFF)
	b3 := byte((val >> 24) & 0xFF)
	ipStr := net.IPv4(b0, b1, b2, b3).String()

	portVal, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil {
		return "", 0, err
	}

	return ipStr, int(portVal), nil
}

func parseIPv6HexIPPort(hexStr string) (string, int, error) {
	parts := strings.Split(hexStr, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid hex IP:port format: %s", hexStr)
	}

	ipHex := parts[0]
	if len(ipHex) != 32 {
		return "", 0, fmt.Errorf("invalid IPv6 hex length: %d", len(ipHex))
	}

	var ip net.IP = make([]byte, 16)
	for i := 0; i < 4; i++ {
		wordStr := ipHex[i*8 : (i+1)*8]
		val, err := strconv.ParseUint(wordStr, 16, 32)
		if err != nil {
			return "", 0, err
		}
		ip[i*4] = byte(val & 0xFF)
		ip[i*4+1] = byte((val >> 8) & 0xFF)
		ip[i*4+2] = byte((val >> 16) & 0xFF)
		ip[i*4+3] = byte((val >> 24) & 0xFF)
	}

	portVal, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil {
		return "", 0, err
	}

	return ip.String(), int(portVal), nil
}

func mapTCPState(st string) string {
	switch st {
	case "01":
		return "ESTABLISHED"
	case "02":
		return "SYN_SENT"
	case "03":
		return "SYN_RECV"
	case "04":
		return "FIN_WAIT1"
	case "05":
		return "FIN_WAIT2"
	case "06":
		return "TIME_WAIT"
	case "07":
		return "CLOSE"
	case "08":
		return "CLOSE_WAIT"
	case "09":
		return "LAST_ACK"
	case "0A":
		return "LISTEN"
	case "0B":
		return "CLOSING"
	default:
		return "UNKNOWN"
	}
}

func mapUDPState(st string) string {
	switch st {
	case "01":
		return "ESTAB"
	case "07":
		return "UNCONN"
	default:
		return "UNKNOWN"
	}
}

func parseProcSockets(data []byte, proto string) ([]SocketEntry, error) {
	var list []SocketEntry
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "sl") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		localHex := fields[1]
		remoteHex := fields[2]
		stateHex := fields[3]
		txRxQueues := fields[4]
		uidVal, _ := strconv.Atoi(fields[7])
		inodeVal, _ := strconv.Atoi(fields[9])

		// Parse queues
		var txQ, rxQ int
		queueParts := strings.Split(txRxQueues, ":")
		if len(queueParts) == 2 {
			txVal, _ := strconv.ParseUint(queueParts[0], 16, 32)
			rxVal, _ := strconv.ParseUint(queueParts[1], 16, 32)
			txQ = int(txVal)
			rxQ = int(rxVal)
		}

		var localIP, remoteIP string
		var localPort, remotePort int
		var errL, errR error

		if strings.HasSuffix(proto, "6") {
			localIP, localPort, errL = parseIPv6HexIPPort(localHex)
			remoteIP, remotePort, errR = parseIPv6HexIPPort(remoteHex)
		} else {
			localIP, localPort, errL = parseIPv4HexIPPort(localHex)
			remoteIP, remotePort, errR = parseIPv4HexIPPort(remoteHex)
		}

		if errL == nil && errR == nil {
			var stateStr string
			if strings.HasPrefix(proto, "tcp") {
				stateStr = mapTCPState(stateHex)
			} else {
				stateStr = mapUDPState(stateHex)
			}

			list = append(list, SocketEntry{
				Protocol:   proto,
				LocalIP:    localIP,
				LocalPort:  localPort,
				RemoteIP:   remoteIP,
				RemotePort: remotePort,
				State:      stateStr,
				RecvQueue:  rxQ,
				SendQueue:  txQ,
				Inode:      inodeVal,
				Uid:        uidVal,
			})
		}
	}
	return list, nil
}

func parseSsFallback(data []byte) ([]SocketEntry, error) {
	var list []SocketEntry
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}

		proto := strings.ToLower(fields[0])
		state := fields[1]
		recvQ, _ := strconv.Atoi(fields[2])
		sendQ, _ := strconv.Atoi(fields[3])
		localAddrPort := fields[4]
		remoteAddrPort := fields[5]

		// Local address and port
		localIP, localPort := parseAddrPortString(localAddrPort)
		remoteIP, remotePort := parseAddrPortString(remoteAddrPort)

		list = append(list, SocketEntry{
			Protocol:   proto,
			LocalIP:    localIP,
			LocalPort:  localPort,
			RemoteIP:   remoteIP,
			RemotePort: remotePort,
			State:      state,
			RecvQueue:  recvQ,
			SendQueue:  sendQ,
		})
	}
	return list, nil
}

func parseAddrPortString(s string) (string, int) {
	idx := strings.LastIndex(s, ":")
	if idx == -1 {
		return s, 0
	}
	ipPart := s[:idx]
	portPart := s[idx+1:]

	// Strip brackets for IPv6 addresses
	if strings.HasPrefix(ipPart, "[") && strings.HasSuffix(ipPart, "]") {
		ipPart = ipPart[1 : len(ipPart)-1]
	}

	// Clean wildcard port representation
	if portPart == "*" {
		return ipPart, 0
	}

	portVal, _ := strconv.Atoi(portPart)
	return ipPart, portVal
}
