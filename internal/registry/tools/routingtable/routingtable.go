package routingtable

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

type RouteEntry struct {
	Interface   string `json:"interface"`
	Destination string `json:"destination"`
	Gateway     string `json:"gateway,omitempty"`
	Mask        string `json:"mask,omitempty"`
	PrefixLen   int    `json:"prefix_len"`
	Metric      int    `json:"metric"`
	Flags       string `json:"flags,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Family      string `json:"family"`
}

type RoutingTableData struct {
	IPv4Routes []RouteEntry `json:"ipv4_routes"`
	IPv6Routes []RouteEntry `json:"ipv6_routes"`
}

type RoutingTableTool struct {
	procnetRoot string
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *RoutingTableTool {
	return &RoutingTableTool{
		procnetRoot: "/proc/net",
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func init() {
	registry.Register(registry.WithCache(5*time.Second, New()))
}

func (t *RoutingTableTool) Name() string {
	return "get_routing_table"
}

func (t *RoutingTableTool) Category() registry.Category {
	return registry.CategoryNetwork
}

func (t *RoutingTableTool) Help() string {
	return `Collects the system network routing table for IPv4 and IPv6 families.

Exposes destination subnets, CIDR prefix masks, gateway next-hops, metrics, associated interfaces, and operational routing flags.

Data Sources:
- /proc/net/route (IPv4 routes)
- /proc/net/ipv6_route (IPv6 routes)
- ip route (via ip -j route fallback execution)`
}

func (t *RoutingTableTool) Description() string {
	return "Query active IPv4/IPv6 routing table and default gateways"
}

func (t *RoutingTableTool) Parameters() []registry.ToolParam {
	return nil
}

func (t *RoutingTableTool) Hidden() bool { return false }

func (t *RoutingTableTool) IsSupported() (bool, string) {
	if !registry.PathExists(t.procnetRoot) {
		_, err := exec.LookPath("ip")
		if err != nil {
			return false, "Routing table not supported (missing /proc/net and ip binary)"
		}
	}
	return true, ""
}

func (t *RoutingTableTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var data RoutingTableData

	// 1. Try primary IPv4 route file
	ipv4Path := filepath.Join(t.procnetRoot, "route")
	if registry.PathExists(ipv4Path) {
		content, err := os.ReadFile(ipv4Path)
		if err == nil {
			routes, parseErr := parseIPv4Routes(content)
			if parseErr == nil {
				data.IPv4Routes = routes
			}
		}
	}

	// 2. Try primary IPv6 route file
	ipv6Path := filepath.Join(t.procnetRoot, "ipv6_route")
	if registry.PathExists(ipv6Path) {
		content, err := os.ReadFile(ipv6Path)
		if err == nil {
			routes, parseErr := parseIPv6Routes(content)
			if parseErr == nil {
				data.IPv6Routes = routes
			}
		}
	}

	// Fallbacks: If IPv4 or IPv6 routes were not loaded natively, run command fallback
	if len(data.IPv4Routes) == 0 {
		output, cmdErr := t.execCommand(ctx, "ip", "-j", "route")
		if cmdErr == nil {
			routes, parseErr := parseIpRouteJSON(output, "ipv4")
			if parseErr == nil {
				data.IPv4Routes = routes
			}
		}
	}

	if len(data.IPv6Routes) == 0 {
		output, cmdErr := t.execCommand(ctx, "ip", "-6", "-j", "route")
		if cmdErr == nil {
			routes, parseErr := parseIpRouteJSON(output, "ipv6")
			if parseErr == nil {
				data.IPv6Routes = routes
			}
		}
	}

	// Calculate Gateways
	gatewaysCount := 0
	for _, r := range data.IPv4Routes {
		if r.Destination == "0.0.0.0" && r.Gateway != "0.0.0.0" {
			gatewaysCount++
		}
	}
	for _, r := range data.IPv6Routes {
		if r.Destination == "::" && r.Gateway != "::" && r.Gateway != "" {
			gatewaysCount++
		}
	}

	summaryStr := fmt.Sprintf("Routing Table: IPv4 Routes: %d, IPv6 Routes: %d, Default Gateways: %d",
		len(data.IPv4Routes),
		len(data.IPv6Routes),
		gatewaysCount)

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func parseIPv4Hex(hexStr string) (string, error) {
	val, err := strconv.ParseUint(hexStr, 16, 32)
	if err != nil {
		return "", err
	}
	b0 := byte(val & 0xFF)
	b1 := byte((val >> 8) & 0xFF)
	b2 := byte((val >> 16) & 0xFF)
	b3 := byte((val >> 24) & 0xFF)
	return net.IPv4(b0, b1, b2, b3).String(), nil
}

func parseIPv6Hex(hexStr string) (string, error) {
	if len(hexStr) != 32 {
		return "", fmt.Errorf("invalid IPv6 hex length: %d", len(hexStr))
	}
	var ip net.IP = make([]byte, 16)
	for i := 0; i < 16; i++ {
		b, err := strconv.ParseUint(hexStr[i*2:i*2+2], 16, 8)
		if err != nil {
			return "", err
		}
		ip[i] = byte(b)
	}
	return ip.String(), nil
}

func maskToPrefixLen(maskStr string) int {
	ip := net.ParseIP(maskStr)
	if ip == nil {
		return 0
	}
	ipv4 := ip.To4()
	if ipv4 == nil {
		return 0
	}
	ones, _ := net.IPv4Mask(ipv4[0], ipv4[1], ipv4[2], ipv4[3]).Size()
	return ones
}

func parseIPv4Routes(data []byte) ([]RouteEntry, error) {
	var list []RouteEntry
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Iface") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		iface := fields[0]
		destHex := fields[1]
		gwHex := fields[2]
		flagsHex := fields[3]
		metricVal, _ := strconv.Atoi(fields[6])
		maskHex := fields[7]

		dest, errDest := parseIPv4Hex(destHex)
		gw, errGw := parseIPv4Hex(gwHex)
		mask, errMask := parseIPv4Hex(maskHex)

		if errDest == nil && errGw == nil && errMask == nil {
			prefixLen := maskToPrefixLen(mask)
			flags, _ := strconv.ParseUint(flagsHex, 16, 32)
			var flagStrings []string
			if flags&0x0001 != 0 {
				flagStrings = append(flagStrings, "UP")
			}
			if flags&0x0002 != 0 {
				flagStrings = append(flagStrings, "GATEWAY")
			}
			if flags&0x0004 != 0 {
				flagStrings = append(flagStrings, "HOST")
			}

			list = append(list, RouteEntry{
				Interface:   iface,
				Destination: dest,
				Gateway:     gw,
				Mask:        mask,
				PrefixLen:   prefixLen,
				Metric:      metricVal,
				Flags:       strings.Join(flagStrings, "|"),
				Family:      "ipv4",
			})
		}
	}
	return list, nil
}

func parseIPv6Routes(data []byte) ([]RouteEntry, error) {
	var list []RouteEntry
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		destHex := fields[0]
		destPrefixHex := fields[1]
		gwHex := fields[4]
		metricHex := fields[5]
		flagsHex := fields[8]
		iface := fields[9]

		dest, errDest := parseIPv6Hex(destHex)
		gw, errGw := parseIPv6Hex(gwHex)
		prefixLen, _ := strconv.ParseInt(destPrefixHex, 16, 32)
		metricVal, _ := strconv.ParseInt(metricHex, 16, 32)
		flags, _ := strconv.ParseUint(flagsHex, 16, 32)

		if errDest == nil && errGw == nil {
			var flagStrings []string
			if flags&0x0001 != 0 {
				flagStrings = append(flagStrings, "UP")
			}
			if flags&0x0002 != 0 {
				flagStrings = append(flagStrings, "GATEWAY")
			}
			if flags&0x0004 != 0 {
				flagStrings = append(flagStrings, "HOST")
			}

			list = append(list, RouteEntry{
				Interface:   iface,
				Destination: dest,
				Gateway:     gw,
				PrefixLen:   int(prefixLen),
				Metric:      int(metricVal),
				Flags:       strings.Join(flagStrings, "|"),
				Family:      "ipv6",
			})
		}
	}
	return list, nil
}

type rawIpRouteNexthop struct {
	Gateway string `json:"gateway"`
	Dev     string `json:"dev"`
}

type rawIpRoute struct {
	Dst      string              `json:"dst"`
	Gateway  string              `json:"gateway,omitempty"`
	Dev      string              `json:"dev,omitempty"`
	Metric   int                 `json:"metric,omitempty"`
	Protocol string              `json:"protocol,omitempty"`
	Nexthops []rawIpRouteNexthop `json:"nexthops,omitempty"`
}

func parseIpRouteJSON(data []byte, family string) ([]RouteEntry, error) {
	var raw []rawIpRoute
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var list []RouteEntry
	for _, item := range raw {
		dest := item.Dst
		prefixLen := 0
		if dest == "default" {
			if family == "ipv4" {
				dest = "0.0.0.0"
				prefixLen = 0
			} else {
				dest = "::"
				prefixLen = 0
			}
		} else {
			parts := strings.Split(dest, "/")
			dest = parts[0]
			if len(parts) == 2 {
				prefixLen, _ = strconv.Atoi(parts[1])
			} else {
				if family == "ipv4" {
					prefixLen = 32
				} else {
					prefixLen = 128
				}
			}
		}

		if len(item.Nexthops) > 0 {
			for _, nh := range item.Nexthops {
				list = append(list, RouteEntry{
					Interface:   nh.Dev,
					Destination: dest,
					Gateway:     nh.Gateway,
					PrefixLen:   prefixLen,
					Metric:      item.Metric,
					Protocol:    item.Protocol,
					Family:      family,
				})
			}
		} else {
			list = append(list, RouteEntry{
				Interface:   item.Dev,
				Destination: dest,
				Gateway:     item.Gateway,
				PrefixLen:   prefixLen,
				Metric:      item.Metric,
				Protocol:    item.Protocol,
				Family:      family,
			})
		}
	}
	return list, nil
}
