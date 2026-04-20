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

// DetectLustreNodeType inspects /proc/fs/lustre/ to determine the Lustre
// node type. Returns "mds", "oss", "client", or "" if Lustre is not present.
func DetectLustreNodeType() string {
	if !PathExists("/proc/fs/lustre") {
		return ""
	}
	if PathExists("/proc/fs/lustre/mdt") {
		return "mds"
	}
	if PathExists("/proc/fs/lustre/obdfilter") || PathExists("/proc/fs/lustre/osd-ldiskfs") {
		return "oss"
	}
	if PathExists("/proc/fs/lustre/llite") {
		return "client"
	}
	return ""
}
