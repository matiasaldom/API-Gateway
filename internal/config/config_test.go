package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
listen_addr: ":9090"
upstream_timeout: 5s
rate_limit_algorithm: token_bucket
routes:
  - prefix: /users
    upstream: http://localhost:8081
  - prefix: /albums
    upstream: http://localhost:8082
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":9090" || cfg.UpstreamTimeout != 5*time.Second || cfg.RateLimitAlgorithm != TokenBucket || len(cfg.Routes) != 2 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Routes[1] != (Route{Prefix: "/albums", Upstream: "http://localhost:8082"}) {
		t.Fatalf("unexpected route: %+v", cfg.Routes[1])
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "routes: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != DefaultListenAddr || cfg.UpstreamTimeout != DefaultUpstreamTimeout || cfg.RateLimitAlgorithm != FixedWindow {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]string{
		"unknown field":    "listen_adr: \":8080\"\n",
		"bad duration":     "upstream_timeout: soon\n",
		"negative timeout": "upstream_timeout: -1s\n",
		"unknown limiter":  "rate_limit_algorithm: leaky_bucket\n",
		"malformed yaml":   "routes: [\n",
	}
	for name, body := range tests {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("missing file: expected error")
	}
}
