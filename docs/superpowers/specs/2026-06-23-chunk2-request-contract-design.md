# Chunk 2 — Request Contract: validation + governance verification + cost-routing

> **Status:** approved design, pre-implementation.
> **Date:** 2026-06-23
> **Scope:** Chunk 2 of the Combined Governance Envelope + Cost-Optimization program.
> First **behavior** chunk: the full combined request contract is accepted, validated,
> persisted, governance-verified, and cost-routed to a model. Builds on Chunks 0–1
> (goose harness + schema), both merged to `main`.
> Branch `chunk2-request-contract` off current `main` (`c52d5bf`). One chunk = one PR.

## 1. Goal & boundaries

Add a single new async generation entry point that carries the full combined contract,
and wire the two behaviors that **act** this chunk:

1. **Request DTOs + OpenAPI** (additive) for the combined contract.
2. **Governance verification gate** (D-3/E-1), behind `GOVERNANCE_ENFORCEMENT = log_only | enforce` (default `log_only`).
3. **Cost-routing**: `render.intent` draft|commit → model selection via the existing capability-floor router (extended with price-aware ranking).

Every contract field is **validated and persisted**. Only governance verification +
cost-routing act. Fields whose behavior belongs to later chunks are validated and stored,
**not acted on** — and where acting-on-them-silently would produce a wrong-cost or
wrong-result bug, the request is **rejected** rather than silently mis-served (see §5).

### Out of scope (later chunks)
Grid slicing (Ch6), transform execution (Ch7), `anchor_asset_id`/`derive_from` derivation (Ch5),
`lazy` generation policy (Ch8), cost reconciliation, the real signature crypto (stubbed here),
core's signing side, and worker execution of the new contract beyond standard single-image
generation. No premature `NOT NULL`/backfills on the Chunk-1 columns.

## 2. What exists today (starting point, with anchors)

- **Generation entry points** (all async via asynq, scope `images:write`): `POST /v1/artifacts/{id}/generate`
  (`internal/http/handlers/artifacts_handler.go:66`), `/v1/characters/{id}/generate-pack` +
  `/v1/places/{id}/generate-pack` (`packs_handler.go:189`), `/v1/styles/{id}/preview`. Routes wired in
  `internal/http/router.go`. DTOs are OpenAPI-generated (`internal/http/apigen/apigen.gen.go`).
- **No `DisallowUnknownFields`** anywhere today — `decodeFromRaw` (`handlers/decode.go:67`) uses a plain
  `json.Decoder`. The raw-body read (`readRawJSONBody`) exists for idempotency hashing.
- **Chokepoint today** (handler → `jobs.Service.CreateAndEnqueue`, `internal/jobs/service.go:354`):
  idempotency pre-check (`artifacts_handler.go:210`) → reuse lookup → **`Resolver.Resolve`**
  (`artifacts_handler.go:268`) → tx { `SetTenantLocal` → `AcquireSupersedeLock` → idempotency recheck →
  concurrent-cap → `InsertGenerationJob` (`service.go:464`) → **`reserver.Reserve`** (`service.go:471`) →
  `SetGenerationJobCost` → `InsertIdempotencyKey` → commit } → enqueue (`service.go:577`).
- **Router** (`internal/providers/routing/routing.go:184` `Resolve`, `:214` `ResolveChain`): hard filters
  (operation, provider pin, availability, quality tier, required capability, preview capability,
  provider-satisfies-route), then tie-break (latency, preference, priority). **No price-aware ranking.**
  Route source `DBRouteSource.ListRoutes` (`dbsource.go:24`) does NOT join prices.
- **Capabilities** (`internal/providers/provider.go:28`): `draft_only`, `scene_capable`,
  `identity_capable`, `pack_capable`, `production_capable`; hierarchy + `ProviderSatisfiesRoute`
  in `capability.go`. Capability requirement is hardcoded per-endpoint today (artifact→`scene_capable`,
  pack→`pack_capable`), **not derived from the request**. `RequiresReferenceImage` flag on adapters
  (fal, ADR-017) enforces reference-conditioning downstream.
- **`intent`, `max_megapixels`, and the other Chunk-1 columns** exist on `generation_jobs` (nullable),
  are scanned in every `RETURNING`, but **no writer sets them** and no routing reads `intent`.
