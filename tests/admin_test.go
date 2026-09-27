package tests

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"api-gateway/internal/admin"
	"api-gateway/internal/config"
	"api-gateway/internal/metrics"
	"api-gateway/internal/requestid"
	"api-gateway/internal/routing"
)

// adminAPI serves the management API against env's database and live route table,
// logging into env.logs, as cmd/gateway mounts it.
func (e authEnv) adminAPI(t *testing.T) string {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(e.logs, nil))
	h, err := admin.New(e.db, e.routes, metrics.NewAnalytics(e.db, e.collector, logger), testAdminToken, logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(requestid.Middleware(h))
	t.Cleanup(srv.Close)
	return srv.URL
}

type adminResp struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (r adminResp) code() string { s, _ := r.body["code"].(string); return s }

func (r adminResp) field(name string) string {
	fields, _ := r.body["fields"].(map[string]any)
	s, _ := fields[name].(string)
	return s
}

// adminCall sends a request with the admin token. A non-empty body is sent as JSON.
func adminCall(t *testing.T, base, method, path, body string) adminResp {
	t.Helper()
	return adminCallAs(t, base, method, path, body, "Bearer "+testAdminToken, "application/json")
}

func adminCallAs(t *testing.T, base, method, path, body, authz, contentType string) adminResp {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	if body != "" && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s %s: Content-Type %q, want JSON for every admin response", method, path, ct)
	}
	out := adminResp{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e authEnv) status(t *testing.T, path, apiKey string) int {
	t.Helper()
	resp, _ := e.get(t, path, apiKey)
	return resp.StatusCode
}

func TestAdminAuthentication(t *testing.T) {
	env := newAuthEnv(t)
	base := env.adminAPI(t)

	for name, tc := range map[string]struct {
		authz string
		want  int
	}{
		"missing token":           {"", 401},
		"wrong scheme":            {"Basic " + testAdminToken, 401},
		"invalid token":           {"Bearer " + strings.Repeat("x", 40), 403},
		"API key is not an admin": {"Bearer " + env.activeKey, 403},
		"valid admin token":       {"Bearer " + testAdminToken, 200},
	} {
		for _, path := range []string{"/admin/plans", "/admin/analytics/summary", "/analytics/summary"} {
			if r := adminCallAs(t, base, http.MethodGet, path, "", tc.authz, ""); r.status != tc.want {
				t.Errorf("%s, GET %s: status %d, want %d (%s)", name, path, r.status, tc.want, r.raw)
			}
		}
	}

	// Unknown endpoints are hidden behind auth, then reported as JSON.
	if r := adminCallAs(t, base, http.MethodGet, "/admin/secrets", "", "", ""); r.status != 401 {
		t.Errorf("unauthenticated unknown path: %d, want 401", r.status)
	}
	if r := adminCall(t, base, http.MethodGet, "/admin/secrets", ""); r.status != 404 || r.code() != "not_found" {
		t.Errorf("unknown path: %d %s", r.status, r.raw)
	}
	if r := adminCall(t, base, http.MethodDelete, "/admin/plans", ""); r.status != 405 || r.code() != "method_not_allowed" || r.header.Get("Allow") != "GET" {
		t.Errorf("wrong method: %d Allow=%q %s", r.status, r.header.Get("Allow"), r.raw)
	}
	if strings.Contains(env.logs.String(), testAdminToken) {
		t.Error("admin token written to logs")
	}
}

