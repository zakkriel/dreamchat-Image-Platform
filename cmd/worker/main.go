package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
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

	pool, err := openPool(cfg.PostgresDSN)
	if err != nil {
		logger.Error("postgres connect failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	store, err := storage.NewS3Storage(context.Background(), storage.S3Config{
		Bucket:          cfg.S3Bucket,
		Region:          cfg.S3Region,
		Endpoint:        cfg.S3Endpoint,
		AccessKeyID:     cfg.S3AccessKeyID,
		SecretAccessKey: cfg.S3SecretAccessKey,
		UsePathStyle:    cfg.S3UsePathStyle,
	})
	if err != nil {
		logger.Error("storage init failed", "error", err)
		os.Exit(1)
	}

	provider := buildProvider(cfg)

	worker := &jobs.Worker{
		Jobs:      jobs.NewRepository(pool),
		Assets:    assets.NewRepository(pool),
		Storage:   store,
		Provider:  provider,
		Logger:    logger,
		Finalizer: cost.NewLifecycle(pool, logger),
	}

	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	}
	srv := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: cfg.WorkerConcurrency,
		Logger:      asynqLogger{logger: logger},
	})

	mux := asynq.NewServeMux()
	mux.HandleFunc(jobs.TaskGenerateArtifact, worker.NewHandlerFunc())
	mux.HandleFunc(jobs.TaskGeneratePack, worker.NewPackHandlerFunc())

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

func buildProvider(cfg *config.Config) providers.ImageProvider {
	// Phase 3 only enables the mock provider end-to-end. BFL stays untouched
	// and is rejected at the API boundary with 503 provider_unavailable, so
	// the worker should never receive a BFL job.
	_ = cfg
	return mock.New()
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
