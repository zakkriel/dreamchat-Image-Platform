package main

import (
	"context"
	"errors"
	stdhttp "net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	apphttp "github.com/zakkriel/drchat-image-platform/internal/http"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	logger := telemetry.NewLogger(cfg.LogLevel)
	logger.Info("api starting",
		"environment", string(cfg.Environment),
		"port", cfg.AppPort,
		"image_provider", string(cfg.ImageProvider),
	)

	pool, err := openPool(cfg.PostgresDSN)
	if err != nil {
		logger.Error("postgres connect failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	deps := apphttp.Deps{
		Logger:   logger,
		Config:   cfg,
		AuthRepo: auth.NewRepository(pool),
	}

	router := apphttp.NewRouter(deps)
	srv := &stdhttp.Server{
		Addr:              ":" + strconv.Itoa(cfg.AppPort),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	logger.Info("api ready", "addr", srv.Addr)

	select {
	case sig := <-stop:
		logger.Info("api shutdown signal", "signal", sig.String())
	case err := <-errCh:
		logger.Error("api listen error", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("api shutdown error", "error", err)
		os.Exit(1)
	}
	logger.Info("api stopped")
}

func openPool(dsn string) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
