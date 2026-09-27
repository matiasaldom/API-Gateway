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
	"syscall"
	"time"

	"api-gateway/internal/auth"
	"api-gateway/internal/cache"
	"api-gateway/internal/config"
	"api-gateway/internal/limiter"
	"api-gateway/internal/proxy"
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

	handler, err := proxy.NewHandler(cfg, logger,
		auth.Middleware(db, logger),
		limiter.Middleware(limiter.New(time.Now), logger),
		cache.Middleware(responseCache, routes.CacheTTL),
	)
	if err != nil {
		return err
	}

	// /metrics sits outside the request pipeline, like /health: no auth, no access log per scrape.
	metrics := responseCache.MetricsHandler()
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" && r.Method == http.MethodGet {
			metrics.ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
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
