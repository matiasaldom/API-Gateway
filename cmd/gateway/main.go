package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"api-gateway/internal/admin"
	"api-gateway/internal/auth"
	"api-gateway/internal/cache"
	"api-gateway/internal/config"
	"api-gateway/internal/httpx"
	"api-gateway/internal/limiter"
	"api-gateway/internal/metrics"
	"api-gateway/internal/proxy"
	"api-gateway/internal/requestid"
	"api-gateway/internal/routing"
	"api-gateway/internal/storage"
)

// ponytail: fixed response-cache budget; when full, new responses go uncached until
// the janitor frees expired ones. Make it a config field if deployments need to tune it.
const cacheMaxBytes = 256 << 20

// ponytail: fixed request body cap for proxied traffic (larger bodies get 413).
// Make it a config field, or per route, if an upstream accepts large uploads.
const maxRequestBody = 10 << 20

func main() {
	configPath := flag.String("config", "gateway.yaml", "path to YAML config file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*configPath, logger); err != nil {
		logger.Error("gateway stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(configPath string, logger *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	db, err := storage.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database unavailable: %w", err)
	}
	defer db.Close()
	logger.Info("database connected")

	// gateway.yaml declares which routes exist; the database holds their managed
	// upstream and cache TTL (editable via the admin API). The live table is what
	// the router, cache, and analytics read on every request.
	declared, err := routing.New(cfg.Routes)
	if err != nil {
		return err
	}
	records, err := db.SyncRoutes(context.Background(), declared.Routes())
	if err != nil {
		return err
	}
	for _, d := range declared.Routes() {
		for _, rec := range records {
			if rec.Prefix == d.Prefix && (rec.Upstream != d.Target.String() || rec.CacheTTL != d.CacheTTL) {
				logger.Warn("route settings differ from gateway.yaml; using managed values from the database",
					"prefix", rec.Prefix, "upstream", rec.Upstream, "cache_ttl", rec.CacheTTL.String())
			}
		}
	}
	router, err := routing.New(storage.RouteConfigs(records))
	if err != nil {
		return fmt.Errorf("stored routes: %w", err)
	}
	routes := routing.NewTable(router)
	responseCache := cache.New(cacheMaxBytes, time.Now)

	// Analytics events are written to PostgreSQL by a background worker.
	// Deferred after db.Close, so it runs first: pending events are flushed
	// (after srv.Shutdown has drained in-flight requests) before the pool closes.
	collector := metrics.NewCollector(db, logger, metrics.Options{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := collector.Close(ctx); err != nil {
			logger.Error("analytics flush on shutdown", "error", err.Error())
		}
		s := collector.Stats()
		logger.Info("analytics collector stopped", "written", s.Written, "failed", s.Failed, "dropped", s.Dropped)
	}()

	var rateLimiter limiter.Allower = limiter.New(time.Now)
	if cfg.RateLimitAlgorithm == config.TokenBucket {
		rateLimiter = limiter.NewTokenBucket(time.Now)
	}

	handler, err := proxy.NewHandler(routes, cfg.UpstreamTimeout, logger,
		metrics.Middleware(collector, routes.RoutePrefix), // first: times and counts everything, including auth failures
		proxy.LimitBody(maxRequestBody),                   // before auth: oversized bodies never cost a key lookup
		auth.Middleware(db, logger),
		metrics.Identify, // records the authenticated key on the analytics event
		limiter.Middleware(rateLimiter, logger),
		cache.Middleware(responseCache, routes.CacheTTL),
	)
	if err != nil {
		return err
	}

	// /metrics, /ready, /admin, and /analytics sit outside the API-key pipeline, like /health.
	// /health says the process is up; /ready also checks PostgreSQL, which every
	// authenticated request needs, so load balancers stop sending traffic without it.
	cacheMetrics := responseCache.MetricsHandler()
	ready := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			logger.WarnContext(r.Context(), "readiness check failed", "error", err.Error())
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "database": "unreachable"})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	var adminAPI http.Handler
	if cfg.AdminToken != "" {
		h, err := admin.New(db, routes, metrics.NewAnalytics(db, collector, logger), cfg.AdminToken, logger)
		if err != nil {
			return fmt.Errorf("ADMIN_TOKEN: %w", err)
		}
		adminAPI = requestid.Middleware(proxy.AccessLog(logger, h))
	} else {
		logger.Warn("admin and analytics endpoints disabled: ADMIN_TOKEN is not set")
	}
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/metrics" && r.Method == http.MethodGet:
			cacheMetrics.ServeHTTP(w, r)
			collector.WriteMetrics(w)
		case r.URL.Path == "/ready" && r.Method == http.MethodGet:
			ready.ServeHTTP(w, r)
		case adminAPI != nil && (strings.HasPrefix(r.URL.Path, "/admin/") || strings.HasPrefix(r.URL.Path, "/analytics/")):
			adminAPI.ServeHTTP(w, r)
		default:
			handler.ServeHTTP(w, r)
		}
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go responseCache.RunJanitor(ctx, time.Minute)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("gateway listening", "addr", cfg.ListenAddr, "routes", len(cfg.Routes), "upstream_timeout", cfg.UpstreamTimeout.String(), "rate_limit_algorithm", cfg.RateLimitAlgorithm)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
