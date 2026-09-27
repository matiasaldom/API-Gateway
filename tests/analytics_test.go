package tests

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-gateway/internal/admin"
	"api-gateway/internal/metrics"
	"api-gateway/internal/storage"
	"api-gateway/internal/testdb"
)

const testAdminToken = "test-admin-token-0123456789abcdef"

// queryAnalytics calls an analytics endpoint on env's database and decodes the JSON body into out.
func queryAnalytics(t *testing.T, env authEnv, path string, out any) {
	t.Helper()
	h, err := admin.New(env.db, env.routes, metrics.NewAnalytics(env.db, env.collector, slog.New(slog.DiscardHandler)), testAdminToken, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

type summaryBody struct {
	metrics.Traffic
	Collector metrics.CollectorStats `json:"collector"`
}

type routesBody struct {
	Routes []metrics.RouteStats `json:"routes"`
}

func routeByName(routes []metrics.RouteStats, name string) (metrics.RouteStats, bool) {
	for _, r := range routes {
		if (r.Route == nil && name == "") || (r.Route != nil && *r.Route == name) {
			return r, true
		}
	}
	return metrics.RouteStats{}, false
}

func TestAnalyticsCapturesGatewayTraffic(t *testing.T) {
	env := newAuthEnv(t)
	limitedKey := env.planKey(t, "free")
	proKey := env.planKey(t, "pro")

	send := func(path, key string, want int) {
		t.Helper()
		authz := ""
		if key != "" {
			authz = "Bearer " + key
		}
		resp := env.request(t, http.MethodGet, path, authz, "")
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("GET %s: status %d, want %d", path, resp.StatusCode, want)
		}
	}
	send("/albums/1", env.activeKey, 200) // cache MISS
	send("/albums/1", env.activeKey, 200) // cache HIT
	send("/users/1", env.activeKey, 200)
	send("/nowhere", env.activeKey, 404)
	send("/users/1", "", 401)
	send("/users/1", env.revokedKey, 403)
	send("/users/1", proKey, 200)
	for range 100 {
		send("/users/2", limitedKey, 200)
	}
	send("/users/2", limitedKey, 429)
	const total = 108

	env.flushAnalytics(t) // graceful shutdown path: everything pending is written

	var s summaryBody
	queryAnalytics(t, env, "/analytics/summary", &s)
	if s.Requests != total || s.RateLimited != 1 || s.Errors != 0 || s.Timeouts != 0 || s.CacheHits != 1 || s.CacheMisses != 1 {
		t.Errorf("summary = %+v, want %d requests, 1 rate limited, 1 hit, 1 miss", s.Traffic, total)
	}
	// Only ordering is checked here: fast requests can measure 0 on coarse clocks (Windows).
	// Exact percentile values are covered by TestAnalyticsAggregatesStoredEvents.
	if s.LatencyMS.P50 < 0 || s.LatencyMS.P50 > s.LatencyMS.P95 || s.LatencyMS.P95 > s.LatencyMS.P99 {
		t.Errorf("latency percentiles = %+v", s.LatencyMS)
	}
	if s.Collector.Written != total || s.Collector.Dropped != 0 || s.Collector.Failed != 0 {
		t.Errorf("collector = %+v", s.Collector)
	}

	var r routesBody
	queryAnalytics(t, env, "/analytics/routes", &r)
	users, _ := routeByName(r.Routes, "/users")
	albums, _ := routeByName(r.Routes, "/albums")
	unrouted, _ := routeByName(r.Routes, "")
	if users.Requests != 105 || users.RateLimited != 1 || albums.Requests != 2 || albums.CacheHits != 1 || unrouted.Requests != 1 {
		t.Errorf("routes: /users=%d (429s %d) /albums=%d (hits %d) unrouted=%d",
			users.Requests, users.RateLimited, albums.Requests, albums.CacheHits, unrouted.Requests)
	}

	var a metrics.Accounts
	queryAnalytics(t, env, "/analytics/accounts", &a)
	// 401 and 403 requests have no authenticated key, so they're excluded here.
	if len(a.Applications) != 3 || len(a.APIKeys) != 3 {
		t.Fatalf("accounts: %d applications, %d keys, want 3 and 3", len(a.Applications), len(a.APIKeys))
	}
	top := a.APIKeys[0] // ordered by requests
	if top.Requests != 101 || top.RateLimited != 1 || top.LimitPerMinute == nil || *top.LimitPerMinute != 100 {
		t.Errorf("limited key stats = %+v", top)
	}
	if top.Utilization == nil || *top.Utilization != float64(top.PeakPerMinute)/100 {
		t.Errorf("utilization = %v, want peak/limit", top.Utilization)
	}
	if app := a.Applications[0]; app.Requests != 101 || app.Plan == nil || *app.Plan != "free" || app.OwnerEmail == nil {
		t.Errorf("top application = %+v", app)
	}
}

func TestAnalyticsAggregatesStoredEvents(t *testing.T) {
	env := newAuthEnv(t)
	ctx := context.Background()

	// 100 events for the free-plan test key, latencies 1..100 ms, split 40/60 across two
	// clock minutes; plus one event outside the query window.
	minute := time.Now().UTC().Truncate(time.Minute).Add(-10 * time.Minute)
	var events []metrics.Event
	for i := 1; i <= 100; i++ {
		e := metrics.Event{
			OccurredAt: minute.Add(time.Duration(i%2) * time.Second), RequestID: "r", Method: "GET",
			Route: "/users", Status: 200, Latency: time.Duration(i) * time.Millisecond,
			APIKeyID: env.activeID, ApplicationID: env.appID, PlanID: 1,
		}
		if i > 40 {
			e.OccurredAt = minute.Add(time.Minute)
		}
		switch {
		case i <= 5:
			e.Status = 500
		case i <= 7:
			e.Status = 504
		case i <= 10:
			e.Status = 429
		case i <= 30:
			e.Cache = "HIT"
		case i <= 35:
			e.Cache = "MISS"
		}
		events = append(events, e)
	}
	events = append(events, metrics.Event{OccurredAt: minute.Add(-2 * time.Hour), RequestID: "old", Method: "GET", Status: 200})
	if err := env.db.WriteEvents(ctx, events); err != nil {
		t.Fatal(err)
	}

	sum, err := env.db.Summary(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// 5xx includes the 504s; the old event is outside the window.
	if sum.Requests != 100 || sum.Errors != 7 || sum.Timeouts != 2 || sum.RateLimited != 3 || sum.CacheHits != 20 || sum.CacheMisses != 5 {
		t.Errorf("summary = %+v", sum)
	}
	// percentile_cont over 1..100: p50 = 50.5, p95 = 95.05, p99 = 99.01.
	for name, got := range map[string][2]float64{"p50": {sum.LatencyMS.P50, 50.5}, "p95": {sum.LatencyMS.P95, 95.05}, "p99": {sum.LatencyMS.P99, 99.01}} {
		if math.Abs(got[0]-got[1]) > 1e-9 {
			t.Errorf("%s = %v ms, want %v", name, got[0], got[1])
		}
	}

	accounts, err := env.db.Accounts(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	k := accounts.APIKeys[0]
	// Busiest minute has 60 requests; the free plan allows 100/min.
	if k.PeakPerMinute != 60 || *k.LimitPerMinute != 100 || math.Abs(*k.Utilization-0.6) > 1e-9 || k.RateLimited != 3 {
		t.Errorf("api key stats = %+v (limit %v, utilization %v)", k, *k.LimitPerMinute, *k.Utilization)
	}

	// A window that excludes everything returns zeros, not an error.
	empty, err := env.db.Summary(ctx, time.Now().Add(time.Hour))
	if err != nil || empty.Requests != 0 || empty.LatencyMS.P99 != 0 {
		t.Errorf("empty window: %+v, %v", empty, err)
	}
}

func TestAnalyticsDatabaseFailureDoesNotAffectTraffic(t *testing.T) {
	// Analytics writes go to a closed pool; authentication uses the healthy one.
	broken, err := storage.Open(context.Background(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	broken.Close()
	env := newAuthEnvWithWriter(t, broken)

	for i := range 20 {
		resp := env.request(t, http.MethodGet, "/users/1", "Bearer "+env.activeKey, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d while analytics storage is down", i+1, resp.StatusCode)
		}
	}
	env.flushAnalytics(t)
	if s := env.collector.Stats(); s.Failed != 20 || s.Written != 0 {
		t.Errorf("collector = %+v, want all 20 events failed and isolated", s)
	}
	if !strings.Contains(env.logs.String(), `"msg":"analytics batch write failed"`) {
		t.Error("analytics write failure was not logged")
	}
}
