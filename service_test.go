package main

import (
	"strings"
	"testing"
)

// --- generateServiceUnit tests ---
// The systemd unit must be valid and contain the correct paths/settings.

func TestGenerateServiceUnit_DefaultPaths(t *testing.T) {
	t.Parallel()

	unit := generateServiceUnit("/opt/cortex-mesh/bin/cortex-mesh")

	// Must contain required systemd sections.
	for _, section := range []string{"[Unit]", "[Service]", "[Install]"} {
		if !strings.Contains(unit, section) {
			t.Errorf("unit missing section %s", section)
		}
	}

	// Must reference the correct binary path.
	if !strings.Contains(unit, "ExecStart=/opt/cortex-mesh/bin/cortex-mesh") {
		t.Error("unit does not reference correct binary path in ExecStart")
	}

	// Must set CORTEX_MESH_SPAWNED=1 so WasDeployed() returns true.
	if !strings.Contains(unit, "CORTEX_MESH_SPAWNED=1") {
		t.Error("unit missing CORTEX_MESH_SPAWNED=1 environment")
	}

	// Must include -daemon flag.
	if !strings.Contains(unit, "-daemon") {
		t.Error("unit missing -daemon flag in ExecStart")
	}

	// Must restart on failure.
	if !strings.Contains(unit, "Restart=on-failure") {
		t.Error("unit missing Restart=on-failure")
	}

	// Must wait for network.
	if !strings.Contains(unit, "After=network-online.target") {
		t.Error("unit missing network dependency")
	}

	// Must be enabled in multi-user.target.
	if !strings.Contains(unit, "WantedBy=multi-user.target") {
		t.Error("unit missing WantedBy=multi-user.target")
	}
}

func TestGenerateServiceUnit_CustomBinaryPath(t *testing.T) {
	t.Parallel()

	unit := generateServiceUnit("/usr/local/bin/my-mesh")

	if !strings.Contains(unit, "ExecStart=/usr/local/bin/my-mesh") {
		t.Error("unit does not use custom binary path")
	}
}

// --- installRemotePath tests ---
// Verifies the default and configurable remote installation path.

func TestInstallRemotePath_Default(t *testing.T) {
	t.Parallel()

	path := installRemotePath("")
	if path != "/opt/cortex-mesh/bin/cortex-mesh" {
		t.Errorf("expected /opt/cortex-mesh/bin/cortex-mesh, got %s", path)
	}
}

func TestInstallRemotePath_Custom(t *testing.T) {
	t.Parallel()

	path := installRemotePath("/usr/local/bin/custom-mesh")
	if path != "/usr/local/bin/custom-mesh" {
		t.Errorf("expected /usr/local/bin/custom-mesh, got %s", path)
	}
}

// --- serviceUnitPath tests ---
// The service unit path should be deterministic.

func TestServiceUnitPath(t *testing.T) {
	t.Parallel()

	path := serviceUnitPath()
	if path != "/etc/systemd/system/cortex-mesh.service" {
		t.Errorf("expected /etc/systemd/system/cortex-mesh.service, got %s", path)
	}
}

// --- installCommands tests ---
// Verifies the sequence of shell commands for service installation.

func TestInstallCommands(t *testing.T) {
	t.Parallel()

	cmds := installCommands()

	// Must contain the key systemd operations in order.
	joined := strings.Join(cmds, " && ")

	if !strings.Contains(joined, "daemon-reload") {
		t.Error("install commands missing systemctl daemon-reload")
	}

	if !strings.Contains(joined, "enable") {
		t.Error("install commands missing systemctl enable")
	}

	if !strings.Contains(joined, "start") || !strings.Contains(joined, "restart") {
		t.Error("install commands missing systemctl start or restart")
	}
}

// --- uninstallCommands tests ---
// Verifies the sequence of shell commands for service removal.

func TestUninstallCommands(t *testing.T) {
	t.Parallel()

	cmds := uninstallCommands()

	joined := strings.Join(cmds, " && ")

	if !strings.Contains(joined, "stop") {
		t.Error("uninstall commands missing systemctl stop")
	}

	if !strings.Contains(joined, "disable") {
		t.Error("uninstall commands missing systemctl disable")
	}

	// Must remove the service unit file.
	if !strings.Contains(joined, "rm") || !strings.Contains(joined, "cortex-mesh.service") {
		t.Error("uninstall commands missing service unit file removal")
	}

	// Must remove the binary.
	if !strings.Contains(joined, defaultInstallPath) {
		t.Error("uninstall commands missing binary removal")
	}

	// Must daemon-reload after removing the unit file.
	if !strings.Contains(joined, "daemon-reload") {
		t.Error("uninstall commands missing daemon-reload after unit removal")
	}
}

