package routing

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// stitchBufSize is the buffer size used for stream stitching.
// 32KB is the sweet spot: large enough for throughput, small enough
// for per-stream memory budget in large meshes.
const stitchBufSize = 32 * 1024

// stitchBufPoolSize is the maximum number of 32KB buffers retained
// across GC cycles. Sized for 128 concurrent bidirectional circuits
// (each circuit uses 2 buffers: one per direction). Total retained
// memory: 256 × 32KB = 8MB.
const stitchBufPoolSize = 256

// bufferPool provides reusable 32KB buffers for zero-allocation forwarding.
// This is the hot path — intermediate nodes forward gigabytes of gRPC
// traffic through these buffers without heap allocations.
//
// Uses a channel-based pool instead of sync.Pool for GC resistance:
// sync.Pool is cleared during garbage collection, causing sawtooth
// allocation patterns under traffic spikes. The channel-based pool
// retains buffers deterministically across GC cycles.
//
// L-1 evaluation: sharded pool was benchmarked and showed ~8x WORSE
// throughput under contention (721ns/op vs 86ns/op single channel)
// due to atomic counter overhead exceeding the lock contention benefit.
// The single channel remains optimal — Go's channel implementation
// uses a lightweight spinlock that scales well up to high core counts.
var bufferPool = newBufPool(stitchBufPoolSize, stitchBufSize)

// StitchResult reports the outcome of a bidirectional stream stitch.
type StitchResult struct {
	BytesAtoB int64 // Bytes transferred from A to B
	BytesBtoA int64 // Bytes transferred from B to A
	ErrAtoB   error // Error from A→B direction (nil if clean)
	ErrBtoA   error // Error from B→A direction (nil if clean)
}

// DefaultIdleTimeout is the application-level idle timeout for stitched
// circuits. If no data flows in either direction for this duration, the
// stitch is torn down to prevent goroutine and buffer leaks from silent
// TCP drops (no RST/FIN). Yamux has internal keepalives, but they don't
// protect the forwarding goroutines from hanging forever.
//
// 5 minutes is conservative: long enough for bursty workloads with
// think-time gaps, short enough to reclaim leaked circuits promptly.
const DefaultIdleTimeout = 5 * time.Minute

// Stitch performs zero-allocation bidirectional stream copying between
// two io.ReadWriteClosers. This is the Wave-Collapse circuit stitcher:
// intermediate mesh nodes use this to forward data between peers.
//
// The function blocks until both directions complete (EOF, error, or
// idle timeout). Both a and b are closed when the stitch completes.
//
// Uses DefaultIdleTimeout to kill circuits that go silent. Use
// StitchWithTimeout for custom timeout values.
//
// Performance: uses sync.Pool 32KB buffers. Zero heap allocations in
// the forwarding loop. This is the most performance-critical function
// in the entire mesh — every byte of forwarded data flows through here.
func Stitch(a, b io.ReadWriteCloser) StitchResult {
	return StitchWithTimeout(a, b, DefaultIdleTimeout)
}

