package lifecycle

import (
	"context"
	"testing"
)

func BenchmarkNewInstallTool(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewInstallTool()
	}
}

func BenchmarkNewUninstallTool(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewUninstallTool()
	}
}

func BenchmarkNewRestartTool(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewRestartTool()
	}
}

func BenchmarkNewStopTool(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewStopTool()
	}
}

func BenchmarkNewUpgradeTool(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewUpgradeTool()
	}
}

func BenchmarkName(b *testing.B) {
	t := NewInstallTool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	t := NewInstallTool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	t := NewInstallTool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	t := NewInstallTool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	t := NewInstallTool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	t := NewInstallTool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	t := NewInstallTool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	t := NewInstallTool()
	ctx := context.Background()
	// DryRun is on by default in tests due to lifecycle state, but we ensure it.
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.Execute(ctx, nil)
	}
}

func BenchmarkEphemeralCleanupOps(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ephemeralCleanupOps("/bin/false")
	}
}

func BenchmarkSelfInstallOps(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = selfInstallOps("/bin/false")
	}
}

func BenchmarkSelfUninstallOps(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = selfUninstallOps()
	}
}

func BenchmarkNodeUpgradeOps(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = nodeUpgradeOps("/tmp/new-binary")
	}
}

func BenchmarkExecuteOps(b *testing.B) {
	// Use harmless ops
	ops := []lifecycleOp{
		{Action: "systemctl", Args: "daemon-reload"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = executeOps(ops)
	}
}

func BenchmarkExecuteOp(b *testing.B) {
	op := lifecycleOp{Action: "systemctl", Args: "daemon-reload"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = executeOp(op)
	}
}

func BenchmarkSplitArgs(b *testing.B) {
	s := "echo 'hello world' test"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = splitArgs(s)
	}
}

func BenchmarkSplitSimple(b *testing.B) {
	s := "echo hello world"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = splitSimple(s)
	}
}

func BenchmarkInstallRemotePath(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = InstallRemotePath("")
	}
}

func BenchmarkServiceUnitPath(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = serviceUnitPath()
	}
}

func BenchmarkGenerateServiceUnit(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = generateServiceUnit("/opt/cortex-mcp/bin/cortex-mcp")
	}
}

func BenchmarkSetLiveMode(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SetLiveMode(false)
	}
}
