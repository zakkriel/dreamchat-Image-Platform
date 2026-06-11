# Phase 7A Confidence Index — Real Provider Routing + BFL Adapter

**Overall: 90/100 — Very High**

Phase 7A replaces the mock-only provider gate with a real, data-driven routing
layer. A generation request now resolves a provider route **once, at job
creation time**, before cost reservation; the resolved provider/model is the
pricing key, is persisted on the job, and is consumed verbatim by the worker —
which selects the provider adapter from a registry by the persisted
`provider_id` and stamps the resolved provider/model/route as visual-asset
provenance. Mock stays a first-class default route; BFL becomes a real,
selectable provider when configured. `true_preview` two-phase generation is
**not** implemented (Phase 7B). No new table — count stays **18**.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | Deterministic route resolver (filters + tested tie-break) | `internal/providers/routing/routing.go` | 92 |
| 2 | DB-backed route source (provider_routes ⋈ provider_models) | `internal/providers/routing/dbsource.go`, `internal/db/queries/provider_routes.sql` | 91 |
| 3 | Provider registry (`provider_id` → adapter; availability) | `internal/providers/registry.go` | 92 |
| 4 | BFL adapter (submit → poll → download; injectable client) | `internal/providers/bfl/bfl.go` | 88 |
| 5 | Idempotency replay **before** route resolution | `internal/jobs/service.go` (`LookupReplay`), handlers | 90 |
| 6 | Handlers resolve route, price resolved model, persist route | `artifacts_handler.go`, `packs_handler.go`, `style_preview_handler.go`, `routing.go` | 90 |
| 7 | Worker selects adapter by persisted id; stamps provenance | `internal/jobs/worker.go`, `worker_pack.go` | 91 |
| 8 | 422 routing errors replace 503 gate | `internal/httperr/errors.go`, handler error mapping | 92 |
| 9 | Seed migration `0006` (BFL provider/model/route/price; DML only) | `migrations/0006_bfl_provider_seed.up.sql` | 92 |
| 10 | Resolver/handler/worker/BFL unit tests + DB & e2e integration tests | `*_test.go` | 89 |
| 11 | Additive OpenAPI `0.6.0 → 0.7.0`, mirrored byte-for-byte | `api/openapi.yaml`, `docs/api/openapi.yaml`, `apigen.gen.go` | 93 |

## Resolver inputs and tie-break (92)

`ResolveRequest{TenantID, OperationType, QualityTier, LatencyTier,
RequiredPreviewCapability, ProviderPreference}` → `ResolvedRoute{ProviderID,
ProviderRouteID, ProviderModelID, OperationType, PreviewCapability}`.

Hard filters (each empty result short-circuits to the noted error):

1. active route (`is_enabled`) + active model (`status='active'`) + operation
   match → else `no_route`;
2. provider availability (only providers configured in this process) → else
   `provider_unavailable_for_route`;
3. quality tier match (when requested) → else `no_route`;
4. **general `required_capability` match** (when requested) → else
   `unsupported_capability`;
5. requested preview capability (when requested) → else
   `unsupported_capability`.

The resolver is **capability-aware on both axes**: `RequiredCapability` filters
on `provider_routes.required_capability` (general route capability:
`scene_capable`, `pack_capable`, …) and `RequiredPreviewCapability` filters on
`preview_capability` — independently. Routes can exist for the operation/quality
yet none satisfy the requested capability; that returns `unsupported_capability`,
**never** collapsed into `no_route` (`TestGeneralCapabilityUnsupportedNotCollapsedToNoRoute`).

**Handler capability mapping**: artifact generation and style preview request
`scene_capable`; pack generation requests `pack_capable` (**Option A**: a seeded
`route_mock_text_to_image_pack` with `required_capability=pack_capable` serves
it, reusing the mock text_to_image price). BFL's model floor is
`{draft_only,scene_capable}` with no pack route, so BFL is correctly **not**
eligible for pack generation; a pack request resolvable only to BFL returns
`unsupported_capability`.

Among survivors, the explicit, tested tie-break ranks: latency-tier match (when
requested) → configured provider preference → `provider_routes.priority` ASC
(lower preferred) → `provider_id` ASC → `model_id` ASC → `route_id` ASC. The
order is total and deterministic, so ties never depend on row/input ordering
(`TestTieBreakDeterministic`, `TestPriorityBeatsModelOrder`).

**Pricing is deliberately not part of resolution.** Route selection is
independent of price; the resolved model is priced at cost-reservation time,
where a missing/expired active price surfaces as `no_price_entry` (422). This is
what lets a request fail with `no_route` vs `no_price_entry` for the right
reason. `provider_routes` has no effective-date columns, so calendar
effective-dating applies only to prices and is enforced at the cost layer; the
"active" dimension the route/model schema supports (route `is_enabled`, model
`status`) is what the resolver filters on.

## Provider registry design (92)

`providers.Registry` maps `provider_id` → `ImageProvider`. A provider is
"available" iff it is **registered**, and it is registered only when configured:
`cmd/worker` registers `mock` always and `bfl` only when `BFL_API_KEY` is set.
The API resolver consults the **same** availability set via
`config.AvailableProviders()`, so the resolver can never select a route to a
provider the worker cannot invoke. The worker resolves the adapter by the
persisted `provider_id`; a missing adapter is a clear terminal failure
(`provider_unavailable`, reservation released) — never a silent fallback to
another provider.

