package mesh

import (
	"testing"
)

// --- upgradeRestartCommand tests ---
// The restart command should exec the binary with the install subcommand, not
// construct raw shell commands.

func TestUpgradeRestartCommand_Systemd(t *testing.T) {
	t.Parallel()

	cmd := upgradeRestartCommand("/opt/cortex-mesh/bin/cortex-mesh")
	expected := "sudo /opt/cortex-mesh/bin/cortex-mesh local-op install"
	if cmd != expected {
		t.Errorf("expected %q, got %q", expected, cmd)
	}
}

// --- needsUpgrade tests ---
// Determines whether we should push a new binary based on flag state.

func TestNeedsUpgrade_SkipDeployFalse(t *testing.T) {
	t.Parallel()

	// When skipDeploy is false, we always upload — so upgrade is needed.
	if !needsUpgrade(false) {
		t.Error("expected upgrade needed when skipDeploy=false")
	}
}

func TestNeedsUpgrade_SkipDeployTrue(t *testing.T) {
	t.Parallel()

	// When skipDeploy is true, skip the upload — no upgrade needed.
	if needsUpgrade(true) {
		t.Error("expected no upgrade when skipDeploy=true")
	}
}
