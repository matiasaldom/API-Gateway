package limiter

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"api-gateway/internal/auth"
)

// clock is a settable time source for deterministic window tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time)     { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }
func newClock(t time.Time) *clock    { return &clock{t: t} }

var windowStart = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) // aligned to a minute

func TestCounterIncrements(t *testing.T) {
	l := New(newClock(windowStart.Add(10 * time.Second)).Now)
	for i := 1; i <= 5; i++ {
		d := l.Allow(1, 5)
		if !d.Allowed || d.Limit != 5 || d.Remaining != 5-i {
			t.Fatalf("request %d: %+v, want allowed with remaining %d", i, d, 5-i)
		}
		if d.RetryAfter != 50*time.Second {
			t.Fatalf("RetryAfter = %s, want 50s", d.RetryAfter)
		}
	}
}

func TestLimitExceeded(t *testing.T) {
	l := New(newClock(windowStart.Add(15 * time.Second)).Now)
	for range 3 {
		l.Allow(1, 3)
	}
	d := l.Allow(1, 3)
	if d.Allowed || d.Remaining != 0 || !d.FirstDenied {
		t.Fatalf("4th request: %+v, want denied, remaining 0, first denial", d)
	}
	for range 10 {
		d = l.Allow(1, 3)
		if d.Allowed || d.Remaining != 0 || d.FirstDenied {
			t.Fatalf("later request: %+v, want denied and not first denial", d)
		}
	}
	if l.counts[1] != 4 {
		t.Errorf("counter = %d, want capped at limit+1", l.counts[1])
	}
}

func TestKeysAreIndependent(t *testing.T) {
	l := New(newClock(windowStart).Now)
	l.Allow(1, 1)
	if d := l.Allow(1, 1); d.Allowed {
		t.Fatal("key 1 should be exhausted")
	}
	if d := l.Allow(2, 1); !d.Allowed {
		t.Fatal("key 2 must not share key 1's counter")
	}
}

func TestWindowReset(t *testing.T) {
	c := newClock(windowStart.Add(30 * time.Second))
	l := New(c.Now)
	for range 3 {
		l.Allow(1, 2)
	}
	c.Add(Window) // same offset, next window
	d := l.Allow(1, 2)
	if !d.Allowed || d.Remaining != 1 || d.FirstDenied {
		t.Fatalf("after reset: %+v, want fresh window", d)
	}
	c.Add(-2 * Window) // clock stepped backwards (e.g. NTP): still a different window, start fresh
	if d := l.Allow(1, 2); !d.Allowed || d.Remaining != 1 {
		t.Fatalf("after backwards step: %+v, want fresh window", d)
	}
}

func TestExactBoundary(t *testing.T) {
	c := newClock(windowStart) // first instant of the window counts in it
	l := New(c.Now)
	if d := l.Allow(1, 2); !d.Allowed || d.RetryAfter != Window {
		t.Fatalf("at window start: %+v, want allowed with a full window to go", d)
	}
	l.Allow(1, 2)

	c.Set(windowStart.Add(Window - time.Nanosecond)) // last instant of the window
	d := l.Allow(1, 2)
	if d.Allowed || d.RetryAfter != time.Nanosecond {
		t.Fatalf("at last nanosecond: %+v, want denied, reset in 1ns", d)
	}
	if got := retryAfterSeconds(d.RetryAfter); got != 1 {
		t.Errorf("Retry-After = %d, want 1 (rounded up, never 0)", got)
	}

	c.Set(windowStart.Add(Window)) // first instant of the next window
	if d := l.Allow(1, 2); !d.Allowed || d.Remaining != 1 {
		t.Fatalf("at next window start: %+v, want allowed in fresh window", d)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for d, want := range map[time.Duration]int{
		time.Nanosecond: 1, 999 * time.Millisecond: 1, time.Second: 1,
		time.Second + 1: 2, 30 * time.Second: 30, Window: 60, 0: 1,
	} {
		if got := retryAfterSeconds(d); got != want {
			t.Errorf("retryAfterSeconds(%s) = %d, want %d", d, got, want)
		}
	}
}

func TestConcurrentRequestsNeverExceedLimit(t *testing.T) {
	l := New(newClock(windowStart.Add(time.Second)).Now)
	const limit, workers, perWorker = 1000, 64, 50 // 3200 attempts per key
	var allowed [3]atomic.Int64

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWorker {
				key := int64((w + i) % 3)
				if l.Allow(key, limit).Allowed {
					allowed[key].Add(1)
				}
			}
		}()
	}
	wg.Wait()
	for key := range allowed {
		if got := allowed[key].Load(); got != limit {
			t.Errorf("key %d: %d requests allowed, want exactly %d", key, got, limit)
		}
	}
}

