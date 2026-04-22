package registry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"
)

// cachedTool wraps a Tool with a strictly bounded, O(1) single-item cache.
type cachedTool struct {
	Tool
	ttl time.Duration

	mu        sync.RWMutex
	lastHash  string
	lastRes   *ToolResult
	expiresAt time.Time
}

// WithCache returns a Tool that memoizes the most recent successful Execute()
// result for the given time-to-live (TTL).
//
// The cache size is strictly O(1) per tool. It only caches the most recent
// execution. If the tool is called with different arguments, the cache is
// immediately overwritten. This prevents unbounded memory growth from
// adversarial or high-cardinality JSON argument permutations.
func WithCache(ttl time.Duration, t Tool) Tool {
	return &cachedTool{
		Tool: t,
		ttl:  ttl,
	}
}

// Execute intercepts the tool invocation to serve from the cache if possible.
func (c *cachedTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	// Hash the arguments to form the cache key.
	h := sha256.New()
	_, _ = h.Write(args)
	key := string(h.Sum(nil))

	// Fast path: check for a cache hit.
	c.mu.RLock()
	hit := c.lastHash == key && time.Now().Before(c.expiresAt)
	var cachedRes *ToolResult
	if hit && c.lastRes != nil {
		cachedRes = copyResult(c.lastRes)
	}
	c.mu.RUnlock()

	if hit {
		return cachedRes, nil
	}

	// Cache miss or expired, call the underlying tool.
	res, err := c.Tool.Execute(ctx, args)
	if err != nil {
		return res, err
	}

	// Only cache successful or warning results.
	if res.Status == StatusOK || res.Status == StatusWarning {
		c.mu.Lock()
		c.lastHash = key
		c.lastRes = copyResult(res)
		c.expiresAt = time.Now().Add(c.ttl)
		c.mu.Unlock()
	}

	return res, nil
}

// copyResult performs a deep copy of a ToolResult to prevent
// reference mutation from corrupting the cache.
func copyResult(r *ToolResult) *ToolResult {
	if r == nil {
		return nil
	}
	cp := *r
	if len(r.Data) > 0 {
		cp.Data = make(json.RawMessage, len(r.Data))
		copy(cp.Data, r.Data)
	}
	return &cp
}
