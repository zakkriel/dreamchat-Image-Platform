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

	"github.com/zakkriel/drchat-image-platform/internal/admincost"
	"github.com/zakkriel/drchat-image-platform/internal/adminjobs"
	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	apphttp "github.com/zakkriel/drchat-image-platform/internal/http"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
	"github.com/zakkriel/drchat-image-platform/internal/ratelimit"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
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

	enqueuer := jobs.NewEnqueuer(cfg.RedisAddr, cfg.RedisPassword)
	defer func() { _ = enqueuer.Close() }()

	// Phase 7C-2: reusable Redis client for per-token request-rate limiting,
	// wired from the same RedisAddr/RedisPassword as asynq. Closed on shutdown.
	// The limiter fails open on Redis errors, so a Redis outage degrades
	// request-rate limiting only — the Postgres-backed concurrent cap still holds.
	redisClient := ratelimit.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword)
	defer func() { _ = redisClient.Close() }()
	rateLimiter := ratelimit.New(ratelimit.NewRedisStore(redisClient), logger)

	finalizer := cost.NewLifecycle(pool, logger)

	// Phase 6B: the API needs the object-storage read side so asset/job-assets
	// reads can mint presigned per-tier download URLs (the worker already has
	// its own write-side client). Config already mandates the S3 env vars.
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

	// Phase 7A: data-driven provider route resolver. It reads provider_routes /
	// provider_models and only selects routes to providers configured in this
	// process (cfg.AvailableProviders): mock always; bfl only with a key.
	resolver := routing.NewResolver(routing.NewDBRouteSource(pool), cfg.AvailableProviders())

	deps := apphttp.Deps{
		Logger:         logger,
		Config:         cfg,
		AuthRepo:       auth.NewRepository(pool),
		StylesRepo:     styles.NewRepository(pool),
		IdentitiesRepo: identities.NewRepository(pool),
		AssetsRepo:     assets.NewRepository(pool),
		JobsRepo:       jobs.NewRepository(pool),
		JobsService:    jobs.NewService(pool, enqueuer, cost.NewService(logger)).WithFinalizer(finalizer),
		AdminCost:      admincost.NewService(pool, logger),
		AdminJobs:      adminjobs.NewService(pool, cost.NewService(logger), finalizer, enqueuer, logger),
		// Phase 7C-4: the API serves the webhook endpoint config surface (it
		// generates the signing secret and persists the per-tenant endpoint). The
		// API process does NOT emit events or enqueue deliveries — that is the
		// worker's job.
		WebhooksConfig: webhooks.NewConfigService(webhooks.NewRepository(pool)),
		Storage:        store,
		Resolver:       resolver,
		RateLimiter:    rateLimiter,
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
