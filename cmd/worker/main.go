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
	"github.com/zakkriel/drchat-image-platform/internal/providers/bfl"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
	"github.com/zakkriel/drchat-image-platform/internal/webhooks"
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

	registry := buildRegistry(cfg, logger)

	// Phase 7C-4: outbound webhooks. The worker owns the emitter (it emits at
	// durable job-lifecycle transitions) and the deliverer (it runs the
	// webhook:deliver asynq task). Both share the webhooks repository over the
	// pool; the emitter enqueues deliver tasks via its own asynq client to the
	// same Redis (closed on shutdown).
	webhooksRepo := webhooks.NewRepository(pool)
	webhookEnqueuer := webhooks.NewEnqueuer(cfg.RedisAddr, cfg.RedisPassword)
	defer func() { _ = webhookEnqueuer.Close() }()
	webhookEmitter := &webhooks.Emitter{
		Repo:     webhooksRepo,
		Enqueuer: webhookEnqueuer,
		Logger:   logger,
	}
	webhookDeliverer := webhooks.NewDeliverer(webhooksRepo, nil, logger)

	worker := &jobs.Worker{
		Jobs:      jobs.NewRepository(pool),
		Assets:    assets.NewRepository(pool),
		Storage:   store,
		Providers: registry,
		Logger:    logger,
		Finalizer: cost.NewLifecycle(pool, logger),
		Webhooks:  webhookEmitter,
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
	mux.HandleFunc(webhooks.TaskDeliver, webhookDeliverer.NewHandlerFunc())

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

// buildRegistry registers exactly the providers configured in this process
// (Phase 7A): mock is always available; bfl is registered only when a
// BFL_API_KEY is set. The route resolver (API side) consults the same
// availability set via cfg.AvailableProviders(), so the worker is never handed a
// job for a provider it cannot invoke.
func buildRegistry(cfg *config.Config, logger interface {
	Info(msg string, args ...any)
}) *providers.Registry {
	reg := providers.NewRegistry()
	reg.Register(mock.ProviderID, mock.New())
	if cfg.BFLAPIKey != "" {
		reg.Register(bfl.ProviderID, bfl.New(cfg.BFLAPIKey))
		logger.Info("bfl provider registered")
	}
	return reg
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
