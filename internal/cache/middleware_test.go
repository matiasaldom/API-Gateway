package cache

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"api-gateway/internal/auth"
)

// upstream counts calls and writes the response configured by respond.
type upstream struct {
	calls   atomic.Int64
	respond func(w http.ResponseWriter, r *http.Request)
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.calls.Add(1)
	if u.respond != nil {
		u.respond(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", `"v1"`)
	w.Header().Set("Date", "Mon, 01 Jan 2001 00:00:00 GMT")
	_, _ = w.Write([]byte(`{"path":"` + r.URL.Path + `"}`))
}

const ttl = 30 * time.Second

func constTTL(d time.Duration) func(string) time.Duration {
	return func(string) time.Duration { return d }
}

// serve sends a request through outer middleware that sets a per-request header
// (as request ID and rate limiting do), then the cache, then up.
func serve(h http.Handler, method, target string, apiKeyID int64, reqID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if apiKeyID != 0 {
		req = req.WithContext(auth.NewContext(req.Context(), auth.APIKey{ID: apiKeyID}))
	}
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-Id", reqID)
	h.ServeHTTP(rec, req)
	return rec
}

func TestMiddlewareHitSkipsUpstream(t *testing.T) {
	c, clk := newTestCache(1 << 20)
	up := &upstream{}
	h := Middleware(c, constTTL(ttl))(up)

	first := serve(h, http.MethodGet, "/albums/1?x=1", 1, "req-1")
	if first.Header().Get("X-Cache") != "MISS" || up.calls.Load() != 1 {
		t.Fatalf("first request: X-Cache=%q calls=%d", first.Header().Get("X-Cache"), up.calls.Load())
	}

	clk.Add(5 * time.Second)
	second := serve(h, http.MethodGet, "/albums/1?x=1", 1, "req-2")
	if up.calls.Load() != 1 {
		t.Fatalf("hit called upstream (calls=%d)", up.calls.Load())
	}
	h2 := second.Header()
	if second.Code != 200 || second.Body.String() != first.Body.String() {
		t.Errorf("hit = %d %q, want 200 %q", second.Code, second.Body, first.Body)
	}
	if h2.Get("X-Cache") != "HIT" || h2.Get("Age") != "5" || h2.Get("ETag") != `"v1"` || h2.Get("Content-Type") != "application/json" {
		t.Errorf("hit headers: %v", h2)
	}
	if h2.Get("X-Request-Id") != "req-2" || len(h2.Values("X-Request-Id")) != 1 {
		t.Errorf("per-request header replayed from cache: %v", h2.Values("X-Request-Id"))
	}
	if h2.Get("Date") != "" {
		t.Error("stale upstream Date replayed from cache")
	}
}

func TestMiddlewareBypass(t *testing.T) {
	tests := map[string]struct {
		method   string
		apiKeyID int64
		ttl      time.Duration
	}{
		"POST is never cached":       {http.MethodPost, 1, ttl},
		"HEAD is not cached":         {http.MethodHead, 1, ttl},
		"TTL 0 bypasses":             {http.MethodGet, 1, 0},
		"unauthenticated not cached": {http.MethodGet, 0, ttl},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestCache(1 << 20)
			up := &upstream{}
			h := Middleware(c, constTTL(tt.ttl))(up)
			for range 2 {
				if rec := serve(h, tt.method, "/albums", tt.apiKeyID, "r"); rec.Header().Get("X-Cache") != "" {
					t.Errorf("X-Cache = %q on a bypassed request", rec.Header().Get("X-Cache"))
				}
			}
			if up.calls.Load() != 2 {
				t.Errorf("upstream calls = %d, want 2", up.calls.Load())
			}
			if s := c.Stats(); s.Hits+s.Misses != 0 || s.Entries != 0 {
				t.Errorf("bypassed requests touched the cache: %+v", s)
			}
		})
	}
}

func TestMiddlewareScopesEntriesToAPIKey(t *testing.T) {
	c, _ := newTestCache(1 << 20)
	up := &upstream{}
	h := Middleware(c, constTTL(ttl))(up)
	serve(h, http.MethodGet, "/me", 1, "r1")
	if rec := serve(h, http.MethodGet, "/me", 2, "r2"); rec.Header().Get("X-Cache") != "MISS" {
		t.Fatal("API key 2 was served API key 1's cached response")
	}
	if up.calls.Load() != 2 {
		t.Errorf("upstream calls = %d, want 2", up.calls.Load())
	}
}

func TestMiddlewareDoesNotStoreUncacheableResponses(t *testing.T) {
	tests := map[string]func(w http.ResponseWriter, r *http.Request){
		"server error": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"not found":    func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"set-cookie": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Set-Cookie", "session=abc")
			_, _ = w.Write([]byte("personal"))
		},
		"cache-control no-store": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte("x"))
		},
		"cache-control private": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "max-age=60, Private")
			_, _ = w.Write([]byte("x"))
		},
		"vary on a header outside the key": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Vary", "Accept-Encoding, X-User-Id")
			_, _ = w.Write([]byte("x"))
		},
		"vary star": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Vary", "*")
			_, _ = w.Write([]byte("x"))
		},
		"truncated body": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("short"))
		},
		"too large": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", MaxEntryBytes)))
			_, _ = w.Write([]byte("x"))
		},
	}
	for name, respond := range tests {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestCache(8 << 20)
			up := &upstream{respond: respond}
			h := Middleware(c, constTTL(ttl))(up)
			serve(h, http.MethodGet, "/x", 1, "r1")
			serve(h, http.MethodGet, "/x", 1, "r2")
			if up.calls.Load() != 2 || c.Stats().Entries != 0 {
				t.Errorf("response was cached (calls=%d entries=%d)", up.calls.Load(), c.Stats().Entries)
			}
		})
	}
}

func TestMiddlewareCachesVaryAcceptEncodingPerEncoding(t *testing.T) {
	c, _ := newTestCache(1 << 20)
	up := &upstream{respond: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Encoding")
		_, _ = w.Write([]byte("encoding=" + r.Header.Get("Accept-Encoding")))
	}}
	h := Middleware(c, constTTL(ttl))(up)

	get := func(enc string) string {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Accept-Encoding", enc)
		req = req.WithContext(auth.NewContext(req.Context(), auth.APIKey{ID: 1}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	get("gzip")
	if got := get("identity"); got != "encoding=identity" {
		t.Errorf("identity client got %q", got)
	}
	if got := get("gzip"); got != "encoding=gzip" || up.calls.Load() != 2 {
		t.Errorf("gzip client got %q (calls=%d), want cached gzip variant", got, up.calls.Load())
	}
}

func TestMiddlewareTTLExpiry(t *testing.T) {
	c, clk := newTestCache(1 << 20)
	up := &upstream{}
	h := Middleware(c, constTTL(ttl))(up)
	serve(h, http.MethodGet, "/a", 1, "r")
	clk.Add(ttl)
	if rec := serve(h, http.MethodGet, "/a", 1, "r"); rec.Header().Get("X-Cache") != "MISS" || up.calls.Load() != 2 {
		t.Errorf("expired entry served (X-Cache=%q calls=%d)", rec.Header().Get("X-Cache"), up.calls.Load())
	}
}
