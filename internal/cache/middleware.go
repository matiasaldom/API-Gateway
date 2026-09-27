package cache

import (
	"bytes"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"api-gateway/internal/auth"
)

// Middleware serves GET responses from c. ttlFor returns the cache TTL for a
// request path (the matching route's cache_ttl); 0 bypasses the cache. It must
// run after auth.Middleware: entries are scoped to the authenticated API key,
// and requests without one are never cached.
//
// Responses carry X-Cache: HIT or MISS when the cache was consulted, and Age on hits.
func Middleware(c *Cache, ttlFor func(path string) time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			ttl := ttlFor(r.URL.Path)
			apiKey, authenticated := auth.FromContext(r.Context())
			if ttl <= 0 || !authenticated {
				next.ServeHTTP(w, r)
				return
			}

			k := KeyFor(r, apiKey.ID)
			if resp, ok := c.Get(k); ok {
				h := w.Header()
				for name, vals := range resp.Header {
					h[name] = slices.Clone(vals)
				}
				h.Set("Age", strconv.Itoa(int(c.now().Sub(resp.StoredAt)/time.Second)))
				h.Set("X-Cache", "HIT")
				w.WriteHeader(resp.Status)
				_, _ = w.Write(resp.Body)
				return
			}

			w.Header().Set("X-Cache", "MISS")
			// Headers already set belong to this request (request ID, rate-limit
			// counts, X-Cache), not to the upstream response: never store them.
			perRequest := make(map[string]bool, len(w.Header()))
			for name := range w.Header() {
				perRequest[name] = true
			}
			rec := &recorder{ResponseWriter: w, perRequest: perRequest}
			next.ServeHTTP(rec, r)
			if resp, ok := rec.cacheable(); ok {
				resp.StoredAt = c.now()
				c.Put(k, resp, ttl)
			}
		})
	}
}

// recorder passes the response through to the client while keeping a copy for the cache.
type recorder struct {
	http.ResponseWriter
	perRequest map[string]bool
	status     int
	header     http.Header // upstream headers, captured when the status is written
	body       bytes.Buffer
	tooBig     bool
}

func (rec *recorder) WriteHeader(code int) {
	if rec.status == 0 && code >= 200 { // 1xx (e.g. 103 Early Hints) is not the final response
		rec.status = code
		rec.header = rec.ResponseWriter.Header().Clone()
		for name := range rec.perRequest {
			delete(rec.header, name)
		}
		delete(rec.header, "Date") // the server stamps a fresh one on each response
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *recorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.WriteHeader(http.StatusOK)
	}
	n, err := rec.ResponseWriter.Write(b)
	if !rec.tooBig {
		if rec.body.Len()+n > MaxEntryBytes {
			rec.tooBig = true
			rec.body = bytes.Buffer{}
		} else {
			rec.body.Write(b[:n])
		}
	}
	return n, err
}

// Unwrap lets http.ResponseController (used by ReverseProxy for flushing) reach the real writer.
func (rec *recorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// cacheable reports whether the recorded response may be stored, and returns it.
// Only complete 200 responses without per-user markers are cached.
func (rec *recorder) cacheable() (Response, bool) {
	if rec.status != http.StatusOK || rec.tooBig {
		return Response{}, false
	}
	h := rec.header
	if _, ok := h["Set-Cookie"]; ok {
		return Response{}, false
	}
	cc := strings.ToLower(strings.Join(h.Values("Cache-Control"), ","))
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "private") {
		return Response{}, false
	}
	// The key already varies on Accept-Encoding; any other Vary means the response
	// depends on request headers the key doesn't capture.
	for _, v := range h.Values("Vary") {
		for _, field := range strings.Split(v, ",") {
			if f := strings.TrimSpace(field); f != "" && !strings.EqualFold(f, "Accept-Encoding") {
				return Response{}, false
			}
		}
	}
	if cl := h.Get("Content-Length"); cl != "" && cl != strconv.Itoa(rec.body.Len()) {
		return Response{}, false // truncated
	}
	return Response{Status: rec.status, Header: h, Body: bytes.Clone(rec.body.Bytes())}, true
}
