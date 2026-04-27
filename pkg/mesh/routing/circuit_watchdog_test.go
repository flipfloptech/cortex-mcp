package routing

import (
	"io"
	"net"
	"testing"
	"time"
)

// --- Watchdog-based idle timeout tests (L-3) ---

// TestIdleWatchdog_KillsIdleCircuit verifies that the watchdog closes
// connections when no data flows for the timeout period.
func TestIdleWatchdog_KillsIdleCircuit(t *testing.T) {
	t.Parallel()

	a, b := net.Pipe()
	tracker := newActivityTracker()

	stop := startIdleWatchdog(tracker, 100*time.Millisecond, a, b)
	defer stop()

	// Don't send any data — watchdog should close connections.
	buf := make([]byte, 1)
	_, err := a.Read(buf)
	if err == nil {
		t.Fatal("expected error from watchdog close")
	}
}

// TestIdleWatchdog_ActivityResetsTimer verifies that data transfer
// resets the watchdog timer — connections stay alive while active.
func TestIdleWatchdog_ActivityResetsTimer(t *testing.T) {
	t.Parallel()

	a1, a2 := net.Pipe()
	tracker := newActivityTracker()

	stop := startIdleWatchdog(tracker, 200*time.Millisecond, a1, a2)
	defer stop()

	// Send data every 100ms, keeping the circuit alive.
	for i := 0; i < 3; i++ {
		tracker.touch()
		time.Sleep(100 * time.Millisecond)
	}

	// Circuit should still be alive.
	go func() { _, _ = a1.Write([]byte("ping")) }()
	buf := make([]byte, 4)
	_, err := io.ReadFull(a2, buf)
	if err != nil {
		t.Fatalf("connection should still be alive: %v", err)
	}

	_ = a1.Close()
	_ = a2.Close()
}

// TestIdleWatchdog_StopPreventsClose verifies that calling stop()
// prevents the watchdog from closing connections — no goroutine leak.
func TestIdleWatchdog_StopPreventsClose(t *testing.T) {
	t.Parallel()

	a, b := net.Pipe()
	tracker := newActivityTracker()

	stop := startIdleWatchdog(tracker, 100*time.Millisecond, a, b)

	// Stop immediately.
	stop()

	// Wait past the timeout — connections should NOT be closed.
	time.Sleep(250 * time.Millisecond)

	// Verify connections are still usable.
	go func() { _, _ = a.Write([]byte("test")) }()
	buf := make([]byte, 4)
	_, err := io.ReadFull(b, buf)
	if err != nil {
		t.Fatalf("connection should be alive after stop: %v", err)
	}

	_ = a.Close()
	_ = b.Close()
}

// BenchmarkCopyPooled_WithWatchdog benchmarks the forwarding path with
// watchdog-based idle detection — no per-Read SetReadDeadline.
func BenchmarkCopyPooled_WithWatchdog(b *testing.B) {
	// Use net.Pipe — doesn't support SetDeadline, so fallback path.
	reader, writer := io.Pipe()

	go func() {
		data := make([]byte, stitchBufSize)
		for {
			if _, err := writer.Write(data); err != nil {
				return
			}
		}
	}()

	b.ResetTimer()
	b.ReportAllocs()
	b.SetBytes(int64(stitchBufSize))

	// We just measure copyPooled without deadline wrapper.
	total := int64(0)
	for i := 0; i < b.N; i++ {
		buf := bufferPool.get()
		n, err := reader.Read(buf)
		total += int64(n)
		bufferPool.put(buf)
		if err != nil {
			break
		}
	}

	_ = writer.Close()
}
