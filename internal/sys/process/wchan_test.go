package process

import (
	"context"
	"os"
	"testing"
)

func TestGetThreadWchan_System(t *testing.T) {
	if _, err := os.Stat("/proc/1/wchan"); err != nil {
		t.Skip("Skipping test because /proc/1/wchan is not readable")
	}

	ctx := context.Background()
	res, err := GetThreadWchan(ctx, nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if res.SystemSummary.TotalThreads == 0 {
		t.Errorf("expected >0 threads, got 0")
	}

	// Make sure we have thread states parsed
	if len(res.ThreadStates) == 0 {
		t.Errorf("expected some thread states, got none")
	}

	if len(res.Threads) > 0 {
		t.Errorf("Mode 1 should not populate flat Threads array")
	}

	// If any threads are blocked, ensure the new nested struct is present
	for wchan, details := range res.BlockedWchan {
		if details.TotalThreads == 0 {
			t.Errorf("wchan %s has 0 TotalThreads", wchan)
		}
		if len(details.AffectedProcesses) == 0 {
			t.Errorf("wchan %s has no affected processes", wchan)
		}
		for comm, procDetails := range details.AffectedProcesses {
			if procDetails.Count == 0 {
				t.Errorf("comm %s has 0 count", comm)
			}
			if len(procDetails.ExamplePids) == 0 {
				t.Errorf("comm %s has no example pids", comm)
			}
			if len(procDetails.ExamplePids) > 15 {
				t.Errorf("comm %s exceeded example pids limit (got %d)", comm, len(procDetails.ExamplePids))
			}
		}
	}
}

func TestGetThreadWchan_TargetPid(t *testing.T) {
	if _, err := os.Stat("/proc/1/wchan"); err != nil {
		t.Skip("Skipping test because /proc/1/wchan is not readable")
	}

	ctx := context.Background()
	pid := 1 // init/systemd
	res, err := GetThreadWchan(ctx, &pid)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if res.SystemSummary.TotalThreads == 0 {
		t.Errorf("expected >0 threads for pid 1, got 0")
	}

	if len(res.BlockedWchan) > 0 {
		t.Errorf("Mode 2 should not populate BlockedWchan map")
	}

	if len(res.Threads) == 0 {
		t.Errorf("Mode 2 should populate flat Threads array")
	}

	// Verify thread details
	for _, thread := range res.Threads {
		if thread.TID == 0 {
			t.Errorf("expected non-zero TID")
		}
		if thread.State == "" {
			t.Errorf("expected non-empty state for TID %d", thread.TID)
		}
	}
}

func BenchmarkGetThreadWchan(b *testing.B) {
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetThreadWchan(ctx, nil)
	}
}

func BenchmarkParseStateFromStatus(b *testing.B) {
	data := []byte("Name:\tfoo\nState:\tS (sleeping)\nUid:\t1000\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseStateFromStatus(data)
	}
}

func BenchmarkGetTIDs(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = getTIDs(1)
	}
}
