package nucleus

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// --- NewResolver ---

func TestNewResolver_EmptyCache(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	entries := r.AllEntries()
	if len(entries) != 0 {
		t.Fatalf("new resolver should have empty cache, got %d entries", len(entries))
	}
}

// --- AddEntry / Resolve ---

func TestResolver_AddAndResolve(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntry("mds-01", []string{"10.0.1.1", "10.0.1.2"}, 0) // TTL=0 means no expiry

	addrs := r.Resolve("mds-01")
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addresses, got %d", len(addrs))
	}
	if addrs[0] != "10.0.1.1" || addrs[1] != "10.0.1.2" {
		t.Fatalf("got %v, want [10.0.1.1, 10.0.1.2]", addrs)
	}
}

func TestResolver_Resolve_NotFound(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	addrs := r.Resolve("nonexistent")
	if len(addrs) != 0 {
		t.Fatalf("expected empty for unknown host, got %v", addrs)
	}
}

func TestResolver_Resolve_ReturnsCopy(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntry("mds-01", []string{"10.0.1.1"}, 0)

	addrs := r.Resolve("mds-01")
	addrs[0] = "INJECTED"

	// Internal state should be unaffected.
	fresh := r.Resolve("mds-01")
	if fresh[0] == "INJECTED" {
		t.Fatal("Resolve should return a copy, not reference internal state")
	}
}

// --- Seed from known hosts (config map) ---

func TestResolver_SeedKnownHosts(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.SeedKnownHosts(map[string][]string{
		"mds-01": {"10.0.1.1", "10.0.1.2"},
		"oss-01": {"10.0.2.1"},
	})

	addrs := r.Resolve("mds-01")
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addresses for mds-01, got %d", len(addrs))
	}

	addrs = r.Resolve("oss-01")
	if len(addrs) != 1 {
		t.Fatalf("expected 1 address for oss-01, got %d", len(addrs))
	}
}

// --- Parse /etc/hosts format ---

func TestResolver_ParseHostsFile(t *testing.T) {
	t.Parallel()

	hostsContent := `# /etc/hosts
127.0.0.1   localhost
::1         localhost ip6-localhost
10.0.1.1    mds-01 mds-01.internal
10.0.1.2    mds-02
10.0.2.1    oss-01 oss-01.prod.internal

# Empty lines and comments are ignored

10.0.3.1    sfa-01
`

	r := NewResolver()
	count, err := r.ParseHostsContent(strings.NewReader(hostsContent))
	if err != nil {
		t.Fatalf("ParseHostsContent: %v", err)
	}

	// Should have parsed entries (localhost, mds-01, mds-02, oss-01, sfa-01 + aliases).
	if count < 5 {
		t.Fatalf("expected at least 5 unique hosts parsed, got %d", count)
	}

	// mds-01 should resolve.
	addrs := r.Resolve("mds-01")
	if len(addrs) == 0 || addrs[0] != "10.0.1.1" {
		t.Fatalf("mds-01 expected [10.0.1.1], got %v", addrs)
	}

	// Aliases should also resolve.
	addrs = r.Resolve("mds-01.internal")
	if len(addrs) == 0 || addrs[0] != "10.0.1.1" {
		t.Fatalf("mds-01.internal expected [10.0.1.1], got %v", addrs)
	}
}

func TestResolver_ParseHostsFile_SkipsInvalidLines(t *testing.T) {
	t.Parallel()

	hostsContent := `# Comments only
   
invalid-line-no-ip
10.0.1.1    valid-host
`

	r := NewResolver()
	count, err := r.ParseHostsContent(strings.NewReader(hostsContent))
	if err != nil {
		t.Fatalf("ParseHostsContent: %v", err)
	}

	if count != 1 {
		t.Fatalf("expected 1 valid host, got %d", count)
	}
}

// --- Merge entries from another resolver ---

