package networkinterfaces

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

type IPAddress struct {
	Family    string `json:"family"` // "inet" or "inet6"
	Address   string `json:"address"`
	PrefixLen int    `json:"prefix_len"`
	Scope     string `json:"scope,omitempty"`
}

type NetworkInterface struct {
	Index     int         `json:"index"`
	Name      string      `json:"name"`
	MAC       string      `json:"mac,omitempty"`
	MTU       int         `json:"mtu"`
	OperState string      `json:"operstate"`
	Flags     []string    `json:"flags"`
	IPs       []IPAddress `json:"ips,omitempty"`
	Carrier   *int        `json:"carrier,omitempty"`
	SpeedMbps *int        `json:"speed_mbps,omitempty"`
	Duplex    string      `json:"duplex,omitempty"`
}

type NetworkInterfacesData struct {
	Interfaces []NetworkInterface `json:"interfaces"`
}

type NetworkInterfacesTool struct {
	sysfsRoot   string
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *NetworkInterfacesTool {
	return &NetworkInterfacesTool{
		sysfsRoot: "/sys/class/net",
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func init() {
	registry.Register(registry.WithCache(5*time.Second, New()))
}

func (t *NetworkInterfacesTool) Name() string {
	return "get_network_interfaces"
}

func (t *NetworkInterfacesTool) Category() registry.Category {
	return registry.CategoryNetwork
}

func (t *NetworkInterfacesTool) Help() string {
	return `Collects network interfaces, including MAC addresses, IP configurations, MTU values, and L2 link operational states.

Data Sources:
- /sys/class/net/
- ip a (via ip -j addr JSON execution fallback)
- Go standard library net package`
}

func (t *NetworkInterfacesTool) Description() string {
	return "Query network interfaces MACs, IPs, MTUs, and L2 link states"
}

func (t *NetworkInterfacesTool) Parameters() []registry.ToolParam {
	return nil
}

func (t *NetworkInterfacesTool) Hidden() bool { return false }

func (t *NetworkInterfacesTool) IsSupported() (bool, string) {
	if !registry.PathExists(t.sysfsRoot) {
		_, err := exec.LookPath("ip")
		if err != nil {
			return false, "Network interfaces not supported (missing /sys/class/net and ip binary)"
		}
	}
	return true, ""
}

func (t *NetworkInterfacesTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var interfaces []NetworkInterface
	var err error

	// Try primary: Sysfs + net package API
	if registry.PathExists(t.sysfsRoot) {
		interfaces, err = parseSysfsInterfaces(t.sysfsRoot)
		if err == nil {
			interfaces = populateIPsAndFlags(interfaces)
		}
	}

	// If any interface has no IPs, or sysfs failed/empty, try to load via ip -j addr fallback
	needsIPs := len(interfaces) == 0 || err != nil
	if !needsIPs {
		for _, iface := range interfaces {
			if len(iface.IPs) == 0 {
				needsIPs = true
				break
			}
		}
	}

	if needsIPs {
		output, cmdErr := t.execCommand(ctx, "ip", "-j", "addr")
		if cmdErr == nil {
			parsed, parseErr := parseIpAddrJSON(output)
			if parseErr == nil {
				if len(interfaces) == 0 {
					interfaces = parsed
					if registry.PathExists(t.sysfsRoot) {
						interfaces = augmentSysfsData(t.sysfsRoot, interfaces)
					}
				} else {
					// Merge IPs and Flags from parsed ip -j addr into interfaces
					for i := range interfaces {
						if len(interfaces[i].IPs) == 0 {
							for _, p := range parsed {
								if p.Name == interfaces[i].Name {
									interfaces[i].IPs = p.IPs
									if len(interfaces[i].Flags) == 0 {
										interfaces[i].Flags = p.Flags
									}
									if interfaces[i].Index == 0 {
										interfaces[i].Index = p.Index
									}
									break
								}
							}
						}
					}
				}
			}
		}
	}

	summaryStr := fmt.Sprintf("Found %d active network interfaces", len(interfaces))

	data := NetworkInterfacesData{
		Interfaces: interfaces,
	}

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func parseSysfsInterfaces(sysfs string) ([]NetworkInterface, error) {
	dirs, err := os.ReadDir(sysfs)
	if err != nil {
		return nil, err
	}
	var list []NetworkInterface
	for _, d := range dirs {
		name := d.Name()
		if name == "lo" {
			continue
		}
		ifaceDir := filepath.Join(sysfs, name)

		macBytes, _ := os.ReadFile(filepath.Join(ifaceDir, "address"))
		mac := strings.TrimSpace(string(macBytes))

		mtuBytes, _ := os.ReadFile(filepath.Join(ifaceDir, "mtu"))
		mtu, _ := strconv.Atoi(strings.TrimSpace(string(mtuBytes)))

		operBytes, _ := os.ReadFile(filepath.Join(ifaceDir, "operstate"))
		operstate := strings.TrimSpace(string(operBytes))

		var carrierPtr *int
		carrierBytes, err := os.ReadFile(filepath.Join(ifaceDir, "carrier"))
		if err == nil {
			val, err := strconv.Atoi(strings.TrimSpace(string(carrierBytes)))
			if err == nil {
				carrierPtr = &val
			}
		}

		var speedPtr *int
		speedBytes, err := os.ReadFile(filepath.Join(ifaceDir, "speed"))
		if err == nil {
			val, err := strconv.Atoi(strings.TrimSpace(string(speedBytes)))
			if err == nil {
				speedPtr = &val
			}
		}

		duplexBytes, _ := os.ReadFile(filepath.Join(ifaceDir, "duplex"))
		duplex := strings.TrimSpace(string(duplexBytes))

		ifindexBytes, _ := os.ReadFile(filepath.Join(ifaceDir, "ifindex"))
		index, _ := strconv.Atoi(strings.TrimSpace(string(ifindexBytes)))

		list = append(list, NetworkInterface{
			Index:     index,
			Name:      name,
			MAC:       mac,
			MTU:       mtu,
			OperState: operstate,
			Carrier:   carrierPtr,
			SpeedMbps: speedPtr,
			Duplex:    duplex,
		})
	}
	return list, nil
}

func populateIPsAndFlags(interfaces []NetworkInterface) []NetworkInterface {
	for i := range interfaces {
		iface, err := net.InterfaceByName(interfaces[i].Name)
		if err == nil {
			if interfaces[i].Index == 0 {
				interfaces[i].Index = iface.Index
			}
			flagStr := iface.Flags.String()
			if flagStr != "" {
				interfaces[i].Flags = strings.Split(strings.ToUpper(flagStr), "|")
			}

			addrs, err := iface.Addrs()
			if err == nil {
				for _, addr := range addrs {
					ipStr := addr.String()
					parts := strings.Split(ipStr, "/")
					if len(parts) == 2 {
						prefixLen, _ := strconv.Atoi(parts[1])
						family := "inet"
						if strings.Contains(parts[0], ":") {
							family = "inet6"
						}
						interfaces[i].IPs = append(interfaces[i].IPs, IPAddress{
							Family:    family,
							Address:   parts[0],
							PrefixLen: prefixLen,
						})
					}
				}
			}
		}
	}
	return interfaces
}

type rawIpAddrInfo struct {
	Family    string `json:"family"`
	Local     string `json:"local"`
	PrefixLen int    `json:"prefixlen"`
	Scope     string `json:"scope"`
}

type rawIpInterface struct {
	IfIndex   int             `json:"ifindex"`
	IfName    string          `json:"ifname"`
	Flags     []string        `json:"flags"`
	MTU       int             `json:"mtu"`
	OperState string          `json:"operstate"`
	Address   string          `json:"address"`
	AddrInfo  []rawIpAddrInfo `json:"addr_info"`
}

func parseIpAddrJSON(data []byte) ([]NetworkInterface, error) {
	var raw []rawIpInterface
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var list []NetworkInterface
	for _, item := range raw {
		if item.IfName == "lo" {
			continue
		}
		var ips []IPAddress
		for _, ip := range item.AddrInfo {
			ips = append(ips, IPAddress{
				Family:    ip.Family,
				Address:   ip.Local,
				PrefixLen: ip.PrefixLen,
				Scope:     ip.Scope,
			})
		}
		list = append(list, NetworkInterface{
			Index:     item.IfIndex,
			Name:      item.IfName,
			MAC:       item.Address,
			MTU:       item.MTU,
			OperState: strings.ToLower(item.OperState),
			Flags:     item.Flags,
			IPs:       ips,
		})
	}
	return list, nil
}

func augmentSysfsData(sysfs string, interfaces []NetworkInterface) []NetworkInterface {
	for i := range interfaces {
		ifaceDir := filepath.Join(sysfs, interfaces[i].Name)
		if registry.PathExists(ifaceDir) {
			carrierBytes, err := os.ReadFile(filepath.Join(ifaceDir, "carrier"))
			if err == nil {
				val, err := strconv.Atoi(strings.TrimSpace(string(carrierBytes)))
				if err == nil {
					interfaces[i].Carrier = &val
				}
			}
			speedBytes, err := os.ReadFile(filepath.Join(ifaceDir, "speed"))
			if err == nil {
				val, err := strconv.Atoi(strings.TrimSpace(string(speedBytes)))
				if err == nil {
					interfaces[i].SpeedMbps = &val
				}
			}
			duplexBytes, err := os.ReadFile(filepath.Join(ifaceDir, "duplex"))
			if err == nil {
				interfaces[i].Duplex = strings.TrimSpace(string(duplexBytes))
			}
		}
	}
	return interfaces
}
