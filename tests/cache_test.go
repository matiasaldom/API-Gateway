package tests

import (
	"io"
	"net/http"
	"testing"
	"time"

	"api-gateway/internal/requestid"
)

// get sends a GET with the given API key and returns the response with its body read.
func (e authEnv) get(t *testing.T, path, apiKey string) (*http.Response, string) {
	t.Helper()
	resp := e.request(t, http.MethodGet, path, "Bearer "+apiKey, "")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestRepeatedGETIsServedFromCache(t *testing.T) {
	env := newAuthEnv(t)

	first, firstBody := env.get(t, "/albums/1?sort=asc", env.activeKey)
	second, secondBody := env.get(t, "/albums/1?sort=asc", env.activeKey)

	if n := env.upstreamCalls.Load(); n != 1 {
		t.Fatalf("backend called %d times, want 1", n)
	}
	if first.Header.Get("X-Cache") != "MISS" || second.Header.Get("X-Cache") != "HIT" {
		t.Errorf("X-Cache = %q then %q, want MISS then HIT", first.Header.Get("X-Cache"), second.Header.Get("X-Cache"))
	}
	if second.StatusCode != http.StatusOK || secondBody != firstBody {
		t.Errorf("hit: status %d body %q, want 200 %q", second.StatusCode, secondBody, firstBody)
	}
	if second.Header.Get("Content-Type") != "application/json" || second.Header.Get("X-Upstream") != "users" {
		t.Errorf("upstream headers not replayed: %v", second.Header)
	}
	// Per-request headers are fresh on a hit, not replayed from the cached response.
	if id1, id2 := first.Header.Get(requestid.Header), second.Header.Get(requestid.Header); id1 == id2 || id2 == "" {
		t.Errorf("request IDs %q and %q: hit must carry its own", id1, id2)
	}
	if first.Header.Get("X-RateLimit-Remaining") != "99" || second.Header.Get("X-RateLimit-Remaining") != "98" {
		t.Errorf("remaining = %q then %q: cache hits must still count against the rate limit",
			first.Header.Get("X-RateLimit-Remaining"), second.Header.Get("X-RateLimit-Remaining"))
	}

	// A different query string is a different entry.
	if resp, _ := env.get(t, "/albums/1?sort=desc", env.activeKey); resp.Header.Get("X-Cache") != "MISS" {
		t.Error("different query served from another query's entry")
	}
	if s := env.cache.Stats(); s.Hits != 1 || s.Misses != 2 {
		t.Errorf("stats = %+v, want 1 hit, 2 misses", s)
	}
}

func TestCacheTTLExpiryCallsBackend(t *testing.T) {
	env := newAuthEnv(t)
	env.get(t, "/albums/1", env.activeKey)

	env.cacheClock.Add(29 * time.Second)
	if resp, _ := env.get(t, "/albums/1", env.activeKey); resp.Header.Get("X-Cache") != "HIT" || resp.Header.Get("Age") != "29" {
		t.Errorf("before expiry: X-Cache=%q Age=%q, want HIT 29", resp.Header.Get("X-Cache"), resp.Header.Get("Age"))
	}

	env.cacheClock.Add(time.Second) // TTL (30s) reached
	resp, _ := env.get(t, "/albums/1", env.activeKey)
	if resp.Header.Get("X-Cache") != "MISS" || env.upstreamCalls.Load() != 2 {
		t.Errorf("after expiry: X-Cache=%q backend calls=%d, want MISS and 2", resp.Header.Get("X-Cache"), env.upstreamCalls.Load())
	}
}

func TestAPIKeysDoNotShareCacheEntries(t *testing.T) {
	env := newAuthEnv(t)
	otherKey := env.planKey(t, "pro") // a different application and key

	_, bodyA := env.get(t, "/albums/1", env.activeKey)
	respB, bodyB := env.get(t, "/albums/1", otherKey)

	if respB.Header.Get("X-Cache") != "MISS" || env.upstreamCalls.Load() != 2 {
		t.Fatalf("second key: X-Cache=%q backend calls=%d; must not see the first key's entry",
			respB.Header.Get("X-Cache"), env.upstreamCalls.Load())
	}
	// Each body echoes the request that produced it: B must see its own request ID, not A's.
	if bodyA == bodyB {
		t.Error("second key received the first key's response body")
	}
	if resp, _ := env.get(t, "/albums/1", otherKey); resp.Header.Get("X-Cache") != "HIT" {
		t.Error("second key's own entry was not cached")
	}
}

func TestPOSTIsNeverCached(t *testing.T) {
	env := newAuthEnv(t)
	for i := range 3 {
		resp := env.request(t, http.MethodPost, "/albums", "Bearer "+env.activeKey, `{"title":"x"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated || resp.Header.Get("X-Cache") != "" {
			t.Errorf("POST %d: status %d X-Cache %q", i+1, resp.StatusCode, resp.Header.Get("X-Cache"))
		}
	}
	if n := env.upstreamCalls.Load(); n != 3 {
		t.Errorf("backend called %d times, want 3", n)
	}
	// A POST must not populate the cache for a later GET either.
	if resp, _ := env.get(t, "/albums", env.activeKey); resp.Header.Get("X-Cache") != "MISS" {
		t.Error("GET after POST was served from cache")
	}
}

func TestRouteWithoutTTLIsNotCached(t *testing.T) {
	env := newAuthEnv(t)
	for range 2 {
		if resp, _ := env.get(t, "/users/1", env.activeKey); resp.Header.Get("X-Cache") != "" {
			t.Errorf("X-Cache = %q on a route with cache_ttl 0", resp.Header.Get("X-Cache"))
		}
	}
	if n := env.upstreamCalls.Load(); n != 2 {
		t.Errorf("backend called %d times, want 2", n)
	}
}
