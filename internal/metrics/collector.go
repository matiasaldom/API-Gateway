// Package metrics collects per-request analytics events off the request path
// and serves aggregated analytics.
//
//	request → Emit (non-blocking) → buffered channel → worker → batch → EventWriter (PostgreSQL)
package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Event describes one completed request. Zero IDs mean the request was not authenticated.
type Event struct {
	OccurredAt    time.Time
	RequestID     string
	Method        string
	Route         string // matched route prefix, "" if none
	Status        int
	Latency       time.Duration
	APIKeyID      int64
	ApplicationID int64
	PlanID        int64
	Cache         string // "HIT", "MISS", or "" when the cache was not consulted
}

// EventWriter persists a batch of events. It must not retain the slice after returning.
type EventWriter interface {
	WriteEvents(ctx context.Context, events []Event) error
}

// Options tune the collector; zero values use the defaults noted.
type Options struct {
	BufferSize    int           // events queued before new ones are dropped (10000)
	BatchSize     int           // events per write (500)
	FlushInterval time.Duration // max time an event waits in a partial batch (1s)
	WriteTimeout  time.Duration // per-batch write deadline (5s)
}

// CollectorStats counts events by outcome since the collector started.
type CollectorStats struct {
	Written uint64 `json:"written"` // persisted
	Failed  uint64 `json:"failed"`  // lost to a write error
	Dropped uint64 `json:"dropped"` // rejected because the buffer was full or the collector closed
}

// Collector queues events and writes them in batches from a single background worker.
// Emit never blocks: when the buffer is full the event is dropped and counted,
// so a slow or failing database can never slow down or fail gateway traffic.
type Collector struct {
	writer EventWriter
	logger *slog.Logger
	opts   Options

	mu     sync.RWMutex // guards closed and the close of events
	closed bool
	events chan Event
	done   chan struct{}

	written, failed, dropped atomic.Uint64
}

// NewCollector starts the background worker. Call Close to flush and stop it.
func NewCollector(w EventWriter, logger *slog.Logger, opts Options) *Collector {
	if opts.BufferSize <= 0 {
		opts.BufferSize = 10000
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = time.Second
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 5 * time.Second
	}
	c := &Collector{
		writer: w,
		logger: logger,
		opts:   opts,
		events: make(chan Event, opts.BufferSize),
		done:   make(chan struct{}),
	}
	go c.run()
	return c
}

// Emit queues e without blocking and reports whether it was accepted.
func (c *Collector) Emit(e Event) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		c.dropped.Add(1)
		return false
	}
	select {
	case c.events <- e:
		return true
	default:
		c.dropped.Add(1)
		return false
	}
}

// Close stops accepting events, writes everything still queued, and waits for
// the worker to finish or ctx to expire. It is safe to call more than once.
func (c *Collector) Close(ctx context.Context) error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.events)
	}
	c.mu.Unlock()

	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("analytics flush incomplete: %w", ctx.Err())
	}
}

func (c *Collector) Stats() CollectorStats {
	return CollectorStats{Written: c.written.Load(), Failed: c.failed.Load(), Dropped: c.dropped.Load()}
}

func (c *Collector) run() {
	defer close(c.done)
	batch := make([]Event, 0, c.opts.BatchSize)
	ticker := time.NewTicker(c.opts.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case e, ok := <-c.events:
			if !ok { // closed: channel is drained, write the remainder and stop
				c.flush(batch)
				return
			}
			batch = append(batch, e)
			if len(batch) >= c.opts.BatchSize {
				c.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			c.flush(batch)
			batch = batch[:0]
		}
	}
}

// flush writes one batch. Errors and panics are contained here: the batch is
// counted as failed and dropped, and the worker keeps going.
func (c *Collector) flush(batch []Event) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.opts.WriteTimeout)
	defer cancel()

	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("event writer panicked: %v", p)
			}
		}()
		return c.writer.WriteEvents(ctx, batch)
	}()
	if err != nil {
		c.failed.Add(uint64(len(batch)))
		c.logger.Warn("analytics batch write failed", "events", len(batch), "error", err.Error())
		return
	}
	c.written.Add(uint64(len(batch)))
}
