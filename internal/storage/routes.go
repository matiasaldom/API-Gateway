package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"api-gateway/internal/config"
	"api-gateway/internal/routing"
)

// ErrNotFound is returned when a record addressed by ID does not exist.
var ErrNotFound = errors.New("not found")

// RouteRecord is a route as stored in the routes table.
type RouteRecord struct {
	ID        int64
	Prefix    string
	Upstream  string
	CacheTTL  time.Duration
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Config converts the record into routing input.
func (r RouteRecord) Config() config.Route {
	return config.Route{Prefix: r.Prefix, Upstream: r.Upstream, CacheTTL: r.CacheTTL}
}

// RouteConfigs converts records into routing input.
func RouteConfigs(records []RouteRecord) []config.Route {
	out := make([]config.Route, len(records))
	for i, r := range records {
		out[i] = r.Config()
	}
	return out
}

const routeColumns = `id, prefix, upstream, cache_ttl_ms, created_at, updated_at`

func scanRoute(row pgx.Row) (RouteRecord, error) {
	var r RouteRecord
	var ttlMS int64
	err := row.Scan(&r.ID, &r.Prefix, &r.Upstream, &ttlMS, &r.CreatedAt, &r.UpdatedAt)
	r.CacheTTL = time.Duration(ttlMS) * time.Millisecond
	return r, err
}

// SyncRoutes makes the routes table hold exactly the given prefixes: new prefixes
// are inserted with their configured values, prefixes no longer declared are
// deleted, and existing rows keep their stored upstream and cache TTL (which
// may have been edited through the admin API). It returns the resulting routes.
func (db *DB) SyncRoutes(ctx context.Context, declared []routing.Route) ([]RouteRecord, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("sync routes: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	prefixes := make([]string, len(declared))
	for i, rt := range declared {
		prefixes[i] = rt.Prefix
		_, err := tx.Exec(ctx,
			`insert into routes (prefix, upstream, cache_ttl_ms) values ($1, $2, $3)
			 on conflict (prefix) do nothing`,
			rt.Prefix, rt.Target.String(), rt.CacheTTL.Milliseconds())
		if err != nil {
			return nil, fmt.Errorf("sync route %q: %w", rt.Prefix, err)
		}
	}
	if _, err := tx.Exec(ctx, `delete from routes where prefix <> all($1)`, prefixes); err != nil {
		return nil, fmt.Errorf("sync routes: remove undeclared: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("sync routes: %w", err)
	}
	return db.ListRoutes(ctx)
}

// ListRoutes returns all routes ordered by prefix.
func (db *DB) ListRoutes(ctx context.Context) ([]RouteRecord, error) {
	rows, err := db.pool.Query(ctx, `select `+routeColumns+` from routes order by prefix`)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	routes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RouteRecord, error) { return scanRoute(row) })
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	return routes, nil
}

func (db *DB) GetRoute(ctx context.Context, id int64) (RouteRecord, error) {
	r, err := scanRoute(db.pool.QueryRow(ctx, `select `+routeColumns+` from routes where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return RouteRecord{}, ErrNotFound
	}
	if err != nil {
		return RouteRecord{}, fmt.Errorf("get route: %w", err)
	}
	return r, nil
}

// UpdateRoute stores a route's upstream and cache TTL.
func (db *DB) UpdateRoute(ctx context.Context, id int64, upstream string, cacheTTL time.Duration) (RouteRecord, error) {
	r, err := scanRoute(db.pool.QueryRow(ctx,
		`update routes set upstream = $2, cache_ttl_ms = $3, updated_at = now()
		 where id = $1 returning `+routeColumns,
		id, upstream, cacheTTL.Milliseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return RouteRecord{}, ErrNotFound
	}
	if err != nil {
		return RouteRecord{}, fmt.Errorf("update route: %w", err)
	}
	return r, nil
}