- **Cost reservation** (`internal/cost/cost.go:92` `Reserver.Reserve(ctx, tx, ReserveInput) (Reservation, error)`):
  prices via `EstimateOperationCost` from `(provider, model, operation_type, units)`. A denial returns
  `nil` error with `Reservation.Status == "failed"` (`res.Failed()`); caller marks the job failed and
  commits without enqueue. Units/operation_type come from the existing quality-tier basis
  (`withCostContextPayload`, `service.go:1182`).
- **Audit events**: `InsertAuditEvent` sqlc query exists (`internal/db/dbgen/admin_cost.sql.go:109`),
  but the only writer is `admincost`'s private `writeAudit` (`admincost.go:509`). Convention:
  dotted `<domain>.<resource>.<action>`. No shared emitter package.
- **Config** (`internal/config/config.go`): typed-string enum pattern (`Provider`, `:12`), loaded via
  `getEnv` in `Load()` (`:79`), validated in `validate()` (`:161`) with a `switch/default`. Services
  receive scalar values (not `*Config`); wired in `cmd/api/main.go` + `cmd/worker/main.go`.
- **OpenAPI**: `docs/api/openapi.yaml` canonical + `api/openapi.yaml` byte-mirror; codegen
  `oapi-codegen.yaml` (types only, `models: true`); `make generate` regenerates; CI `openapi` job
  validates both + `diff -q` mirror; the `go` job runs `make generate` + `git diff --exit-code`.
- **RLS**: two-pool integration harness (`internal/jobs/integration_test.go`, `rls_integration_test.go`)
  — `POSTGRES_DSN` (owner/BYPASSRLS) vs `POSTGRES_API_DSN` (`image_platform_api`, RLS-enforced),
  `withGUC` sets `app.current_tenant`. CI `migrations` job asserts policy presence for the Chunk-1
  tables and cross-tenant blocking for baseline `generation_jobs`.

## 3. Decisions locked during brainstorming

- **Endpoint:** new additive **`POST /v1/generations`** (async, `images:write`). Strict
  `DisallowUnknownFields`; 422 on unknown/old-shape/bad enums. Existing endpoints untouched.
- **Cost-routing floor:** `identity_capable` whenever `subject.identity_id` is present (this closes the
  anchor-creation downgrade hole — anchor creation has `identity_id` set but no `anchor_asset_id` yet),
  else `scene_capable`. `intent=draft` → cheapest priced route at/above floor; `intent=commit` → premium
  (identity-capable / highest-tier) at/above floor; never below floor (422 if none); `provider_id` pin
  must still pass the floor.
- **Idempotency:** body `idempotency_key` is canonical (feeds the existing
  `(token,key,endpoint,request_hash)` machinery). Header present and ≠ body → 422; header-only (no body
  key) → 422 on this endpoint.
- **Required change #1 — reject deferred-behavior invocations (501):** a request with `render.transform_only == true`
  or `grid.enabled == true` is rejected **`501 Not Implemented`** and never routed through the single-image
  path (transform_only promises *no* provider spend but would spend; grid promises *N* assets but would
  return 1 — both are silent wrong-cost/wrong-result bugs). When these are off/absent, the fields are still
  validated and persisted. **Stored-not-acted (graceful, no 501):** `derive_from`, `anchor_asset_id`, `lazy`,
  and a present `render.transform` with `transform_only == false` — these are validated and persisted but not
  acted on this chunk. Documented caveat: a set `derive_from` (Ch5) and a present `transform` with
  `transform_only=false` (Ch7) **still produce a full, untransformed single-image generation this chunk** —
  only `transform_only=true` (which would falsely promise zero spend) and `grid.enabled=true` are rejected.
- **Required change #2 — reservation prices the existing basis, not the clamp:** cost reservation prices
  against what the worker actually generates this chunk (existing route + quality-tier `operation_type`/`units`),
  **NOT** the clamped `max_megapixels` (the worker does not enforce pixels yet) — avoiding reserved≠actual drift.
- **Required change #3 — real checks now, crypto stubbed, enforce warning:** the governance verifier runs the
  REAL field-presence / `issued_at` freshness / `authorized_by` allowlist checks now; only the signature
  **crypto** is an explicit no-op pass marked `TODO(core-signing)`. A **startup WARN** fires if
  `GOVERNANCE_ENFORCEMENT=enforce` while the signature verifier is still the stub, so `enforce` is not
  falsely trusted for signature integrity.

