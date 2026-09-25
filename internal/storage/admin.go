package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"api-gateway/internal/auth"
)

// KeyInfo is an API key as shown to operators. It never carries the key or its hash.
type KeyInfo struct {
	ID            int64
	Status        auth.Status
	ApplicationID int64
	Application   string
	OwnerEmail    string
	Plan          string
	CreatedAt     time.Time
	RevokedAt     *time.Time
}

// EnsureApplication returns the ID of the application named appName owned by email,
// creating the plan, user, and application if they don't exist. planName only
// applies when the application is created; an existing application keeps its plan.
func (db *DB) EnsureApplication(ctx context.Context, email, appName, planName string) (int64, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var userID, appID int64
	// "do update" (a no-op write) makes "returning" yield the existing row's ID on conflict.
	err = tx.QueryRow(ctx,
		`insert into users (email) values ($1)
		 on conflict ((lower(email))) do update set email = users.email
		 returning id`, email,
	).Scan(&userID)
	if err != nil {
		return 0, fmt.Errorf("ensure user: %w", err)
	}

	err = tx.QueryRow(ctx,
		`select id from applications where user_id = $1 and name = $2 order by id limit 1`, userID, appName,
	).Scan(&appID)
	if errors.Is(err, pgx.ErrNoRows) {
		var planID int64
		err = tx.QueryRow(ctx,
			`insert into plans (name) values ($1)
			 on conflict (name) do update set name = excluded.name
			 returning id`, planName,
		).Scan(&planID)
		if err != nil {
			return 0, fmt.Errorf("ensure plan: %w", err)
		}
		err = tx.QueryRow(ctx,
			`insert into applications (user_id, plan_id, name) values ($1, $2, $3) returning id`,
			userID, planID, appName,
		).Scan(&appID)
	}
	if err != nil {
		return 0, fmt.Errorf("ensure application: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return appID, nil
}

// ListAPIKeys returns all API keys, oldest first.
func (db *DB) ListAPIKeys(ctx context.Context) ([]KeyInfo, error) {
	rows, err := db.pool.Query(ctx, `
		select k.id, k.status, a.id, a.name, u.email, p.name, k.created_at, k.revoked_at
		from api_keys k
		join applications a on a.id = k.application_id
		join users u on u.id = a.user_id
		join plans p on p.id = a.plan_id
		order by k.id`)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (KeyInfo, error) {
		var k KeyInfo
		err := row.Scan(&k.ID, &k.Status, &k.ApplicationID, &k.Application, &k.OwnerEmail, &k.Plan, &k.CreatedAt, &k.RevokedAt)
		return k, err
	})
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, nil
}
