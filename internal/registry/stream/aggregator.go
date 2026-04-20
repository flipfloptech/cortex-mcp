package stream

// Aggregator performs on-the-fly group-by aggregation on a stream of records.
// Records are assigned to groups by a key function, and running statistics
// (count, sum, min, max) are maintained per group.
//
// Memory is bounded by maxGroups — when exceeded, the least-seen group
// is evicted to make room.
//
// Usage:
//
//	agg := NewAggregator(1000) // max 1000 groups
//	agg.Add("oss1", 42.0)
//	agg.Add("oss2", 17.0)
//	agg.Add("oss1", 58.0)
//	stats := agg.Get("oss1") // count=2, sum=100, min=42, max=58, avg=50
type Aggregator struct {
	groups    map[string]*GroupStats
	order     []string // insertion order for eviction
	maxGroups int
}

// GroupStats holds running statistics for a single group.
type GroupStats struct {
	Count int     `json:"count"`
	Sum   float64 `json:"sum"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}

// Avg returns the arithmetic mean of all values in this group.
func (gs *GroupStats) Avg() float64 {
	if gs.Count == 0 {
		return 0
	}
	return gs.Sum / float64(gs.Count)
}

// NewAggregator creates a group-by aggregator with bounded group count.
// Panics if maxGroups <= 0.
func NewAggregator(maxGroups int) *Aggregator {
	if maxGroups <= 0 {
		panic("stream: Aggregator maxGroups must be > 0")
	}
	return &Aggregator{
		groups:    make(map[string]*GroupStats, maxGroups),
		order:     make([]string, 0, maxGroups),
		maxGroups: maxGroups,
	}
}

// Add records a value under the given group key.
func (a *Aggregator) Add(key string, value float64) {
	gs, ok := a.groups[key]
	if !ok {
		// Evict oldest group if at capacity.
		if len(a.groups) >= a.maxGroups {
			oldest := a.order[0]
			a.order = a.order[1:]
			delete(a.groups, oldest)
		}
		gs = &GroupStats{Min: value, Max: value}
		a.groups[key] = gs
		a.order = append(a.order, key)
	}

	gs.Count++
	gs.Sum += value
	if value < gs.Min {
		gs.Min = value
	}
	if value > gs.Max {
		gs.Max = value
	}
}

// Get returns the statistics for a group. Returns nil if the group
// doesn't exist or was evicted.
func (a *Aggregator) Get(key string) *GroupStats {
	return a.groups[key]
}

// Groups returns the number of active groups.
func (a *Aggregator) Groups() int {
	return len(a.groups)
}

// All returns a snapshot of all group statistics.
func (a *Aggregator) All() map[string]*GroupStats {
	out := make(map[string]*GroupStats, len(a.groups))
	for k, v := range a.groups {
		cp := *v
		out[k] = &cp
	}
	return out
}
