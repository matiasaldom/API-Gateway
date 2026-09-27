package storage

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"api-gateway/internal/metrics"
)

var eventColumns = []string{
	"occurred_at", "request_id", "method", "route", "status", "latency_us",
	"api_key_id", "application_id", "plan_id", "cache_status",
}

// WriteEvents implements metrics.EventWriter with a single COPY per batch.
func (db *DB) WriteEvents(ctx context.Context, events []metrics.Event) error {
	_, err := db.pool.CopyFrom(ctx, pgx.Identifier{"request_events"}, eventColumns,
		pgx.CopyFromSlice(len(events), func(i int) ([]any, error) {
			e := events[i]
			return []any{
				e.OccurredAt, e.RequestID, e.Method, nullString(e.Route), int16(e.Status),
				int32(min(e.Latency.Microseconds(), math.MaxInt32)),
				nullID(e.APIKeyID), nullID(e.ApplicationID), nullID(e.PlanID), nullString(e.Cache),
			}, nil
		}))
	if err != nil {
		return fmt.Errorf("write request events: %w", err)
	}
	return nil
}

// trafficColumns aggregates request_events rows into metrics.Traffic, in field order.
const trafficColumns = `
	count(*),
	count(*) filter (where status >= 500),
	count(*) filter (where status = 504),
	count(*) filter (where status = 429),
	count(*) filter (where cache_status = 'HIT'),
	count(*) filter (where cache_status = 'MISS'),
	coalesce(percentile_cont(0.50) within group (order by latency_us), 0) / 1000.0::float8,
	coalesce(percentile_cont(0.95) within group (order by latency_us), 0) / 1000.0::float8,
	coalesce(percentile_cont(0.99) within group (order by latency_us), 0) / 1000.0::float8`

func trafficDest(t *metrics.Traffic) []any {
	return []any{&t.Requests, &t.Errors, &t.Timeouts, &t.RateLimited, &t.CacheHits, &t.CacheMisses,
		&t.LatencyMS.P50, &t.LatencyMS.P95, &t.LatencyMS.P99}
}

func (db *DB) Summary(ctx context.Context, since time.Time) (metrics.Traffic, error) {
	var t metrics.Traffic
	err := db.pool.QueryRow(ctx, `select `+trafficColumns+` from request_events where occurred_at >= $1`, since).
		Scan(trafficDest(&t)...)
	if err != nil {
		return metrics.Traffic{}, fmt.Errorf("analytics summary: %w", err)
	}
	return t, nil
}

func (db *DB) Routes(ctx context.Context, since time.Time) ([]metrics.RouteStats, error) {
	rows, err := db.pool.Query(ctx, `
		select route, `+trafficColumns+`
		from request_events
		where occurred_at >= $1
		group by route
		order by count(*) desc, route nulls last`, since)
	if err != nil {
		return nil, fmt.Errorf("analytics routes: %w", err)
	}
	stats, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (metrics.RouteStats, error) {
		var s metrics.RouteStats
		return s, row.Scan(append([]any{&s.Route}, trafficDest(&s.Traffic)...)...)
	})
	if err != nil {
		return nil, fmt.Errorf("analytics routes: %w", err)
	}
	return stats, nil
}

func (db *DB) Accounts(ctx context.Context, since time.Time) (metrics.Accounts, error) {
	var out metrics.Accounts

	rows, err := db.pool.Query(ctx, `
		select e.application_id, a.name, u.email, p.name,
		       count(*),
		       count(*) filter (where e.status >= 500),
		       count(*) filter (where e.status = 429)
		from request_events e
		left join applications a on a.id = e.application_id
		left join users u on u.id = a.user_id
		left join plans p on p.id = a.plan_id
		where e.occurred_at >= $1 and e.application_id is not null
		group by e.application_id, a.name, u.email, p.name
		order by count(*) desc, e.application_id`, since)
	if err != nil {
		return out, fmt.Errorf("analytics applications: %w", err)
	}
	out.Applications, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (metrics.ApplicationStats, error) {
		var s metrics.ApplicationStats
		return s, row.Scan(&s.ApplicationID, &s.Application, &s.OwnerEmail, &s.Plan, &s.Requests, &s.Errors, &s.RateLimited)
	})
	if err != nil {
		return out, fmt.Errorf("analytics applications: %w", err)
	}

	// Per-minute buckets use the same clock-aligned minutes as the rate limiter.
	rows, err = db.pool.Query(ctx, `
		with per_minute as (
			select api_key_id, application_id, date_trunc('minute', occurred_at) as minute,
			       count(*) as requests,
			       count(*) filter (where status >= 500) as errors,
			       count(*) filter (where status = 429) as rate_limited
			from request_events
			where occurred_at >= $1 and api_key_id is not null
			group by api_key_id, application_id, minute
		)
		select m.api_key_id, m.application_id,
		       sum(m.requests)::bigint, sum(m.errors)::bigint, sum(m.rate_limited)::bigint,
		       max(m.requests), p.requests_per_minute::bigint,
		       max(m.requests)::float8 / p.requests_per_minute
		from per_minute m
		left join applications a on a.id = m.application_id
		left join plans p on p.id = a.plan_id
		group by m.api_key_id, m.application_id, p.requests_per_minute
		order by sum(m.requests) desc, m.api_key_id`, since)
	if err != nil {
		return out, fmt.Errorf("analytics api keys: %w", err)
	}
	out.APIKeys, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (metrics.APIKeyStats, error) {
		var s metrics.APIKeyStats
		return s, row.Scan(&s.APIKeyID, &s.ApplicationID, &s.Requests, &s.Errors, &s.RateLimited,
			&s.PeakPerMinute, &s.LimitPerMinute, &s.Utilization)
	})
	if err != nil {
		return out, fmt.Errorf("analytics api keys: %w", err)
	}
	return out, nil
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}
