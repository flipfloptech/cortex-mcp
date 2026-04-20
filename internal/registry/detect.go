package registry

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// Platform detection helpers for tool IsSupported() implementations.
// These are composable building blocks — tools combine them to express
// their platform requirements.

// HasBinary checks if a binary is available in PATH.
func HasBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// IsLinux returns true if the runtime OS is linux.
func IsLinux() bool {
	return runtime.GOOS == "linux"
}

// PathExists checks if a filesystem path exists.
func PathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// osReleaseCache stores parsed /etc/os-release content.
var (
	osReleaseOnce sync.Once
	osReleaseData map[string]string
)

// ReadOSRelease parses /etc/os-release and returns key-value pairs.
// Results are cached after the first call. Returns an empty map on
// non-Linux systems or if the file cannot be read.
func ReadOSRelease() map[string]string {
	osReleaseOnce.Do(func() {
		osReleaseData = make(map[string]string)
		data, err := os.ReadFile("/etc/os-release")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := parts[0]
			val := strings.Trim(parts[1], "\"")
			osReleaseData[key] = val
		}
	})
	return osReleaseData
}

// IsDistro checks if the running OS matches any of the given distro identifiers.
// Matches against the ID field in /etc/os-release (e.g., "rocky", "ubuntu", "centos").
// Case-insensitive.
func IsDistro(names ...string) bool {
	release := ReadOSRelease()
	id := strings.ToLower(release["ID"])
	for _, name := range names {
		if strings.ToLower(name) == id {
			return true
		}
	}
	return false
}

// IsSFAController detects if the running node is an SFA controller by checking
// for the presence of proprietary SFA device drivers in sysfs.
func IsSFAController() bool {
	return PathExists("/sys/module/jsysdd") ||
		PathExists("/sys/class/jsys") ||
		PathExists("/sys/module/jnvme") ||
		PathExists("/sys/class/jnvme")
}

// LustreNodeInfo describes the Lustre roles active on this node.
// A single server can have multiple roles (e.g., both MDTs and OSTs).
type LustreNodeInfo struct {
	// IsMGS is true if this node has an active Management Server (MGS).
	IsMGS bool `json:"is_mgs"`

	// IsMDS is true if this node has active Metadata Targets (MDTs).
	IsMDS bool `json:"is_mds"`

	// IsOSS is true if this node has active Object Storage Targets (OSTs).
	IsOSS bool `json:"is_oss"`

	// IsClient is true if this node has mounted Lustre filesystems.
	IsClient bool `json:"is_client"`

	// MGSs lists the names of active MGS instances (usually just ["MGS"]).
	MGSs []string `json:"mgss,omitempty"`

	// MDTs lists the names of active MDTs (e.g., ["myfs-MDT0000"]).
	MDTs []string `json:"mdts,omitempty"`

	// OSTs lists the names of active OSTs (e.g., ["myfs-OST0000", "myfs-OST0001"]).
	OSTs []string `json:"osts,omitempty"`

	// Clients lists mounted Lustre filesystem names (e.g., ["myfs"]).
	Clients []string `json:"clients,omitempty"`
}

// HasLustre returns true if any Lustre role is detected.
func (l *LustreNodeInfo) HasLustre() bool {
	return l.IsMGS || l.IsMDS || l.IsOSS || l.IsClient
}

// Roles returns a human-readable list of active roles (e.g., ["mgs", "mds", "oss"]).
func (l *LustreNodeInfo) Roles() []string {
	var roles []string
	if l.IsMGS {
		roles = append(roles, "mgs")
	}
	if l.IsMDS {
		roles = append(roles, "mds")
	}
	if l.IsOSS {
		roles = append(roles, "oss")
	}
	if l.IsClient {
		roles = append(roles, "client")
	}
	return roles
}

// DetectLustreNodeType inspects /sys/fs/lustre/ to determine which Lustre
// roles are active on this node. A single server can serve multiple roles
// simultaneously (e.g., MGS + MDS, or MDS + OSS).
//
// Detection is based on the presence of active subdirectories:
//   - /sys/fs/lustre/mgs/       → MGS instance
//   - /sys/fs/lustre/mdt/       → MDT (MDS role)
//   - /sys/fs/lustre/obdfilter/ → OST (OSS role)
//   - /sys/fs/lustre/llite/     → mounted client
//
// Each subdirectory under these paths represents an active target/mount.
func DetectLustreNodeType() *LustreNodeInfo {
	info := &LustreNodeInfo{}

	info.MGSs = listSubdirs("/sys/fs/lustre/mgs")
	info.IsMGS = len(info.MGSs) > 0

	info.MDTs = listSubdirs("/sys/fs/lustre/mdt")
	info.IsMDS = len(info.MDTs) > 0

	info.OSTs = listSubdirs("/sys/fs/lustre/obdfilter")
	info.IsOSS = len(info.OSTs) > 0

	info.Clients = listSubdirs("/sys/fs/lustre/llite")
	info.IsClient = len(info.Clients) > 0

	return info
}

// listSubdirs returns the names of immediate subdirectories under path.
// Returns nil if the path doesn't exist or can't be read.
func listSubdirs(path string) []string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}
