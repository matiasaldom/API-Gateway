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

func analyticsMux(t *testing.T, q Querier) http.Handler {
	t.Helper()
	c := NewCollector(&fakeWriter{}, discard, Options{})
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	a := NewAnalytics(q, c, discard)
	mux := http.NewServeMux()
	mux.HandleFunc("/analytics/summary", a.Summary)
	mux.HandleFunc("/analytics/routes", a.Routes)
	mux.HandleFunc("/analytics/accounts", a.Accounts)
	return mux
}

func call(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestAnalyticsEndpoints(t *testing.T) {
	q := &fakeQuerier{}
	h := analyticsMux(t, q)

	rec := call(h, http.MethodGet, "/analytics/summary?window=15m")
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

	rec = call(h, http.MethodGet, "/analytics/routes")
	if !strings.Contains(rec.Body.String(), `"route":"/users"`) || !strings.Contains(rec.Body.String(), `"route":null`) ||
		!strings.Contains(rec.Body.String(), `"window":"1h0m0s"`) {
		t.Errorf("routes body = %s", rec.Body)
	}

	rec = call(h, http.MethodGet, "/analytics/accounts")
	if !strings.Contains(rec.Body.String(), `"applications":[]`) || !strings.Contains(rec.Body.String(), `"api_keys":[]`) {
		t.Errorf("accounts body = %s", rec.Body)
	}
}

func TestAnalyticsErrors(t *testing.T) {
	h := analyticsMux(t, &fakeQuerier{})
	for target, want := range map[string]int{
		"/analytics/summary?window=soon": http.StatusBadRequest,
		"/analytics/summary?window=-1h":  http.StatusBadRequest,
		"/analytics/summary?window=800h": http.StatusBadRequest,
	} {
		if rec := call(h, http.MethodGet, target); rec.Code != want {
			t.Errorf("GET %s: status %d, want %d", target, rec.Code, want)
		}
	}
	if rec := call(h, http.MethodPost, "/analytics/summary"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want 405", rec.Code)
	}

	failing := analyticsMux(t, &fakeQuerier{err: errors.New("db down")})
	rec := call(failing, http.MethodGet, "/analytics/summary")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "db down") {
		t.Errorf("query failure: status %d body %s (internal errors must not leak)", rec.Code, rec.Body)
	}
}
