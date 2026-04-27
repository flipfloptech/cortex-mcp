package nodeset

import (
	"testing"
)

// --- Fold basic cases ---

func TestFold_SingleNode(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"node1"})
	if got != "node1" {
		t.Fatalf("Fold() = %q, want %q", got, "node1")
	}
}

func TestFold_SimpleRange(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"node1", "node2", "node3"})
	if got != "node[1-3]" {
		t.Fatalf("Fold() = %q, want %q", got, "node[1-3]")
	}
}

func TestFold_PaddedRange(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"node01", "node02", "node03"})
	if got != "node[01-03]" {
		t.Fatalf("Fold() = %q, want %q", got, "node[01-03]")
	}
}

func TestFold_DisjointRange(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"node1", "node2", "node5", "node6"})
	if got != "node[1-2,5-6]" {
		t.Fatalf("Fold() = %q, want %q", got, "node[1-2,5-6]")
	}
}

func TestFold_MixedPrefixes(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"mds1", "mds2", "oss1"})
	want := "mds[1-2],oss1"
	if got != want {
		t.Fatalf("Fold() = %q, want %q", got, want)
	}
}

func TestFold_NoCommonSuffix(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"alpha", "beta"})
	if got != "alpha,beta" {
		t.Fatalf("Fold() = %q, want %q", got, "alpha,beta")
	}
}

func TestFold_Autostep(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"node1", "node3", "node5"})
	if got != "node[1-5/2]" {
		t.Fatalf("Fold() = %q, want %q", got, "node[1-5/2]")
	}
}

func TestFold_Empty(t *testing.T) {
	t.Parallel()
	got := Fold(nil)
	if got != "" {
		t.Fatalf("Fold() = %q, want empty", got)
	}
}

func TestFold_SingleNonNumeric(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"gateway"})
	if got != "gateway" {
		t.Fatalf("Fold() = %q, want %q", got, "gateway")
	}
}

func TestFold_MixedPrefixesSorted(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"oss1", "oss2", "oss3", "mds1", "mds2"})
	want := "mds[1-2],oss[1-3]"
	if got != want {
		t.Fatalf("Fold() = %q, want %q", got, want)
	}
}

func TestFold_SingleWithSuffix(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"node5"})
	if got != "node5" {
		t.Fatalf("Fold() = %q, want %q", got, "node5")
	}
}

func TestFold_RealWorldHPC(t *testing.T) {
	t.Parallel()
	nodes := []string{
		"memp-aqr-mvm00", "memp-aqr-mvm01",
		"memp-aqr-mvm02", "memp-aqr-mvm03",
		"memp-aqs-oss24",
	}
	got := Fold(nodes)
	want := "memp-aqr-mvm[00-03],memp-aqs-oss24"
	if got != want {
		t.Fatalf("Fold() = %q, want %q", got, want)
	}
}

func TestFold_WidePadding(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"srv001", "srv002", "srv003"})
	if got != "srv[001-003]" {
		t.Fatalf("Fold() = %q, want %q", got, "srv[001-003]")
	}
}

func TestFold_MixedSingleAndRange(t *testing.T) {
	t.Parallel()
	got := Fold([]string{"node1", "node2", "node3", "node5"})
	if got != "node[1-3,5]" {
		t.Fatalf("Fold() = %q, want %q", got, "node[1-3,5]")
	}
}