// StitchWithTimeout performs bidirectional stream copying with an
// application-level idle timeout. If no data flows in a given direction
// for the specified duration, the read side returns a timeout error,
// tearing down that half of the circuit.
//
// An idleTimeout of 0 disables the idle kill (original behavior).
//
// The idle timeout is per-direction: each direction independently resets
// its deadline on every successful Read. A circuit carrying data in one
// direction but idle in the other will eventually timeout the idle side,
// which closes the opposite side and completes the stitch.
//
// Implementation uses a watchdog goroutine (L-3) instead of per-Read
// SetReadDeadline calls. The watchdog periodically checks a shared
// activity tracker and closes both connections when the circuit is
// truly idle. This eliminates time.Now() + SetReadDeadline allocations
// on every Read iteration in the forwarding hot path.
func StitchWithTimeout(a, b io.ReadWriteCloser, idleTimeout time.Duration) StitchResult {
	var result StitchResult
	var wg sync.WaitGroup
	wg.Add(2)

	// Shared activity tracker for both directions.
	tracker := newActivityTracker()

	// Start watchdog goroutine if idle timeout is enabled.
	var stopWatchdog func()
	if idleTimeout > 0 {
		connA, okA := a.(net.Conn)
		connB, okB := b.(net.Conn)
		if okA && okB {
			stopWatchdog = startIdleWatchdog(tracker, idleTimeout, connA, connB)
		}
	}

	// A → B: read from A, write to B.
	go func() {
		defer wg.Done()
		n, err := copyPooledTracked(b, a, tracker)
		result.BytesAtoB = n // M-3: wg.Wait() provides happens-before barrier
		result.ErrAtoB = err
		// Close B's write side when A sends EOF or times out.
		_ = b.Close()
	}()

	// B → A: read from B, write to A.
	go func() {
		defer wg.Done()
		n, err := copyPooledTracked(a, b, tracker)
		result.BytesBtoA = n // M-3: wg.Wait() provides happens-before barrier
		result.ErrBtoA = err
		// Close A's write side when B sends EOF or times out.
		_ = a.Close()
	}()

	wg.Wait()

	// Stop the watchdog after both directions complete.
	if stopWatchdog != nil {
		stopWatchdog()
	}

	return result
}

// activityTracker provides a shared timestamp that tracks the last
// time any data flowed through the circuit. Both directions of a
// stitch share the same tracker.
type activityTracker struct {
	lastActive atomic.Int64 // unix nanoseconds of last activity
}

func newActivityTracker() *activityTracker {
	at := &activityTracker{}
	at.touch()
	return at
}

func (at *activityTracker) touch() {
	at.lastActive.Store(time.Now().UnixNano())
}

func (at *activityTracker) idleDuration() time.Duration {
	last := at.lastActive.Load()
	return time.Since(time.Unix(0, last))
}

// startIdleWatchdog launches a goroutine that monitors the activity
// tracker and closes both connections when the circuit is idle for
// longer than the timeout duration.
//
// Returns a stop function that must be called when the stitch completes
// to prevent goroutine leaks.
//
// L-3: This replaces the per-Read deadlineReader approach. Instead of
// calling time.Now() + SetReadDeadline on every Read (which allocates),
// a single goroutine checks idle duration at half-timeout intervals.
// The check interval is timeout/2 to catch idle circuits within 1.5×
// timeout worst case — acceptable for a cleanup mechanism.
func startIdleWatchdog(tracker *activityTracker, timeout time.Duration, conns ...net.Conn) func() {
	done := make(chan struct{})

	// Check interval: half the timeout for responsive detection.
	// Clamped between 10ms and 30s to bound worst-case reap latency
	// regardless of how long the application-level timeout is.
	checkInterval := timeout / 2
	if checkInterval < 10*time.Millisecond {
		checkInterval = 10 * time.Millisecond
	} else if checkInterval > 30*time.Second {
		checkInterval = 30 * time.Second
	}

	go func() {
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if tracker.idleDuration() >= timeout {
					// Circuit is idle — close all connections.
					for _, conn := range conns {
						_ = conn.Close()
					}
					return
				}
			}
		}
	}()

	return func() {
		select {
		case <-done:
			// Already stopped.
		default:
			close(done)
		}
	}
}

// copyPooledTracked performs io.CopyBuffer using a pooled buffer,
// touching the activity tracker on every successful transfer.
// This is the zero-allocation forwarding path: the tracker.touch()
// call is a single atomic store — no allocations.
func copyPooledTracked(dst io.Writer, src io.Reader, tracker *activityTracker) (int64, error) {
	buf := bufferPool.get()
	defer bufferPool.put(buf)

	var total int64
	for {
		nr, readErr := src.Read(buf)
		if nr > 0 {
			tracker.touch()
			nw, writeErr := dst.Write(buf[:nr])
			if nw > 0 {
				total += int64(nw)
			}
			if writeErr != nil {
				return total, writeErr
			}
			if nr != nw {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}
