package metrics

import (
	"context"
	"net/http"
	"time"

	"api-gateway/internal/auth"
	"api-gateway/internal/httpx"
	"api-gateway/internal/requestid"
)

type identityKey struct{}

// identity carries the authenticated key from Identify (inside auth) back out to Middleware.
type identity struct {
	key auth.APIKey
	ok  bool
}

// Middleware emits one Event per request after the response completes. Place it
// before auth.Middleware so latency includes authentication and rejected
// requests are counted, and place Identify after auth.Middleware so events carry
// the API key, application, and plan. routeFor maps a path to its route prefix.
//
// The only work on the request path is building the event and a non-blocking send.
func Middleware(c *Collector, routeFor func(path string) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			id := &identity{}
			rec := httpx.NewStatusRecorder(w)
			// Deferred so requests aborted by a panic (e.g. an upstream dying mid-body) still count.
			defer func() {
				e := Event{
					OccurredAt: start,
					RequestID:  requestid.FromContext(r.Context()),
					Method:     r.Method,
					Route:      routeFor(r.URL.Path),
					Status:     rec.Status,
					Latency:    time.Since(start),
				}
				// Only the gateway cache's own values; an upstream CDN's X-Cache on an
				// uncached route is not ours and would violate the column's check.
				if v := rec.Header().Get("X-Cache"); v == "HIT" || v == "MISS" {
					e.Cache = v
				}
				if id.ok {
					e.APIKeyID, e.ApplicationID, e.PlanID = id.key.ID, id.key.ApplicationID, id.key.PlanID
				}
				c.Emit(e)
			}()
			next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
		})
	}
}

// Identify records the authenticated API key for Middleware's event. It must run after auth.Middleware.
func Identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := r.Context().Value(identityKey{}).(*identity); ok {
			id.key, id.ok = auth.FromContext(r.Context())
		}
		next.ServeHTTP(w, r)
	})
}
