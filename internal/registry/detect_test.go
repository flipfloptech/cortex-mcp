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
// returns an empty LustreNodeInfo on non-Lustre hosts.
func TestDetectLustreNodeType_NoLustre(t *testing.T) {
	t.Parallel()
	// On a dev machine without Lustre, all roles should be false.
	if !registry.PathExists("/sys/fs/lustre") {
		info := registry.DetectLustreNodeType()
		if info.HasLustre() {
			t.Errorf("HasLustre() = true on non-Lustre host, roles: %v", info.Roles())
		}
		if info.IsMGS || info.IsMDS || info.IsOSS || info.IsClient {
			t.Error("no role flags should be set on non-Lustre host")
		}
		if len(info.MGSs) > 0 || len(info.MDTs) > 0 || len(info.OSTs) > 0 || len(info.Clients) > 0 {
			t.Error("no targets should be listed on non-Lustre host")
		}
	}
}

// TestLustreNodeInfo_Roles verifies the Roles() method.
func TestLustreNodeInfo_Roles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info registry.LustreNodeInfo
		want []string
	}{
		{"empty", registry.LustreNodeInfo{}, nil},
		{"mgs only", registry.LustreNodeInfo{IsMGS: true}, []string{"mgs"}},
		{"mds only", registry.LustreNodeInfo{IsMDS: true}, []string{"mds"}},
		{"oss only", registry.LustreNodeInfo{IsOSS: true}, []string{"oss"}},
		{"client only", registry.LustreNodeInfo{IsClient: true}, []string{"client"}},
		{"dual mgs+mds", registry.LustreNodeInfo{IsMGS: true, IsMDS: true}, []string{"mgs", "mds"}},
		{"dual mds+oss", registry.LustreNodeInfo{IsMDS: true, IsOSS: true}, []string{"mds", "oss"}},
		{"hyperconverged", registry.LustreNodeInfo{IsMGS: true, IsMDS: true, IsOSS: true}, []string{"mgs", "mds", "oss"}},
		{"all roles", registry.LustreNodeInfo{IsMGS: true, IsMDS: true, IsOSS: true, IsClient: true}, []string{"mgs", "mds", "oss", "client"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			roles := tt.info.Roles()
			if len(roles) != len(tt.want) {
				t.Errorf("Roles() = %v, want %v", roles, tt.want)
				return
			}
			for i, r := range roles {
				if r != tt.want[i] {
					t.Errorf("Roles()[%d] = %q, want %q", i, r, tt.want[i])
				}
			}
		})
	}
}

// TestLustreNodeInfo_HasLustre verifies the HasLustre() method.
func TestLustreNodeInfo_HasLustre(t *testing.T) {
	t.Parallel()

	empty := registry.LustreNodeInfo{}
	if empty.HasLustre() {
		t.Error("empty LustreNodeInfo should return HasLustre=false")
	}

	mgs := registry.LustreNodeInfo{IsMGS: true}
	if !mgs.HasLustre() {
		t.Error("MGS node should return HasLustre=true")
	}

	mds := registry.LustreNodeInfo{IsMDS: true}
	if !mds.HasLustre() {
		t.Error("MDS node should return HasLustre=true")
	}

	dual := registry.LustreNodeInfo{IsMGS: true, IsMDS: true}
	if !dual.HasLustre() {
		t.Error("dual-role node should return HasLustre=true")
	}
}
