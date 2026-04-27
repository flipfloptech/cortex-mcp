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
			if strings.Contains(op.Path, "cortex-mcp.service") {
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
	ops := SelfUninstallOps()
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Action != "spawn_detached" {
		t.Errorf("expected spawn_detached, got %s", ops[0].Action)
	}
	if !strings.Contains(ops[0].Args, "systemd-run") {
		t.Errorf("expected systemd-run args, got %s", ops[0].Args)
	}
}

func TestDetachedUninstallOps(t *testing.T) {
	ops := DetachedUninstallOps()
	var hasStop bool
	var hasDisable bool
	var hasRemoveUnit bool

	for _, op := range ops {
		if op.Action == "systemctl" && strings.Contains(op.Args, "stop") {
			hasStop = true
		}
		if op.Action == "systemctl" && strings.Contains(op.Args, "disable") {
			hasDisable = true
		}
		if op.Action == "remove_file" && strings.Contains(op.Path, ".service") {
			hasRemoveUnit = true
		}
	}

	if !hasStop || !hasDisable || !hasRemoveUnit {
		t.Errorf("DetachedUninstallOps missing critical actions")
	}
}

func BenchmarkDetachedUninstallOps(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = DetachedUninstallOps()
	}
}
