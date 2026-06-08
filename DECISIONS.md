# Implementation Decisions

> **Status**: locked for Phase 0–7 implementation. Captures the stack
> choices, environment configuration, and explicit deferrals agreed
> before code starts. New threads / new contributors should read this
> file first.

## Locked stack

| Concern | Choice | Rationale (one line) |
|---|---|---|
| Language | Go | ADR-002; HTTP+queue+S3+provider-call shape, low memory, single binary. |
| HTTP router | `chi` | Minimal, idiomatic, plays well with oapi-codegen handler interfaces. |
| OpenAPI codegen | `oapi-codegen` | Conservative, widely used, generates chi-compatible servers. |
| DB | Postgres | ADR-012; transactional metadata + JSONB. |
| DB access | `sqlc` + `pgx` | Typed SQL without an ORM; fits asset-state-first model. |
| Queue | `asynq` | ADR-013; matches Redis-for-everything-short-lived, good retry/delay primitives. |
| Queue backend | Redis | ADR-013. |
| Object storage | S3-compatible (AWS S3 default) | ADR-011; MinIO in local, R2 viable later. |
| First real provider | BFL | Closer to model provider than a marketplace abstraction; shapes the adapter interface correctly. |
| Test / dev provider | `mock_provider` | Works from day one, deterministic bytes, no provider key needed. |

## Production target (AWS-shaped)

| Component | Service |
|---|---|
| API + workers | Go Docker containers on ECS Fargate (or any Docker-compatible host) |
| DB host | AWS RDS Postgres |
| Cache + queue | **AWS ElastiCache Redis, single-shard with replicas, Cluster Mode Disabled** |
| Object storage | AWS S3 |
| Secrets | AWS Secrets Manager in prod; env vars OK for MVP |

**ElastiCache topology lock-in**: single-shard + replicas, **Cluster Mode
Disabled**. Asynq supports Redis Cluster only with hash-tag gymnastics
that aren't worth the trouble at MVP scale. Revisit when single-shard
write throughput becomes the bottleneck.

## Local development

```yaml
# docker-compose.yml services
image-platform-api      # cmd/api binary
image-platform-worker   # cmd/worker binary
postgres                # matches RDS in prod
redis                   # matches ElastiCache in prod
minio                   # S3-compatible local
minio-init              # one-shot job that creates the bucket
```

`make up` brings the stack up. `make migrate` applies
`docs/db/initial_schema.sql`. `make dev` is the full bootstrap (compose
+ migrate + seed dev token). `make test` runs `go test ./...`.

## Canonical environment variables

There is **one** name per concern. No aliases.

```
APP_PORT                     # default 8080
ENVIRONMENT                  # dev | test | live (matches token environments)
LOG_LEVEL                    # info | debug
WORKER_CONCURRENCY           # asynq worker pool size

POSTGRES_DSN                 # pgx-compatible connection string
REDIS_ADDR                   # host:port; auth via REDIS_PASSWORD if set
REDIS_PASSWORD               # optional

S3_BUCKET
S3_REGION
S3_ENDPOINT                  # full URL; configurable so MinIO/R2 work
S3_ACCESS_KEY_ID
S3_SECRET_ACCESS_KEY

IMAGE_PROVIDER               # mock | bfl  — the single provider switch
BFL_API_KEY                  # only required when IMAGE_PROVIDER=bfl

API_TOKEN_PEPPER             # for hashed token storage (ADR-005)
OPENAPI_DOCS_ENABLED         # true in dev/test; gated in live
```

**Provider switch is `IMAGE_PROVIDER` only.** No `PROVIDER_DEFAULT`, no
aliases.

## Provider adapter interface

Adapters live in `internal/providers/{mock,bfl,...}` and implement:

```go
type ImageProvider interface {
    Generate(ctx context.Context, req ProviderGenerateRequest) (ProviderGenerateResult, error)
    PollStatus(ctx context.Context, providerJobID string) (ProviderJobStatus, error) // may return ErrNotApplicable for sync providers
    Upscale(ctx context.Context, req ProviderUpscaleRequest) (ProviderGenerateResult, error) // may return ErrNotImplemented
    Capabilities() ProviderCapabilities // includes preview_capability
}
```

**Not on the adapter:**

- `GeneratePack` — pack orchestration is platform-side per ADR-008. The
  worker fans out per pack item, owns retry/reuse, writes
  `asset_pack_items`.
- Anything provider-specific (BFL request shapes, vendor error codes).
  Adapters normalize errors to the platform vocabulary in
  `docs/api/errors.md` before they leave the adapter package.

## Storage config

- Use `aws-sdk-go-v2`.
- Do **not** hardcode AWS endpoints anywhere in code or docs.
- Endpoint is set from `S3_ENDPOINT` via the SDK's endpoint resolver
  (whichever the current SDK version exposes — phrase config generically
  so SDK API changes don't ripple through docs).
- Path-style addressing in local dev (MinIO); virtual-hosted-style in
  prod and R2. Driven by `S3_USE_PATH_STYLE` env var if needed.

## Deferred to later phases (explicit)

These are **not** Phase 0–7 scope. They are tracked here so nobody
accidentally pulls them in.

- `POST /v1/admin/audit-events` endpoint — runbooks reference it for
  manual audit entries during the **MANUAL** fallback period. Add it
  when admin tooling lands (Phase 7).
- Token-pepper rotation runbook — design noted in ADR-005, no runbook
  written. Defer past Phase 3.
- LLM-judge → 1–5 score mapping for the benchmark runner. Human review
  is the only scoring method until then.
- Configurable safety margin on cost reservations
  (`reserved_amount = estimated_amount × (1 + margin)`). Pick a default
  before enabling enforcement, not before code starts.
- UTC vs. tenant-local midnight for budget period reset. UTC for MVP;
  revisit when serving customers across timezones.
- Provider-reported cost reconciliation worker. Reservations stay in
  `committed` with `actual_amount = estimated_amount` until a future
  reconciliation job overwrites with real reported cost.
- Row-level security (RLS) policies. Tenant isolation in the
  application layer for MVP; RLS is a future hardening pass.
- Webhooks (`generation_job.preview_ready`, `.completed`, `.failed`).
  Clients poll for MVP; webhooks land alongside web-app integration in
  Phase 6 if needed.

## Phase 0 acceptance

`make dev` brings the stack up. `curl localhost:8080/health` returns
`200 OK` with a `request_id` response header. CI runs
`openapi-spec-validator docs/api/openapi.yaml`, `go test ./...`,
`sqlc vet`, and applies `docs/db/initial_schema.sql` to a throwaway
Postgres. That's the bar.
