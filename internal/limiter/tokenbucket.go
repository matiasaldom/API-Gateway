package limiter

import (
	"sync"
	"time"
)

// TokenBucket gives each key a bucket holding up to limit tokens that refills
// at limit tokens per Window (limit/60 per second for per-minute limits). A
// request takes one token; an empty bucket means 429.
//
// A client can still burst its full limit at once, but after that it gets the
// steady refill rate. Fixed windows allow up to 2x the limit around a window
// boundary (the end of one window plus the start of the next); a token bucket
// never admits more than limit + rate*t requests over any span t.
//
// Like Limiter, state is in memory and per gateway instance.
type TokenBucket struct {
	now func() time.Time

	mu        sync.Mutex
	buckets   map[int64]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens   float64
	last     time.Time // when tokens was last brought up to date
	warnedAt time.Time // last rejection reported as FirstDenied
}

// NewTokenBucket returns a TokenBucket using now as its clock (time.Now in production).
func NewTokenBucket(now func() time.Time) *TokenBucket {
	return &TokenBucket{now: now, buckets: make(map[int64]*bucket), lastSweep: now()}
}

// Allow takes one token from key's bucket and reports whether the request may proceed.
func (tb *TokenBucket) Allow(key int64, limit int) Decision {
	now := tb.now()
	capacity := float64(limit)
	perSecond := capacity / Window.Seconds()

	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.sweep(now)

	b, ok := tb.buckets[key]
	if !ok {
		b = &bucket{tokens: capacity, last: now}
		tb.buckets[key] = b
	} else if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * perSecond
		b.last = now
	}
	b.tokens = min(b.tokens, capacity) // also applies a lowered plan limit immediately

	if b.tokens >= 1 {
		b.tokens--
		return Decision{Allowed: true, Limit: limit, Remaining: int(b.tokens)}
	}
	// Report at most one rejection per key per Window, matching Limiter's
	// one log line per key per window for a client hammering past its limit.
	first := now.Sub(b.warnedAt) >= Window
	if first {
		b.warnedAt = now
	}
	return Decision{
		Allowed:     false,
		Limit:       limit,
		RetryAfter:  time.Duration((1 - b.tokens) / perSecond * float64(time.Second)),
		FirstDenied: first,
	}
}

// sweep drops buckets idle for a full Window: they have refilled completely,
// which is the same state a new bucket starts in. The caller holds tb.mu.
//
// ponytail: runs inline once per Window and scans every bucket under the lock;
// move it to a background goroutine if active keys reach the hundreds of thousands.
func (tb *TokenBucket) sweep(now time.Time) {
	if now.Sub(tb.lastSweep) < Window {
		return
	}
	tb.lastSweep = now
	for key, b := range tb.buckets {
		if now.Sub(b.last) >= Window {
			delete(tb.buckets, key)
		}
	}
}
