package mesh

import (
	"testing"
)

// --- upgradeRestartCommand tests ---
// The restart command should exec the binary with the install subcommand, not
// construct raw shell commands.

func TestUpgradeRestartCommand_Systemd(t *testing.T) {
	t.Parallel()

	cmd := upgradeRestartCommand("/opt/cortex-mcp/bin/cortex-mcp")
	expected := "sudo /opt/cortex-mcp/bin/cortex-mcp local-op install"
	if cmd != expected {
		t.Errorf("expected %q, got %q", expected, cmd)
	}
}