func TestAdminManagesAPIKeys(t *testing.T) {
	env := newAuthEnv(t)
	base := env.adminAPI(t)

	// Create: the raw key is returned once and works at the gateway immediately.
	r := adminCall(t, base, http.MethodPost, "/admin/api-keys", `{"email":"ops@example.com","application":"Billing","plan":"pro"}`)
	if r.status != 201 || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d Cache-Control=%q %s", r.status, r.header.Get("Cache-Control"), r.raw)
	}
	raw, _ := r.body["api_key"].(string)
	key, _ := r.body["key"].(map[string]any)
	if !strings.HasPrefix(raw, "gw_") || key["plan"] != "pro" || key["status"] != "active" || key["application"] != "Billing" {
		t.Fatalf("create body: %s", r.raw)
	}
	newID := int64(key["id"].(float64))
	if s := env.status(t, "/users/1", raw); s != 200 {
		t.Fatalf("new key at gateway: %d, want 200", s)
	}
	// Plan defaults to free.
	if r := adminCall(t, base, http.MethodPost, "/admin/api-keys", `{"email":"a@example.com","application":"Default"}`); r.status != 201 || r.body["key"].(map[string]any)["plan"] != "free" {
		t.Errorf("default plan: %d %s", r.status, r.raw)
	}

	// Create validation.
	for body, wantField := range map[string]string{
		`{"email":"not-an-email","application":"x"}`:                                   "email",
		`{"email":"Ops <ops@example.com>","application":"x"}`:                          "email",
		`{"email":"ops@example.com"}`:                                                  "application",
		`{"email":"ops@example.com","application":"   "}`:                              "application",
		`{"email":"ops@example.com","application":"` + strings.Repeat("a", 101) + `"}`: "application",
		`{"email":"ops@example.com","application":"bad\nname"}`:                        "application",
		`{"email":"ops@example.com","application":"x","plan":"enterprise"}`:            "plan",
		`{"email":"ops@example.com","application":"x","plan":""}`:                      "plan",
		`{"email":"ops@example.com","application":5}`:                                  "application",
	} {
		if r := adminCall(t, base, http.MethodPost, "/admin/api-keys", body); r.status != 400 || r.code() != "validation_failed" || r.field(wantField) == "" {
			t.Errorf("POST %s: %d %s, want validation error on %q", body, r.status, r.raw, wantField)
		}
	}
	for body, wantCode := range map[string]string{
		`{"email":"ops@example.com","application":"x","admin":true}`: "invalid_json",
		`{"email":`: "invalid_json",
		`{"email":"ops@example.com","application":"x"} {}`: "invalid_json",
		`[]`: "invalid_json",
	} {
		if r := adminCall(t, base, http.MethodPost, "/admin/api-keys", body); r.status != 400 || r.code() != wantCode {
			t.Errorf("POST %s: %d %s, want %s", body, r.status, r.raw, wantCode)
		}
	}
	if r := adminCallAs(t, base, http.MethodPost, "/admin/api-keys", `{"email":"a@b.co","application":"x"}`, "Bearer "+testAdminToken, "text/plain"); r.status != 415 {
		t.Errorf("text/plain body: %d, want 415", r.status)
	}
	if r := adminCall(t, base, http.MethodPost, "/admin/api-keys", `{"email":"`+strings.Repeat("a", 70_000)+`"}`); r.status != 413 {
		t.Errorf("oversized body: %d, want 413", r.status)
	}

	// List: never exposes key material; filters and pagination.
	r = adminCall(t, base, http.MethodGet, "/admin/api-keys", "")
	keys, _ := r.body["api_keys"].([]any)
	if r.status != 200 || len(keys) != 4 || strings.Contains(r.raw, raw[3:]) || strings.Contains(r.raw, "hash") {
		t.Fatalf("list: %d, %d keys, body %s", r.status, len(keys), r.raw)
	}
	if r := adminCall(t, base, http.MethodGet, "/admin/api-keys?status=revoked", ""); len(r.body["api_keys"].([]any)) != 1 {
		t.Errorf("status filter: %s", r.raw)
	}
	if r := adminCall(t, base, http.MethodGet, "/admin/api-keys?application_id="+strconv.FormatInt(env.appID, 10), ""); len(r.body["api_keys"].([]any)) != 2 {
		t.Errorf("application filter: %s", r.raw)
	}
	page1 := adminCall(t, base, http.MethodGet, "/admin/api-keys?limit=3", "")
	next, ok := page1.body["next_after"].(float64)
	if len(page1.body["api_keys"].([]any)) != 3 || !ok {
		t.Fatalf("page 1: %s", page1.raw)
	}
	page2 := adminCall(t, base, http.MethodGet, "/admin/api-keys?limit=3&after="+strconv.Itoa(int(next)), "")
	if len(page2.body["api_keys"].([]any)) != 1 || page2.body["next_after"] != nil {
		t.Errorf("page 2: %s", page2.raw)
	}
	for query, field := range map[string]string{
		"status=paused": "status", "application_id=0": "application_id", "after=-1": "after",
		"limit=0": "limit", "limit=1001": "limit", "limit=ten": "limit", "sort=id": "sort",
	} {
		if r := adminCall(t, base, http.MethodGet, "/admin/api-keys?"+query, ""); r.status != 400 || r.field(field) == "" {
			t.Errorf("GET ?%s: %d %s, want validation error on %q", query, r.status, r.raw, field)
		}
	}

	// Revoke: takes effect at the gateway at once, is idempotent, and validates the ID.
	path := "/admin/api-keys/" + strconv.FormatInt(newID, 10) + "/revoke"
	if r := adminCall(t, base, http.MethodPost, path, ""); r.status != 200 || r.body["status"] != "revoked" || r.body["revoked_at"] == nil {
		t.Fatalf("revoke: %d %s", r.status, r.raw)
	}
	if s := env.status(t, "/users/1", raw); s != 403 {
		t.Errorf("revoked key at gateway: %d, want 403", s)
	}
	if r := adminCall(t, base, http.MethodPost, path, ""); r.status != 200 || r.body["status"] != "revoked" {
		t.Errorf("second revoke: %d %s", r.status, r.raw)
	}
	for p, want := range map[string]int{
		"/admin/api-keys/999999/revoke": 404, "/admin/api-keys/abc/revoke": 400, "/admin/api-keys/0/revoke": 400,
	} {
		if r := adminCall(t, base, http.MethodPost, p, ""); r.status != want {
			t.Errorf("POST %s: %d, want %d", p, r.status, want)
		}
	}
	if r := adminCall(t, base, http.MethodGet, path, ""); r.status != 405 || r.header.Get("Allow") != "POST" {
		t.Errorf("GET revoke: %d Allow=%q", r.status, r.header.Get("Allow"))
	}

	logs := env.logs.String()
	if strings.Contains(logs, raw[3:]) {
		t.Fatal("raw API key written to logs")
	}
	for _, want := range []string{`"action":"api_key.create"`, `"action":"api_key.revoke"`, `"previous_status":"revoked"`, `"actor":"admin:`} {
		if !strings.Contains(logs, want) {
			t.Errorf("audit log missing %s", want)
		}
	}
}

