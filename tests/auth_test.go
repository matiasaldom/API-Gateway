package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"api-gateway/internal/auth"
	"api-gateway/internal/config"
	"api-gateway/internal/proxy"
	"api-gateway/internal/requestid"
	"api-gateway/internal/storage"
	"api-gateway/internal/testdb"
)

// authEnv is a gateway backed by a real PostgreSQL key store, in front of an echo upstream.
type authEnv struct {
	dbURL      string
	db         *storage.DB
	appID      int64
	activeKey  string
	activeID   int64
	revokedKey string
	gatewayURL string
	logs       *syncBuffer
}

func newAuthEnv(t *testing.T) authEnv {
	t.Helper()
	ctx := context.Background()
	env := authEnv{dbURL: testdb.URL(t), logs: &syncBuffer{}}

	db, err := storage.Open(ctx, env.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	env.db = db

	if env.appID, err = db.EnsureApplication(ctx, "dev@example.com", "test app", "free"); err != nil {
		t.Fatal(err)
	}
	if env.activeKey, env.activeID, err = db.CreateAPIKey(ctx, env.appID); err != nil {
		t.Fatal(err)
	}
	var revokedID int64
	if env.revokedKey, revokedID, err = db.CreateAPIKey(ctx, env.appID); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeAPIKey(ctx, revokedID); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewJSONHandler(env.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	upstream := newEchoUpstream(t, "users")
	h, err := proxy.NewHandler(
		&config.Config{UpstreamTimeout: 5 * time.Second, Routes: []config.Route{{Prefix: "/users", Upstream: upstream.URL}}},
		logger, auth.Middleware(db, logger),
	)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	env.gatewayURL = srv.URL
	return env
}

func (e authEnv) request(t *testing.T, method, path, authorization, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.gatewayURL+path, strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type errorBody struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id"`
}

// assertGatewayError checks the documented error response. A decoded gateway
// error body also proves the upstream was never reached.
func assertGatewayError(t *testing.T, resp *http.Response, status int, msg string) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != status {
		t.Errorf("status = %d, want %d", resp.StatusCode, status)
	}
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error != msg {
		t.Errorf("error = %q, want %q", body.Error, msg)
	}
	if body.RequestID == "" || body.RequestID != resp.Header.Get(requestid.Header) {
		t.Errorf("request_id = %q, header = %q", body.RequestID, resp.Header.Get(requestid.Header))
	}
}

func TestAuthenticationFlow(t *testing.T) {
	env := newAuthEnv(t)
	unknownKey, _, _ := auth.GenerateKey()

	t.Run("missing key returns 401", func(t *testing.T) {
		resp := env.request(t, http.MethodGet, "/users/1", "", "")
		if got := resp.Header.Get("WWW-Authenticate"); got != `Bearer realm="api-gateway"` {
			t.Errorf("WWW-Authenticate = %q", got)
		}
		assertGatewayError(t, resp, http.StatusUnauthorized, "missing api key")
	})

	t.Run("invalid key returns 401", func(t *testing.T) {
		for name, authz := range map[string]string{
			"unknown key":   "Bearer " + unknownKey,
			"malformed key": "Bearer not-a-real-key",
			"wrong scheme":  "Basic " + env.activeKey,
		} {
			t.Run(name, func(t *testing.T) {
				resp := env.request(t, http.MethodGet, "/users/1", authz, "")
				if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, `error="invalid_token"`) {
					t.Errorf("WWW-Authenticate = %q", got)
				}
				assertGatewayError(t, resp, http.StatusUnauthorized, "invalid api key")
			})
		}
	})

	t.Run("revoked key returns 403", func(t *testing.T) {
		resp := env.request(t, http.MethodGet, "/users/1", "Bearer "+env.revokedKey, "")
		assertGatewayError(t, resp, http.StatusForbidden, "api key revoked")
	})

	t.Run("valid key is forwarded", func(t *testing.T) {
		resp := env.request(t, http.MethodPost, "/users?notify=true", "Bearer "+env.activeKey, `{"name":"ada"}`)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201 from upstream", resp.StatusCode)
		}
		e := decodeEcho(t, resp)
		if e.Method != http.MethodPost || e.Path != "/users" || e.Query != "notify=true" || e.Body != `{"name":"ada"}` {
			t.Errorf("upstream got %s %s?%s body=%s", e.Method, e.Path, e.Query, e.Body)
		}
		if e.Header.Get("Authorization") != "" {
			t.Error("gateway API key was forwarded to the upstream")
		}
		if e.Header.Get(requestid.Header) == "" {
			t.Error("request ID not forwarded")
		}
	})

	t.Run("health needs no key", func(t *testing.T) {
		resp := env.request(t, http.MethodGet, "/health", "", "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("authentication runs before routing", func(t *testing.T) {
		assertGatewayError(t, env.request(t, http.MethodGet, "/unrouted", "", ""), http.StatusUnauthorized, "missing api key")
		assertGatewayError(t, env.request(t, http.MethodGet, "/unrouted", "Bearer "+env.activeKey, ""), http.StatusNotFound, "no route for path")
	})

	// Runs last so it covers every request above. Auth decisions are logged
	// before the response is written, so the log is complete here.
	t.Run("raw keys never appear in logs", func(t *testing.T) {
		logs := env.logs.String()
		for _, k := range []string{env.activeKey, env.revokedKey, unknownKey} {
			if strings.Contains(logs, k[len("gw_"):]) {
				t.Fatalf("raw API key found in logs:\n%s", logs)
			}
		}
		for _, want := range []string{`"reason":"missing_credential"`, `"reason":"malformed_credential"`,
			`"reason":"unknown_key"`, `"reason":"revoked_key"`, `"msg":"authenticated"`} {
			if !strings.Contains(logs, want) {
				t.Errorf("logs missing %s", want)
			}
		}
	})
}

