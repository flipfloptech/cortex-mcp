package registry_test

import (
	"runtime"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// TestHasBinary_Exists verifies that HasBinary finds a binary that exists.
func TestHasBinary_Exists(t *testing.T) {
	t.Parallel()
	// "go" should always be in PATH during tests.
	if !registry.HasBinary("go") {
		t.Error("HasBinary(\"go\") = false, want true")
	}
}

// TestHasBinary_NotExists verifies that HasBinary returns false for
// a binary that does not exist.
func TestHasBinary_NotExists(t *testing.T) {
	t.Parallel()
	if registry.HasBinary("__nonexistent_binary_xyz__") {
		t.Error("HasBinary(\"__nonexistent_binary_xyz__\") = true, want false")
	}
}

// TestIsLinux verifies that IsLinux returns true on Linux.
func TestIsLinux(t *testing.T) {
	t.Parallel()
	got := registry.IsLinux()
	want := runtime.GOOS == "linux"
	if got != want {
		t.Errorf("IsLinux() = %v, want %v (GOOS=%s)", got, want, runtime.GOOS)
	}
}

// TestPathExists_Exists verifies that PathExists returns true for known paths.
func TestPathExists_Exists(t *testing.T) {
	t.Parallel()
	// /etc should always exist on Linux.
	if runtime.GOOS == "linux" && !registry.PathExists("/etc") {
		t.Error("PathExists(\"/etc\") = false, want true")
	}
}

// TestPathExists_NotExists verifies that PathExists returns false for
// nonexistent paths.
func TestPathExists_NotExists(t *testing.T) {
	t.Parallel()
	if registry.PathExists("/nonexistent_path_xyz_12345") {
		t.Error("PathExists returned true for nonexistent path")
	}
}

// TestIsDistro_NoMatch verifies that IsDistro returns false when
// the distro doesn't match.
func TestIsDistro_NoMatch(t *testing.T) {
	t.Parallel()
	if registry.IsDistro("__fake_distro__") {
		t.Error("IsDistro(\"__fake_distro__\") = true, want false")
	}
}

// TestReadOSRelease_ReturnsMap verifies that ReadOSRelease returns
// a non-nil map (even if empty on non-Linux).
func TestReadOSRelease_ReturnsMap(t *testing.T) {
	t.Parallel()
	release := registry.ReadOSRelease()
	if release == nil {
		t.Error("ReadOSRelease() returned nil, want non-nil map")
	}
	// On Linux, ID should be populated.
	if runtime.GOOS == "linux" {
		if _, ok := release["ID"]; !ok {
			t.Error("ReadOSRelease() missing ID key on Linux")
		}
	}
}

// TestDetectLustreNodeType_NoLustre verifies that DetectLustreNodeType
// returns empty string when /proc/fs/lustre doesn't exist.
func TestDetectLustreNodeType_NoLustre(t *testing.T) {
	t.Parallel()
	// On a dev machine without Lustre, this should return "".
	if !registry.PathExists("/proc/fs/lustre") {
		nodeType := registry.DetectLustreNodeType()
		if nodeType != "" {
			t.Errorf("DetectLustreNodeType() = %q, want empty on non-Lustre host", nodeType)
		}
	}
}
