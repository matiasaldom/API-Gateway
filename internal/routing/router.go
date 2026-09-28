// Package routing matches request paths to upstreams by path prefix.
package routing

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
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
	if u.User != nil {
		return nil, fmt.Errorf("upstream %q must not contain credentials", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("upstream %q must not contain a query or fragment", raw)
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !SafeUpstreamIP(ip) {
		return nil, fmt.Errorf("upstream %q points at a link-local or unspecified address", raw)
	}
	return u, nil
}

// SafeUpstreamIP reports whether the gateway may connect to ip. Link-local
// addresses (169.254.0.0/16, fe80::/10) are refused because cloud metadata
// services live there; a route pointed at one would hand out instance
// credentials. Private and loopback addresses stay allowed: upstreams are
// usually internal services. The proxy's dialer checks this again after DNS
// resolution, so a hostname can't be used to get around it.
func SafeUpstreamIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified()
}

// ValidateUpstream reports whether raw is a usable upstream base URL.
func ValidateUpstream(raw string) error {
	_, err := parseUpstream(raw)
	return err
}

// Table holds the live Router. Readers get a consistent snapshot per call;
// Replace swaps in a new route set atomically, without a restart.
type Table struct {
	current atomic.Pointer[Router]
}

func NewTable(r *Router) *Table {
	t := &Table{}
	t.current.Store(r)
	return t
}

func (t *Table) Router() *Router   { return t.current.Load() }
func (t *Table) Replace(r *Router) { t.current.Store(r) }

func (t *Table) Match(path string) (Route, bool)    { return t.Router().Match(path) }
func (t *Table) CacheTTL(path string) time.Duration { return t.Router().CacheTTL(path) }

// RoutePrefix returns the prefix of the route matching path, or "" if none matches.
func (t *Table) RoutePrefix(path string) string {
	rt, _ := t.Router().Match(path)
	return rt.Prefix
}