func TestOnlyKeyHashesAreStored(t *testing.T) {
	env := newAuthEnv(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, env.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	// Every column of every key row, rendered as text, must not contain a raw key.
	rows, err := conn.Query(ctx, `select k::text from api_keys k`)
	if err != nil {
		t.Fatal(err)
	}
	dump, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{env.activeKey, env.revokedKey} {
		if strings.Contains(strings.Join(dump, "\n"), k[len("gw_"):]) {
			t.Fatal("raw API key stored in api_keys")
		}
	}

	// The stored value is exactly SHA-256 of the raw key, as computed independently by Postgres.
	var matches bool
	err = conn.QueryRow(ctx,
		`select key_hash = sha256(convert_to($2, 'UTF8')) from api_keys where id = $1`, env.activeID, env.activeKey,
	).Scan(&matches)
	if err != nil || !matches {
		t.Fatalf("stored key_hash is not SHA-256 of the raw key (err=%v)", err)
	}
}

func TestAPIKeyMetadataInRequestContext(t *testing.T) {
	env := newAuthEnv(t)
	ctx := context.Background()

	var wantUser, wantPlan int64
	conn, err := pgx.Connect(ctx, env.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if err := conn.QueryRow(ctx, `select user_id, plan_id from applications where id = $1`, env.appID).Scan(&wantUser, &wantPlan); err != nil {
		t.Fatal(err)
	}

	var got auth.APIKey
	var ok bool
	h := auth.Middleware(env.db, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got, ok = auth.FromContext(r.Context()) }),
	)
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set("Authorization", "Bearer "+env.activeKey)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !ok {
		t.Fatal("no API key metadata in request context")
	}
	if got.ID != env.activeID || got.ApplicationID != env.appID || got.UserID != wantUser ||
		got.PlanID != wantPlan || got.Status != auth.StatusActive {
		t.Errorf("context metadata = %+v, want id=%d app=%d user=%d plan=%d active",
			got, env.activeID, env.appID, wantUser, wantPlan)
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent log writes from server goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
