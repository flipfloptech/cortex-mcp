package stream

// ThresholdEmitter annotates streaming records that cross a threshold.
// Unlike Top-K, nothing is discarded — every record passes through,
// but those exceeding the threshold are marked with a severity level.
//
// Usage:
//
//	te := NewThresholdEmitter(90.0, 95.0) // warn at 90%, critical at 95%
//	for _, record := range stream {
//	    annotated := te.Evaluate(record.Value)
//	    // annotated.Severity is "ok", "warning", or "critical"
//	}
type ThresholdEmitter struct {
	warnThreshold     float64
	criticalThreshold float64
	totalSeen         int
	totalWarning      int
	totalCritical     int
}

// ThresholdResult is a value annotated with severity after threshold evaluation.
type ThresholdResult struct {
	Value    float64  `json:"value"`
	Severity Severity `json:"severity"`
}

// Severity classifies how a value relates to thresholds.
type Severity string

const (
	SeverityOK       Severity = "ok"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// NewThresholdEmitter creates a threshold emitter.
// Values >= warnThreshold are "warning", values >= criticalThreshold are "critical".
// warnThreshold must be <= criticalThreshold.
func NewThresholdEmitter(warnThreshold, criticalThreshold float64) *ThresholdEmitter {
	if warnThreshold > criticalThreshold {
		warnThreshold, criticalThreshold = criticalThreshold, warnThreshold
	}
	return &ThresholdEmitter{
		warnThreshold:     warnThreshold,
		criticalThreshold: criticalThreshold,
	}
}

// Evaluate annotates a single value with its severity classification.
func (te *ThresholdEmitter) Evaluate(value float64) ThresholdResult {
	te.totalSeen++

	var sev Severity
	switch {
	case value >= te.criticalThreshold:
		sev = SeverityCritical
		te.totalCritical++
	case value >= te.warnThreshold:
		sev = SeverityWarning
		te.totalWarning++
	default:
		sev = SeverityOK
	}

	return ThresholdResult{Value: value, Severity: sev}
}

// Stats returns aggregate counts of evaluated values.
func (te *ThresholdEmitter) Stats() ThresholdStats {
	return ThresholdStats{
		TotalSeen:     te.totalSeen,
		TotalWarning:  te.totalWarning,
		TotalCritical: te.totalCritical,
		TotalOK:       te.totalSeen - te.totalWarning - te.totalCritical,
	}
}

// ThresholdStats provides aggregate counts from threshold evaluation.
type ThresholdStats struct {
	TotalSeen     int `json:"total_seen"`
	TotalOK       int `json:"total_ok"`
	TotalWarning  int `json:"total_warning"`
	TotalCritical int `json:"total_critical"`
}
