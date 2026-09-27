// Package routing matches request paths to upstreams by path prefix.
package routing

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"api-gateway/internal/config"
)

type Route struct {
	Prefix   string
	Target   *url.URL
	CacheTTL time.Duration // 0 disables response caching
}

// Router resolves a request path to the route with the longest matching prefix.
// Prefixes match on path-segment boundaries: "/users" matches "/users" and
// "/users/42" but not "/usersettings".
type Router struct {
	routes []Route // sorted by prefix length, longest first
}

func New(defs []config.Route) (*Router, error) {
	if len(defs) == 0 {
		return nil, errors.New("at least one route is required")
	}

	seen := make(map[string]bool, len(defs))
	routes := make([]Route, 0, len(defs))
	for _, d := range defs {
		prefix, err := normalizePrefix(d.Prefix)
		if err != nil {
			return nil, err
		}
		if seen[prefix] {
			return nil, fmt.Errorf("duplicate route prefix %q", prefix)
		}
		seen[prefix] = true

		target, err := parseUpstream(d.Upstream)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", prefix, err)
		}
		if d.CacheTTL < 0 {
			return nil, fmt.Errorf("route %q: cache_ttl must not be negative", prefix)
		}
		routes = append(routes, Route{Prefix: prefix, Target: target, CacheTTL: d.CacheTTL})
	}

	sort.SliceStable(routes, func(i, j int) bool {
		return len(routes[i].Prefix) > len(routes[j].Prefix)
	})
	return &Router{routes: routes}, nil
}

// Match returns the route with the longest prefix matching path.
func (r *Router) Match(path string) (Route, bool) {
	for _, rt := range r.routes {
		if rt.Prefix == "/" || path == rt.Prefix || strings.HasPrefix(path, rt.Prefix+"/") {
			return rt, true
		}
	}
	return Route{}, false
}

// CacheTTL returns the cache TTL of the route matching path, or 0 if none matches.
func (r *Router) CacheTTL(path string) time.Duration {
	rt, ok := r.Match(path)
	if !ok {
		return 0
	}
	return rt.CacheTTL
}

// Routes returns all configured routes, longest prefix first.
func (r *Router) Routes() []Route {
	return append([]Route(nil), r.routes...)
}

func normalizePrefix(p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("route prefix %q must start with /", p)
	}
	if p != "/" {
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
	}
	return p, nil
}

func parseUpstream(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid upstream %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("upstream %q must use http or https", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("upstream %q has no host", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("upstream %q must not contain a query or fragment", raw)
	}
	return u, nil
}
