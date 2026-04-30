package storage

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var statfsFunc = unix.Statfs
var defaultStatfsTimeout = 2000 * time.Millisecond

type MountCapacity struct {
	TotalGiB float64 `json:"total_gib"`
	UsedGiB  float64 `json:"used_gib"`
	FreeGiB  float64 `json:"free_gib"`
	UsagePct float64 `json:"usage_pct"`
}

type MountInodes struct {
	Total    uint64  `json:"total"`
	Used     uint64  `json:"used"`
	Free     uint64  `json:"free"`
	UsagePct float64 `json:"usage_pct"`
}

type MountStats struct {
	MountPoint string        `json:"mount_point"`
	Filesystem string        `json:"filesystem"`
	Device     string        `json:"device"`
	Status     string        `json:"status"`
	Error      string        `json:"error,omitempty"`
	Capacity   MountCapacity `json:"capacity,omitempty"`
	Inodes     MountInodes   `json:"inodes,omitempty"`
}

func GetMountStats(ctx context.Context, procBase string) ([]MountStats, error) {
	mounts := parseMounts(procBase)
	var stats []MountStats

	for _, m := range mounts {
		if !shouldKeepMount(m) {
			continue
		}

		stat := MountStats{
			MountPoint: m.MountPoint,
			Filesystem: m.Filesystem,
			Device:     m.Device,
			Status:     "ok",
		}

		err := statfsWithTimeout(ctx, m.MountPoint, &stat)
		if err != nil {
			stat.Status = "hung"
			stat.Error = err.Error()
		}

		stats = append(stats, stat)
	}

	return stats, nil
}

type mountEntry struct {
	MountPoint string
	Filesystem string
	Device     string
}

func parseMounts(procBase string) []mountEntry {
	path := filepath.Join(procBase, "self/mountinfo")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var entries []mountEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}

		mountPoint := fields[4]

		// Find the separator "-" to get filesystem type and source.
		sepIdx := -1
		for i, f := range fields {
			if f == "-" {
				sepIdx = i
				break
			}
		}

		if sepIdx == -1 || sepIdx+2 >= len(fields) {
			continue
		}

		fsType := fields[sepIdx+1]
		device := fields[sepIdx+2]

		entries = append(entries, mountEntry{
			MountPoint: mountPoint,
			Filesystem: fsType,
			Device:     device,
		})
	}

	return entries
}

func shouldKeepMount(m mountEntry) bool {
	// Rule 3 (Blacklist): Explicitly discard tmpfs and devtmpfs
	if m.Filesystem == "tmpfs" || m.Filesystem == "devtmpfs" {
		return false
	}

	// Rule 1 (Block Devices): Keep if source starts with /dev/
	if strings.HasPrefix(m.Device, "/dev/") {
		return true
	}

	// Rule 2 (Network/Fabric): Keep if source contains : or @
	if strings.Contains(m.Device, ":") || strings.Contains(m.Device, "@") {
		return true
	}

	return false
}

func statfsWithTimeout(ctx context.Context, path string, stat *MountStats) error {
	type result struct {
		buf unix.Statfs_t
		err error
	}

	ch := make(chan result, 1)

	fn := statfsFunc
	go func() {
		var buf unix.Statfs_t
		err := fn(path, &buf)
		ch <- result{buf: buf, err: err}
	}()

	timeout := defaultStatfsTimeout

	select {
	case res := <-ch:
		if res.err != nil {
			return fmt.Errorf("statfs error: %v", res.err)
		}
		populateCapacity(stat, res.buf)
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("statfs timed out after %dms", timeout.Milliseconds())
	case <-ctx.Done():
		return ctx.Err()
	}
}

func populateCapacity(stat *MountStats, buf unix.Statfs_t) {
	bsize := float64(buf.Bsize)

	totalBytes := float64(buf.Blocks) * bsize
	freeBytes := float64(buf.Bavail) * bsize
	usedBytes := totalBytes - freeBytes

	stat.Capacity.TotalGiB = roundGiB(totalBytes)
	stat.Capacity.FreeGiB = roundGiB(freeBytes)
	stat.Capacity.UsedGiB = roundGiB(usedBytes)

	if totalBytes > 0 {
		pct := (usedBytes / totalBytes) * 100.0
		stat.Capacity.UsagePct = math.Round(pct*10) / 10
	}

	stat.Inodes.Total = buf.Files
	stat.Inodes.Free = buf.Ffree
	if buf.Files > buf.Ffree {
		stat.Inodes.Used = buf.Files - buf.Ffree
	}

	if buf.Files > 0 {
		pct := float64(stat.Inodes.Used) / float64(buf.Files) * 100.0
		stat.Inodes.UsagePct = math.Round(pct*10) / 10
	}
}

func roundGiB(bytes float64) float64 {
	gib := bytes / (1024.0 * 1024.0 * 1024.0)
	return math.Round(gib*10) / 10
}
