package process

import (
	"context"
	"fmt"
	"time"
)

type ProcessTreeNode struct {
	PID         int                `json:"pid"`
	PPID        int                `json:"ppid"`
	UID         int                `json:"uid"`
	User        string             `json:"user"`
	Name        string             `json:"name"`
	State       string             `json:"state"`
	Cmdline     *string            `json:"cmdline"`
	CPUPercent  float64            `json:"cpu_percent"`
	MemoryBytes uint64             `json:"memory_bytes"`
	Children    []*ProcessTreeNode `json:"children,omitempty"`
}

// GetTree returns a tree of processes containing the targetPID, its ancestors, and all its descendants.
func GetTree(ctx context.Context, targetPID int) (*ProcessTreeNode, error) {
	uptime1, err := readUptime()
	if err != nil {
		return nil, fmt.Errorf("failed to read uptime: %w", err)
	}

	pids := getPIDs()
	stats1 := make(map[int]procStat, len(pids))
	for _, pid := range pids {
		if stat, err := readStat(pid); err == nil {
			stats1[pid] = stat
		}
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}

	uptime2, err := readUptime()
	if err != nil {
		return nil, fmt.Errorf("failed to read uptime: %w", err)
	}

	deltaUptime := uptime2 - uptime1
	if deltaUptime <= 0 {
		deltaUptime = 0.001
	}

	nodes := make(map[int]*ProcessTreeNode)
	userCache := make(map[int]string)

	for pid, stat1 := range stats1 {
		stat2, err := readStat(pid)
		if err != nil {
			continue // Process likely died
		}

		uid, vmRSS, err := readStatus(pid)
		if err != nil {
			continue
		}

		processTimeDelta := float64((stat2.utime - stat1.utime) + (stat2.stime - stat1.stime))
		cpuPercent := 100.0 * (processTimeDelta / userHZ) / deltaUptime

		username := getUsername(uid, userCache)

		cmdlineStr, err := readCmdline(pid)
		var cmdline *string
		if err == nil {
			cmdline = &cmdlineStr
		}

		nodes[pid] = &ProcessTreeNode{
			PID:         pid,
			PPID:        stat2.ppid,
			UID:         uid,
			User:        username,
			Name:        stat2.name,
			State:       stat2.state,
			Cmdline:     cmdline,
			CPUPercent:  cpuPercent,
			MemoryBytes: vmRSS,
			Children:    make([]*ProcessTreeNode, 0),
		}
	}

	// Link children to parents
	for _, node := range nodes {
		if node.PID == 1 || node.PPID == 0 {
			continue // PID 1 has no parent
		}
		if parent, ok := nodes[node.PPID]; ok {
			parent.Children = append(parent.Children, node)
		}
	}

	// Make sure the targetPID exists
	targetNode, ok := nodes[targetPID]
	if !ok {
		return nil, fmt.Errorf("target PID %d not found", targetPID)
	}

	// Traverse upwards from targetPID to find the root of the ancestry path
	pathNodes := make(map[int]bool)
	curr := targetPID
	for {
		pathNodes[curr] = true
		currNode, exists := nodes[curr]
		if !exists || currNode.PID == 1 || currNode.PPID == 0 {
			break
		}
		curr = currNode.PPID
	}

	// Prune branches:
	// - If the node IS the targetPID, keep all its children (and descendants).
	// - If the node is an ancestor of targetPID, keep ONLY the child that leads to targetPID.
	for pid := range pathNodes {
		if pid == targetPID {
			continue // Keep all children of the target
		}
		node := nodes[pid]

		var nextInLineage *ProcessTreeNode
		for _, child := range node.Children {
			if pathNodes[child.PID] {
				nextInLineage = child
				break
			}
		}

		if nextInLineage != nil {
			node.Children = []*ProcessTreeNode{nextInLineage}
		} else {
			node.Children = nil
		}
	}

	rootPID := curr
	rootNode := nodes[rootPID]
	if rootNode == nil {
		rootNode = targetNode
	}

	return rootNode, nil
}
