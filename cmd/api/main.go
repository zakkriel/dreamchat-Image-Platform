package main

import (
	"context"
	"errors"
	"log/slog"
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
	appdb "github.com/zakkriel/drchat-image-platform/internal/db"
	"github.com/zakkriel/drchat-image-platform/internal/governance"
	apphttp "github.com/zakkriel/drchat-image-platform/internal/http"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/bootstrap"
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

	// Phase 7C-3: two pools. The tenant pool connects as the RLS-enforced API
	// role (POSTGRES_DSN / image_platform_api) and backs every normal tenant
	// request handler + the jobs/admin-job services (which set app.current_tenant
	// inside their own transactions). The system pool connects as the BYPASSRLS
	// role (POSTGRES_SYSTEM_DSN / image_platform_system) and backs only the
	// explicit system / pre-tenant / admin-cross-tenant paths: auth token lookup,
	// the async last-used touch, the route resolver (global reference data), and
	// the admin cost surface.
	tenantPool, err := openPool(cfg.PostgresDSN)
	if err != nil {
		logger.Error("postgres connect failed (tenant pool)", "error", err)
		os.Exit(1)
	}
	defer tenantPool.Close()

	systemPool, err := openPool(cfg.SystemDSN())
	if err != nil {
		logger.Error("postgres connect failed (system pool)", "error", err)
		os.Exit(1)
	}
	defer systemPool.Close()
	systemDB := appdb.NewSystemDB(systemPool)

	enqueuer := jobs.NewEnqueuer(cfg.RedisAddr, cfg.RedisPassword)
	defer func() { _ = enqueuer.Close() }()

	// Phase 7C-2: reusable Redis client for per-token request-rate limiting,
	// wired from the same RedisAddr/RedisPassword as asynq. Closed on shutdown.
	// The limiter fails open on Redis errors, so a Redis outage degrades
	// request-rate limiting only — the Postgres-backed concurrent cap still holds.
	redisClient := ratelimit.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword)
	defer func() { _ = redisClient.Close() }()
	rateLimiter := ratelimit.New(ratelimit.NewRedisStore(redisClient), logger)

	// The finalizer is invoked from request-path services (jobs create enqueue
	// failure, admin cancel/retry) only via its executor-agnostic in-tx methods,
	// composed into tenant-scoped transactions on the tenant pool.
	finalizer := cost.NewLifecycle(tenantPool, logger)

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
	// Route resolution reads global reference tables (provider_routes /
	// provider_models), which carry no tenant_id and are NOT RLS-protected, so it
	// runs on the system pool.
	//
	// PRD 03 §8: wire the provider capability index so resolution enforces the
	// provider-satisfies-route check (a DB route cannot overstate its provider's
	// real capabilities) as defense-in-depth behind the boot-time reconciler.
	routeSource := routing.NewDBRouteSource(systemDB.Pool())
	capabilityIndex := bootstrap.CapabilityIndex(cfg)
	resolver := routing.NewResolver(routeSource, cfg.AvailableProviders()).
		WithProviderCapabilities(capabilityIndex).
		WithSyntheticIdentityAllowed(cfg.AllowSyntheticProviders)

	// PRD 03 §8: reconcile every configured route against the registered provider
	// capabilities at boot and log the result (route id, provider, model, required
	// capability, provider capabilities, decision) plus identity readiness — so a
	// misconfigured identity/pack route or a missing real identity-capable
	// provider is visible in logs rather than silently producing inconsistent
	// recurring characters. Reconciliation does not fail startup; the resolver
	// fails the request closed, matching the repo's fail-at-resolution pattern.
	reconcileRoutesAtBoot(context.Background(), logger, routeSource, capabilityIndex, cfg.AllowSyntheticProviders)

	// Chunk 2 governance gate. The RealVerifier uses the stub signature
	// verifier until the cross-system signing contract is finalized. Emit a
	// startup WARN when enforce+stub are combined so operators know signatures
	// are not actually verified (Task 9).
	sig := governance.StubSignatureVerifier{}
	gmode := governance.Mode(cfg.GovernanceEnforcement)
	if w := governance.EnforceWithStubWarning(gmode, sig); w != "" {
		logger.Warn(w)
	}

	deps := apphttp.Deps{
		Logger: logger,
		Config: cfg,
		// Auth resolves api_tokens BEFORE a tenant is known, and the async
		// last-used touch runs around auth too — both go through the system
		// (BYPASSRLS) pool. The api_tokens RLS policy is NOT weakened to allow a
		// pre-tenant prefix lookup; the system executor is the seam.
		AuthRepo:       auth.NewRepository(systemDB.Pool()),
		StylesRepo:     styles.NewRepository(tenantPool),
		IdentitiesRepo: identities.NewRepository(tenantPool),
		AssetsRepo:     assets.NewRepository(tenantPool),
		JobsRepo:       jobs.NewRepository(tenantPool),
		JobsService:    jobs.NewService(tenantPool, enqueuer, cost.NewService(logger)).WithFinalizer(finalizer),
		// Admin cost is an explicit admin-cross-tenant surface (guarded by the
		// admin:costs scope in the router): it lists/maintains cost data across
		// tenants and global price-book rows, so it uses the system executor.
		AdminCost: admincost.NewService(systemDB.Pool(), logger),
		// Admin job control is tenant-local (tenant from the principal), so it
		// runs on the tenant pool and sets app.current_tenant inside its own
		// cancel/retry transactions.
		AdminJobs: adminjobs.NewService(tenantPool, cost.NewService(logger), finalizer, enqueuer, logger),
		// Phase 7C-4: the API serves the webhook endpoint config surface (it
		// generates the signing secret and persists the per-tenant endpoint). It
		// is tenant-local (tenant from the principal), so it runs on the TENANT
		// pool and its repository sets app.current_tenant inside each
		// transaction — the webhook_endpoints RLS policy enforces isolation. The
		// API process does NOT emit events or enqueue deliveries (the worker does
		// that, on the system pool).
		WebhooksConfig: webhooks.NewConfigService(webhooks.NewRepository(tenantPool)),
		Storage:        store,
		Resolver:       resolver,
		RateLimiter:    rateLimiter,
		// Mode maps directly from the GOVERNANCE_ENFORCEMENT config variable;
		// sig and gmode are declared above where the startup WARN is emitted.
		GovernanceVerifier: governance.NewVerifier(
			sig,
			cfg.GovernanceMaxAge,
			cfg.GovernanceAuthorizedIssuers,
		),
		GovernanceMode: gmode,
		TenantPool:     tenantPool,
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

// reconcileRoutesAtBoot lists every configured route across the known operation
// types and reconciles it against the registered provider capabilities (PRD 03
// §8), logging the per-route decision and the identity-readiness summary. A
// failure to list routes (e.g. transient DB error) is logged but does not abort
// startup — the route resolver still enforces the same provider-satisfies-route
// check on every request as defense-in-depth.
func reconcileRoutesAtBoot(ctx context.Context, logger *slog.Logger, source routing.RouteSource, index map[string]providers.ProviderCapabilities, allowSyntheticIdentity bool) {
	routes, err := routing.GatherRoutes(ctx, source, reconcileOperations)
	if err != nil {
		logger.Warn("provider route reconciliation skipped: could not list routes", "error", err)
		// Still log readiness from the capability index alone (no routes needed).
		routing.LogReconciliation(logger, routing.Reconcile(nil, index, allowSyntheticIdentity))
		return
	}
	routing.LogReconciliation(logger, routing.Reconcile(routes, index, allowSyntheticIdentity))
}

// reconcileOperations is the set of operation types boot reconciliation inspects;
// it mirrors providers.OperationType so reconciliation covers every route the
// resolver could ever select.
var reconcileOperations = []string{
	string(providers.OperationTextToImage),
	string(providers.OperationImageToImage),
	string(providers.OperationUpscale),
	string(providers.OperationVariantPack),
	string(providers.OperationEdit),
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