## 4. The combined contract (OpenAPI, additive)

New `components/schemas` in `docs/api/openapi.yaml` (+ `api/` mirror), regenerated into `apigen.gen.go`.
Top-level request schema `GenerationRequest`:

```jsonc
{
  "governance": {                      // all fields required; persisted to generation_jobs.governance_envelope (JSONB) + scalar cols
    "schema_version": "string",        // D-4 (required)
    "classification_id": "string",
    "visibility": "string",            // real column; vocabulary not pinned this chunk → validated as non-empty string
    "content_class": "string",         // OPAQUE — stored/logged, never parsed
    "authorized_by": "string",
    "issued_at": "date-time",
    "signature": "string"
  },
  "subject": {
    "identity_id": "string",           // required; must resolve (tenant-scoped visual_identities)
    "anchor_asset_id": "string|null",  // optional; validated as a reference when present
    "derive_from": "string|null"       // optional; soft ref; SET ⇒ still a full generation this chunk
  },
  "render": {
    "intent": "draft|commit",          // required enum → cost-routing
    "transform_only": false,           // bool, default false; TRUE ⇒ 501 (§5)
    "transform": null,                 // object|null; when present requires schema_version (D-4); execution Ch7
    "max_megapixels": 0,               // optional number; validated, clamped to platform ceiling, persisted (NOT priced)
    "provider_id": "string|null"       // optional pin; must pass the capability floor
  },
  "grid": {
    "enabled": false,                  // bool, default false; TRUE ⇒ 501 (§5)
    "contract_id": "string|null",      // validated when enabled (but enabled ⇒ 501)
    "cells": []                        // validated when enabled (but enabled ⇒ 501)
  },
  "lazy": false,                       // bool, default false; policy Ch8 (stored, not acted)
  "idempotency_key": "string"          // required; body-canonical
}
```

- New enum `Intent` (`draft|commit`) in the canonical enum block; referenced by `$ref`. Other nested
  objects are new schemas (`GovernanceEnvelope`, `GenerationSubject`, `RenderOptions`, `GridOptions`).