## BFL high-res decision (92)

The seed and the adapter now **agree**: `provider_models.supports_high_res =
false` (migration 0006) matches `Capabilities().SupportsHighRes = false`. BFL
stays at its conservative floor — no high-res is claimed without provider /
benchmark evidence. CI asserts the seeded BFL model row has
`supports_high_res = false`.

## BFL configuration and testing (88)

Configured by `IMAGE_PROVIDER=bfl` (provider preference, ranked in tie-break) +
`BFL_API_KEY` (availability + auth). The adapter (`internal/providers/bfl`)
implements `ImageProvider.Generate` as submit → poll → download against the BFL
API using an **injectable** `Doer` HTTP client, a **bounded** overall timeout
(`context.WithTimeout`), a polling ticker that honors **context cancellation**,
and explicit error mapping (HTTP non-2xx, terminal/moderated statuses, malformed
JSON, empty image) wrapping `bfl.ErrProvider`. Documented request/response
assumptions are in the package doc comment (see below). Unit tests
(`bfl_test.go`) drive submit shape, poll, success mapping, content type, provider
error, malformed response, context cancellation, and bounded timeout — all
against a stub client with **no real network**. An end-to-end integration test
(`routing_integration_test.go`) drives the worker against a stubbed BFL HTTP
client and a real DB + MinIO.

## Lifecycle: idempotency replay → resolve → reserve → persist → enqueue (90)

The handler runs **idempotency replay first** (`Service.LookupReplay`): a replay
returns the existing job and does **not** re-resolve a route, re-reserve cost,
or re-enqueue (`TestIdempotencySameKeySameBodyReturnsSameJob` now asserts a
single create call). Only a new request resolves the route, then
`CreateAndEnqueue` reserves cost using the **resolved** model and persists the
job. The in-transaction `ON CONFLICT` idempotency machinery is retained for the
concurrent-race case.

## Where the resolved route is persisted, and proof of consistency (91)

`generation_jobs` has no first-class provider/model columns, so the resolved
route lives in `input_payload`. **The `jobs.Service` is the single persister**:
`CreateAndEnqueue` stamps `provider_id` / `model_id` / `provider_route_id` onto
the payload **from the cost params** (`withResolvedRoutePayload`), so the
worker-consumed route is identical to the pricing key by construction — and
every caller that sets the pricing context (handlers and direct-service tests)
gets a worker-runnable job with no separate payload step to keep in sync. The
handler also mirrors the values via `applyResolvedRoute` (identical values) so
handler-level tests can observe them. The worker reads them back
(`resolvedRouteFromPayload`) and stamps them on the `visual_assets` row. Proof:

- **Pricing uses the resolved model**: `applyResolvedRoute` sets
  `params.ProviderID/ModelID` from the resolved route, and
  `cost.Reserve` prices `(ProviderID, ModelID, OperationType)`.
  `TestArtifactGeneratePassesResolvedModelAndPreference` asserts the resolved
  model reaches `CreateAndEnqueue`; the e2e test asserts the echoed estimate is
  the resolved model's price (mock `0.0100`, BFL `0.0400`).
- **Worker uses the persisted provider id**:
  `TestEndToEndStampsResolvedMockProvenance` /`TestBFLRouteEndToEnd` assert the
  worker selected the adapter by the persisted id and produced an asset.
- **Provenance matches**: the same e2e tests read back
  `visual_assets.provider_id/model_id/provider_route_id` and assert they equal
  the resolved route (`mock`/`pm_mock_v1`/`route_mock_text_to_image_standard`,
  and the BFL trio).

## Seed migration choice (92)

`0006_bfl_provider_seed.up.sql` is **seed-only DML** (BFL provider model, route,
price rows, plus the `pack_capable` mock route for capability-aware pack routing)
— it adds **no table and no column**, so it is intentionally **not** listed in
`sqlc.yaml` (sqlc only needs schema-defining migrations) and the table count
stays **18** (verified locally). BFL's route is given a higher `priority` number (200) than
mock's (100), so mock stays the default when both are available and no provider
preference is supplied; BFL is selected when `IMAGE_PROVIDER=bfl` provides a
preference. CI applies `0006` and asserts the BFL seed rows exist.

## OpenAPI changes (93)

Strictly additive, `0.6.0 → 0.7.0`, mirrored byte-for-byte across
`api/openapi.yaml` and `docs/api/openapi.yaml`. `Error.code` stays a free-form
string (no enum), so the change is documentation-only: the `Error` schema and a
changelog entry document the new `422` codes (`no_route`,
`unsupported_capability`, `provider_unavailable_for_route`) that replace the
pre-7A `503 provider_unavailable` gate. No request/response field is added,
removed, or made required. `GET /v1/models` was **not** added (out of scope for
routing correctness).

## Explicit non-goals (not implemented)

`true_preview` two-phase preview/final generation, preview-first job lifecycle,
provider preview routing, rate limits, RLS, webhooks, admin retry/cancel, period
reset, CDN/signed-cookie delivery, tenant-specific provider overrides,
multi-image batches, provider fallback chains, automatic cross-provider retry,
route-management admin APIs, image moderation, provider cost reconciliation.
