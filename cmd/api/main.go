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

	"github.com/zakkriel/drchat-image-platform/internal/config"
	apphttp "github.com/zakkriel/drchat-image-platform/internal/http"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Logger isn't built yet; print and exit cleanly.
		println("config error:", err.Error())
		os.Exit(1)
	}

	logger := telemetry.NewLogger(cfg.LogLevel)
	logger.Info("api starting",
		"environment", string(cfg.Environment),
		"port", cfg.AppPort,
		"image_provider", string(cfg.ImageProvider),
	)

	router := apphttp.NewRouter(logger)
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