func TestConcurrentRequestsAcrossWindowRollover(t *testing.T) {
	c := newClock(windowStart.Add(Window - time.Millisecond))
	l := New(c.Now)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 16 {
				c.Add(time.Millisecond)
			}
			for range 100 {
				l.Allow(int64(i%4), 50)
			}
		}()
	}
	wg.Wait() // meaningful under -race: rollover and counting share state
}

// --- middleware ---

func serve(t *testing.T, l Allower, key *auth.APIKey, logs *bytes.Buffer) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Middleware(l, slog.New(slog.NewJSONHandler(logs, nil)))(next)
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	if key != nil {
		req = req.WithContext(auth.NewContext(req.Context(), *key))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMiddleware(t *testing.T) {
	var logs bytes.Buffer
	l := New(newClock(windowStart.Add(20 * time.Second)).Now)
	key := &auth.APIKey{ID: 7, ApplicationID: 70, PlanID: 1, RequestsPerMinute: 2}

	for i, wantRemaining := range []string{"1", "0"} {
		rec := serve(t, l, key, &logs)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, rec.Code)
		}
		if rec.Header().Get("X-RateLimit-Limit") != "2" || rec.Header().Get("X-RateLimit-Remaining") != wantRemaining {
			t.Errorf("request %d headers: %v", i+1, rec.Header())
		}
		if rec.Header().Get("Retry-After") != "" {
			t.Errorf("request %d: Retry-After set on an allowed request", i+1)
		}
	}

	for range 3 {
		rec := serve(t, l, key, &logs)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", rec.Code)
		}
		h := rec.Header()
		if h.Get("X-RateLimit-Limit") != "2" || h.Get("X-RateLimit-Remaining") != "0" || h.Get("Retry-After") != "40" {
			t.Errorf("429 headers: %v", h)
		}
		if !strings.Contains(rec.Body.String(), `"error":"rate limit exceeded"`) {
			t.Errorf("body = %s", rec.Body)
		}
	}
	if n := strings.Count(logs.String(), `"msg":"rate limit exceeded"`); n != 1 {
		t.Errorf("logged %d rate limit warnings, want 1 per key per window:\n%s", n, logs.String())
	}
	for _, want := range []string{`"api_key_id":7`, `"application_id":70`, `"limit":2`, `"retry_after_s":40`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log missing %s:\n%s", want, logs.String())
		}
	}
}

func TestMiddlewareWithoutAuthFailsClosed(t *testing.T) {
	var logs bytes.Buffer
	rec := serve(t, New(time.Now), nil, &logs)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// --- benchmarks ---

func BenchmarkAllow(b *testing.B) {
	l := New(time.Now)
	for b.Loop() {
		l.Allow(1, math.MaxInt32)
	}
}

func BenchmarkAllowParallel(b *testing.B) {
	l := New(time.Now)
	var next atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		key := next.Add(1) // one key per goroutine, as with many clients
		for pb.Next() {
			l.Allow(key, math.MaxInt32)
		}
	})
}

// BenchmarkMiddlewareOverhead compares a request through a no-op handler with and
// without the limiter; the difference is the limiter's per-request cost.
func BenchmarkMiddlewareOverhead(b *testing.B) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	key := auth.APIKey{ID: 1, RequestsPerMinute: math.MaxInt32}
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req = req.WithContext(auth.NewContext(context.Background(), key))

	for name, h := range map[string]http.Handler{
		"baseline":     next,
		"with_limiter": Middleware(New(time.Now), slog.New(slog.DiscardHandler))(next),
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				h.ServeHTTP(httptest.NewRecorder(), req)
			}
		})
	}
}
