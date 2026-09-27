package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"api-gateway/internal/httpx"
	"api-gateway/internal/requestid"
)

const (
	defaultWindow = time.Hour
	maxWindow     = 31 * 24 * time.Hour
	queryTimeout  = 10 * time.Second
)

// Latency percentiles in milliseconds.
type Latency struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}

// Traffic is the engineering view of a set of requests. Errors are 5xx
// responses, Timeouts 504s, RateLimited 429s.
type Traffic struct {
	Requests    int64   `json:"requests"`
	Errors      int64   `json:"errors"`
	Timeouts    int64   `json:"timeouts"`
	RateLimited int64   `json:"rate_limited"`
	CacheHits   int64   `json:"cache_hits"`
	CacheMisses int64   `json:"cache_misses"`
	LatencyMS   Latency `json:"latency_ms"`
}

type RouteStats struct {
	Route *string `json:"route"` // null: requests that matched no route
	Traffic
}

type ApplicationStats struct {
	ApplicationID int64   `json:"application_id"`
	Application   *string `json:"application"` // null if the application was since deleted
	OwnerEmail    *string `json:"owner_email"`
	Plan          *string `json:"plan"`
	Requests      int64   `json:"requests"`
	Errors        int64   `json:"errors"`
	RateLimited   int64   `json:"rate_limited"`
}

// APIKeyStats includes plan utilization: the key's busiest clock minute in the
// window divided by its plan's current per-minute limit. Rejected requests count,
// so utilization above 1.0 means demand exceeded the plan.
type APIKeyStats struct {
	APIKeyID       int64    `json:"api_key_id"`
	ApplicationID  int64    `json:"application_id"`
	Requests       int64    `json:"requests"`
	Errors         int64    `json:"errors"`
	RateLimited    int64    `json:"rate_limited"`
	PeakPerMinute  int64    `json:"peak_requests_per_minute"`
	LimitPerMinute *int64   `json:"limit_requests_per_minute"`
	Utilization    *float64 `json:"plan_utilization"`
}

type Accounts struct {
	Applications []ApplicationStats `json:"applications"`
	APIKeys      []APIKeyStats      `json:"api_keys"`
}

// Querier aggregates stored events that occurred at or after since.
type Querier interface {
	Summary(ctx context.Context, since time.Time) (Traffic, error)
	Routes(ctx context.Context, since time.Time) ([]RouteStats, error)
	Accounts(ctx context.Context, since time.Time) (Accounts, error)
}

type window struct {
	Window string    `json:"window"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
}

// Analytics serves aggregated analytics as JSON. It performs no authentication:
// mount it behind admin.RequireToken, because the data spans all accounts.
//
// Each endpoint accepts ?window=<duration> (default 1h, max 744h).
type Analytics struct {
	q      Querier
	c      *Collector
	logger *slog.Logger
}

func NewAnalytics(q Querier, c *Collector, logger *slog.Logger) *Analytics {
	return &Analytics{q: q, c: c, logger: logger}
}

// Summary serves engineering totals and latency percentiles, plus collector health.
func (a *Analytics) Summary(w http.ResponseWriter, r *http.Request) {
	a.serve(w, r, func(ctx context.Context, win window) (any, error) {
		t, err := a.q.Summary(ctx, win.From)
		return struct {
			window
			Traffic
			Collector CollectorStats `json:"collector"`
		}{win, t, a.c.Stats()}, err
	})
}

// Routes serves the summary numbers per route.
func (a *Analytics) Routes(w http.ResponseWriter, r *http.Request) {
	a.serve(w, r, func(ctx context.Context, win window) (any, error) {
		routes, err := a.q.Routes(ctx, win.From)
		return struct {
			window
			Routes []RouteStats `json:"routes"`
		}{win, nonNil(routes)}, err
	})
}

// Accounts serves requests per application and per API key, with plan utilization.
func (a *Analytics) Accounts(w http.ResponseWriter, r *http.Request) {
	a.serve(w, r, func(ctx context.Context, win window) (any, error) {
		acc, err := a.q.Accounts(ctx, win.From)
		acc.Applications, acc.APIKeys = nonNil(acc.Applications), nonNil(acc.APIKeys)
		return struct {
			window
			Accounts
		}{win, acc}, err
	})
}

func (a *Analytics) serve(w http.ResponseWriter, r *http.Request, query func(context.Context, window) (any, error)) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	d := defaultWindow
	if v := r.URL.Query().Get("window"); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil || parsed <= 0 || parsed > maxWindow {
			httpx.Error(w, r, http.StatusBadRequest, "window must be a duration between 1ns and 744h, e.g. 15m or 24h")
			return
		}
		d = parsed
	}
	now := time.Now().UTC()
	win := window{Window: d.String(), From: now.Add(-d), To: now}

	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	body, err := query(ctx, win)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "analytics query failed",
			"request_id", requestid.FromContext(r.Context()), "path", r.URL.Path, "error", err.Error())
		httpx.Error(w, r, http.StatusInternalServerError, "analytics query failed")
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}

// nonNil makes empty results encode as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
