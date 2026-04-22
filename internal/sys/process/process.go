package process

import (
	"context"
	"regexp"
	"time"
)

type Process struct {
	PID         int     `json:"pid"`
	UID         int     `json:"uid"`
	User        string  `json:"user"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	Cmdline     *string `json:"cmdline"` // Nullable if unreadable
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryBytes uint64  `json:"memory_bytes"`
}

type FilterOptions struct {
	SortBy         string // "cpu" or "memory"
	Limit          int
	SampleDuration time.Duration
	UserRegex      *regexp.Regexp
	NameRegex      *regexp.Regexp
	StateRegex     *regexp.Regexp
	CmdlineRegex   *regexp.Regexp
}

// GetList is the main entry point to gather processes.
func GetList(ctx context.Context, opts FilterOptions) ([]Process, error) {
	return nil, nil // stub
}
