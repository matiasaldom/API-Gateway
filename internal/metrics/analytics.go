package metrics

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"api-gateway/internal/httpx"
	"api-gateway/internal/requestid"
)

const (
	defaultWindow = time.Hour
	maxWindow     = 31 * 24 * time.Hour
	queryTimeout  = 10 * time.Second
	// MinAdminTokenLen guards against guessable admin tokens (e.g. `openssl rand -hex 32` gives 64).
	MinAdminTokenLen = 32
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

// AnalyticsHandler serves, to callers presenting adminToken as a Bearer token:
//
//	GET /analytics/summary   engineering totals and latency percentiles, plus collector health
//	GET /analytics/routes    the same, per route
//	GET /analytics/accounts  requests per application and per API key, with plan utilization
//
// Each accepts ?window=<duration> (default 1h, max 744h). The data spans all
// accounts, so API keys are not accepted here.
func AnalyticsHandler(q Querier, c *Collector, adminToken string, logger *slog.Logger) (http.Handler, error) {
	if len(adminToken) < MinAdminTokenLen {
		return nil, errors.New("admin token must be at least 32 characters")
	}
	want := sha256.Sum256([]byte(adminToken))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		got := sha256.Sum256([]byte(token)) // hash both sides: constant time regardless of length
		if !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			logger.WarnContext(r.Context(), "analytics access denied",
				"request_id", requestid.FromContext(r.Context()), "path", r.URL.Path, "remote_addr", r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", `Bearer realm="analytics"`)
			httpx.Error(w, r, http.StatusUnauthorized, "admin token required")
			return
		}
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
		var body any
		var err error
		switch r.URL.Path {
		case "/analytics/summary":
			var t Traffic
			t, err = q.Summary(ctx, win.From)
			body = struct {
				window
				Traffic
				Collector CollectorStats `json:"collector"`
			}{win, t, c.Stats()}
		case "/analytics/routes":
			var routes []RouteStats
			routes, err = q.Routes(ctx, win.From)
			body = struct {
				window
				Routes []RouteStats `json:"routes"`
			}{win, nonNil(routes)}
		case "/analytics/accounts":
			var a Accounts
			a, err = q.Accounts(ctx, win.From)
			a.Applications, a.APIKeys = nonNil(a.Applications), nonNil(a.APIKeys)
			body = struct {
				window
				Accounts
			}{win, a}
		default:
			httpx.Error(w, r, http.StatusNotFound, "unknown analytics endpoint")
			return
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "analytics query failed",
				"request_id", requestid.FromContext(r.Context()), "path", r.URL.Path, "error", err.Error())
			httpx.Error(w, r, http.StatusInternalServerError, "analytics query failed")
			return
		}
		httpx.JSON(w, http.StatusOK, body)
	}), nil
}

// nonNil makes empty results encode as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
