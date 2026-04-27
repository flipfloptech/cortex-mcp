package lifecycle

import "fmt"

const (
	// defaultInstallPath is where the binary goes on persistent installs.
	defaultInstallPath = "/opt/cortex-mcp/bin/cortex-mcp"

	// ServiceName is the systemd service unit name.
	ServiceName = "cortex-mcp"

	// serviceUnitDir is the standard systemd unit directory.
	serviceUnitDir = "/etc/systemd/system"
)

// InstallRemotePath returns the remote binary path for persistent installs.
// If custom is empty, returns the default /opt/cortex-mcp/bin/cortex-mcp.
func InstallRemotePath(custom string) string {
	if custom != "" {
		return custom
	}
	return defaultInstallPath
}

// serviceUnitPath returns the full path to the systemd service unit file.
func serviceUnitPath() string {
	return fmt.Sprintf("%s/%s.service", serviceUnitDir, ServiceName)
}

// generateServiceUnit produces a systemd unit file for a persistent mesh node.
// The binary is expected at binaryPath and will run with the "daemon" subcommand
// to enter persistent fleet node mode.
func generateServiceUnit(binaryPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Cortex Mesh Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
`, binaryPath)
}
