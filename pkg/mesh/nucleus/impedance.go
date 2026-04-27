package nucleus

import (
	"context"
	"sync/atomic"
	"time"
)

// MetricSource provides system metrics for impedance calculation.
// Implementations sample the underlying OS to report resource usage.
// A pluggable interface enables deterministic testing with mock sources.
type MetricSource interface {
	// CPUPercent returns the current CPU usage as a percentage [0, 100].
	CPUPercent() float64
	// MemoryPercent returns the current memory usage as a percentage [0, 100].
	MemoryPercent() float64
}

// Impedance weight distribution for the formula:
//
//	impedance = 1.0 + (cpu * cpuWeight + mem * memWeight + streams * streamWeight)
//
// Weights are chosen so that:
//   - CPU is the dominant factor (runs tools, forwards data)
//   - Memory pressure is secondary (swap thrashing kills performance)
//   - Stream count is a lighter signal (each stream is cheap individually)
const (
	cpuWeight    = 0.60 // 60% weight on CPU
	memWeight    = 0.25 // 25% weight on memory
	streamWeight = 0.15 // 15% weight on stream pressure
)

// maxStreamPressure is the stream count at which stream pressure
// contributes maximally to impedance. Above this, it saturates.
const maxStreamPressure = 500

// ImpedanceCalculator computes real-time impedance for a mesh node.
// It reads system metrics from a MetricSource and tracks active yamux
// streams to produce a composite impedance value in [1.0, 100.0].
//
// The calculator is goroutine-safe — Calculate, Update, AddStreams,
// and RemoveStreams can be called concurrently.
type ImpedanceCalculator struct {
	manifest *Manifest
	source   MetricSource
	streams  atomic.Int64
}

// ImpedanceOption configures an ImpedanceCalculator.
type ImpedanceOption func(*ImpedanceCalculator)

// WithMetricSource sets a custom MetricSource for the calculator.
// If not provided, a default system metric source is used.
func WithMetricSource(src MetricSource) ImpedanceOption {
	return func(c *ImpedanceCalculator) {
		c.source = src
	}
}

// NewImpedanceCalculator creates a new impedance calculator bound to
// the given manifest. The calculator reads metrics and pushes the
// computed impedance to the manifest via SetImpedance.
func NewImpedanceCalculator(m *Manifest, opts ...ImpedanceOption) *ImpedanceCalculator {
	c := &ImpedanceCalculator{
		manifest: m,
		source:   &defaultMetricSource{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Calculate computes the current impedance value from system metrics
// and active stream count. Returns a value in [1.0, 100.0].
//
// Formula:
//
//	impedance = 1.0 + 99.0 * (cpu*0.60 + mem*0.25 + streams*0.15)
//
// Where cpu, mem, and streams are each normalized to [0.0, 1.0].
func (c *ImpedanceCalculator) Calculate() float64 {
	// Sample system metrics.
	cpuNorm := clampNorm(c.source.CPUPercent() / 100.0)
	memNorm := clampNorm(c.source.MemoryPercent() / 100.0)

	// Normalize stream count: saturates at maxStreamPressure.
	streamCount := float64(c.streams.Load())
	if streamCount < 0 {
		streamCount = 0
	}
	streamNorm := streamCount / float64(maxStreamPressure)
	if streamNorm > 1.0 {
		streamNorm = 1.0
	}

	// Weighted combination.
	composite := cpuNorm*cpuWeight + memNorm*memWeight + streamNorm*streamWeight

	// Map to [1.0, 100.0].
	impedance := MinImpedance + composite*(MaxImpedance-MinImpedance)

	// Clamp (should be unnecessary, but defensive).
	if impedance < MinImpedance {
		impedance = MinImpedance
	}
	if impedance > MaxImpedance {
		impedance = MaxImpedance
	}

	return impedance
}

// Update calculates the current impedance and pushes it to the manifest.
func (c *ImpedanceCalculator) Update() {
	c.manifest.SetImpedance(c.Calculate())
}

// AddStreams increments the active stream count.
// Called when new yamux streams are opened.
func (c *ImpedanceCalculator) AddStreams(n int) {
	c.streams.Add(int64(n))
}

// RemoveStreams decrements the active stream count.
// Called when yamux streams are closed. Floors at zero.
func (c *ImpedanceCalculator) RemoveStreams(n int) {
	for {
		current := c.streams.Load()
		newVal := current - int64(n)
		if newVal < 0 {
			newVal = 0
		}
		if c.streams.CompareAndSwap(current, newVal) {
			return
		}
	}
}

// Start begins periodic impedance sampling in a background goroutine.
// The goroutine runs until the context is canceled.
// The interval controls how often metrics are sampled and the manifest
// is updated (typical: 1-3 seconds for production, 10ms for tests).
func (c *ImpedanceCalculator) Start(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.Update()
			}
		}
	}()
}

// clampNorm clamps a value to [0.0, 1.0].
func clampNorm(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1.0 {
		return 1.0
	}
	return v
}

// defaultMetricSource provides zero values when no custom source is configured.
// In production, this would sample /proc/stat and /proc/meminfo.
type defaultMetricSource struct{}

func (d *defaultMetricSource) CPUPercent() float64    { return 0 }
func (d *defaultMetricSource) MemoryPercent() float64 { return 0 }
