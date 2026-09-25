package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api-gateway/internal/requestid"
)

type fakeStore struct {
	keys map[string]APIKey // keyed by string(hash)
	err  error
}

func (f *fakeStore) LookupAPIKey(_ context.Context, hash []byte) (APIKey, error) {
	if f.err != nil {
		return APIKey{}, f.err
	}
	k, ok := f.keys[string(hash)]
	if !ok {
		return APIKey{}, ErrKeyNotFound
	}
	return k, nil
}

type fixture struct {
	handler            http.Handler
	logs               *bytes.Buffer
	store              *fakeStore
	activeKey, revoked string
	reached            *bool
	gotKey             *APIKey
	gotAuthHeader      *string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	active, activeHash := mustKey(t)
	revoked, revokedHash := mustKey(t)
	store := &fakeStore{keys: map[string]APIKey{
		string(activeHash):  {ID: 1, ApplicationID: 10, UserID: 100, PlanID: 1000, Status: StatusActive, Hash: activeHash},
		string(revokedHash): {ID: 2, ApplicationID: 20, UserID: 200, PlanID: 2000, Status: StatusRevoked, Hash: revokedHash},
	}}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	f := fixture{logs: &logs, store: store, activeKey: active, revoked: revoked,
		reached: new(bool), gotKey: new(APIKey), gotAuthHeader: new(string)}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*f.reached = true
		*f.gotKey, _ = FromContext(r.Context())
		*f.gotAuthHeader = r.Header.Get("Authorization")
	})
	f.handler = requestid.Middleware(Middleware(store, logger)(next))
	return f
}

func mustKey(t *testing.T) (string, []byte) {
	t.Helper()
	raw, hash, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return raw, hash
}

func (f fixture) do(authorization ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/users/1", nil)
	for _, v := range authorization {
		req.Header.Add("Authorization", v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f fixture) assertRejected(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d (body %s)", rec.Code, status, rec.Body)
	}
	if *f.reached {
		t.Error("request reached the upstream handler")
	}
	if !strings.Contains(rec.Body.String(), `"request_id"`) {
		t.Errorf("error body missing request_id: %s", rec.Body)
	}
	f.assertNoRawKeysLogged(t)
}

func (f fixture) assertNoRawKeysLogged(t *testing.T) {
	t.Helper()
	for _, k := range []string{f.activeKey, f.revoked} {
		// Checking a slice of the key catches partial leaks too.
		if strings.Contains(f.logs.String(), k[len(keyPrefix):len(keyPrefix)+12]) {
			t.Fatalf("raw API key leaked into logs:\n%s", f.logs)
		}
	}
}

func TestMissingHeader(t *testing.T) {
	f := newFixture(t)
	rec := f.do()
	f.assertRejected(t, rec, http.StatusUnauthorized)
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="api-gateway"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	if !strings.Contains(f.logs.String(), `"reason":"missing_credential"`) {
		t.Errorf("missing reason in logs:\n%s", f.logs)
	}
}

func TestInvalidHeaderFormat(t *testing.T) {
	valid := newFixture(t).activeKey // well-formed key, used to build malformed headers
	tests := map[string][]string{
		"basic scheme":          {"Basic dXNlcjpwYXNz"},
		"scheme only":           {"Bearer"},
		"empty token":           {"Bearer "},
		"no scheme":             {valid},
		"double space":          {"Bearer  " + valid},
		"trailing garbage":      {"Bearer " + valid + " extra"},
		"wrong key prefix":      {"Bearer sk_" + valid[len(keyPrefix):]},
		"truncated key":         {"Bearer " + valid[:len(valid)-1]},
		"non-base64url key":     {"Bearer " + valid[:len(valid)-1] + "+"},
		"multiple auth headers": {"Bearer " + valid, "Bearer " + valid},
	}
	for name, headers := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			rec := f.do(headers...)
			f.assertRejected(t, rec, http.StatusUnauthorized)
			if !strings.Contains(rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
				t.Errorf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
			}
			if strings.Contains(f.logs.String(), valid[len(keyPrefix):]) {
				t.Errorf("presented credential leaked into logs:\n%s", f.logs)
			}
		})
	}
}

func TestInvalidKey(t *testing.T) {
	f := newFixture(t)
	unknown, _ := mustKey(t)
	rec := f.do("Bearer " + unknown)
	f.assertRejected(t, rec, http.StatusUnauthorized)
	if strings.Contains(f.logs.String(), unknown[len(keyPrefix):]) {
		t.Errorf("presented key leaked into logs:\n%s", f.logs)
	}
	if !strings.Contains(f.logs.String(), `"reason":"unknown_key"`) {
		t.Errorf("missing reason in logs:\n%s", f.logs)
	}
}

func TestRevokedKey(t *testing.T) {
	f := newFixture(t)
	rec := f.do("Bearer " + f.revoked)
	f.assertRejected(t, rec, http.StatusForbidden)
	if !strings.Contains(f.logs.String(), `"reason":"revoked_key"`) || !strings.Contains(f.logs.String(), `"api_key_id":2`) {
		t.Errorf("revocation not logged with key ID:\n%s", f.logs)
	}
}

func TestValidKey(t *testing.T) {
	for name, scheme := range map[string]string{"canonical": "Bearer", "case-insensitive scheme": "bearer"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			rec := f.do(scheme + " " + f.activeKey)
			if rec.Code != http.StatusOK || !*f.reached {
				t.Fatalf("status = %d, reached = %v; want request to proceed", rec.Code, *f.reached)
			}
			want := f.store.keys[string(HashKey(f.activeKey))]
			if f.gotKey.ID != want.ID || f.gotKey.ApplicationID != 10 || f.gotKey.UserID != 100 || f.gotKey.PlanID != 1000 {
				t.Errorf("context metadata = %+v, want %+v", *f.gotKey, want)
			}
			if *f.gotAuthHeader != "" {
				t.Error("Authorization header forwarded past the gateway")
			}
			f.assertNoRawKeysLogged(t)
		})
	}
}

func TestStoreFailureFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.store.err = errors.New("connection refused")
	rec := f.do("Bearer " + f.activeKey)
	f.assertRejected(t, rec, http.StatusServiceUnavailable)
}

func TestGenerateKey(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		raw, hash, err := GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		if !wellFormed(raw) {
			t.Fatalf("generated key %q is not well-formed", raw)
		}
		if !bytes.Equal(hash, HashKey(raw)) || len(hash) != 32 {
			t.Fatal("hash is not SHA-256 of the raw key")
		}
		if seen[raw] {
			t.Fatal("duplicate key generated")
		}
		seen[raw] = true
	}
}
