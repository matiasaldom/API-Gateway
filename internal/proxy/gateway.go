// Package proxy wires routing, reverse proxying, and middleware into the gateway handler.
package proxy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"api-gateway/internal/config"
	"api-gateway/internal/httpx"
	"api-gateway/internal/requestid"
	"api-gateway/internal/routing"
)

const healthPath = "/health"

type gateway struct {
	router  *routing.Router
	proxies map[string]*httputil.ReverseProxy // keyed by route prefix
	timeout time.Duration
	logger  *slog.Logger
}

// NewHandler builds the full gateway handler:
//
//	request ID → access log → /health (public) | protect[0] → protect[1] → … → router → proxy
//
// protect runs in order on every non-health request; in production that is
// authentication followed by rate limiting. At least one is required so the
// gateway can't be built open by accident.
func NewHandler(cfg *config.Config, logger *slog.Logger, protect ...func(http.Handler) http.Handler) (http.Handler, error) {
	if len(protect) == 0 {
		return nil, errors.New("at least one protecting middleware (authentication) is required")
	}
	router, err := routing.New(cfg.Routes)
	if err != nil {
		return nil, err
	}

	g := &gateway{
		router:  router,
		proxies: make(map[string]*httputil.ReverseProxy),
		timeout: cfg.UpstreamTimeout,
		logger:  logger,
	}
	for _, rt := range router.Routes() {
		g.proxies[rt.Prefix] = g.newReverseProxy(rt.Target)
	}

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
	route, ok := g.router.Match(r.URL.Path)
	if !ok {
		httpx.Error(w, r, http.StatusNotFound, "no route for path")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), g.timeout)
	defer cancel()
	g.proxies[route.Prefix].ServeHTTP(w, r.WithContext(ctx))
}

func (g *gateway) newReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		// SetURL keeps the incoming path and query, appending them to the target.
		// Headers (including X-Request-ID) and body are forwarded as-is; hop-by-hop
		// headers are stripped by ReverseProxy.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			status := http.StatusBadGateway
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
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
