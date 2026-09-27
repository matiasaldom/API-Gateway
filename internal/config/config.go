// Package config loads the gateway configuration from YAML.
package config

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultListenAddr      = ":8080"
	DefaultUpstreamTimeout = 30 * time.Second
)

type Config struct {
	ListenAddr      string        `yaml:"listen_addr"`
	UpstreamTimeout time.Duration `yaml:"upstream_timeout"`
	Routes          []Route       `yaml:"routes"`

	// DatabaseURL comes from the DATABASE_URL environment variable so credentials stay out of the config file.
	DatabaseURL string `yaml:"-"`
	// AdminToken (ADMIN_TOKEN) guards /analytics/*; when empty those endpoints are not served.
	AdminToken string `yaml:"-"`
}

// Route maps a path prefix to an upstream base URL.
// Route validation lives in the routing package, which owns matching semantics.
type Route struct {
	Prefix   string `yaml:"prefix"`
	Upstream string `yaml:"upstream"`
	// CacheTTL caches successful GET responses for this long; 0 (the default) disables caching.
	CacheTTL time.Duration `yaml:"cache_ttl"`
}

// Load reads and decodes the YAML file at path, applying defaults.
// Unknown keys are rejected so typos fail at startup instead of being ignored.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = DefaultListenAddr
	}
	switch {
	case cfg.UpstreamTimeout < 0:
		return nil, fmt.Errorf("upstream_timeout must be positive, got %s", cfg.UpstreamTimeout)
	case cfg.UpstreamTimeout == 0:
		cfg.UpstreamTimeout = DefaultUpstreamTimeout
	}
	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	cfg.AdminToken = os.Getenv("ADMIN_TOKEN")
	return &cfg, nil
}
