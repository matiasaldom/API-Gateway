// Package proxy wires routing, reverse proxying, and middleware into the gateway handler.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"api-gateway/internal/config"
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
//	request ID → access log → /health (public) | authenticate → router → proxy
func NewHandler(cfg *config.Config, logger *slog.Logger, authenticate func(http.Handler) http.Handler) (http.Handler, error) {
	if authenticate == nil {
		return nil, errors.New("authentication middleware is required")
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

	protected := authenticate(g)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Checked before auth and routing so load balancers can probe it
		// and a "/" catch-all route can't shadow it.
		if r.URL.Path == healthPath {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		protected.ServeHTTP(w, r)
	})
	return requestid.Middleware(AccessLog(logger, root)), nil
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := g.router.Match(r.URL.Path)
	if !ok {
		writeError(w, r, http.StatusNotFound, "no route for path")
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
			writeError(w, r, status, http.StatusText(status))
		},
	}
}

func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	writeJSON(w, status, map[string]string{
		"error":      msg,
		"request_id": requestid.FromContext(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
