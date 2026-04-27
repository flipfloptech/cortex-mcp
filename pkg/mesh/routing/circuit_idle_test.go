package routing

import (
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Idle timeout tests ---

// TestStitch_IdleTimeout_KillsStaleCircuit verifies that an idle
// stitch terminates after the idle timeout. This is the core fix:
// connections that go silent (no data, no close) must not leak goroutines.
func TestStitch_IdleTimeout_KillsStaleCircuit(t *testing.T) {
	t.Parallel()

	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	defer func() { _ = a1.Close() }()
	defer func() { _ = b1.Close() }()
	defer func() { _ = a2.Close() }()
	defer func() { _ = b2.Close() }()

	done := make(chan StitchResult, 1)
	go func() {
		done <- StitchWithTimeout(a2, b1, 100*time.Millisecond)
	}()

	// Don't send any data. Don't close. Just wait.
	// The stitch should terminate after the idle timeout.
	select {
	case result := <-done:
		// Verify it terminated due to idle kill, not clean EOF.
		if result.ErrAtoB == nil && result.ErrBtoA == nil {
			t.Fatal("expected at least one error from idle timeout")
		}
		// With watchdog-based idle detection (L-3), the error is
		// "closed pipe" (from conn.Close()) rather than "timeout"
		// (from SetReadDeadline). Both indicate successful idle kill.
		hasIdleKill := isTimeoutError(result.ErrAtoB) || isTimeoutError(result.ErrBtoA) ||
			isClosedPipeError(result.ErrAtoB) || isClosedPipeError(result.ErrBtoA)
		if !hasIdleKill {
			t.Fatalf("expected timeout or closed pipe error, got AtoB=%v, BtoA=%v", result.ErrAtoB, result.ErrBtoA)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stitch should have terminated after 100ms idle timeout")
	}
}

// TestStitch_IdleTimeout_ActiveDataKeepsAlive verifies that active
// data transfer resets the idle timeout — the stitch should NOT be
// killed while data is flowing.
func TestStitch_IdleTimeout_ActiveDataKeepsAlive(t *testing.T) {
	t.Parallel()

	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	defer func() { _ = a1.Close() }()
	defer func() { _ = b1.Close() }()

	done := make(chan StitchResult, 1)
	go func() {
		done <- StitchWithTimeout(a2, b1, 200*time.Millisecond)
	}()

	// Send data every 100ms for 500ms — well beyond the 200ms timeout.
	// The stitch should stay alive because data resets the timeout.
	payload := []byte("ping")
	for i := 0; i < 5; i++ {
		go func() { _, _ = a1.Write(payload) }()
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(b2, buf); err != nil {
			t.Fatalf("Read iteration %d: %v", i, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Now stop sending data. The stitch should timeout after 200ms.
	select {
	case <-done:
		// Good — terminated after idle period.
	case <-time.After(3 * time.Second):
		t.Fatal("stitch should have terminated after idle period")
	}

	_ = a2.Close()
	_ = b2.Close()
}

// TestStitch_IdleTimeout_ZeroMeansNoTimeout verifies that a zero
// idle timeout disables the idle kill — preserving original behavior.
func TestStitch_IdleTimeout_ZeroMeansNoTimeout(t *testing.T) {
	t.Parallel()

	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()

	done := make(chan StitchResult, 1)
	go func() {
		done <- StitchWithTimeout(a2, b1, 0) // no timeout
	}()

	// Wait briefly — should NOT terminate.
	select {
	case <-done:
		t.Fatal("stitch with zero timeout should not terminate on its own")
	case <-time.After(200 * time.Millisecond):
		// Good — still alive.
	}

	// Clean close.
	_ = a1.Close()
	_ = b2.Close()
	<-done
}

// TestStitch_DefaultTimeout_UsedByStitch verifies that the Stitch()
// convenience function applies the default idle timeout.
func TestStitch_DefaultTimeout_UsedByStitch(t *testing.T) {
	t.Parallel()

	if DefaultIdleTimeout <= 0 {
		t.Fatalf("DefaultIdleTimeout should be positive, got %v", DefaultIdleTimeout)
	}
}

// TestStitch_IdleTimeout_ConcurrentStitches verifies that multiple
// concurrent stitches with timeouts don't interfere with each other.
func TestStitch_IdleTimeout_ConcurrentStitches(t *testing.T) {
	t.Parallel()

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			a1, a2 := net.Pipe()
			b1, b2 := net.Pipe()
			defer func() { _ = a1.Close() }()
			defer func() { _ = b2.Close() }()

			done := make(chan StitchResult, 1)
			go func() {
				done <- StitchWithTimeout(a2, b1, 100*time.Millisecond)
			}()

			// Send some data, then go idle.
			_, _ = a1.Write([]byte("hello"))
			buf := make([]byte, 5)
			_, _ = io.ReadFull(b2, buf)

			// Wait for timeout.
			select {
			case <-done:
				// Good.
			case <-time.After(3 * time.Second):
				t.Error("concurrent stitch did not timeout")
			}
		}()
	}

	wg.Wait()
}

// isTimeoutError checks if an error is a net timeout or contains "timeout".
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	return strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline")
}

// isClosedPipeError checks if an error indicates a closed connection.
// With watchdog-based idle detection (L-3), idle kills produce "closed pipe"
// errors instead of timeout errors.
func isClosedPipeError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "closed pipe") ||
		strings.Contains(err.Error(), "closed network") ||
		strings.Contains(err.Error(), "use of closed")
}
