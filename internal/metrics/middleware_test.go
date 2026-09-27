package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"api-gateway/internal/auth"
	"api-gateway/internal/requestid"
)

func routeFor(path string) string {
	if len(path) >= 7 && path[:7] == "/albums" {
		return "/albums"
	}
	return ""
}

// fakeAuth authenticates requests carrying X-Test-Key and rejects the rest with 401, like auth.Middleware.
func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		key := auth.APIKey{ID: 7, ApplicationID: 70, PlanID: 2}
		next.ServeHTTP(w, r.WithContext(auth.NewContext(r.Context(), key)))
	})
}

// chain mirrors production order: request ID → Middleware → auth → Identify → handler.
func chain(c *Collector, h http.HandlerFunc) http.Handler {
	return requestid.Middleware(Middleware(c, routeFor)(fakeAuth(Identify(h))))
}

func TestMiddlewareCollectsEvent(t *testing.T) {
	w := &fakeWriter{}
	c := NewCollector(w, discard, Options{FlushInterval: time.Hour})
	h := chain(c, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.Header().Set("X-Cache", "HIT")
		w.WriteHeader(http.StatusCreated)
	})

	req := httptest.NewRequest(http.MethodPost, "/albums/1?x=1", nil)
	req.Header.Set("X-Test-Key", "k")
	req.Header.Set(requestid.Header, "req-abc")
	h.ServeHTTP(httptest.NewRecorder(), req)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/other", nil)) // unauthenticated
	closeNow(t, c)

	events := w.all()
	if len(events) != 2 {
		t.Fatalf("collected %d events, want 2", len(events))
	}
	e := events[0]
	if e.RequestID != "req-abc" || e.Method != "POST" || e.Route != "/albums" || e.Status != 201 || e.Cache != "HIT" {
		t.Errorf("event = %+v", e)
	}
	if e.APIKeyID != 7 || e.ApplicationID != 70 || e.PlanID != 2 {
		t.Errorf("identity = key %d app %d plan %d, want 7 70 2", e.APIKeyID, e.ApplicationID, e.PlanID)
	}
	if e.Latency < 2*time.Millisecond || e.OccurredAt.IsZero() {
		t.Errorf("latency = %s, occurred_at = %s", e.Latency, e.OccurredAt)
	}

	anon := events[1]
	if anon.Status != 401 || anon.APIKeyID != 0 || anon.Route != "" || anon.Cache != "" {
		t.Errorf("unauthenticated event = %+v", anon)
	}
}

func TestMiddlewareIgnoresForeignCacheHeader(t *testing.T) {
	w := &fakeWriter{}
	c := NewCollector(w, discard, Options{FlushInterval: time.Hour})
	h := chain(c, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Cache", "Hit from cloudfront") // upstream CDN, not the gateway cache
	})
	req := httptest.NewRequest(http.MethodGet, "/albums", nil)
	req.Header.Set("X-Test-Key", "k")
	h.ServeHTTP(httptest.NewRecorder(), req)
	closeNow(t, c)
	if got := w.all()[0].Cache; got != "" {
		t.Errorf("Cache = %q, want empty for a non-gateway X-Cache value", got)
	}
}

func TestMiddlewareCountsAbortedRequests(t *testing.T) {
	w := &fakeWriter{}
	c := NewCollector(w, discard, Options{FlushInterval: time.Hour})
	h := chain(c, func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) })
	req := httptest.NewRequest(http.MethodGet, "/albums", nil)
	req.Header.Set("X-Test-Key", "k")

	func() {
		defer func() {
			if p := recover(); p != http.ErrAbortHandler {
				t.Errorf("panic = %v, want ErrAbortHandler re-raised", p)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()
	closeNow(t, c)
	if len(w.all()) != 1 {
		t.Error("aborted request was not counted")
	}
}

func TestTrafficUnaffectedByAnalyticsOutage(t *testing.T) {
	w := &fakeWriter{block: make(chan struct{})} // database hung
	defer close(w.block)
	c := NewCollector(w, discard, Options{BufferSize: 5, BatchSize: 1, WriteTimeout: time.Hour})
	h := chain(c, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	start := time.Now()
	for range 500 {
		req := httptest.NewRequest(http.MethodGet, "/albums", nil)
		req.Header.Set("X-Test-Key", "k")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d during analytics outage", rec.Code)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("500 requests took %s with analytics stuck", elapsed)
	}
	if c.Stats().Dropped == 0 {
		t.Error("expected overflow events to be dropped, not queued without bound")
	}
}

// BenchmarkMiddlewareOverhead compares a request through a no-op handler with and
// without analytics; the difference is the per-request cost on the hot path.
func BenchmarkMiddlewareOverhead(b *testing.B) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	c := NewCollector(&fakeWriter{}, discard, Options{BufferSize: 1 << 20, BatchSize: 1000})
	defer c.Close(b.Context())
	req := httptest.NewRequest(http.MethodGet, "/albums/1", nil)

	for name, h := range map[string]http.Handler{
		"baseline":       next,
		"with_analytics": Middleware(c, routeFor)(Identify(next)),
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				h.ServeHTTP(httptest.NewRecorder(), req)
			}
		})
	}
}
