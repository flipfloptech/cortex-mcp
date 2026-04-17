// Package main provides systemd service installation for persistent mesh nodes.
//
// When the install subcommand is used, the deployer:
//  1. Uploads the binary to /opt/cortex-mesh/bin/cortex-mesh
//  2. Writes cortex-mesh.service to /etc/systemd/system/
//  3. Runs systemctl daemon-reload && systemctl enable --now cortex-mesh
//  4. Waits for the service to start listening on :4443
//  5. Connects via mTLS
//
// This transforms the mesh from a temporary process into permanent infrastructure
// that survives reboots and is managed via standard systemctl commands.
package main

import "fmt"

const (
	// defaultInstallPath is where the binary goes on persistent installs.
	defaultInstallPath = "/opt/cortex-mesh/bin/cortex-mesh"

	// serviceName is the systemd service unit name.
	serviceName = "cortex-mesh"

	// serviceUnitDir is the standard systemd unit directory.
	serviceUnitDir = "/etc/systemd/system"
)

// installRemotePath returns the remote binary path for persistent installs.
// If custom is empty, returns the default /opt/cortex-mesh/bin/cortex-mesh.
func installRemotePath(custom string) string {
	if custom != "" {
		return custom
	}
	return defaultInstallPath
}

// serviceUnitPath returns the full path to the systemd service unit file.
func serviceUnitPath() string {
	return fmt.Sprintf("%s/%s.service", serviceUnitDir, serviceName)
}

// generateServiceUnit produces a systemd unit file for a persistent mesh node.
// The binary is expected at binaryPath and will run with -daemon and
// CORTEX_MESH_SPAWNED=1 to enter fleet node mode.
func generateServiceUnit(binaryPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Cortex Mesh Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s -daemon
Environment=CORTEX_MESH_SPAWNED=1
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
`, binaryPath)
}
