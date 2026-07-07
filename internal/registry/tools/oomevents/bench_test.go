package oomevents

import (
	"context"
	"testing"
)

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := newFakeTool(b, ringFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := newFakeTool(b, ringFixture)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkParseOOMEvents(b *testing.B) {
	raw := []byte(ringFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseOOMEvents(raw, 1700000000, true)
	}
}

func BenchmarkParseLinePrefix(b *testing.B) {
	line := "<3>[12345.678901] Out of memory: Killed process 4321 (myapp) total-vm:1234kB"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = parseLinePrefix(line)
	}
}

func BenchmarkParseKilledLine(b *testing.B) {
	msg := "Out of memory: Killed process 4321 (myapp) total-vm:1234kB, anon-rss:567kB, file-rss:89kB, shmem-rss:0kB, UID:1000 pgtables:12kB oom_score_adj:0"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseKilledLine(msg)
	}
}

func BenchmarkParseOOMKillLine(b *testing.B) {
	msg := "oom-kill:constraint=CONSTRAINT_MEMCG,nodemask=(null),cpuset=/,mems_allowed=0,oom_memcg=/system.slice/foo,task_memcg=/system.slice/foo,task=myapp,pid=4321,uid=1000"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseOOMKillLine(msg)
	}
}

func BenchmarkKbToMB(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = kbToMB(204800)
	}
}

func BenchmarkReadBtime(b *testing.B) {
	root := writeProcStat(b, procStatFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readBtime(root)
	}
}

func BenchmarkOffsetToRFC3339(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = offsetToRFC3339(1700000000, 12345.678901)
	}
}
