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

	"api-gateway/internal/auth"
	"api-gateway/internal/cache"
	"api-gateway/internal/config"
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

	// The cache resolves each path's TTL from the same route table the router uses.
	routes, err := routing.New(cfg.Routes)
	if err != nil {
		return err
	}
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
	routeFor := func(path string) string {
		rt, _ := routes.Match(path)
		return rt.Prefix
	}

	handler, err := proxy.NewHandler(cfg, logger,
		metrics.Middleware(collector, routeFor), // first: times and counts everything, including auth failures
		auth.Middleware(db, logger),
		metrics.Identify, // records the authenticated key on the analytics event
		limiter.Middleware(limiter.New(time.Now), logger),
		cache.Middleware(responseCache, routes.CacheTTL),
	)
	if err != nil {
		return err
	}

	// /metrics and /analytics sit outside the API-key pipeline, like /health.
	cacheMetrics := responseCache.MetricsHandler()
	var analytics http.Handler
	if cfg.AdminToken != "" {
		h, err := metrics.AnalyticsHandler(db, collector, cfg.AdminToken, logger)
		if err != nil {
			return fmt.Errorf("ADMIN_TOKEN: %w", err)
		}
		analytics = requestid.Middleware(proxy.AccessLog(logger, h))
	} else {
		logger.Warn("analytics endpoints disabled: ADMIN_TOKEN is not set")
	}
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/metrics" && r.Method == http.MethodGet:
			cacheMetrics.ServeHTTP(w, r)
		case analytics != nil && strings.HasPrefix(r.URL.Path, "/analytics/"):
			analytics.ServeHTTP(w, r)
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
	logger.Info("gateway listening", "addr", cfg.ListenAddr, "routes", len(cfg.Routes), "upstream_timeout", cfg.UpstreamTimeout.String())

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
