package nucleus

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- MetricSource: pluggable system metrics ---

func TestCalculator_CustomMetricSource(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	src := &staticMetricSource{cpuPercent: 50.0, memPercent: 30.0}
	calc := NewImpedanceCalculator(m, WithMetricSource(src))
	calc.AddStreams(10)

	imp := calc.Calculate()
	if imp < MinImpedance || imp > MaxImpedance {
		t.Fatalf("impedance %f out of range [%f, %f]", imp, MinImpedance, MaxImpedance)
	}
}

// --- Idle system → low impedance ---

func TestCalculator_IdleSystem(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	src := &staticMetricSource{cpuPercent: 0, memPercent: 0}
	calc := NewImpedanceCalculator(m, WithMetricSource(src))

	imp := calc.Calculate()
	if imp != MinImpedance {
		t.Fatalf("idle system impedance = %f, want %f", imp, MinImpedance)
	}
}

// --- Saturated system → high impedance ---

func TestCalculator_SaturatedSystem(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	src := &staticMetricSource{cpuPercent: 100, memPercent: 100}
	calc := NewImpedanceCalculator(m, WithMetricSource(src))
	calc.AddStreams(1000) // well above maxStreamPressure

	imp := calc.Calculate()
	if imp != MaxImpedance {
		t.Fatalf("saturated system impedance = %f, want %f", imp, MaxImpedance)
	}
}

// --- Stream pressure increases impedance ---

func TestCalculator_StreamPressure(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	src := &staticMetricSource{cpuPercent: 0, memPercent: 0}

	// Low stream count.
	calcLow := NewImpedanceCalculator(m, WithMetricSource(src))
	calcLow.AddStreams(5)
	impLow := calcLow.Calculate()

	// High stream count.
	calcHigh := NewImpedanceCalculator(m, WithMetricSource(src))
	calcHigh.AddStreams(200)
	impHigh := calcHigh.Calculate()

	if impHigh <= impLow {
		t.Fatalf("higher stream count should increase impedance: low=%f, high=%f", impLow, impHigh)
	}
}

// --- CPU pressure increases impedance ---

func TestCalculator_CPUPressure(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")

	srcLow := &staticMetricSource{cpuPercent: 10, memPercent: 0}
	calcLow := NewImpedanceCalculator(m, WithMetricSource(srcLow))
	impLow := calcLow.Calculate()

	srcHigh := &staticMetricSource{cpuPercent: 90, memPercent: 0}
	calcHigh := NewImpedanceCalculator(m, WithMetricSource(srcHigh))
	impHigh := calcHigh.Calculate()

	if impHigh <= impLow {
		t.Fatalf("higher CPU should increase impedance: low=%f, high=%f", impLow, impHigh)
	}
}

// --- Memory pressure increases impedance ---

func TestCalculator_MemoryPressure(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")

	srcLow := &staticMetricSource{cpuPercent: 0, memPercent: 10}
	calcLow := NewImpedanceCalculator(m, WithMetricSource(srcLow))
	impLow := calcLow.Calculate()

	srcHigh := &staticMetricSource{cpuPercent: 0, memPercent: 90}
	calcHigh := NewImpedanceCalculator(m, WithMetricSource(srcHigh))
	impHigh := calcHigh.Calculate()

	if impHigh <= impLow {
		t.Fatalf("higher memory should increase impedance: low=%f, high=%f", impLow, impHigh)
	}
}

// --- Update pushes impedance to manifest ---

func TestCalculator_Update(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	src := &staticMetricSource{cpuPercent: 50, memPercent: 20}
	calc := NewImpedanceCalculator(m, WithMetricSource(src))

	calc.Update()

	imp := m.Impedance()
	if imp == MinImpedance {
		t.Fatal("manifest impedance should have been updated from default")
	}
	if imp < MinImpedance || imp > MaxImpedance {
		t.Fatalf("manifest impedance %f out of range", imp)
	}
}

// --- Concurrent Update is safe ---

func TestCalculator_ConcurrentUpdate(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	src := &staticMetricSource{cpuPercent: 25, memPercent: 25}
	calc := NewImpedanceCalculator(m, WithMetricSource(src))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			calc.Update()
			_ = calc.Calculate()
		}()
	}
	wg.Wait()
}

// --- AddStreams / RemoveStreams ---

func TestCalculator_StreamTracking(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	src := &staticMetricSource{cpuPercent: 0, memPercent: 0}
	calc := NewImpedanceCalculator(m, WithMetricSource(src))

	calc.AddStreams(5)
	imp1 := calc.Calculate()

	calc.AddStreams(50)
	imp2 := calc.Calculate()

	if imp2 <= imp1 {
		t.Fatalf("adding streams should increase impedance: %f → %f", imp1, imp2)
	}

	calc.RemoveStreams(50)
	imp3 := calc.Calculate()

	if imp3 >= imp2 {
		t.Fatalf("removing streams should decrease impedance: %f → %f", imp2, imp3)
	}
}

func TestCalculator_RemoveStreams_FloorAtZero(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	src := &staticMetricSource{}
	calc := NewImpedanceCalculator(m, WithMetricSource(src))

	calc.AddStreams(3)
	calc.RemoveStreams(10) // should not go negative

	imp := calc.Calculate()
	if imp < MinImpedance {
		t.Fatalf("impedance %f should not go below minimum", imp)
	}
}

// --- Start/Stop periodic sampling ---

func TestCalculator_StartStop(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	var callCount atomic.Int64
	src := &countingMetricSource{count: &callCount}
	calc := NewImpedanceCalculator(m, WithMetricSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	calc.Start(ctx, 10*time.Millisecond) // fast interval for testing

	// Wait for some samples.
	time.Sleep(60 * time.Millisecond)
	cancel()

	count := callCount.Load()
	if count < 3 {
		t.Fatalf("expected at least 3 samples, got %d", count)
	}

	// Verify manifest was updated.
	if m.Impedance() == MinImpedance {
		t.Fatal("manifest should have been updated by periodic sampling")
	}
}

// --- Test helpers ---

// staticMetricSource returns fixed metric values (for deterministic tests).
type staticMetricSource struct {
	cpuPercent float64
	memPercent float64
}

func (s *staticMetricSource) CPUPercent() float64    { return s.cpuPercent }
func (s *staticMetricSource) MemoryPercent() float64 { return s.memPercent }

// countingMetricSource counts how many times it's been sampled.
type countingMetricSource struct {
	count *atomic.Int64
}

func (c *countingMetricSource) CPUPercent() float64 {
	c.count.Add(1)
	return 25.0 // Non-zero to ensure impedance changes from default.
}

func (c *countingMetricSource) MemoryPercent() float64 {
	return 10.0
}
