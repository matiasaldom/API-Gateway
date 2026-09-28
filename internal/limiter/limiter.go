// Package limiter enforces per-API-key request limits, with fixed one-minute
// windows (Limiter, the default) or token buckets (TokenBucket).
package limiter

import (
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"api-gateway/internal/auth"
	"api-gateway/internal/httpx"
	"api-gateway/internal/requestid"
)

// Window is the fixed window length. Windows are aligned to the clock
// (12:00:00.000–12:00:59.999, 12:01:00.000–…), so every key rolls over at the same instant.
const Window = time.Minute

// Canonical forms of X-RateLimit-Limit / X-RateLimit-Remaining (header names are case-insensitive).
var (
	headerLimit     = http.CanonicalHeaderKey("X-RateLimit-Limit")
	headerRemaining = http.CanonicalHeaderKey("X-RateLimit-Remaining")
)

// Limiter counts requests per key in the current window, in memory only:
// limits apply per gateway instance and reset on restart.
//
// Because windows are clock-aligned, a rollover makes every counter stale at
// once, so the whole map is cleared instead of tracking a window per key. Memory
// is bounded by the number of keys active in the current window.
type Limiter struct {
	now func() time.Time

	// ponytail: one mutex over all keys; the critical section is a map lookup and
	// an increment. Shard by key if BenchmarkAllowParallel shows contention.
	mu      sync.Mutex
	current int64         // index of the active window (unix nanos / Window)
	counts  map[int64]int // requests per key in the active window, capped at limit+1
}

// Decision is the outcome of one Allow call.
type Decision struct {
	Allowed    bool
	Limit      int
	Remaining  int           // requests left in this window after this one
	RetryAfter time.Duration // time until the window resets
	// FirstDenied is true only for a key's first rejection in a window,
	// so a client hammering past its limit produces one log line, not thousands.
	FirstDenied bool
}

// New returns a Limiter using now as its clock (time.Now in production).
func New(now func() time.Time) *Limiter {
	return &Limiter{now: now, counts: make(map[int64]int)}
}

// Allow counts one request for key against limit and reports whether it may proceed.
func (l *Limiter) Allow(key int64, limit int) Decision {
	now := l.now()
	idx := now.UnixNano() / int64(Window)

	l.mu.Lock()
	if idx != l.current {
		clear(l.counts)
		l.current = idx
	}
	n := l.counts[key] // requests already counted before this one
	if n <= limit {
		l.counts[key] = n + 1 // stop at limit+1: enough to detect the first rejection
	}
	l.mu.Unlock()

	return Decision{
		Allowed:     n < limit,
		Limit:       limit,
		Remaining:   max(0, limit-n-1),
		RetryAfter:  time.Unix(0, (idx+1)*int64(Window)).Sub(now),
		FirstDenied: n == limit,
	}
}

// Allower is a rate-limiting algorithm: Limiter (fixed window) or TokenBucket.
type Allower interface {
	Allow(key int64, limit int) Decision
}

// Middleware limits each request by its authenticated API key's plan limit.
// It must run after auth.Middleware. Every limited response carries
// X-RateLimit-Limit and X-RateLimit-Remaining; a 429 also carries Retry-After.
func Middleware(l Allower, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := auth.FromContext(r.Context())
			if !ok {
				// Wiring bug, not a client error: fail closed.
				logger.ErrorContext(r.Context(), "rate limiter ran without an authenticated api key",
					"request_id", requestid.FromContext(r.Context()))
				httpx.Error(w, r, http.StatusInternalServerError, "internal error")
				return
			}

			d := l.Allow(key.ID, key.RequestsPerMinute)
			h := w.Header()
			// Direct assignment with pre-canonicalized keys skips Header.Set's per-call canonicalization.
			h[headerLimit] = []string{strconv.Itoa(d.Limit)}
			h[headerRemaining] = []string{strconv.Itoa(d.Remaining)}
			if d.Allowed {
				next.ServeHTTP(w, r)
				return
			}

			retryAfter := retryAfterSeconds(d.RetryAfter)
			h.Set("Retry-After", strconv.Itoa(retryAfter))
			if d.FirstDenied {
				logger.WarnContext(r.Context(), "rate limit exceeded",
					"request_id", requestid.FromContext(r.Context()),
					"api_key_id", key.ID,
					"application_id", key.ApplicationID,
					"plan_id", key.PlanID,
					"limit", d.Limit,
					"window", Window.String(),
					"retry_after_s", retryAfter,
				)
			}
			httpx.Error(w, r, http.StatusTooManyRequests, "rate limit exceeded")
		})
	}
}

// retryAfterSeconds rounds up so clients never retry before the window resets.
func retryAfterSeconds(d time.Duration) int {
	return max(1, int((d+time.Second-1)/time.Second))
}
