package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/network-observer/network-observer/internal/config"
	"github.com/network-observer/network-observer/internal/server"
	"github.com/network-observer/network-observer/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("network observer stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	repository, err := store.Open(startupCtx, cfg.PostgresDSN, cfg.ClickHouseDSN, cfg.SummaryRetentionDays)
	cancelStartup()
	if err != nil {
		return err
	}
	defer repository.Close()

	app, err := server.New(cfg, repository, logger)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	httpServer := &http.Server{
		Addr: cfg.HTTPAddr, Handler: app, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	results := make(chan error, 2)
	go func() {
		logger.Info("HTTP server listening", "address", cfg.HTTPAddr)
		results <- httpServer.ListenAndServe()
	}()
	go func() {
		logger.Info("agent gRPC server starting", "address", cfg.GRPCAddr)
		results <- app.ServeGRPC(ctx)
	}()

	select {
	case <-ctx.Done():
	case err := <-results:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			cancel()
			return err
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return nil
}
