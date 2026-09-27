package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const adminToken = "0123456789abcdef0123456789abcdef"

type fakeQuerier struct {
	since time.Time
	err   error
}

func (f *fakeQuerier) Summary(_ context.Context, since time.Time) (Traffic, error) {
	f.since = since
	return Traffic{Requests: 10, Errors: 1, LatencyMS: Latency{P50: 1.5, P95: 9, P99: 20}}, f.err
}

func (f *fakeQuerier) Routes(_ context.Context, since time.Time) ([]RouteStats, error) {
	f.since = since
	r := "/users"
	return []RouteStats{{Route: &r, Traffic: Traffic{Requests: 10}}, {Route: nil}}, f.err
}

func (f *fakeQuerier) Accounts(_ context.Context, since time.Time) (Accounts, error) {
	f.since = since
	return Accounts{}, f.err // nil slices must encode as []
}

func analyticsHandler(t *testing.T, q Querier) http.Handler {
	t.Helper()
	c := NewCollector(&fakeWriter{}, discard, Options{})
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	h, err := AnalyticsHandler(q, c, adminToken, discard)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func call(h http.Handler, method, target, authz string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAnalyticsRejectsWeakToken(t *testing.T) {
	if _, err := AnalyticsHandler(&fakeQuerier{}, nil, "short", discard); err == nil {
		t.Error("expected error for an admin token under 32 characters")
	}
}

func TestAnalyticsRequiresAdminToken(t *testing.T) {
	h := analyticsHandler(t, &fakeQuerier{})
	for name, authz := range map[string]string{
		"missing":      "",
		"wrong token":  "Bearer " + strings.Repeat("x", 32),
		"prefix only":  "Bearer " + adminToken[:31],
		"wrong scheme": "Basic " + adminToken,
		"an api key":   "Bearer gw_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		if rec := call(h, http.MethodGet, "/analytics/summary", authz); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, rec.Code)
		}
	}
}

func TestAnalyticsEndpoints(t *testing.T) {
	q := &fakeQuerier{}
	h := analyticsHandler(t, q)
	authz := "Bearer " + adminToken

	rec := call(h, http.MethodGet, "/analytics/summary?window=15m", authz)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body)
	}
	var summary map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &summary)
	if summary["window"] != "15m0s" || summary["requests"] != float64(10) || summary["collector"] == nil {
		t.Errorf("summary body = %s", rec.Body)
	}
	if lat, _ := summary["latency_ms"].(map[string]any); lat["p95"] != float64(9) {
		t.Errorf("latency = %v", summary["latency_ms"])
	}
	if d := time.Since(q.since); d < 15*time.Minute || d > 16*time.Minute {
		t.Errorf("query since %s ago, want the 15m window", d)
	}

	rec = call(h, http.MethodGet, "/analytics/routes", authz)
	if !strings.Contains(rec.Body.String(), `"route":"/users"`) || !strings.Contains(rec.Body.String(), `"route":null`) ||
		!strings.Contains(rec.Body.String(), `"window":"1h0m0s"`) {
		t.Errorf("routes body = %s", rec.Body)
	}

	rec = call(h, http.MethodGet, "/analytics/accounts", authz)
	if !strings.Contains(rec.Body.String(), `"applications":[]`) || !strings.Contains(rec.Body.String(), `"api_keys":[]`) {
		t.Errorf("accounts body = %s", rec.Body)
	}
}

func TestAnalyticsErrors(t *testing.T) {
	authz := "Bearer " + adminToken
	h := analyticsHandler(t, &fakeQuerier{})
	for target, want := range map[string]int{
		"/analytics/summary?window=soon": http.StatusBadRequest,
		"/analytics/summary?window=-1h":  http.StatusBadRequest,
		"/analytics/summary?window=800h": http.StatusBadRequest,
		"/analytics/nope":                http.StatusNotFound,
	} {
		if rec := call(h, http.MethodGet, target, authz); rec.Code != want {
			t.Errorf("GET %s: status %d, want %d", target, rec.Code, want)
		}
	}
	if rec := call(h, http.MethodPost, "/analytics/summary", authz); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want 405", rec.Code)
	}

	failing := analyticsHandler(t, &fakeQuerier{err: errors.New("db down")})
	rec := call(failing, http.MethodGet, "/analytics/summary", authz)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "db down") {
		t.Errorf("query failure: status %d body %s (internal errors must not leak)", rec.Code, rec.Body)
	}
}