func TestResolver_Merge(t *testing.T) {
	t.Parallel()

	r1 := NewResolver()
	r1.AddEntry("mds-01", []string{"10.0.1.1"}, 0)

	r2 := NewResolver()
	r2.AddEntry("oss-01", []string{"10.0.2.1"}, 0)
	r2.AddEntry("mds-01", []string{"10.0.1.1", "10.0.1.3"}, 0) // Updated for mds-01

	r1.Merge(r2.AllEntries())

	// mds-01 should have the merged addresses.
	addrs := r1.Resolve("mds-01")
	if len(addrs) < 2 {
		t.Fatalf("expected merged addresses for mds-01, got %v", addrs)
	}

	// oss-01 should now be resolvable on r1.
	addrs = r1.Resolve("oss-01")
	if len(addrs) != 1 {
		t.Fatalf("expected oss-01 after merge, got %v", addrs)
	}
}

// --- TTL expiry ---

func TestResolver_TTL_Expiry(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntry("temp-host", []string{"10.0.9.1"}, 50*time.Millisecond)

	// Should resolve immediately.
	if addrs := r.Resolve("temp-host"); len(addrs) == 0 {
		t.Fatal("entry should resolve before TTL expiry")
	}

	// Wait for TTL to expire.
	time.Sleep(80 * time.Millisecond)

	// Should no longer resolve.
	if addrs := r.Resolve("temp-host"); len(addrs) != 0 {
		t.Fatalf("entry should be expired, got %v", addrs)
	}
}

func TestResolver_TTL_ZeroMeansNoExpiry(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntry("permanent-host", []string{"10.0.1.1"}, 0) // no expiry

	time.Sleep(20 * time.Millisecond)

	if addrs := r.Resolve("permanent-host"); len(addrs) == 0 {
		t.Fatal("zero-TTL entry should never expire")
	}
}

// --- Goroutine safety ---

func TestResolver_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntry("base-host", []string{"10.0.1.1"}, 0)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.AddEntry("host-concurrent", []string{"10.0.1.2"}, 0)
		}()
		go func() {
			defer wg.Done()
			_ = r.Resolve("base-host")
			_ = r.AllEntries()
		}()
	}
	wg.Wait()
}

// --- AllEntries ---

func TestResolver_AllEntries(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntry("mds-01", []string{"10.0.1.1"}, 0)
	r.AddEntry("oss-01", []string{"10.0.2.1"}, 0)

	entries := r.AllEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

// --- Transport type ---

func TestResolver_AddEntryWithTransport(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntryWithTransport("mds-01", []string{"10.0.1.1"}, 0, "ssh")

	entries := r.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Transport != "ssh" {
		t.Fatalf("Transport = %q, want %q", entries[0].Transport, "ssh")
	}
}

func TestResolver_AddEntry_DefaultTransportEmpty(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntry("mds-01", []string{"10.0.1.1"}, 0)

	entries := r.AllEntries()
	if entries[0].Transport != "" {
		t.Fatalf("default transport should be empty, got %q", entries[0].Transport)
	}
}

func TestResolver_Merge_PreservesTransport(t *testing.T) {
	t.Parallel()

	r1 := NewResolver()
	r2 := NewResolver()
	r2.AddEntryWithTransport("mds-01", []string{"10.0.1.1"}, 0, "https")

	r1.Merge(r2.AllEntries())

	entries := r1.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after merge, got %d", len(entries))
	}
	if entries[0].Transport != "https" {
		t.Fatalf("Transport = %q, want %q after merge", entries[0].Transport, "https")
	}
}

// --- AllDialTargets ---

