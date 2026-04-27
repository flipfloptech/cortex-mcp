package nucleus

import (
	"testing"
)

// --- M-1: Resolver Merge O(1) dedup ---

func TestResolverMerge_DedupO1(t *testing.T) {
	t.Parallel()

	r := NewResolver()

	// Seed with initial addresses.
	r.AddEntry("host-1", []string{"10.0.1.1", "10.0.1.2"}, 0)

	// Merge with duplicates + new address.
	r.Merge([]ResolverEntry{
		{
			Hostname:  "host-1",
			Addresses: []string{"10.0.1.1", "10.0.1.3"}, // 10.0.1.1 is dup, 10.0.1.3 is new
		},
	})

	addrs := r.Resolve("host-1")
	if len(addrs) != 3 {
		t.Fatalf("expected 3 addresses after dedup merge, got %d: %v", len(addrs), addrs)
	}

	// Verify no duplicates.
	seen := make(map[string]bool)
	for _, addr := range addrs {
		if seen[addr] {
			t.Fatalf("duplicate address %q found after merge", addr)
		}
		seen[addr] = true
	}
}

func TestResolverMerge_LargeDedup(t *testing.T) {
	t.Parallel()

	r := NewResolver()

	// Build a host with 1000 addresses.
	initial := make([]string, 1000)
	for i := range initial {
		initial[i] = "10.0.0." + string(rune(i))
	}
	r.AddEntry("big-host", initial, 0)

	// Merge the same 1000 addresses again — should be O(1) per addr.
	r.Merge([]ResolverEntry{
		{
			Hostname:  "big-host",
			Addresses: initial,
		},
	})

	addrs := r.Resolve("big-host")
	if len(addrs) != 1000 {
		t.Fatalf("expected 1000 addresses, got %d", len(addrs))
	}
}

func TestResolverMerge_NewHostAdded(t *testing.T) {
	t.Parallel()

	r := NewResolver()

	r.Merge([]ResolverEntry{
		{
			Hostname:  "new-host",
			Addresses: []string{"10.0.2.1"},
			Transport: "ssh",
		},
	})

	addrs := r.Resolve("new-host")
	if len(addrs) != 1 || addrs[0] != "10.0.2.1" {
		t.Fatalf("expected [10.0.2.1], got %v", addrs)
	}
}

func TestResolverMerge_ConcurrentSafety(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.AddEntry("concurrent-host", []string{"10.0.0.1"}, 0)

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				r.Merge([]ResolverEntry{
					{
						Hostname:  "concurrent-host",
						Addresses: []string{"10.0.0." + string(rune(n*100+j))},
					},
				})
			}
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
