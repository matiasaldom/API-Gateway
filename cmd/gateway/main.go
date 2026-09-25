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
	"api-gateway/internal/config"
	"api-gateway/internal/proxy"
	"api-gateway/internal/storage"
)

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

	handler, err := proxy.NewHandler(cfg, logger, auth.Middleware(db, logger))
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
