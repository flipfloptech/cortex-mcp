// Package stream provides shared streaming data processing primitives
// that tool plugins compose. These are the building blocks for tools
// that process high-volume data efficiently.
//
// All primitives are designed for single-goroutine use within a tool's
// Execute method. For concurrent access, the caller must synchronize.
package stream
