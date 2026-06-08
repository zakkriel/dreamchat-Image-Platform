package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/hibiken/asynq"

	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	logger := telemetry.NewLogger(cfg.LogLevel)
	logger.Info("worker starting",
		"environment", string(cfg.Environment),
		"concurrency", cfg.WorkerConcurrency,
		"image_provider", string(cfg.ImageProvider),
	)

	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	}
	srv := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: cfg.WorkerConcurrency,
		Logger:      asynqLogger{logger: logger},
	})

	mux := asynq.NewServeMux()
	// No task handlers registered yet — Phase 0 is just the bones.

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Run(mux); err != nil {
			errCh <- err
		}
	}()

	logger.Info("worker ready")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-stop:
		logger.Info("worker shutdown signal", "signal", sig.String())
		srv.Shutdown()
	case err := <-errCh:
		logger.Error("worker error", "error", err)
		os.Exit(1)
	}

	logger.Info("worker stopped")
}

// asynqLogger adapts slog to asynq's logger interface.
type asynqLogger struct {
	logger interface {
		Debug(msg string, args ...any)
		Info(msg string, args ...any)
		Warn(msg string, args ...any)
		Error(msg string, args ...any)
	}
}

func (a asynqLogger) Debug(args ...any) { a.logger.Debug(joinArgs(args)) }
func (a asynqLogger) Info(args ...any)  { a.logger.Info(joinArgs(args)) }
func (a asynqLogger) Warn(args ...any)  { a.logger.Warn(joinArgs(args)) }
func (a asynqLogger) Error(args ...any) { a.logger.Error(joinArgs(args)) }
func (a asynqLogger) Fatal(args ...any) { a.logger.Error(joinArgs(args)) }

func joinArgs(args []any) string {
	if len(args) == 0 {
		return ""
	}
	if s, ok := args[0].(string); ok {
		return s
	}
	return ""
}
