package api

import (
	"math"
	"testing"
	"time"
)

// --- P2-2: Jittered gossip ticker ---
//
// Fleet-wide simultaneous boot creates phase-locked gossip traffic waves.
// The fix jitters both the first tick and subsequent intervals:
//   - First tick: uniform random in [0, interval)
//   - Subsequent ticks: interval ± 10% jitter
//
// Contract:
//   1. jitteredFirstDelay returns values in [0, interval).
//   2. jitteredInterval returns values in [interval*0.9, interval*1.1].
//   3. Over many calls, the jitter distribution fills the expected range
//      (not clustered at a single value).

func TestJitteredFirstDelay_InRange(t *testing.T) {
	t.Parallel()

	interval := 3 * time.Second

	for i := 0; i < 1000; i++ {
		d := jitteredFirstDelay(interval)
		if d < 0 || d >= interval {
			t.Fatalf("jitteredFirstDelay(%v) = %v; want [0, %v)", interval, d, interval)
		}
	}
}

func TestJitteredFirstDelay_Distribution(t *testing.T) {
	t.Parallel()

	// Over 10k samples, the first delay should span a wide range,
	// not be clustered at zero or near the interval.
	interval := 1 * time.Second
	var minD, maxD time.Duration
	minD = math.MaxInt64

	for i := 0; i < 10_000; i++ {
		d := jitteredFirstDelay(interval)
		if d < minD {
			minD = d
		}
		if d > maxD {
			maxD = d
		}
	}

	// The range should cover at least 80% of the interval.
	spread := maxD - minD
	if spread < time.Duration(float64(interval)*0.8) {
		t.Fatalf("jitter spread = %v; expected at least 80%% of %v (%v)",
			spread, interval, time.Duration(float64(interval)*0.8))
	}
}

func TestJitteredInterval_InRange(t *testing.T) {
	t.Parallel()

	interval := 3 * time.Second
	lo := time.Duration(float64(interval) * 0.9)
	hi := time.Duration(float64(interval) * 1.1)

	for i := 0; i < 1000; i++ {
		d := jitteredInterval(interval)
		if d < lo || d > hi {
			t.Fatalf("jitteredInterval(%v) = %v; want [%v, %v]", interval, d, lo, hi)
		}
	}
}

func TestJitteredInterval_Distribution(t *testing.T) {
	t.Parallel()

	// Over 10k samples, should span most of the ±10% range.
	interval := 1 * time.Second
	var minD, maxD time.Duration
	minD = math.MaxInt64

	for i := 0; i < 10_000; i++ {
		d := jitteredInterval(interval)
		if d < minD {
			minD = d
		}
		if d > maxD {
			maxD = d
		}
	}

	// Expected range: 200ms (±10% of 1s).
	// Should cover at least 80% of that = 160ms.
	expectedRange := time.Duration(float64(interval) * 0.2)
	spread := maxD - minD
	if spread < time.Duration(float64(expectedRange)*0.8) {
		t.Fatalf("jitter spread = %v; expected at least 80%% of %v (%v)",
			spread, expectedRange, time.Duration(float64(expectedRange)*0.8))
	}
}
