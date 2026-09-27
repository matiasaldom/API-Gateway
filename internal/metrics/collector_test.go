package metrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeWriter records batches. It can fail, panic, or block on demand.
type fakeWriter struct {
	mu      sync.Mutex
	batches [][]Event
	calls   int
	failOn  map[int]error // call number (1-based) → error
	panicOn map[int]bool
	block   chan struct{} // when non-nil, writes wait for it to close (or ctx)
}

func (f *fakeWriter) WriteEvents(ctx context.Context, events []Event) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.panicOn[f.calls] {
		panic("writer exploded")
	}
	if err := f.failOn[f.calls]; err != nil {
		return err
	}
	f.batches = append(f.batches, slices.Clone(events)) // the collector reuses its slice
	return nil
}

func (f *fakeWriter) sizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s []int
	for _, b := range f.batches {
		s = append(s, len(b))
	}
	return s
}

func (f *fakeWriter) all() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Event
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func closeNow(t *testing.T, c *Collector) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func ev(i int) Event { return Event{RequestID: fmt.Sprint(i), Method: "GET", Status: 200} }

func TestBatching(t *testing.T) {
	w := &fakeWriter{}
	c := NewCollector(w, discard, Options{BatchSize: 10, FlushInterval: time.Hour})
	for i := range 25 {
		c.Emit(ev(i))
	}
	waitFor(t, "two full batches", func() bool { return len(w.sizes()) == 2 })
	if got := w.sizes(); !slices.Equal(got, []int{10, 10}) {
		t.Fatalf("batches before close = %v, want [10 10] (partial batch must wait)", got)
	}
	closeNow(t, c)
	if got := w.sizes(); !slices.Equal(got, []int{10, 10, 5}) {
		t.Fatalf("batches after close = %v, want [10 10 5]", got)
	}
	for i, e := range w.all() {
		if e.RequestID != fmt.Sprint(i) {
			t.Fatalf("event %d out of order: %s", i, e.RequestID)
		}
	}
	if s := c.Stats(); s.Written != 25 || s.Failed != 0 || s.Dropped != 0 {
		t.Errorf("stats = %+v", s)
	}
}

func TestPartialBatchFlushedOnInterval(t *testing.T) {
	w := &fakeWriter{}
	c := NewCollector(w, discard, Options{BatchSize: 100, FlushInterval: 10 * time.Millisecond})
	defer closeNow(t, c)
	for i := range 3 {
		c.Emit(ev(i))
	}
	waitFor(t, "interval flush", func() bool { return len(w.all()) == 3 })
	if got := w.sizes(); !slices.Equal(got, []int{3}) {
		t.Errorf("batches = %v, want [3]", got)
	}
}

func TestCloseFlushesPendingEvents(t *testing.T) {
	w := &fakeWriter{}
	c := NewCollector(w, discard, Options{BatchSize: 1000, FlushInterval: time.Hour})
	for i := range 42 {
		c.Emit(ev(i))
	}
	closeNow(t, c)
	if n := len(w.all()); n != 42 {
		t.Fatalf("written after Close = %d, want 42", n)
	}

	// After Close: Emit is refused without panicking, and Close is idempotent.
	if c.Emit(ev(99)) {
		t.Error("Emit accepted an event after Close")
	}
	closeNow(t, c)
	if s := c.Stats(); s.Dropped != 1 || s.Written != 42 {
		t.Errorf("stats = %+v, want 42 written, 1 dropped", s)
	}
}

func TestCloseRespectsDeadline(t *testing.T) {
	w := &fakeWriter{block: make(chan struct{})}
	defer close(w.block)
	c := NewCollector(w, discard, Options{BatchSize: 1, WriteTimeout: time.Hour})
	c.Emit(ev(1))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close with a stuck writer = %v, want deadline exceeded", err)
	}
	if time.Since(start) > time.Second {
		t.Error("Close did not return at its deadline")
	}
}

func TestWriterFailuresAreIsolated(t *testing.T) {
	w := &fakeWriter{
		failOn:  map[int]error{1: errors.New("connection refused")},
		panicOn: map[int]bool{2: true},
	}
	c := NewCollector(w, discard, Options{BatchSize: 2, FlushInterval: time.Hour})
	for i := range 6 { // three batches: error, panic, success
		if !c.Emit(ev(i)) {
			t.Fatal("Emit refused while the writer was failing")
		}
	}
	closeNow(t, c)
	if s := c.Stats(); s.Failed != 4 || s.Written != 2 {
		t.Errorf("stats = %+v, want 4 failed (error + panic), 2 written after recovery", s)
	}
	if got := w.all(); len(got) != 2 || got[0].RequestID != "4" {
		t.Errorf("written = %v, want the third batch", got)
	}
}

func TestEmitNeverBlocksOnStuckWriter(t *testing.T) {
	w := &fakeWriter{block: make(chan struct{})}
	defer close(w.block)
	c := NewCollector(w, discard, Options{BufferSize: 10, BatchSize: 1, WriteTimeout: time.Hour})

	start := time.Now()
	for i := range 10_000 {
		c.Emit(ev(i))
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("10k Emits took %s with a stuck writer; Emit must not block", elapsed)
	}
	// At most the buffer plus the one batch in flight can be held; the rest are dropped.
	if s := c.Stats(); s.Dropped < 10_000-11 {
		t.Errorf("dropped = %d, want ≥ %d", s.Dropped, 10_000-11)
	}
}

func TestConcurrentEmit(t *testing.T) {
	w := &fakeWriter{}
	c := NewCollector(w, discard, Options{BufferSize: 20_000, BatchSize: 64, FlushInterval: time.Millisecond})
	const goroutines, each = 50, 200

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				if !c.Emit(ev(g*each + i)) {
					t.Error("event dropped with a large buffer")
				}
			}
		}()
	}
	// Close concurrently with a second wave of emitters: no panics, no lost accepted events.
	var accepted sync.WaitGroup
	var late [goroutines]bool
	for g := range goroutines {
		accepted.Add(1)
		go func() {
			defer accepted.Done()
			late[g] = c.Emit(Event{RequestID: fmt.Sprintf("late-%d", g)})
		}()
	}
	wg.Wait()
	closeNow(t, c)
	accepted.Wait()

	want := goroutines * each
	for _, ok := range late {
		if ok {
			want++
		}
	}
	seen := map[string]bool{}
	for _, e := range w.all() {
		if seen[e.RequestID] {
			t.Fatalf("event %s written twice", e.RequestID)
		}
		seen[e.RequestID] = true
	}
	if len(seen) != want {
		t.Errorf("written %d distinct events, want every accepted one (%d)", len(seen), want)
	}
}