func TestAdminEditsPlanLimits(t *testing.T) {
	env := newAuthEnv(t)
	base := env.adminAPI(t)

	r := adminCall(t, base, http.MethodGet, "/admin/plans", "")
	plans, _ := r.body["plans"].([]any)
	if r.status != 200 || len(plans) != 2 {
		t.Fatalf("list plans: %d %s", r.status, r.raw)
	}
	var freeID string
	for _, p := range plans {
		p := p.(map[string]any)
		if p["name"] == "free" {
			freeID = strconv.Itoa(int(p["id"].(float64)))
			if p["requests_per_minute"] != float64(100) || p["applications"] != float64(1) {
				t.Errorf("free plan: %v", p)
			}
		}
	}

	r = adminCall(t, base, http.MethodPatch, "/admin/plans/"+freeID, `{"requests_per_minute":2}`)
	if r.status != 200 || r.body["requests_per_minute"] != float64(2) {
		t.Fatalf("patch plan: %d %s", r.status, r.raw)
	}
	// Applies to the next request: the limit is read with the key.
	for i, want := range []int{200, 200, 429} {
		resp, _ := env.get(t, "/users/1", env.activeKey)
		if resp.StatusCode != want || resp.Header.Get("X-RateLimit-Limit") != "2" {
			t.Errorf("request %d after lowering the limit: %d (limit header %q), want %d", i+1, resp.StatusCode, resp.Header.Get("X-RateLimit-Limit"), want)
		}
	}

	for body, wantStatus := range map[string]int{
		`{"requests_per_minute":0}`:       400,
		`{"requests_per_minute":-5}`:      400,
		`{"requests_per_minute":1000001}`: 400,
		`{"requests_per_minute":"100"}`:   400,
		`{"requests_per_minute":1.5}`:     400,
		`{"requests_per_minute":null}`:    400,
		`{}`:                              400,
		`{"name":"gold"}`:                 400, // plan names are not editable
	} {
		if r := adminCall(t, base, http.MethodPatch, "/admin/plans/"+freeID, body); r.status != wantStatus {
			t.Errorf("PATCH %s: %d %s", body, r.status, r.raw)
		}
	}
	if r := adminCall(t, base, http.MethodPatch, "/admin/plans/999999", `{"requests_per_minute":5}`); r.status != 404 {
		t.Errorf("unknown plan: %d", r.status)
	}
	if !strings.Contains(env.logs.String(), `"action":"plan.update"`) ||
		!strings.Contains(env.logs.String(), `"requests_per_minute_before":100,"requests_per_minute_after":2`) {
		t.Errorf("plan change not audited:\n%s", env.logs.String())
	}
}

