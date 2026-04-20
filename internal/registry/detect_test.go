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

// TestDetectNodeRoles_Generic verifies that DetectNodeRoles
// returns a generic NodeRoleInfo on a non-specialized host.
func TestDetectNodeRoles_Generic(t *testing.T) {
	t.Parallel()
	// On a dev machine without Lustre or SFA drivers, all storage roles should be false.
	if !registry.PathExists("/sys/fs/lustre") && !registry.PathExists("/sys/module/jsysdd") {
		info := registry.DetectNodeRoles()
		if info.HasStorageRole() {
			t.Errorf("HasStorageRole() = true on generic host, roles: %v", info.Roles())
		}
		if info.IsSFA || info.IsMGS || info.IsMDS || info.IsOSS || info.IsClient {
			t.Error("no specialized role flags should be set on generic host")
		}
		roles := info.Roles()
		if len(roles) != 1 || roles[0] != "generic" {
			t.Errorf("Roles() on generic host should be ['generic'], got %v", roles)
		}
	}
}

// TestNodeRoleInfo_Roles verifies the Roles() method.
func TestNodeRoleInfo_Roles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info registry.NodeRoleInfo
		want []string
	}{
		{"generic", registry.NodeRoleInfo{}, []string{"generic"}},
		{"sfa only", registry.NodeRoleInfo{IsSFA: true}, []string{"sfa"}},
		{"mgs only", registry.NodeRoleInfo{IsMGS: true}, []string{"mgs"}},
		{"mds only", registry.NodeRoleInfo{IsMDS: true}, []string{"mds"}},
		{"oss only", registry.NodeRoleInfo{IsOSS: true}, []string{"oss"}},
		{"client only", registry.NodeRoleInfo{IsClient: true}, []string{"client"}},
		{"dual sfa+mgs", registry.NodeRoleInfo{IsSFA: true, IsMGS: true}, []string{"sfa", "mgs"}},
		{"dual mgs+mds", registry.NodeRoleInfo{IsMGS: true, IsMDS: true}, []string{"mgs", "mds"}},
		{"dual mds+oss", registry.NodeRoleInfo{IsMDS: true, IsOSS: true}, []string{"mds", "oss"}},
		{"hyperconverged", registry.NodeRoleInfo{IsMGS: true, IsMDS: true, IsOSS: true}, []string{"mgs", "mds", "oss"}},
		{"sfa hyperconverged", registry.NodeRoleInfo{IsSFA: true, IsMGS: true, IsMDS: true, IsOSS: true}, []string{"sfa", "mgs", "mds", "oss"}},
		{"all roles", registry.NodeRoleInfo{IsSFA: true, IsMGS: true, IsMDS: true, IsOSS: true, IsClient: true}, []string{"sfa", "mgs", "mds", "oss", "client"}},
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

// TestNodeRoleInfo_HasStorageRole verifies the HasStorageRole() method.
func TestNodeRoleInfo_HasStorageRole(t *testing.T) {
	t.Parallel()

	generic := registry.NodeRoleInfo{}
	if generic.HasStorageRole() {
		t.Error("empty NodeRoleInfo should return HasStorageRole=false")
	}

	sfa := registry.NodeRoleInfo{IsSFA: true}
	if !sfa.HasStorageRole() {
		t.Error("SFA node should return HasStorageRole=true")
	}

	mgs := registry.NodeRoleInfo{IsMGS: true}
	if !mgs.HasStorageRole() {
		t.Error("MGS node should return HasStorageRole=true")
	}

	mds := registry.NodeRoleInfo{IsMDS: true}
	if !mds.HasStorageRole() {
		t.Error("MDS node should return HasStorageRole=true")
	}

	dual := registry.NodeRoleInfo{IsMGS: true, IsMDS: true}
	if !dual.HasStorageRole() {
		t.Error("dual-role node should return HasStorageRole=true")
	}
}
