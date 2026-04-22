package lifecycle

import (
	"strings"
	"testing"
)

// --- selfInstall / selfUninstall tests ---
// These test the local lifecycle operations that the binary performs
// on itself. They produce operation lists (not shell commands) so
// we can verify the logic without root.

func TestSelfInstallOps(t *testing.T) {
	t.Parallel()

	ops := SelfInstallOps()

	// Must write a service unit file.
	var hasWriteUnit bool
	var hasReload bool
	var hasEnable bool
	var hasStart bool

	for _, op := range ops {
		switch op.Action {
		case "write_file":
			if strings.Contains(op.Path, "cortex-mesh.service") {
				hasWriteUnit = true
				if !strings.Contains(op.Content, "ExecStart=") {
					t.Error("service unit missing ExecStart")
				}
			}
		case "systemctl":
			switch {
			case strings.Contains(op.Args, "daemon-reload"):
				hasReload = true
			case strings.Contains(op.Args, "enable"):
				hasEnable = true
			case strings.Contains(op.Args, "start") || strings.Contains(op.Args, "restart"):
				hasStart = true
			}
		case "copy_binary":
			if op.Path == "" {
				t.Error("copy_binary missing destination path")
			}
		}
	}

	if !hasWriteUnit {
		t.Error("selfInstallOps missing service unit write")
	}
	if !hasReload {
		t.Error("selfInstallOps missing daemon-reload")
	}
	if !hasEnable {
		t.Error("selfInstallOps missing enable")
	}
	if !hasStart {
		t.Error("selfInstallOps missing start/restart")
	}
}

func TestSelfUninstallOps(t *testing.T) {
	t.Parallel()

	ops := SelfUninstallOps()

	var hasStop bool
	var hasDisable bool
	var hasRemoveUnit bool
	var hasReload bool
	var hasRemoveBinary bool

	for _, op := range ops {
		switch op.Action {
		case "systemctl":
			switch {
			case strings.Contains(op.Args, "stop"):
				hasStop = true
			case strings.Contains(op.Args, "disable"):
				hasDisable = true
			case strings.Contains(op.Args, "daemon-reload"):
				hasReload = true
			}
		case "remove_file":
			if strings.Contains(op.Path, ".service") {
				hasRemoveUnit = true
			}
			if strings.Contains(op.Path, InstallRemotePath("")) {
				hasRemoveBinary = true
			}
		}
	}

	if !hasStop {
		t.Error("selfUninstallOps missing stop")
	}
	if !hasDisable {
		t.Error("selfUninstallOps missing disable")
	}
	if !hasRemoveUnit {
		t.Error("selfUninstallOps missing unit file removal")
	}
	if !hasReload {
		t.Error("selfUninstallOps missing daemon-reload")
	}
	if !hasRemoveBinary {
		t.Error("selfUninstallOps missing binary removal")
	}
}

// --- Ephemeral cleanup ops ---
// When a node is running ephemerally (from /tmp), uninstall should
// self-destruct rather than try systemd operations.

func TestEphemeralCleanupOps(t *testing.T) {
	t.Parallel()

	ops := ephemeralCleanupOps("/tmp/cortex-mesh-abc123")

	if len(ops) == 0 {
		t.Fatal("expected at least one cleanup op")
	}

	var hasRemove bool
	for _, op := range ops {
		if op.Action == "remove_file" && op.Path == "/tmp/cortex-mesh-abc123" {
			hasRemove = true
		}
	}

	if !hasRemove {
		t.Error("ephemeralCleanupOps should remove the binary path")
	}
}
