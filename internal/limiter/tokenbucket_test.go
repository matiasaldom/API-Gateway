package limiter

import (
	"bytes"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"api-gateway/internal/auth"
)

// allowN calls Allow n times and returns how many were allowed.
func allowN(a Allower, key int64, limit, n int) int {
	allowed := 0
	for range n {
		if a.Allow(key, limit).Allowed {
			allowed++
		}
	}
	return allowed
}

func TestTokenBucketBurstThenRefill(t *testing.T) {
	c := newClock(windowStart)
	tb := NewTokenBucket(c.Now)
	const limit = 60 // one token per second

	if got := allowN(tb, 1, limit, 100); got != limit {
		t.Fatalf("initial burst allowed %d, want %d", got, limit)
	}
	d := tb.Allow(1, limit)
	if d.Allowed || d.Remaining != 0 || d.RetryAfter != time.Second {
		t.Fatalf("empty bucket: %+v, want denied with RetryAfter 1s", d)
	}

	c.Add(time.Second)
	if got := allowN(tb, 1, limit, 5); got != 1 {
		t.Errorf("after 1s allowed %d, want 1 (one token refilled)", got)
	}

	c.Add(10 * time.Minute)
	if got := allowN(tb, 1, limit, 100); got != limit {
		t.Errorf("after a long idle allowed %d, want %d (refill is capped at the limit)", got, limit)
	}
}

// Fixed windows let a client spend its limit at the end of one window and again
// at the start of the next. A token bucket only refills what time has earned.
func TestTokenBucketHasNoWindowBoundaryBurst(t *testing.T) {
	const limit = 60
	for name, tc := range map[string]struct {
		newAllower func(func() time.Time) Allower
		want       int
	}{
		"fixed window": {func(now func() time.Time) Allower { return New(now) }, 2 * limit},
		"token bucket": {func(now func() time.Time) Allower { return NewTokenBucket(now) }, limit + 2},
	} {
		t.Run(name, func(t *testing.T) {
			c := newClock(windowStart.Add(Window - time.Second)) // 1s before the boundary
			a := tc.newAllower(c.Now)
			got := allowN(a, 1, limit, 1000)
			c.Add(2 * time.Second) // 1s after the boundary
			got += allowN(a, 1, limit, 1000)
			if got != tc.want {
				t.Errorf("allowed %d requests within 2s, want %d", got, tc.want)
			}
		})
	}
}

func TestTokenBucketLoweredLimitAppliesImmediately(t *testing.T) {
	tb := NewTokenBucket(newClock(windowStart).Now)
	tb.Allow(1, 1000) // bucket now holds 999 tokens
	if got := allowN(tb, 1, 10, 100); got != 10 {
		t.Errorf("after downgrade to 10/min allowed %d, want 10", got)
	}
}

func TestTokenBucketKeysAreIndependent(t *testing.T) {
	tb := NewTokenBucket(newClock(windowStart).Now)
	allowN(tb, 1, 5, 5)
	if !tb.Allow(2, 5).Allowed {
		t.Error("key 2 limited by key 1's usage")
	}
}

func TestTokenBucketSweepsIdleBuckets(t *testing.T) {
	c := newClock(windowStart)
	tb := NewTokenBucket(c.Now)
	for key := range int64(3) {
		tb.Allow(key, 10)
	}
	c.Add(Window)
	tb.Allow(99, 10)
	tb.mu.Lock()
	n := len(tb.buckets)
	tb.mu.Unlock()
	if n != 1 {
		t.Errorf("%d buckets after a window of inactivity, want 1 (only the new key)", n)
	}
}

func TestTokenBucketConcurrentRequestsNeverExceedLimit(t *testing.T) {
	tb := NewTokenBucket(newClock(windowStart).Now) // frozen clock: no refill
	const limit, workers, perWorker = 500, 64, 50
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed.Add(int64(allowN(tb, 1, limit, perWorker)))
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != limit {
		t.Errorf("%d requests allowed, want exactly %d", got, limit)
	}
}

func TestTokenBucketMiddleware(t *testing.T) {
	var logs bytes.Buffer
	c := newClock(windowStart)
	tb := NewTokenBucket(c.Now)
	key := &auth.APIKey{ID: 7, RequestsPerMinute: 2}

	for range 2 {
		if rec := serve(t, tb, key, &logs); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	for range 3 {
		rec := serve(t, tb, key, &logs)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "30" {
			t.Fatalf("status = %d, Retry-After = %q; want 429 and 30 (2/min refills one token every 30s)",
				rec.Code, rec.Header().Get("Retry-After"))
		}
	}
	if n := strings.Count(logs.String(), `"msg":"rate limit exceeded"`); n != 1 {
		t.Errorf("logged %d rate limit warnings, want 1 per key per minute", n)
	}
}

func BenchmarkTokenBucketAllow(b *testing.B) {
	tb := NewTokenBucket(time.Now)
	for b.Loop() {
		tb.Allow(1, math.MaxInt32)
	}
}
