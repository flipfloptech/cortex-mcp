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
