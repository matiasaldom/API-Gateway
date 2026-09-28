// Package storage is the PostgreSQL persistence layer.
package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"api-gateway/internal/auth"
)

// connectTimeout bounds startup; an unreachable database fails fast instead of hanging.
const connectTimeout = 5 * time.Second

type DB struct {
	pool *pgxpool.Pool
}

// Open creates a connection pool and verifies the database is reachable.
// Pool sizing is tuned through the URL (e.g. ?pool_max_conns=20&pool_max_conn_idle_time=5m).
func Open(ctx context.Context, databaseURL string) (*DB, error) {
	if databaseURL == "" {
		return nil, errors.New("database URL is required")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Parse errors can echo the URL, which may contain a password.
		return nil, errors.New("invalid database URL")
	}

	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (db *DB) Close() { db.pool.Close() }

// Ping reports whether the database is reachable (used by the /ready endpoint).
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

// LookupAPIKey implements auth.KeyStore.
func (db *DB) LookupAPIKey(ctx context.Context, hash []byte) (auth.APIKey, error) {
	var k auth.APIKey
	err := db.pool.QueryRow(ctx, `
		select k.id, k.application_id, a.user_id, a.plan_id, p.requests_per_minute, k.status, k.key_hash
		from api_keys k
		join applications a on a.id = k.application_id
		join plans p on p.id = a.plan_id
		where k.key_hash = $1`, hash,
	).Scan(&k.ID, &k.ApplicationID, &k.UserID, &k.PlanID, &k.RequestsPerMinute, &k.Status, &k.Hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.APIKey{}, auth.ErrKeyNotFound
	}
	if err != nil {
		return auth.APIKey{}, fmt.Errorf("lookup api key: %w", err)
	}
	return k, nil
}

// CreateAPIKey issues a new active key for an application. The returned raw key
// is the only copy; only its hash is persisted.
func (db *DB) CreateAPIKey(ctx context.Context, applicationID int64) (raw string, id int64, err error) {
	raw, hash, err := auth.GenerateKey()
	if err != nil {
		return "", 0, fmt.Errorf("generate api key: %w", err)
	}
	err = db.pool.QueryRow(ctx,
		`insert into api_keys (application_id, key_hash) values ($1, $2) returning id`,
		applicationID, hash,
	).Scan(&id)
	if err != nil {
		return "", 0, fmt.Errorf("insert api key: %w", err)
	}
	return raw, id, nil
}

// RevokeAPIKey marks a key revoked. Revoking an already-revoked key is a no-op.
func (db *DB) RevokeAPIKey(ctx context.Context, id int64) error {
	tag, err := db.pool.Exec(ctx,
		`update api_keys set status = 'revoked', revoked_at = coalesce(revoked_at, now()) where id = $1`, id)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrKeyNotFound
	}
	return nil
}
