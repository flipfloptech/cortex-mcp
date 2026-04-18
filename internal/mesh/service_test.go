package mesh

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

	// Must use the daemon subcommand.
	if !strings.Contains(unit, "daemon") {
		t.Error("unit missing daemon subcommand in ExecStart")
	}

	// Must NOT set CORTEX_MESH_SPAWNED (replaced by subcommand dispatch).
	if strings.Contains(unit, "CORTEX_MESH_SPAWNED") {
		t.Error("unit should not set CORTEX_MESH_SPAWNED (use daemon subcommand instead)")
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
