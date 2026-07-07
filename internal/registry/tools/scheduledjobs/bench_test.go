package scheduledjobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// benchTool wires a hermetic tool with fake cron trees and mocked systemctl.
func benchTool(b *testing.B) *Tool {
	b.Helper()
	etc := b.TempDir()
	spool := filepath.Join(b.TempDir(), "crontabs")

	write := func(path, content string) {
		b.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	write(filepath.Join(etc, "crontab"), "SHELL=/bin/sh\n17 * * * * root run-parts /etc/cron.hourly\n")
	write(filepath.Join(etc, "cron.d", "backup"), "0 2 * * * backup /usr/local/bin/backup.sh\n")
	write(filepath.Join(spool, "alice"), "*/10 * * * * /home/alice/poll.sh\n")

	tool := New()
	tool.etcRoot = etc
	tool.spoolRoots = []string{spool}
	tool.execLookPath = func(file string) (string, error) {
		if file == "systemctl" {
			return "/usr/bin/systemctl", nil
		}
		return "", fmt.Errorf("%s: not found", file)
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(timersJSON), nil
	}
	return tool
}

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := benchTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := benchTool(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkCollectTimers(b *testing.B) {
	tool := benchTool(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.collectTimers(ctx)
	}
}

func BenchmarkParseTimersJSON(b *testing.B) {
	data := []byte(timersJSON)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseTimersJSON(data)
	}
}

func BenchmarkParseTimersText(b *testing.B) {
	data := []byte(timersText)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseTimersText(data)
	}
}

func BenchmarkParseSystemdStamp(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = parseSystemdStamp("Tue 2025-08-19 00:00:00 UTC")
	}
}

func BenchmarkUsecToISO(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = usecToISO(1755561600000000)
	}
}

func BenchmarkAsInt64(b *testing.B) {
	var v interface{} = "1755561600000000"
	for i := 0; i < b.N; i++ {
		_, _ = asInt64(v)
	}
}

func BenchmarkCollectCron(b *testing.B) {
	tool := benchTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.collectCron()
	}
}

func BenchmarkParseCrontabContent(b *testing.B) {
	content := "SHELL=/bin/sh\n17 * * * * root run-parts /etc/cron.hourly\n@reboot root /usr/local/bin/warmup.sh\n"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseCrontabContent(content, true, "", "/etc/crontab")
	}
}

func BenchmarkTruncateCommand(b *testing.B) {
	long := "/usr/bin/very-long-command --with --many --flags --and --arguments --that --exceed --the --limit --by --quite --a --lot --indeed --truly"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = truncateCommand(long)
	}
}
