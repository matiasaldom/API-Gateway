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
// creating the user and application if they don't exist. planName must name an
// existing plan (plans carry rate limits, so they are defined by migrations, not
// created ad hoc). It only applies when the application is created; an existing
// application keeps its plan.
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
		err = tx.QueryRow(ctx, `select id from plans where name = $1`, planName).Scan(&planID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("%w %q", ErrUnknownPlan, planName)
		}
		if err != nil {
			return 0, fmt.Errorf("find plan: %w", err)
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

// ErrUnknownPlan is returned when a plan name does not exist.
var ErrUnknownPlan = errors.New("unknown plan")

// KeyFilter narrows ListAPIKeys. Zero values mean "no filter"; Limit 0 means no limit.
type KeyFilter struct {
	Status        auth.Status
	ApplicationID int64
	AfterID       int64 // keyset pagination: only keys with a larger ID
	Limit         int
}

const keyInfoQuery = `
	select k.id, k.status, a.id, a.name, u.email, p.name, k.created_at, k.revoked_at
	from api_keys k
	join applications a on a.id = k.application_id
	join users u on u.id = a.user_id
	join plans p on p.id = a.plan_id`

func scanKeyInfo(row pgx.Row) (KeyInfo, error) {
	var k KeyInfo
	err := row.Scan(&k.ID, &k.Status, &k.ApplicationID, &k.Application, &k.OwnerEmail, &k.Plan, &k.CreatedAt, &k.RevokedAt)
	return k, err
}

// ListAPIKeys returns API keys matching f, ordered by ID.
func (db *DB) ListAPIKeys(ctx context.Context, f KeyFilter) ([]KeyInfo, error) {
	limit := any(nil) // SQL "limit null" means no limit
	if f.Limit > 0 {
		limit = f.Limit
	}
	rows, err := db.pool.Query(ctx, keyInfoQuery+`
		where ($1 = '' or k.status = $1)
		  and ($2 = 0 or k.application_id = $2)
		  and k.id > $3
		order by k.id
		limit $4`, string(f.Status), f.ApplicationID, f.AfterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (KeyInfo, error) { return scanKeyInfo(row) })
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, nil
}

// GetAPIKey returns one key's listing info, or ErrNotFound.
func (db *DB) GetAPIKey(ctx context.Context, id int64) (KeyInfo, error) {
	k, err := scanKeyInfo(db.pool.QueryRow(ctx, keyInfoQuery+` where k.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return KeyInfo{}, ErrNotFound
	}
	if err != nil {
		return KeyInfo{}, fmt.Errorf("get api key: %w", err)
	}
	return k, nil
}

// Plan is a rate-limit tier.
type Plan struct {
	ID                int64
	Name              string
	RequestsPerMinute int
	Applications      int // applications currently on the plan
	CreatedAt         time.Time
}

const planQuery = `
	select p.id, p.name, p.requests_per_minute,
	       (select count(*) from applications a where a.plan_id = p.id)::int, p.created_at
	from plans p`

func scanPlan(row pgx.Row) (Plan, error) {
	var p Plan
	err := row.Scan(&p.ID, &p.Name, &p.RequestsPerMinute, &p.Applications, &p.CreatedAt)
	return p, err
}

func (db *DB) ListPlans(ctx context.Context) ([]Plan, error) {
	rows, err := db.pool.Query(ctx, planQuery+` order by p.id`)
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	plans, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Plan, error) { return scanPlan(row) })
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	return plans, nil
}

func (db *DB) GetPlan(ctx context.Context, id int64) (Plan, error) {
	p, err := scanPlan(db.pool.QueryRow(ctx, planQuery+` where p.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, fmt.Errorf("get plan: %w", err)
	}
	return p, nil
}

// UpdatePlanLimit sets a plan's per-minute limit. It applies to each key's next
// request, because the limit is read with the key on every request.
func (db *DB) UpdatePlanLimit(ctx context.Context, id int64, requestsPerMinute int) (Plan, error) {
	tag, err := db.pool.Exec(ctx, `update plans set requests_per_minute = $2 where id = $1`, id, requestsPerMinute)
	if err != nil {
		return Plan{}, fmt.Errorf("update plan: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Plan{}, ErrNotFound
	}
	return db.GetPlan(ctx, id)
}
