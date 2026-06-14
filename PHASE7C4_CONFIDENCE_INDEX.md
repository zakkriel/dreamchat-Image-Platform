# Phase 7C-4 Confidence Index — Provider Fallback Chains + Outbound Webhooks

The final slice (4 of 4) of Phase 7C. Two independent capabilities shipped in
one PR: **same-price-class provider fallback** (migration-free) and **MVP-tight
outbound webhooks** (two new tables). Both integrate with the Phase 7C-3 RLS
hardening that merged immediately before this work.

Confidence is 0–100: how sure I am the deliverable is correct and complete as
described, not how important it is.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | `ResolveChain` returns the ordered candidate set via a shared `candidates()` helper (so `Resolve` and `ResolveChain[0]` cannot drift) | `providers/routing/routing.go` | 93 |
| 2 | `LookupActiveUnitPrice` for same-price-class comparison | `db/queries/cost.sql` | 94 |
| 3 | `samePriceFallbacks` keeps alternates whose `(price_per_unit, unit_type, currency)` equals the primary's; stamps `fallback_routes` on the payload | `jobs/service.go` | 90 |
| 4 | Handlers resolve the chain and pass alternates (artifact, style-preview, pack); chain error degrades to no fallbacks | `http/handlers/routing.go` + the three create handlers | 90 |
| 5 | `generateWithFallback` walks `[primary, …fallbacks]`, one attempt per route, skips unregistered adapters, stamps the WINNER as provenance | `jobs/worker.go` | 90 |
| 6 | `recordFailure` split into `recordAttemptFailure` + `failJobOnFinalAttempt`; post-generate paths unchanged | `jobs/worker.go` | 91 |
| 7 | No re-reservation: the single primary-priced reservation commits unchanged regardless of which same-price route wins | `jobs/worker.go` (commit), `cost` (untouched) | 91 |
| 8 | `webhook_endpoints` (one active config/tenant) + `webhook_deliveries` (delivery log); table count 18 → 20 | `migrations/0010_webhooks.up.sql` | 92 |
| 9 | `Sign` = `sha256=` + hex HMAC-SHA256 over the exact posted bytes | `webhooks/signer.go` | 95 |
| 10 | `Emitter`: lookup endpoint → insert pending delivery → enqueue `webhook:deliver`; nil-safe no-op when none | `webhooks/webhooks.go` | 91 |
| 11 | `Deliverer`: sign + POST, 2xx → delivered, else record failure + return for asynq retry (`MaxRetry 5`, exp backoff) | `webhooks/webhooks.go` | 91 |
| 12 | Worker emits completed (single + two-phase), preview_ready, and all terminal failures, AFTER durable commit, best-effort | `jobs/worker.go` | 90 |
| 13 | `PUT`/`GET /v1/admin/webhook-endpoint` (admin:jobs scope; tenant from principal; secret returned on PUT only) | `http/handlers/webhooks_handler.go`, `http/router.go` | 90 |
| 14 | Webhook tables get the SAME 7C-3 ENABLE+FORCE RLS + deny-by-default policy; config path on tenant pool via `db.WithTenant`, worker on BYPASSRLS system pool | `migrations/0010`, `webhooks/repository.go` | 89 |
| 15 | Additive OpenAPI `0.10.0 → 0.11.0`, api + docs mirrored | `api/openapi.yaml`, `docs/api/openapi.yaml` | 92 |
| 16 | Unit tests: fallback (3 cases), signer, deliverer (httptest), emitter, config handler | `*_test.go` | 89 |

## Same-price-class fallback (90)

Price is keyed on `(provider_id, model_id, operation_type)`, independent of
quality tier, and units are identical across the chain (same operation), so
comparing the three price components proves same-price class. The comparison is
on `price_per_unit::text` (canonical numeric text), so it is exact — no float
equality. Resolution stays at creation; the worker only consumes the persisted
chain. Because every persisted fallback is same-price, the existing reservation
is exactly valid for any winner, so there is no re-reservation and no worker
re-resolution. Asset/cost-event provenance honestly reflects the winning route,
while the job payload's primary `provider_id`/`model_id` (the priced key) is
unchanged.

## Webhook delivery + RLS (89)

The deliverer signs and POSTs the EXACT stored payload bytes (sign-and-send the
same `[]byte`), so a receiver recomputing the HMAC over the raw body matches.
At-least-once: asynq retries on non-2xx/transport error with bounded backoff;
the delivery row is the durable record (queue payload carries only the delivery
id). RLS: both tables are directly tenant-scoped and reuse the 7C-3 policy; the
tenant-scoped repo methods wrap `db.WithTenant` (so the config surface on the
tenant pool is genuinely gated), while the worker's by-id methods run on the
BYPASSRLS system pool exactly like the rest of the worker.

## What is explicitly NOT here

- No re-reservation on fallback (option A, by design) — fallback is limited to
  same-price routes; cross-price failover is future work.
- No webhook subscription management, dead-letter queue, event filtering,
  multiple endpoints per tenant, or signature-rotation endpoint.
- Events are NOT emitted for admin cancel, a preflight denial at job creation,
  or an enqueue failure — only the worker's durable lifecycle transitions.
- audit-events endpoint, product-safety filter, and the cost-reservation margin
  are out of scope (post-7C reconciliation pass, not this PR).

## Tests

- Unit (no DB/Redis): `routing` ResolveChain ordering + parity; worker fallback
  (primary-fails/fallback-succeeds with winner provenance, whole-chain-fail →
  failed+released, unavailable-adapter skip); webhook signer; deliverer against
  an `httptest.Server` (delivered + signature verified; 5xx → failed+retry);
  emitter no-op-without-endpoint and insert+enqueue-with-endpoint; config
  PUT-then-GET + no-endpoint 404.
- `go build ./...`, `go vet ./...`, `gofmt -l .` clean, full `go test ./...`
  green. sqlc v1.27.0 regeneration is additive-only (no version churn);
  OpenAPI api/docs byte-identical.

## Residual risk

- Webhook-table RLS is proven by construction (same policy/migration shape as
  7C-3) but not yet by a dedicated DB integration test in the 7C-3 RLS harness;
  recommended as a small follow-up alongside the post-7C reconciliation.
- Same-price fallback only fires when the route table holds ≥2 same-priced
  routes for an operation; default seeds price providers differently, so the
  chain is inert until such routes exist (intended trade-off of option A).
- Receivers should dedupe on `(job_id, event)` since at-least-once delivery can
  re-POST; the event body carries no separate delivery id in this MVP.
