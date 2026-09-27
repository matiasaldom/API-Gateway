package routing

import (
	"testing"
	"time"

	"api-gateway/internal/config"
)

func TestMatch(t *testing.T) {
	r, err := New([]config.Route{
		{Prefix: "/users", Upstream: "http://localhost:8081"},
		{Prefix: "/albums/", Upstream: "http://localhost:8082"}, // trailing slash normalized
		{Prefix: "/users/admin", Upstream: "http://localhost:9000"},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path       string
		wantPrefix string // "" means no match
	}{
		{"/users", "/users"},
		{"/users/", "/users"},
		{"/users/42", "/users"},
		{"/users/42/profile", "/users"},
		{"/users/admin", "/users/admin"}, // longest prefix wins
		{"/users/admin/settings", "/users/admin"},
		{"/users/administrator", "/users"}, // segment boundary, not /users/admin
		{"/albums", "/albums"},
		{"/albums/7", "/albums"},
		{"/usersettings", ""}, // segment boundary
		{"/", ""},
		{"/photos", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got, ok := r.Match(tt.path)
		if tt.wantPrefix == "" {
			if ok {
				t.Errorf("Match(%q) = %q, want no match", tt.path, got.Prefix)
			}
			continue
		}
		if !ok || got.Prefix != tt.wantPrefix {
			t.Errorf("Match(%q) = %q (ok=%v), want %q", tt.path, got.Prefix, ok, tt.wantPrefix)
		}
	}
}

func TestMatchTarget(t *testing.T) {
	r, err := New([]config.Route{
		{Prefix: "/users", Upstream: "http://localhost:8081"},
		{Prefix: "/albums", Upstream: "http://localhost:8082"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := r.Match("/albums/1")
	if !ok || got.Target.String() != "http://localhost:8082" {
		t.Fatalf("Match(/albums/1) target = %v (ok=%v), want http://localhost:8082", got.Target, ok)
	}
}

func TestRootPrefixIsCatchAll(t *testing.T) {
	r, err := New([]config.Route{
		{Prefix: "/", Upstream: "http://fallback"},
		{Prefix: "/users", Upstream: "http://users"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Match("/anything"); got.Prefix != "/" {
		t.Errorf("Match(/anything) = %q, want /", got.Prefix)
	}
	if got, _ := r.Match("/users/1"); got.Prefix != "/users" {
		t.Errorf("Match(/users/1) = %q, want /users", got.Prefix)
	}
}

func TestNewRejectsInvalidRoutes(t *testing.T) {
	tests := map[string][]config.Route{
		"no routes":               nil,
		"relative prefix":         {{Prefix: "users", Upstream: "http://a"}},
		"duplicate prefix":        {{Prefix: "/users", Upstream: "http://a"}, {Prefix: "/users/", Upstream: "http://b"}},
		"missing scheme":          {{Prefix: "/users", Upstream: "localhost:8081"}},
		"unsupported scheme":      {{Prefix: "/users", Upstream: "ftp://a"}},
		"missing host":            {{Prefix: "/users", Upstream: "http://"}},
		"query in upstream":       {{Prefix: "/users", Upstream: "http://a?x=1"}},
		"negative cache ttl":      {{Prefix: "/users", Upstream: "http://a", CacheTTL: -time.Second}},
		"credentials in upstream": {{Prefix: "/users", Upstream: "http://user:pw@a"}},
		"unparseable address":     {{Prefix: "/users", Upstream: "http://a b"}},
	}
	for name, routes := range tests {
		if _, err := New(routes); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCacheTTL(t *testing.T) {
	r, err := New([]config.Route{
		{Prefix: "/users", Upstream: "http://a"},
		{Prefix: "/albums", Upstream: "http://b", CacheTTL: 30 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]time.Duration{"/albums/1": 30 * time.Second, "/users/1": 0, "/unrouted": 0} {
		if got := r.CacheTTL(path); got != want {
			t.Errorf("CacheTTL(%q) = %s, want %s", path, got, want)
		}
	}
}