func TestAdminEditsRoutesLive(t *testing.T) {
	env := newAuthEnv(t)
	base := env.adminAPI(t)
	v2 := newEchoUpstream(t, "users-v2")

	r := adminCall(t, base, http.MethodGet, "/admin/routes", "")
	routes, _ := r.body["routes"].([]any)
	if r.status != 200 || len(routes) != 2 {
		t.Fatalf("list routes: %d %s", r.status, r.raw)
	}
	ids := map[string]string{}
	for _, rt := range routes {
		rt := rt.(map[string]any)
		ids[rt["prefix"].(string)] = strconv.Itoa(int(rt["id"].(float64)))
		if rt["prefix"] == "/albums" && (rt["cache_ttl"] != "30s" || rt["cache_ttl_ms"] != float64(30000)) {
			t.Errorf("/albums route: %v", rt)
		}
	}

	// Point /users at a new upstream: the next request goes there.
	r = adminCall(t, base, http.MethodPatch, "/admin/routes/"+ids["/users"], `{"upstream":"`+v2.URL+`"}`)
	if r.status != 200 || r.body["upstream"] != v2.URL || r.body["cache_ttl"] != "0s" {
		t.Fatalf("patch upstream: %d %s", r.status, r.raw)
	}
	resp, body := env.get(t, "/users/1", env.activeKey)
	if resp.StatusCode != 200 || !strings.Contains(body, `"service":"users-v2"`) {
		t.Errorf("after upstream change: %d %s", resp.StatusCode, body)
	}

	// Turn off caching for /albums: no X-Cache on subsequent requests.
	if r := adminCall(t, base, http.MethodPatch, "/admin/routes/"+ids["/albums"], `{"cache_ttl":"0s"}`); r.status != 200 || r.body["cache_ttl_ms"] != float64(0) {
		t.Fatalf("patch cache_ttl: %d %s", r.status, r.raw)
	}
	for range 2 {
		if resp, _ := env.get(t, "/albums/1", env.activeKey); resp.Header.Get("X-Cache") != "" {
			t.Errorf("X-Cache %q after cache_ttl set to 0", resp.Header.Get("X-Cache"))
		}
	}

	// Edits persist: a restart re-syncs gateway.yaml but keeps managed values.
	declared, _ := routing.New(env.declaredRoutes)
	records, err := env.db.SyncRoutes(t.Context(), declared.Routes())
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range records {
		if rec.Prefix == "/users" && rec.Upstream != v2.URL {
			t.Errorf("upstream edit lost on re-sync: %s", rec.Upstream)
		}
	}

	for body, field := range map[string]string{
		`{}`:                                     "upstream",
		`{"upstream":"ftp://files.example.com"}`: "upstream",
		`{"upstream":"not a url"}`:               "upstream",
		`{"upstream":"http://user:pw@example.com"}`: "upstream",
		`{"upstream":"http://example.com?x=1"}`:     "upstream",
		`{"cache_ttl":"-1s"}`:                       "cache_ttl",
		`{"cache_ttl":"25h"}`:                       "cache_ttl",
		`{"cache_ttl":"soon"}`:                      "cache_ttl",
		`{"cache_ttl":"1500us"}`:                    "cache_ttl",
	} {
		if r := adminCall(t, base, http.MethodPatch, "/admin/routes/"+ids["/users"], body); r.status != 400 || r.field(field) == "" {
			t.Errorf("PATCH %s: %d %s, want validation error on %q", body, r.status, r.raw, field)
		}
	}
	if r := adminCall(t, base, http.MethodPatch, "/admin/routes/"+ids["/users"], `{"prefix":"/other"}`); r.status != 400 || r.code() != "invalid_json" {
		t.Errorf("prefix change: %d %s, want rejection", r.status, r.raw)
	}
	if r := adminCall(t, base, http.MethodPatch, "/admin/routes/999999", `{"cache_ttl":"1s"}`); r.status != 404 {
		t.Errorf("unknown route: %d", r.status)
	}
	// Failed validation left the live route untouched.
	if resp, body := env.get(t, "/users/1", env.activeKey); !strings.Contains(body, `"service":"users-v2"`) {
		t.Errorf("route changed by a rejected edit: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(env.logs.String(), `"action":"route.update"`) || !strings.Contains(env.logs.String(), `"upstream_after":"`+v2.URL+`"`) {
		t.Errorf("route change not audited")
	}
}

func TestAdminAnalyticsSummary(t *testing.T) {
	env := newAuthEnv(t)
	base := env.adminAPI(t)
	for range 3 {
		env.get(t, "/users/1", env.activeKey)
	}
	env.flushAnalytics(t)

	r := adminCall(t, base, http.MethodGet, "/admin/analytics/summary?window=1h", "")
	if r.status != 200 || r.body["requests"] != float64(3) || r.body["latency_ms"] == nil {
		t.Errorf("summary: %d %s", r.status, r.raw)
	}
	if r := adminCall(t, base, http.MethodGet, "/admin/analytics/summary?window=forever", ""); r.status != 400 {
		t.Errorf("bad window: %d", r.status)
	}
	for _, p := range []string{"/admin/analytics/routes", "/admin/analytics/accounts"} {
		if r := adminCall(t, base, http.MethodGet, p, ""); r.status != 200 {
			t.Errorf("GET %s: %d %s", p, r.status, r.raw)
		}
	}
}

func TestSyncRoutesFollowsGatewayYAML(t *testing.T) {
	env := newAuthEnv(t) // declared: /users, /albums (30s cache)

	// A restart with /albums removed and /photos added.
	declared, err := routing.New([]config.Route{
		{Prefix: "/users", Upstream: "http://ignored.example.com"}, // existing: DB value wins
		{Prefix: "/photos", Upstream: "http://photos.example.com", CacheTTL: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err := env.db.SyncRoutes(t.Context(), declared.Routes())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range records {
		got[r.Prefix] = r.Upstream + " " + r.CacheTTL.String()
	}
	if len(got) != 2 || got["/photos"] != "http://photos.example.com 1m0s" || strings.Contains(got["/users"], "ignored") {
		t.Errorf("after sync: %v; want /albums removed, /photos added, /users unchanged", got)
	}
}
