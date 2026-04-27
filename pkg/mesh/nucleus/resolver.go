package nucleus

import (
	"bufio"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// ResolverEntry represents a cached hostname → address mapping.
type ResolverEntry struct {
	Hostname  string    `json:"hostname"`
	Addresses []string  `json:"addresses"`
	Transport string    `json:"transport,omitempty"`  // "ssh", "https", or "" (unknown)
	ProxyURL  string    `json:"proxy_url,omitempty"`  // HTTP CONNECT proxy URL, or "" (direct)
	ExpiresAt time.Time `json:"expires_at,omitempty"` // zero value = never expires
}

// Resolver is a distributed hostname → address cache.
// Each mesh node maintains its own cache, seeded from:
//   - Known hosts (config/hardcoded)
//   - /etc/hosts file
//   - DNS lookups
//   - Mesh gossip (Merge from peers)
//
// Resolver is goroutine-safe — all methods can be called concurrently.
type Resolver struct {
	mu    sync.RWMutex
	cache map[string]*resolverRecord
}

// resolverRecord is the internal cache entry with TTL support.
type resolverRecord struct {
	addresses []string
	addrSet   map[string]struct{} // M-1: O(1) dedup lookup
	transport string              // "ssh", "https", or ""
	proxyURL  string              // HTTP CONNECT proxy URL, or "" (direct)
	expiresAt time.Time           // zero value = never expires
}

// isExpired reports whether the record has expired.
func (r *resolverRecord) isExpired() bool {
	if r.expiresAt.IsZero() {
		return false // TTL=0 means no expiry
	}
	return time.Now().After(r.expiresAt)
}

// NewResolver creates a new Resolver with an empty cache.
func NewResolver() *Resolver {
	return &Resolver{
		cache: make(map[string]*resolverRecord),
	}
}

// AddEntry adds or updates a hostname → address mapping in the cache.
// If ttl is 0, the entry never expires.
// The addresses slice is copied — mutations to the original do not
// affect the cache.
func (r *Resolver) AddEntry(hostname string, addresses []string, ttl time.Duration) {
	r.AddEntryFull(hostname, addresses, ttl, "", "")
}

// AddEntryWithTransport adds or updates a hostname → address mapping
// with the specified transport type ("ssh", "https", or "").
func (r *Resolver) AddEntryWithTransport(hostname string, addresses []string, ttl time.Duration, transport string) {
	r.AddEntryFull(hostname, addresses, ttl, transport, "")
}

// AddEntryFull adds or updates a hostname → address mapping with
// transport and proxy URL. This is the most general form of AddEntry.
func (r *Resolver) AddEntryFull(hostname string, addresses []string, ttl time.Duration, transport, proxyURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	addrs := make([]string, len(addresses))
	copy(addrs, addresses)

	// Build address set for O(1) dedup lookups.
	addrSet := make(map[string]struct{}, len(addrs))
	for _, a := range addrs {
		addrSet[a] = struct{}{}
	}

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}

	r.cache[hostname] = &resolverRecord{
		addresses: addrs,
		addrSet:   addrSet,
		transport: transport,
		proxyURL:  proxyURL,
		expiresAt: expiresAt,
	}
}

// Resolve looks up a hostname in the cache.
// Returns a copy of the cached addresses, or an empty slice if not found
// or expired. The returned slice is safe to mutate.
func (r *Resolver) Resolve(hostname string) []string {
	r.mu.RLock()
	rec, ok := r.cache[hostname]
	r.mu.RUnlock()

	if !ok || rec.isExpired() {
		return nil
	}

	// Return a copy.
	r.mu.RLock()
	defer r.mu.RUnlock()
	addrs := make([]string, len(rec.addresses))
	copy(addrs, rec.addresses)
	return addrs
}

// SeedKnownHosts bulk-populates the cache from a config map.
// Entries added via SeedKnownHosts never expire (TTL=0).
func (r *Resolver) SeedKnownHosts(hosts map[string][]string) {
	for hostname, addrs := range hosts {
		r.AddEntry(hostname, addrs, 0)
	}
}

