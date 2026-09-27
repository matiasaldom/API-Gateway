package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

func newTestCache(maxBytes int) (*Cache, *clock) {
	c := &clock{t: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	return New(maxBytes, c.Now), c
}

func resp(body string) Response {
	return Response{Status: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(body)}
}

func key(path string, apiKeyID int64) Key {
	return Key{Method: http.MethodGet, Path: path, APIKeyID: apiKeyID}
}

func TestPutGet(t *testing.T) {
	c, _ := newTestCache(1 << 20)
	if _, ok := c.Get(key("/a", 1)); ok {
		t.Fatal("empty cache returned a hit")
	}
	if !c.Put(key("/a", 1), resp(`{"a":1}`), time.Minute) {
		t.Fatal("Put refused")
	}
	got, ok := c.Get(key("/a", 1))
	if !ok || string(got.Body) != `{"a":1}` || got.Status != 200 || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("Get = %+v, %v", got, ok)
	}

	c.Put(key("/a", 1), resp(`{"a":2}`), time.Minute) // replace
	if got, _ := c.Get(key("/a", 1)); string(got.Body) != `{"a":2}` {
		t.Errorf("after replace: %s", got.Body)
	}
	if s := c.Stats(); s.Hits != 2 || s.Misses != 1 || s.Entries != 1 {
		t.Errorf("stats = %+v, want 2 hits, 1 miss, 1 entry", s)
	}
}

func TestExpiration(t *testing.T) {
	c, clk := newTestCache(1 << 20)
	c.Put(key("/a", 1), resp("x"), 10*time.Second)

	clk.Add(10*time.Second - time.Nanosecond)
	if _, ok := c.Get(key("/a", 1)); !ok {
		t.Fatal("entry expired early")
	}
	clk.Add(time.Nanosecond) // exactly at expiry
	if _, ok := c.Get(key("/a", 1)); ok {
		t.Fatal("entry served at its expiry instant")
	}
	if s := c.Stats(); s.Entries != 0 || s.Bytes != 0 {
		t.Errorf("expired entry not removed on read: %+v", s)
	}
}

func TestSweepRemovesExpiredEntries(t *testing.T) {
	c, clk := newTestCache(1 << 20)
	c.Put(key("/short", 1), resp("x"), time.Second)
	c.Put(key("/long", 1), resp("y"), time.Hour)
	clk.Add(time.Minute)

	if n := c.Sweep(); n != 1 {
		t.Errorf("Sweep removed %d entries, want 1", n)
	}
	if s := c.Stats(); s.Entries != 1 {
		t.Errorf("entries after sweep = %d, want 1", s.Entries)
	}
	if _, ok := c.Get(key("/long", 1)); !ok {
		t.Error("unexpired entry was swept")
	}
}

func TestRunJanitorStopsWithContext(t *testing.T) {
	c, clk := newTestCache(1 << 20)
	c.Put(key("/a", 1), resp("x"), time.Second)
	clk.Add(time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.RunJanitor(ctx, time.Millisecond); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for c.Stats().Entries != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if c.Stats().Entries != 0 {
		t.Error("janitor did not remove the expired entry")
	}
}

func TestByteLimit(t *testing.T) {
	c, clk := newTestCache(100)
	if !c.Put(key("/a", 1), resp(strings.Repeat("x", 60)), time.Second) {
		t.Fatal("first entry refused")
	}
	if c.Put(key("/b", 1), resp(strings.Repeat("y", 60)), time.Second) {
		t.Fatal("entry exceeding the byte limit was admitted")
	}
	if !c.Put(key("/a", 1), resp(strings.Repeat("z", 60)), time.Second) {
		t.Fatal("replacing an entry must reuse its space")
	}
	clk.Add(time.Minute)
	c.Sweep()
	if !c.Put(key("/b", 1), resp(strings.Repeat("y", 60)), time.Second) {
		t.Fatal("space not reclaimed after sweep")
	}
}

func TestKeyFor(t *testing.T) {
	base := httptest.NewRequest(http.MethodGet, "/albums/1?sort=asc&page=2", nil)
	want := Key{Method: "GET", Path: "/albums/1", RawQuery: "sort=asc&page=2", APIKeyID: 7}
	if got := KeyFor(base, 7); got != want {
		t.Fatalf("KeyFor = %+v, want %+v", got, want)
	}
	if KeyFor(base, 7) != KeyFor(httptest.NewRequest(http.MethodGet, "/albums/1?sort=asc&page=2", nil), 7) {
		t.Error("identical requests produced different keys")
	}

	gzip := httptest.NewRequest(http.MethodGet, "/albums/1?sort=asc&page=2", nil)
	gzip.Header.Set("Accept-Encoding", "gzip")
	for name, other := range map[string]Key{
		"method":          KeyFor(httptest.NewRequest(http.MethodHead, "/albums/1?sort=asc&page=2", nil), 7),
		"path":            KeyFor(httptest.NewRequest(http.MethodGet, "/albums/2?sort=asc&page=2", nil), 7),
		"query":           KeyFor(httptest.NewRequest(http.MethodGet, "/albums/1?sort=desc&page=2", nil), 7),
		"no query":        KeyFor(httptest.NewRequest(http.MethodGet, "/albums/1", nil), 7),
		"api key":         KeyFor(base, 8),
		"accept-encoding": KeyFor(gzip, 7),
	} {
		if other == want {
			t.Errorf("keys differing only in %s are equal", name)
		}
	}
}

func TestConcurrentAccess(t *testing.T) {
	c, clk := newTestCache(1 << 20)
	var wg sync.WaitGroup
	for w := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				k := key(fmt.Sprintf("/item/%d", i%20), int64(w%4))
				if r, ok := c.Get(k); ok && string(r.Body) != k.Path {
					t.Errorf("got %q for %s", r.Body, k.Path)
					return
				}
				c.Put(k, resp(k.Path), time.Second)
				if i%50 == 0 {
					clk.Add(300 * time.Millisecond)
					c.Sweep()
				}
				_ = c.Stats()
			}
		}()
	}
	wg.Wait()
	s := c.Stats()
	if s.Hits+s.Misses != 32*200 {
		t.Errorf("hits+misses = %d, want %d", s.Hits+s.Misses, 32*200)
	}
}

func TestMetricsHandler(t *testing.T) {
	c, _ := newTestCache(1 << 20)
	c.Get(key("/a", 1))
	c.Put(key("/a", 1), resp("x"), time.Minute)
	c.Get(key("/a", 1))
	c.Get(key("/a", 1))

	rec := httptest.NewRecorder()
	c.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"cache_hits 2\n", "cache_misses 1\n", "cache_entries 1\n", "# TYPE cache_hits counter"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics missing %q:\n%s", want, rec.Body)
		}
	}
}