func TestResolver_AllDialTargets(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntryWithTransport("mds-01", []string{"10.0.1.1"}, 0, "ssh")
	r.AddEntryWithTransport("oss-01", []string{"10.0.2.1", "10.0.2.2"}, 0, "https")

	targets := r.AllDialTargets()

	// Should have 3 targets total (1 for mds-01, 2 for oss-01).
	if len(targets) != 3 {
		t.Fatalf("expected 3 dial targets, got %d", len(targets))
	}

	// Verify structure.
	for _, dt := range targets {
		if dt.Hostname == "" {
			t.Fatal("dial target should have hostname")
		}
		if dt.Address == "" {
			t.Fatal("dial target should have address")
		}
	}
}

func TestResolver_AllDialTargets_Empty(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	targets := r.AllDialTargets()
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets from empty resolver, got %d", len(targets))
	}
}

func TestResolver_AllDialTargets_SkipsExpired(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntryWithTransport("temp", []string{"10.0.9.1"}, 30*time.Millisecond, "ssh")
	r.AddEntryWithTransport("perm", []string{"10.0.1.1"}, 0, "https")

	time.Sleep(50 * time.Millisecond)

	targets := r.AllDialTargets()
	if len(targets) != 1 {
		t.Fatalf("expected 1 target (expired skipped), got %d", len(targets))
	}
	if targets[0].Hostname != "perm" {
		t.Fatalf("surviving target should be 'perm', got %q", targets[0].Hostname)
	}
}

// --- Proxy URL ---

func TestResolver_AddEntryWithProxy(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntryFull("jumphost", []string{"10.0.1.1"}, 0, "https", "http://proxy.corp:8080")

	entries := r.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ProxyURL != "http://proxy.corp:8080" {
		t.Fatalf("ProxyURL = %q, want %q", entries[0].ProxyURL, "http://proxy.corp:8080")
	}
}

func TestResolver_Merge_ProxyURL_NonEmptyWins(t *testing.T) {
	t.Parallel()

	r1 := NewResolver()
	r1.AddEntry("jumphost", []string{"10.0.1.1"}, 0) // no proxy

	r2 := NewResolver()
	r2.AddEntryFull("jumphost", []string{"10.0.1.1"}, 0, "https", "http://proxy.corp:8080")

	r1.Merge(r2.AllEntries())

	entries := r1.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ProxyURL != "http://proxy.corp:8080" {
		t.Fatalf("merge should adopt proxy: got %q", entries[0].ProxyURL)
	}
}

func TestResolver_Merge_ProxyURL_NeverOverwriteWithEmpty(t *testing.T) {
	t.Parallel()

	r1 := NewResolver()
	r1.AddEntryFull("jumphost", []string{"10.0.1.1"}, 0, "https", "http://proxy.corp:8080")

	r2 := NewResolver()
	r2.AddEntry("jumphost", []string{"10.0.1.2"}, 0) // no proxy

	r1.Merge(r2.AllEntries())

	entries := r1.AllEntries()
	if entries[0].ProxyURL != "http://proxy.corp:8080" {
		t.Fatalf("merge should NOT overwrite proxy with empty: got %q", entries[0].ProxyURL)
	}
}

func TestResolver_AllDialTargets_IncludesProxy(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntryFull("behind-fw", []string{"10.0.1.1"}, 0, "https", "http://proxy.corp:8080")
	r.AddEntry("direct", []string{"10.0.2.1"}, 0)

	targets := r.AllDialTargets()
	if len(targets) != 2 {
		t.Fatalf("expected 2 dial targets, got %d", len(targets))
	}

	var proxyTarget, directTarget *DialTarget
	for i := range targets {
		switch targets[i].Hostname {
		case "behind-fw":
			proxyTarget = &targets[i]
		case "direct":
			directTarget = &targets[i]
		}
	}

	if proxyTarget == nil || proxyTarget.ProxyURL != "http://proxy.corp:8080" {
		t.Fatalf("expected proxy URL on behind-fw target, got %+v", proxyTarget)
	}
	if directTarget == nil || directTarget.ProxyURL != "" {
		t.Fatalf("expected no proxy on direct target, got %+v", directTarget)
	}
}