// ParseHostsContent parses /etc/hosts-format content and populates the cache.
// Returns the number of unique hostnames added.
//
// Format: each line is "IP hostname [alias1 alias2 ...]"
// Comments (#) and empty lines are ignored.
// Each hostname/alias maps to the IP on that line.
func (r *Resolver) ParseHostsContent(reader io.Reader) (int, error) {
	scanner := bufio.NewScanner(reader)
	added := make(map[string]struct{})

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue // Need at least IP + hostname
		}

		ip := fields[0]

		// Validate that the first field looks like an IP address.
		if net.ParseIP(ip) == nil {
			continue
		}

		// Each subsequent field is a hostname or alias.
		for _, hostname := range fields[1:] {
			r.mu.Lock()
			rec, exists := r.cache[hostname]
			if !exists {
				r.cache[hostname] = &resolverRecord{
					addresses: []string{ip},
					addrSet:   map[string]struct{}{ip: {}},
				}
			} else {
				// O(1) dedup via addrSet.
				if _, dup := rec.addrSet[ip]; !dup {
					rec.addresses = append(rec.addresses, ip)
					rec.addrSet[ip] = struct{}{}
				}
			}
			r.mu.Unlock()
			added[hostname] = struct{}{}
		}
	}

	return len(added), scanner.Err()
}

// AllEntries returns a copy of all non-expired cache entries.
// Used for gossip exchange — sending our resolver state to peers.
func (r *Resolver) AllEntries() []ResolverEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := make([]ResolverEntry, 0, len(r.cache))
	for hostname, rec := range r.cache {
		if rec.isExpired() {
			continue
		}

		addrs := make([]string, len(rec.addresses))
		copy(addrs, rec.addresses)

		entries = append(entries, ResolverEntry{
			Hostname:  hostname,
			Addresses: addrs,
			Transport: rec.transport,
			ProxyURL:  rec.proxyURL,
			ExpiresAt: rec.expiresAt,
		})
	}

	return entries
}

// Merge incorporates resolver entries from another node (via gossip).
// Existing entries are updated with new addresses (deduplicated).
// Entries from the merge are added with no expiry (TTL=0).
func (r *Resolver) Merge(entries []ResolverEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, entry := range entries {
		rec, exists := r.cache[entry.Hostname]
		if !exists {
			addrs := make([]string, len(entry.Addresses))
			copy(addrs, entry.Addresses)
			addrSet := make(map[string]struct{}, len(addrs))
			for _, a := range addrs {
				addrSet[a] = struct{}{}
			}
			r.cache[entry.Hostname] = &resolverRecord{
				addresses: addrs,
				addrSet:   addrSet,
				transport: entry.Transport,
				proxyURL:  entry.ProxyURL,
			}
			continue
		}

		// Update transport if the incoming entry has one and we don't.
		if rec.transport == "" && entry.Transport != "" {
			rec.transport = entry.Transport
		}

		// Update proxy URL: non-empty wins over empty (never overwrite with empty).
		if rec.proxyURL == "" && entry.ProxyURL != "" {
			rec.proxyURL = entry.ProxyURL
		}

		// M-1: Merge addresses with O(1) dedup via addrSet.
		if rec.addrSet == nil {
			rec.addrSet = make(map[string]struct{}, len(rec.addresses))
			for _, a := range rec.addresses {
				rec.addrSet[a] = struct{}{}
			}
		}
		for _, addr := range entry.Addresses {
			if _, dup := rec.addrSet[addr]; !dup {
				rec.addresses = append(rec.addresses, addr)
				rec.addrSet[addr] = struct{}{}
			}
		}
	}
}

// DialTarget represents a single reconnection candidate:
// one specific address for one specific hostname with a transport hint.
type DialTarget struct {
	Hostname  string // node identity (e.g., "mds-01")
	Address   string // network address (e.g., "10.0.1.1:443")
	Transport string // "ssh", "https", or "" (try both)
	ProxyURL  string // HTTP CONNECT proxy URL, or "" (direct)
}

// AllDialTargets returns all known, non-expired hostname+address pairs
// as individual dial targets for reconnection. Each address in each
// resolver entry becomes its own DialTarget.
func (r *Resolver) AllDialTargets() []DialTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var targets []DialTarget
	for hostname, rec := range r.cache {
		if rec.isExpired() {
			continue
		}
		for _, addr := range rec.addresses {
			targets = append(targets, DialTarget{
				Hostname:  hostname,
				Address:   addr,
				Transport: rec.transport,
				ProxyURL:  rec.proxyURL,
			})
		}
	}

	return targets
}
