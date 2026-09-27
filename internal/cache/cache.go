// Package cache is an in-memory, per-API-key cache for GET responses with per-route TTLs.
package cache

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// MaxEntryBytes is the largest response body that is cached; larger responses pass through uncached.
const MaxEntryBytes = 1 << 20

// Key identifies a cached response.
//
// APIKeyID scopes every entry to one credential, so a response is never served to
// a different API key. AcceptEncoding is included because the same URL can come
// back gzip-encoded or not depending on it.
type Key struct {
	Method         string
	Path           string
	RawQuery       string
	APIKeyID       int64
	AcceptEncoding string
}

// KeyFor builds the cache key for a request made with the given API key.
func KeyFor(r *http.Request, apiKeyID int64) Key {
	return Key{
		Method:         r.Method,
		Path:           r.URL.Path,
		RawQuery:       r.URL.RawQuery,
		APIKeyID:       apiKeyID,
		AcceptEncoding: r.Header.Get("Accept-Encoding"),
	}
}

// Response is a stored upstream response. Its Header and Body are shared with the
// cache and must not be modified.
type Response struct {
	Status   int
	Header   http.Header
	Body     []byte
	StoredAt time.Time
}

type entry struct {
	resp    Response
	expires time.Time
	size    int
}

// Stats is a point-in-time view of cache activity.
type Stats struct {
	Hits, Misses uint64
	Entries      int
	Bytes        int
}

// Cache is safe for concurrent use. Expired entries are dropped when read and
// by RunJanitor. Admission is capped at maxBytes: when full, new responses are
// simply not cached until expired entries are swept (no eviction policy).
type Cache struct {
	now      func() time.Time
	maxBytes int

	mu      sync.RWMutex
	entries map[Key]entry
	bytes   int

	hits, misses atomic.Uint64
}

// New returns a cache holding at most maxBytes of responses, using now as its clock.
func New(maxBytes int, now func() time.Time) *Cache {
	return &Cache{now: now, maxBytes: maxBytes, entries: make(map[Key]entry)}
}

// Get returns the fresh response for k and records a hit or miss.
func (c *Cache) Get(k Key) (Response, bool) {
	now := c.now()
	c.mu.RLock()
	e, ok := c.entries[k]
	c.mu.RUnlock()

	if ok && now.Before(e.expires) {
		c.hits.Add(1)
		return e.resp, true
	}
	if ok { // expired: drop it now rather than waiting for the janitor
		c.mu.Lock()
		if cur, still := c.entries[k]; still && !now.Before(cur.expires) {
			c.remove(k, cur)
		}
		c.mu.Unlock()
	}
	c.misses.Add(1)
	return Response{}, false
}

// Put stores resp under k for ttl. It reports false if the entry was not
// stored because it would exceed the cache's byte limit.
func (c *Cache) Put(k Key, resp Response, ttl time.Duration) bool {
	size := entrySize(k, resp)
	c.mu.Lock()
	defer c.mu.Unlock()

	old, replacing := c.entries[k]
	if c.bytes-old.size+size > c.maxBytes {
		return false
	}
	if replacing {
		c.remove(k, old)
	}
	c.entries[k] = entry{resp: resp, expires: c.now().Add(ttl), size: size}
	c.bytes += size
	return true
}

// Sweep removes every expired entry and returns how many were removed.
func (c *Cache) Sweep() int {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			c.remove(k, e)
			n++
		}
	}
	return n
}

// RunJanitor sweeps expired entries every interval until ctx is done.
func (c *Cache) RunJanitor(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Sweep()
		}
	}
}

func (c *Cache) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Stats{Hits: c.hits.Load(), Misses: c.misses.Load(), Entries: len(c.entries), Bytes: c.bytes}
}

// MetricsHandler serves cache statistics in the Prometheus text format.
func (c *Cache) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := c.Stats()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP cache_hits Responses served from the cache.\n# TYPE cache_hits counter\ncache_hits %d\n", s.Hits)
		fmt.Fprintf(w, "# HELP cache_misses Cacheable requests not found in the cache.\n# TYPE cache_misses counter\ncache_misses %d\n", s.Misses)
		fmt.Fprintf(w, "# HELP cache_entries Responses currently cached.\n# TYPE cache_entries gauge\ncache_entries %d\n", s.Entries)
		fmt.Fprintf(w, "# HELP cache_bytes Approximate bytes held by cached responses.\n# TYPE cache_bytes gauge\ncache_bytes %d\n", s.Bytes)
	})
}

// remove deletes k; the caller holds c.mu.
func (c *Cache) remove(k Key, e entry) {
	delete(c.entries, k)
	c.bytes -= e.size
}

// entrySize approximates an entry's memory: body, headers, and key strings.
func entrySize(k Key, resp Response) int {
	n := len(resp.Body) + len(k.Method) + len(k.Path) + len(k.RawQuery) + len(k.AcceptEncoding)
	for name, vals := range resp.Header {
		n += len(name)
		for _, v := range vals {
			n += len(v)
		}
	}
	return n
}
