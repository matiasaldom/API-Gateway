// Package proxy wires routing, reverse proxying, and middleware into the gateway handler.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"sync"
	"syscall"
	"time"

	"api-gateway/internal/httpx"
	"api-gateway/internal/requestid"
	"api-gateway/internal/routing"
)

const healthPath = "/health"

type gateway struct {
	routes  *routing.Table
	proxies sync.Map // upstream URL string → *httputil.ReverseProxy
	timeout time.Duration
	logger  *slog.Logger
}

// NewHandler builds the full gateway handler:
//
//	request ID → access log → /health (public) | protect[0] → protect[1] → … → router → proxy
//
// Routes are read from the live table on every request, so route changes made
// through the admin API apply without a restart. protect runs in order on every
// non-health request; at least one is required so the gateway can't be built
// open by accident.
func NewHandler(routes *routing.Table, upstreamTimeout time.Duration, logger *slog.Logger, protect ...func(http.Handler) http.Handler) (http.Handler, error) {
	if len(protect) == 0 {
		return nil, errors.New("at least one protecting middleware (authentication) is required")
	}
	g := &gateway{routes: routes, timeout: upstreamTimeout, logger: logger}

	var protected http.Handler = g
	for i := len(protect) - 1; i >= 0; i-- {
		protected = protect[i](protected)
	}
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Checked before auth and routing so load balancers can probe it
		// and a "/" catch-all route can't shadow it.
		if r.URL.Path == healthPath {
			httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		protected.ServeHTTP(w, r)
	})
	return requestid.Middleware(AccessLog(logger, root)), nil
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := g.routes.Match(r.URL.Path)
	if !ok {
		httpx.Error(w, r, http.StatusNotFound, "no route for path")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), g.timeout)
	defer cancel()
	g.proxyFor(route.Target).ServeHTTP(w, r.WithContext(ctx))
}

// transport is http.DefaultTransport with two changes:
//
//   - A pool sized for a proxy. The default keeps only 2 idle connections per
//     host, so under concurrency almost every request opened a new TCP
//     connection and closed it afterwards. Benchmarks showed throughput
//     collapsing and 502s from ephemeral-port exhaustion (ADR-009).
//   - A dialer that refuses unsafe upstream addresses after DNS resolution
//     (see routing.SafeUpstreamIP).
var transport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: refuseUnsafeDial}
	t.DialContext = d.DialContext
	// ponytail: fixed pool size; idle connections beyond it are closed and
	// reopened. Make it configurable if one upstream sees more concurrency.
	t.MaxIdleConns = 4096
	t.MaxIdleConnsPerHost = 512
	return t
}()

// refuseUnsafeDial runs just before each upstream connection, with the resolved IP.
func refuseUnsafeDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !routing.SafeUpstreamIP(ip) {
		return fmt.Errorf("refusing to connect to %s: link-local or unspecified address", ip)
	}
	return nil
}

// LimitBody rejects request bodies larger than maxBytes with 413. A declared
// Content-Length is checked up front; chunked bodies are cut off by
// http.MaxBytesReader, and the proxy's error handler turns that into 413 too.
func LimitBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// proxyFor returns the reverse proxy for target, creating it on first use.
// Proxies share one transport, so connection pooling is per upstream host.
func (g *gateway) proxyFor(target *url.URL) *httputil.ReverseProxy {
	key := target.String()
	if p, ok := g.proxies.Load(key); ok {
		return p.(*httputil.ReverseProxy)
	}
	p, _ := g.proxies.LoadOrStore(key, g.newReverseProxy(target))
	return p.(*httputil.ReverseProxy)
}

func (g *gateway) newReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: transport,
		// SetURL keeps the incoming path and query, appending them to the target.
		// Headers (including X-Request-ID) and body are forwarded as-is; hop-by-hop
		// headers are stripped by ReverseProxy.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			status := http.StatusBadGateway
			var tooLarge *http.MaxBytesError
			switch {
			case errors.As(err, &tooLarge):
				status = http.StatusRequestEntityTooLarge
			case errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded):
				status = http.StatusGatewayTimeout
			}
			g.logger.ErrorContext(r.Context(), "upstream request failed",
				"request_id", requestid.FromContext(r.Context()),
				"upstream", target.String(),
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"error", err.Error(),
			)
			httpx.Error(w, r, status, http.StatusText(status))
		},
	}
}