- The new contract has **no `quality_tier` knob** (unlike the legacy endpoints) — `render.intent` is the
  quality/cost selector. The worker generates a single image at its standard output basis (existing render
  edge); `max_megapixels` only clamps/persists (it does not set the worker's output this chunk, per change #2).
- `additionalProperties: false` is **not** the validation source of truth — Go-side `DisallowUnknownFields`
  is (oapi-codegen generates types only). Validation is explicit in the handler (matching the existing
  per-field validation style), returning 422 via `httperr`.
- Response: `202` with the existing `GenerationJobAccepted` schema.

## 5. Deferred-behavior rejection (501) — change #1

Immediately after structural validation and **before** the governance gate / routing / reservation:

- `render.transform_only == true` → `501` (`code: transform_only_not_supported`).
- `grid.enabled == true` → `501` (`code: grid_not_supported`).

No job is created for a 501. Rationale: these actively invoke Chunk-6/7 behavior that does not exist;
routing them through the single-image path would silently produce wrong cost (transform_only) or wrong
result count (grid). Requests with these fields **off/absent** proceed normally and the fields are persisted
(`transform_only=false`, grid disabled, `transform=null`). Placed before governance because it is a
system-capability rejection, independent of authorization. Tested.

## 6. Governance gate (D-3/E-1) — new `internal/governance`

- **`Envelope`** mirrors the `governance` object (+ `schema_version`). **`Verifier.Verify(ctx, env Envelope, subj SubjectMeta) Result`**
  where `SubjectMeta` carries only `identity_id`/`anchor_asset_id`/`derive_from` — **never prompt/description text**.
- **Real checks (run now):** (a) required fields present + `schema_version` present (D-4); (b) `issued_at`
  freshness within a configurable window `GOVERNANCE_MAX_AGE` (default `24h` — lenient for the log_only era;
  tighten when `enforce` ships; also rejects `issued_at` in the future beyond small skew); (c) `authorized_by`
  ∈ allowlist (`GOVERNANCE_AUTHORIZED_ISSUERS`, comma-list; empty ⇒ treated as "none recognized" → would-block
  in log_only).
- **Signature seam (crypto stubbed):** `SignatureVerifier.VerifySignature(ctx, env) (bool, error)` interface;
  `StubSignatureVerifier` returns `true` (pass) with `// TODO(core-signing): canonicalization + crypto is a
  cross-system contract with core, not yet designed — do not invent`. The real checks above still gate.
- **Enforcement** (`GOVERNANCE_ENFORCEMENT`, default `log_only`):
  - `log_only`: any failed real check → emit `media.eligibility_blocked` (reason + would-have-rejected) → **proceed**.
  - `enforce`: failed real check → **reject 403** + emit `media.eligibility_blocked`.
  - success → emit `media.eligibility_verified`, set `governance_verified_at`, proceed.
- **Startup WARN** (change #3): if `enforce` AND the active `SignatureVerifier` is the stub, log a WARN at
  boot (`cmd/api`, `cmd/worker`): "enforce active but signatures are stubbed — signature integrity is NOT verified."
- **`content_class` opaque**: stored + logged, never parsed/branched on.
- **Audit**: new minimal **`internal/audit`** emitter wrapping `InsertAuditEvent` (event_type, tenant_id,
  actor_token_id, resource_type=`generation`, resource_id=jobID-or-request-id, metadata JSONB). `admincost`
  left as-is (no unrelated refactor). Audit rows are tenant-scoped (RLS) → emitted within a tenant-scoped exec.
- Runs in the handler **before route resolution and before cost reservation** (chokepoint order).

## 7. Cost-routing (extend `internal/providers/routing`) — change #2

- Add to `ResolveRequest`: `Intent string` and a derived `RequiredCapability` floor =
  `identity_capable` if `subject.identity_id != ""` else `scene_capable`.
- **Intent-driven ranking** (new): the new path does NOT pass an exact `QualityTier` hard filter; `Intent`
  drives ranking among capability-valid candidates at/above the floor. `intent=draft` → **cheapest** by active
  `provider_model_prices` for the candidate's `(provider, model, operation_type)`. `intent=commit` → **premium**:
  highest `quality_tier` rank (high > standard > draft), tie-broken toward identity-capable, then the existing
  priority/availability tie-break. Implemented by enriching the candidate set with the active unit price
  (`LookupActiveUnitPrice` exists) + `quality_tier`, and adding the two comparators; the capability floor stays
  a hard filter. Never select below floor → 422 (`code: no_capable_route`).
- `provider_id` pin: hard filter that must still pass the floor (existing `ResolveRequest.ProviderID`).
- **No silent identity downgrade**, including anchor creation (`identity_id` set, `anchor_asset_id`+`derive_from`
  null) → identity-capable route under BOTH `draft` and `commit`.
- **`max_megapixels`**: validated; clamped to a platform ceiling; the clamped value persisted to the job
  payload. **NOT used for cost pricing** (change #2) and **NOT enforced at worker pixels** this chunk (deferred).
- **Reservation basis (change #2):** `reserver.Reserve` is called with `operation_type`/`units` derived from the
  resolved route + the worker's standard single-image output (the existing cost basis via the unchanged
  `withCostContextPayload` path), **NOT** from `max_megapixels` — so reserved cost matches what the worker
  actually generates and there is no reserved≠actual drift. A test asserts the reserved estimate is independent
  of `max_megapixels` (two requests differing only in `max_megapixels` reserve the same amount).
- Resolved provider/model/route persisted to `input_payload` via the existing `applyResolvedRoute` pattern.

## 8. Persistence + chokepoint

- **Writer:** extend the `InsertGenerationJob` sqlc query (explicit full-column list — standing Chunk-1 rule,
  no `*`, no row-adapter) to also write: `governance_envelope, classification_id, visibility, content_class,
  authorized_by, governance_verified_at, intent, transform_only, transform, max_megapixels, lazy,
  anchor_asset_id, derive_from`. The 3 existing generate handlers pass these as `nil` (additive, no behavior
  change). The full `subject`/`render`/`grid` objects are also stored in `input_payload` JSONB. `make generate`
  → zero drift.
- **Chokepoint (handler → tx):** decode + `DisallowUnknownFields` validate → **501 check (§5)** → idempotency
  resolution (body-canonical; header mismatch/header-only → 422) → idempotency pre-check (replay) →
  **verify governance (§6)** → **resolve route / cost-routing (§7)** → `CreateAndEnqueue` tx{ tenant GUC →
  advisory lock → idempotency recheck → concurrent cap → `InsertGenerationJob`(+Chunk-1 cols) → **reserve cost** →
  `SetGenerationJobCost` → `InsertIdempotencyKey` → commit } → enqueue. Governance failure (enforce) and
  capability failure both reject **before** cost reservation and before any provider call.
- **Worker:** the new endpoint enqueues via the existing artifact-generation task; the worker generates a
  single image from the resolved route (existing behavior). `transform_only`/`grid` can never reach the worker
  (501'd). `derive_from`/`anchor_asset_id`/`lazy` are stored, not acted (full generation still happens). No
  worker change this chunk.

## 9. Tests + RLS enforcement

- **RLS enforcement (carried from Chunk 1):** the new endpoint issues real writes/reads of the governance
  columns on `generation_jobs`, so prove **cross-tenant blocking** under `image_platform_api` (not just policy
  presence): a governance-bearing tenant-B `generation_jobs` row is invisible/unwritable to tenant A. Extend
  `internal/jobs/rls_integration_test.go` (add the Chunk-1 tables to the `protected` list; add a
  governance-column cross-tenant blocking assertion) and the CI `migrations` job (psql cross-tenant SELECT on a
  governance-bearing row under `image_platform_api`).
- **TDD, failing-test-first.** Key behaviors:
  - chokepoint order: governance runs before cost reservation (and a governance enforce-reject leaves no reservation).
  - **prompt never read by the gate** (gate input excludes prompt; a hostile prompt does not change the verdict).
  - **`content_class` opaque** (verdict independent of `content_class` value).
  - cost-routing: `draft`→cheapest≥floor, `commit`→premium≥floor; `no_capable_route` 422 when nothing meets floor.
  - **anchor-creation no-downgrade**: `identity_id` set, `anchor_asset_id`+`derive_from` null → identity-capable under BOTH intents.
  - `provider_id` pin passes floor (and is rejected when it cannot).
  - **reservation independent of `max_megapixels`** (change #2); MP clamp persisted.
  - **501** for `transform_only=true` and `grid.enabled=true`; off/default persists fields.
  - idempotency 4-case: header+body match → one record; mismatch → 422; body-only → works; header-only → 422.
  - `DisallowUnknownFields` → 422; old-shape → 422.
  - governance `log_only` proceeds + emits `media.eligibility_blocked`; `enforce` rejects 403; success emits `media.eligibility_verified`.
  - enforce-with-stub startup WARN present.

## 10. Rules, docs, follow-ups

- **D-3/E-1** (verify & store, never own policy / read prompt), **D-4** (governance_envelope + transform JSONB
  carry `schema_version` + validation), **D-8** (async only; no sync path), **D-9** (doc edits cite proving code).
- **sqlc explicit-column rule** honored; OpenAPI additive + mirror; CI green.
- **PR cites rule IDs** and records these **documented follow-ups** (do NOT build now):
  - **Governance hole:** the legacy resource-scoped endpoints (`/artifacts/.../generate`, pack, style-preview)
    remain ungoverned. A later chunk must route them through the same gate or retire them, else prod has one
    governed door and several ungoverned ones.
  - **Worker MP enforcement:** pixel-level clamp to `max_megapixels` rides with the later worker-wiring chunk.
  - **`derive_from` derivation:** a set `derive_from` still does a full generation this chunk (Chunk 5 adds derivation).
  - **Real signature crypto:** lands when core ships signing and the canonicalization format is pinned; flip default to `enforce` then.

## 11. Definition of done

Full contract DTOs + additive OpenAPI (mirror in sync, validator + diff green); every field validated and
persisted; `transform_only`/`grid.enabled` → 501; governance gate runs before cost reservation with `log_only`
default, real presence/freshness/allowlist checks, stubbed-signature seam, and the enforce-with-stub startup
WARN; prompt never read by the gate (tested); `content_class` opaque (tested); cost-routing selects cheapest
capability-valid by intent with no silent identity downgrade (anchor-creation tested), MP clamp computed +
persisted, reservation priced on the existing basis (tested independent of `max_megapixels`); audit events
emitted; RLS cross-tenant blocking asserted in CI + Go under `image_platform_api`; sqlc explicit-list honored,
zero codegen drift; PR cites rule IDs + the follow-ups. Spec → plan → first failing test.
