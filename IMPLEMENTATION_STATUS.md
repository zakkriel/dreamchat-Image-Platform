# Implementation Status

Canonical phase list for the implementation track. This is the source of
truth for "what's done / what's next" — the roadmaps in `prds/06` and
`prds/07` use different numbering and should not be used for sequencing.

Rule of thumb: the Phase 0–7 implementation track and cost-optimization Waves
1–3 are **complete**, and the three-repo integration is **functionally complete**
— the world backend is a live consumer through the pull contract (see
"Integration state" below). What remains before production-ready is not new
phases: it is Wave 4 (specification-only, behind release gates), the catalogued
non-MVP residue below, and the four cross-team items in
"Before production (integration track)" — chief among them the real signing
contract needed to leave `log_only`.

## Done

- **Phase 0** — skeleton: health, config, docker, migrations.
- **Phase 1** — auth + docs surface (status correction; this line was
  previously missing from the Done list even though the work shipped in
  `092059f` / `d2dc4e2`). Bearer-token authentication (ADR-004), store-only
  hashed tokens with `API_TOKEN_PEPPER` (ADR-005), the scope-enforcement
  middleware (`images:*`, `jobs:*`, `styles:*`, `models:*`, `admin:*`), DB
  wiring, and the OpenAPI docs surface (ADR-015): `GET /openapi.json` +
  `GET /docs`, gated by `OPENAPI_DOCS_ENABLED` (served unauthenticated in
  dev/test; default-off in live, and bearer-gated when enabled in live).
- **Phase 2** — visual-identity CRUD + style profiles.
- **Phase 3** — generation pipeline: artifact generate, jobs, worker,
  idempotency, S3 writes.
- **Phase 4A** — cost pre-flight: price book lookup, estimation, atomic
  budget reservation, failed-preflight replay.
- **Phase 4B** — cost lifecycle (commit/release, budget-hold
  reversibility) + admin cost surface + asset provenance (`model_id`).
- **Phase 5A** — pack fan-out basics: character/place pack jobs, multiple
  variants per job, batch orchestration (per-item generation, partial
  completion), pack status lifecycle. Variant keys are opaque strings;
  retrieval/reuse and preview-first remain 6A/6B.
- **Phase 5B** — variant logic: deterministic variant classification
  (`internal/assets/variants.go`), compatibility/provenance fields stamped
  on generated pack assets (`variant_family`, `compatibility_tags`,
  `fallback_allowed`, `fallback_rank`, structured `metadata`), named pack
  templates (`pack_template` request field, custom-pack override) — the
  minimal templates are the PRD 04 §4.2/§5.2 starter packs (7 character / 6
  place roles) and the no-template default derives from them — and a
  pure compatibility-matrix library (`internal/assets/compatibility.go`)
  built and tested for Phase 6A to consume. No DB retrieval is wired to the
  matrix yet; pack-completeness storage is deferred (no column exists).
- **Phase 6A1 — retrieval substrate / asset search**: the deterministic
  retrieval decision layer (`internal/assets/retrieval.go`) consuming the 5B
  classifier + matrix (exact → compatible → preview → generated_required,
  gated by `fallback_policy`); exact/candidate/compat-tag SQL
  (`internal/db/queries/visual_assets.sql`) on the existing indexes;
  retrieval-facing repository methods (`FindExact`,
  `ListRetrievalCandidates`, `ListRetrievalCandidatesByCompatTag`); and
  `POST /v1/assets/search` (tenant-scoped, `images:read`). Substrate only —
  **no generation, pack, cost, or preview behavior changed**; the
  product-safety filter (matrix §2) is a deliberate stub. No migration
  (table count stays 18); the search endpoint/schemas pre-existed and were
  wired, with two additive `AssetSearchRequest` fields
  (`style_profile_version`, `quality_tier`). Generated assets (artifact +
  pack paths) now persist `style_profile_id` so retrieval can find
  platform-produced assets, not just manually seeded rows — provenance
  stamping only, no generation/skip/reuse behavior change.

- **Phase 6A2 — single-artifact exact reuse**: artifact
  retrieval-before-generation on a deterministic prompt-hash. The artifact
  generate path (`POST /v1/artifacts/{artifact_id}/generate`) computes a
  deterministic render hash (`internal/assets/artifact_hash.go`, including
  `artifact_id` since artifacts have no durable visual identity) and, before
  reserving cost or enqueuing, looks for a ready artifact with that hash
  (`FindReadyArtifactByPromptHash`). A hit creates an already-completed
  cache-hit job (`cache_result=exact_match`, `final_asset_ids=[asset]`, zero
  cost, **no** reservation/provider attempt/enqueue/S3 write) via
  `Service.CreateCompletedCacheHitJob`; a miss generates as before and the
  worker now persists the render hash as `prompt_hash`, the request
  `quality_tier`, and the provider hash under
  `metadata.provider_prompt_hash`. Exact reuse is allowed for every
  `fallback_policy` (including `none`). Artifact reuse is **exact-hash only** —
  no compatible/preview/matrix/embedding fallback, no artifact visual
  identities. No new table (count stays 18); no OpenAPI change (the 202 stays
  an acceptance envelope, the completed state is observed via GET
  `/v1/jobs/{id}`). Pack reuse is untouched.

- **Phase 6A3 — pack reuse-first + completeness storage**: pack fan-out
  (`POST /v1/characters/{id}/generate-pack`, `POST /v1/places/{id}/generate-pack`)
  is now retrieval-first. At creation, before reserving cost or enqueuing, the
  handler resolves every required template role through the 6A1 identity/matrix
  retrieval layer (exact → compatible → preview → generated_required, gated by
  `fallback_policy`) and splits roles into **reused** (a ready asset satisfies
  them, persisted as `asset_pack_items` pointing at the existing assets in the
  create transaction) and **missing**. Pricing is **misses-only**
  (`Units = len(missing)`; zero misses → zero reservation). All-hits packs
  complete synchronously via `Service.CreateCompletedPackReuseJob` (pack +
  job `status=completed`, aggregate `cache_result`, `actual_cost_usd=0`, **no**
  reservation/provider attempt/enqueue) — the pack analogue of the 6A2 cache-hit
  job. Partial packs reserve for the misses, enqueue, and the worker generates
  only the missing roles (the reused items are already present, so the existing
  items-skip never regenerates them). Pack completeness
  (`required_roles`/`delivered_roles`/`missing_roles`) is stored on `asset_packs`
  (migration `0004`, additive columns — table count stays 18) and finalized by
  the worker; the worker derives final pack status from completeness
  (all delivered → `completed`, some missing/failed → `completed_with_warnings`,
  none → `failed`). No OpenAPI change. Idempotency unchanged (same body+key →
  same pack job + `asset_pack_id`, no duplicates). Artifact reuse (6A2) and
  `/v1/assets/search` (6A1) are untouched.

- **Phase 6A4 — forced regeneration (supersede-on-regenerate)**: a
  `force_regenerate` boolean (default `false`, strictly additive on
  `GenerateArtifactRequest`/`GenerateCharacterPackRequest`/`GeneratePlacePackRequest`)
  bypasses reuse and always generates. The artifact path skips the 6A2
  exact-hash lookup; the pack path skips per-role retrieval (`planPackReuse`),
  treats every required role as missing, and prices/generates the whole pack
  (no misses-only discount, no all-hits shortcut). A forced regeneration is a
  **real** generation (reservation + provider attempt + new asset + full budget
  spend) — there is no free/cache-hit regenerate. The worker then **supersedes**
  the slot: in one transaction, under a `pg_advisory_xact_lock` keyed on the
  exact slot, it inserts the new asset `ready` with `version = prior_max + 1`
  and archives every prior `ready` row of that exact slot
  (`status='archived'`, `superseded_by_asset_id` → new asset). The slot
  predicate is the exact reuse predicate (artifact prompt-hash slot;
  pack identity+variant+state+style+quality slot) — never matrix-based, so a
  compatible/preview neighbor is never archived. Committed readers therefore
  never see zero or multiple ready rows, and a subsequent non-forced request
  reuses the regenerated row (6A1 retrieval is `ready`-only and unchanged). Old
  packs are preserved historical snapshots: a forced pack creates a new
  `asset_packs` row with all-new assets and only flips the prior assets'
  `status`/link — prior `asset_pack_items` keep pointing at the now-archived
  assets. Idempotency is unchanged (`force_regenerate` is part of the request
  hash; a replayed forced request returns the same job and supersedes once).
  Schema: one additive nullable `visual_assets.superseded_by_asset_id`
  (migration `0005`, no new table — count stays 18). This closes Phase 6A.

- **Phase 6B — Delivery readiness** (Done): finished assets are now
  deliverable to a client. (1) **Presigned reads** — `storage.Storage` grew a
  `Presign(ctx, key, ttl)` minted from the deterministic object key via the
  AWS SDK v2 presign client, honoring `S3_ENDPOINT`/`S3_USE_PATH_STYLE` so
  MinIO (path-style) and R2 both work. URLs are computed **at read time and
  never persisted**: the `s3://` canonical URLs stay the durable provenance on
  `visual_assets`. (2) **Real resolution tiers** — the worker downscales the
  provider output (a fixed Catmull-Rom kernel in `internal/imaging`) into three
  genuinely distinct PNG tiers: `high`=final (provider output), `low`=preview
  (~768px short edge), `thumb`=thumbnail (~256px), never upscaled — so
  `derived_preview` is honest. (3) **Asset read UX** —
  `GET /v1/assets/{asset_id}` now additionally returns presigned per-tier
  `https` URLs (`thumbnail/preview/final_download_url` + `url_expires_at`,
  TTL=`S3_PRESIGN_TTL`, default 15m), and a new `GET /v1/jobs/{job_id}/assets`
  returns a job's delivered assets in deterministic delivery order (pack:
  `asset_pack_items.sort_order`; artifact: `final_asset_ids` order) — not
  restricted to `status='ready'` (archived assets stay displayable). Both are
  tenant-scoped + `images:read`-gated; a URL is only minted after the
  tenant-scoped row lookup succeeds, and keys are **derived**
  (`storage.ObjectKey`), never client-supplied. (4) **Style preview** —
  `POST /v1/styles/{style_id}/preview` (requires `world_id`, since assets are
  world-scoped) reserves + enqueues one sample artifact through the normal
  generate path; the sample is a normal delivered `visual_asset` read back
  through the same presigned machinery. Strictly additive OpenAPI
  (`0.5.4 → 0.6.0`, mirrored); **no migration** — presigning + tiers are
  runtime and the preview asset is found via job → asset, so the table count
  stays **18**. `true_preview` provider routing (a real latency-saving
  preview/final two-phase path) is explicitly **deferred to Phase 7** along
  with the BFL adapter and provider routing.

- **Phase 7A — Real provider routing + BFL adapter** (Done): generation is now
  routed through a data-driven resolver instead of the mock-only gate.
  (1) **Route resolver** (`internal/providers/routing`) selects a provider route
  from `provider_routes` joined to `provider_models`, filtering on active
  route + active model + operation + quality tier and on provider
  **availability** (only providers configured in this process), with an explicit
  tested tie-break (latency match → provider preference → route `priority` ASC →
  provider_id/model_id/route_id ASC). It is **capability-aware** on both
  `provider_routes.required_capability` (general route capability) and
  `preview_capability`: a request whose operation/quality matches but whose
  capability nothing satisfies returns `unsupported_capability` (not `no_route`).
  Handlers set the requirement explicitly: artifact + style preview →
  `scene_capable`, pack → `pack_capable` (served by a seeded pack_capable mock
  route; BFL's conservative floor is `scene_capable`, so BFL is correctly not
  eligible for packs). (2) **Resolve once, at
  job creation** — the handler runs idempotency-replay **first**, then resolves
  the route, then reserves cost **using the resolved model** (the pricing key),
  then persists the resolved `provider_id`/`model_id`/`provider_route_id` in
  `generation_jobs.input_payload` (no first-class columns; no migration for it).
  (3) **Provider registry** (`providers.Registry`) maps `provider_id` → adapter;
  the worker selects the adapter by the **persisted** provider id and never
  re-resolves, stamping the resolved provider/model/route as `visual_assets`
  provenance; a missing adapter fails the job clearly. (4) **BFL adapter**
  (`internal/providers/bfl`) is a real `ImageProvider`: submit → poll → download
  against the BFL API with an injectable HTTP client, bounded timeout, context
  cancellation, and meaningful error mapping; selectable when
  `IMAGE_PROVIDER=bfl` + `BFL_API_KEY` are set. BFL stays conservative — **no
  high-res**: the seed (`supports_high_res=false`) and the adapter
  (`SupportsHighRes:false`) agree. (5) **Error behavior** — route
  resolution failures are `422` (`no_route`, `unsupported_capability`,
  `provider_unavailable_for_route`), replacing the old `503 provider_unavailable`
  gate; a resolved model with no active price is still `422 no_price_entry`.
  Mock remains a first-class, default route through the same resolver. Seed
  migration `0006` adds the BFL provider/model/route/price rows **and** the
  pack_capable mock route (DML only — **no new table**, count stays 18; not in
  `sqlc.yaml`). Strictly additive OpenAPI (`0.6.0 → 0.7.0`, mirrored).
  `true_preview` two-phase generation is **not** implemented (Phase 7B).

- **Phase 7B — `true_preview` two-phase generation** (Done): a request can now
  opt into preview-first delivery and get a lighter preview asset before the
  final one, in a single job with a single charge.
  (1) **Request opt-in** — `delivery_mode: final_only | preview_first` (default
  `final_only`) added to `GenerateArtifactRequest` and `StylePreviewRequest`
  (OpenAPI `0.7.0 → 0.8.0`, strictly additive, mirrored). Packs stay
  final-only: the pack schema does not expose `delivery_mode`, and the pack
  handler ignores a stray field (the lenient decoder drops unknown keys), so a
  pack can never two-phase. (2) **Hard true_preview routing** — `preview_first`
  sets the resolver's `RequiredPreviewCapability=true_preview` alongside the
  normal `scene_capable`. Mock (a `true_preview` route) serves it; BFL
  (`no_preview`) is excluded, so a BFL-only `preview_first` request returns
  `422 unsupported_capability` **before** cost reservation, job creation, or
  enqueue. There is **no** downgrade to `final_only` and **no** `derived_preview`
  fallback (deferred). `final_only` is unchanged: no preview requirement, BFL
  stays selectable. Resolution still happens **once**, at job creation, after
  the idempotency-replay check; the resolved route's `preview_capability` is
  persisted on the payload so the worker never re-resolves. (3) **Worker
  two-phase lifecycle** — a job whose payload carries `delivery_mode=preview_first`
  **and** a `true_preview` route runs: generate a lower-resolution preview
  (`previewRenderEdge=512` < `deliveryRenderEdge=1024`), upload its tiers, insert
  a `visual_asset` `status=preview_ready` tagged `preview_safe`, then **commit**
  the job to `preview_ready` with `preview_asset_ids` — in DB transactions
  **separate from and before** final generation, so the preview is externally
  observable through `GET /v1/jobs/{job_id}` and `GET /v1/jobs/{job_id}/assets`
  before the final asset exists. It then generates the full-resolution final,
  inserts a `status=ready` asset, completes the job with `final_asset_ids`, and
  commits the cost reservation. Preview and final are **distinct rows**, both
  stamped with the resolved provider/model/route provenance. (4) **Cost once** —
  reserved once at creation, committed once after final success; the preview is
  never separately charged. (5) **Retry safety** — `preview_ready` is not
  terminal, so a retried preview-ready job resumes at **final**: a non-empty
  `preview_asset_ids` skips preview generation entirely (no duplicate preview,
  no re-charge). A completed/failed job still only finalizes cost (the Phase 7A
  short-circuit). (6) **Failure after preview** — if final generation fails after
  the preview was delivered, the job is `failed`, the reservation is **released**,
  `final_asset_ids` stays empty, and the preview asset stays readable
  (`preview_ready`, not archived/superseded); `GET /v1/jobs/{job_id}/assets`
  returns the preview (final takes precedence only when present). Single-phase
  `final_only`/omitted generation is **behaviorally unchanged** from Phase 7A.
  **No new table** (count stays **18**): only the existing `preview_ready`
  status / `preview_asset_ids` primitives plus two additive sqlc queries
  (`InsertPreviewVisualAsset`, `MarkGenerationJobPreviewReady`).

- **Phase 7C-1 — Admin job control + budget period reset** (Done): the platform
  can now cancel a non-terminal job (reclaiming its reserved cost), retry a
  failed job (without re-resolving its route), and enforce daily/monthly budgets
  per actual period instead of as lifetime caps. This is **slice 1 of 4** of
  Phase 7C; rate limiting + hard concurrent caps (7C-2), RLS / tenant isolation
  (7C-3), and provider fallback chains + webhooks (7C-4) are **not** in this
  slice.
  (1) **Admin cancel** — `POST /v1/admin/jobs/{job_id}/cancel` (scope
  `admin:jobs`; tenant from the principal, never the path/body). Allowed from
  `queued | running | preview_ready`; from `completed | failed` returns
  `409 invalid_state`; from `cancelled` it is idempotent (`200` with the existing
  job). A successful cancel sets `status=cancelled`, `completed_at=now()`,
  `error_code=cancelled`, a useful `error_message`, `retryable=false`, and
  releases the cost reservation **exactly once** — the status flip and the
  release commit in one transaction (`cost.Lifecycle.ReleaseInTx`), so the budget
  hold is reclaimed atomically. (2) **In-flight cancel guard** — the worker now
  persists output through guarded methods
  (`InsertFinalAssetAndCompleteJobIfNotCancelled`,
  `InsertPreviewAssetAndMarkPreviewReadyIfNotCancelled`) that lock the
  `generation_jobs` row, skip the write if the job is `cancelled`, and otherwise
  insert the asset + transition the job **atomically**. Admin cancel takes the
  same row lock, so a cancelled job can never end up with a final/preview output
  attached even if the provider returned just before the cancel landed. The
  worker treats `cancelled` as terminal (no provider call, upload, asset, commit;
  release is the only cleanup). (3) **Admin retry** —
  `POST /v1/admin/jobs/{job_id}/retry` (scope `admin:jobs`). Allowed only from
  `failed`; otherwise `409 invalid_state`. Retry keeps the **same job identity**
  and re-reserves cost against the **persisted resolved route** read from
  `input_payload` (`provider_id`, `model_id`, `provider_route_id`, operation,
  units) — it **never** calls the resolver. On a successful reservation the job
  returns to `queued` (failure fields + run timestamps cleared, stale
  `final_asset_ids` cleared, `preview_asset_ids` preserved so a preview-first job
  resumes at final), the fresh reservation is linked, and the job is enqueued. A
  denied reservation returns `422 no_price_entry` / `422 budget_exceeded`, leaves
  the job `failed`, and creates no live reservation (the speculative failed
  reservation rolls back). An enqueue failure after commit mirrors the create
  path: mark failed + release the fresh reservation. (4) **Lazy budget period
  reset** — `cost_budgets.period_start` (migration `0007`, additive column, table
  count stays **18**) anchors each budget to its current UTC window. At
  reservation time, inside the reserve transaction, a budget whose window has
  elapsed is rolled over atomically: `period_start` advances (daily → UTC date
  floor, monthly → first of the UTC month), `spent_amount` resets to 0, an
  `exceeded` status returns to `active` (a `paused` budget stays paused), and
  `reserved_amount` is **not** force-zeroed (a live hold opened just before the
  reset survives until its job terminates). The reset is idempotent under
  concurrency (conditional `period_start < window` update + row lock). The admin
  budget surface exposes `period_start` additively (create accepts an optional
  `period_start`, defaulting to the current window; update still mutates only
  `limit_amount`/`status`). **No cron, scheduler, or background worker** — reset
  is purely lazy. OpenAPI `0.8.0 → 0.9.0` (strictly additive, mirrored).

- **Phase 7C-2 — Rate limiting + hard concurrent job caps** (Done): the platform
  now throttles authenticated `/v1` traffic per token and hard-caps live
  generation jobs per token, before either can exhaust the API, queue, or
  provider path. This is **slice 2 of 4** of Phase 7C; RLS / tenant isolation
  (7C-3) and provider fallback chains + webhooks (7C-4) are **not** in this
  slice.
  (1) **Request-rate limiting** — a new `internal/ratelimit` package implements a
  fixed-window, per-token Redis counter for `requests_per_minute` (default 60)
  and `requests_per_hour` (default 1000). The counter increment and its TTL are
  created **atomically** in one Lua script (`INCR` then `PEXPIRE` only on the
  first increment), so a dropped connection can never leave a key without an
  expiry. Mounted as middleware on the `/v1` group **after** auth (it needs the
  principal) and **before** handlers/scope gates, so every authenticated
  request is counted — reads and admin endpoints included (admin tokens are
  mitigated via higher per-token overrides, not exemptions). Over-limit returns
  `429 rate_limit_exceeded` with `Retry-After` and `X-RateLimit-Requests-Per-*`
  headers (`*-Reset` = Unix seconds at the next window boundary). A denied
  request still increments the counter (the documented fixed-window trade-off).
  Redis errors **fail open**: the request is allowed, a warning is logged, and
  headers are omitted — so a Redis outage degrades request-rate limiting only.
  The limiter is nil-safe (no Redis configured ⇒ pass-through), so existing
  tests need no Redis. (2) **Hard concurrent-job cap** — a per-token cap on live
  generation jobs (`max_concurrent_jobs`, default 5), enforced in
  `jobs.Service.CreateAndEnqueue` **before** cost reserve / job insert /
  idempotency insert / enqueue. "Live" = `queued | running | preview_ready`
  (`preview_ready` counts because it is not terminal); `completed | failed |
  cancelled` free the slot, so a Phase 7C-1 cancel reclaims capacity. The cap is
  **hard** under parallel requests: inside the create transaction the service
  takes a transaction-scoped advisory lock keyed on the token (reusing the Phase
  6A4 `AcquireSupersedeLock`/`pg_advisory_xact_lock` helper), so concurrent
  creates for the same token serialize before counting
  (`idx_generation_jobs_token_status` supports the count). Over the cap returns
  `429 concurrent_jobs_exceeded` with **no** `Retry-After` (concurrency clears at
  a terminal state, not a fixed window) plus `X-RateLimit-Concurrent-Jobs[-
  Remaining]`; the denial has **no side effects**. The effective cap is threaded
  in via `CreateAndEnqueueParams` — the jobs service never reads the request
  context. (3) **Idempotency always wins over the cap** — both replay points
  bypass it: the handler pre-check (`LookupReplay`) returns the existing job
  before `CreateAndEnqueue`, and an in-transaction same-key conflict is detected
  under the advisory lock and replayed before the cap is counted. A replay is
  never denied by the cap, even at the cap, and creates no new load.
  (4) **Cache-hit exemption** — instant cache-hit completions
  (`CreateCompletedCacheHitJob`, `CreateCompletedPackReuseJob`) land at
  `completed` without reserving/enqueuing, occupy no live slot, and are not
  cap-checked. (5) **Per-token overrides** — `api_tokens` gains nullable
  `rate_limit_rpm` / `rate_limit_rph` / `max_concurrent_jobs` (migration `0008`,
  additive columns + `idx_generation_jobs_token_status`, table count stays
  **18**). `NULL` means platform default; the effective limits are resolved at
  auth and carried on the `Principal`, so neither the middleware nor the jobs
  service issues an extra query. (6) **Cost limits are untouched** — budget
  enforcement remains `422 no_price_entry` / `422 budget_exceeded`; rate limiting
  owns only the two `429` codes. OpenAPI `0.9.0 → 0.10.0` (strictly additive,
  mirrored): shared `429` `TooManyRequests` response + rate-limit header
  components on the four generation-create endpoints.

- **Phase 7C-3 — RLS / tenant isolation hardening** (Done): the database now
  **enforces** tenant isolation as defense in depth, so a missing or wrong
  `WHERE tenant_id = $1` in any current or future query can no longer leak rows
  across tenants. This is **slice 3 of 4** of Phase 7C; provider fallback chains
  + webhooks (7C-4) are **not** in this slice. The existing app-level tenant
  predicates **remain** — RLS is an additional layer, not a replacement.
  (1) **Forced RLS + deny-by-default policies** — migration `0009` enables AND
  **forces** row-level security (the owner is normally exempt; FORCE subjects it
  too) on every directly tenant-scoped table (`api_tokens`, `style_profiles`,
  `visual_identities`, `visual_assets`, `generation_jobs`, `asset_packs`,
  `cost_budgets`, `cost_reservations`, `generation_cost_events`, plus
  `audit_events`) and on the five tenant-owned child tables
  (`visual_identity_versions`, `asset_pack_items`, `provider_attempts`,
  `idempotency_keys`, `cost_reservation_budget_holds`) via parent-join `EXISTS`
  policies. The canonical predicate is text-safe — `tenant_id = NULLIF(current_
  setting('app.current_tenant', true), '')`, **never** a uuid cast (ids are TEXT
  like `tenant_it_jobs`) — and **deny-by-default**: an unset/empty GUC becomes
  `NULL`, matching no rows. Global reference tables (`provider_models`,
  `provider_routes`, `provider_model_prices`) are deliberately left readable.
  **No new table — count stays 18.** (2) **Two DB roles** — `image_platform_api`
  (non-superuser, no BYPASSRLS, subject to RLS) backs the tenant request path;
  `image_platform_system` (BYPASSRLS) backs the system/pre-tenant/admin-cross-
  tenant paths. Table ownership alone is **not** a valid bypass under FORCE RLS,
  so the system role gets explicit `BYPASSRLS`. (3) **Tenant executor** —
  `internal/db.WithTenant` runs request work in a transaction with
  `set_config('app.current_tenant', $1, true)` (transaction-local, so it never
  leaks across pooled connections); `SetTenantLocal` sets the same GUC inside a
  service-owned transaction. The hot write paths set the GUC internally:
  `jobs.Service.CreateAndEnqueue` (+ cache-hit and pack-reuse create), the cost
  reserve inside create, `identities` upsert, and `adminjobs` cancel/retry. The
  read-path repositories (styles, identities, assets, jobs read) run their reads
  inside `WithTenant` using the `tenant_id` they already receive. (4) **System
  executor** — `internal/db.SystemDB` is a distinct named type wrapping the
  BYPASSRLS pool, reachable only where deliberately wired: auth token lookup
  (pre-tenant) and the async `TouchAPITokenLastUsed`, the worker (job lookup by
  id), the route resolver (global reference data), and the admin cost surface
  (admin-cross-tenant after an `admin:costs` scope check). The `api_tokens`
  policy is **not** weakened for prefix lookup — auth uses the system executor
  instead. (5) **Executor-agnostic cost lifecycle** — `cost.Lifecycle` operates
  on the tx/pool it is handed: the worker calls standalone `Commit`/`Release`
  on the system pool (bypass), while admin cancel/retry compose `CommitInTx`/
  `ReleaseInTx` into a tenant-scoped transaction; it never chooses its own pool
  or hardcodes the system executor. (6) **Two-pool test harness + CI** — fixture
  seed/cleanup and every pre-existing integration test run on the system/bypass
  DSN (`POSTGRES_DSN`), so they pass unchanged; the new RLS-enforcement and
  tenant-executor tests run on the non-superuser API role (`POSTGRES_API_DSN`),
  the only way to actually observe enforcement. CI provisions the API role,
  asserts RLS is enabled+forced + policies exist + isolation/deny-by-default/
  WITH CHECK under the API role, and keeps the table count at 18. (7) **No
  client-visible change** — cross-tenant access still behaves like `404
  not_found`; OpenAPI is byte-for-byte unchanged (`0.10.0`).

- **Phase 7C-4 — Provider fallback chains + outbound webhooks** (Done): the
  **final slice 4 of 4** of Phase 7C, completing the production-controls track.
  (1) **Provider fallback chains (same-price class)** — the handler resolves an
  ordered fallback chain at job creation (`routing.Resolver.ResolveChain`, which
  shares the exact Stage 1–5 filters with `Resolve` via a private `candidates`
  helper, so `ResolveChain[0]` is always `Resolve`'s pick), then the jobs service
  filters the alternates to the **same-price class** — routes whose active unit
  price `(price_per_unit, unit_type, currency)` exactly equals the primary's
  (`LookupActiveUnitPrice`) — and persists the survivors as `fallback_routes` on
  `input_payload`. The worker (`generateWithFallback`) walks `[primary,
  …fallbacks]` on a provider failure, records a `provider_attempts` row per route,
  skips a route whose adapter is not registered in this process, and stamps the
  **winning** route as the asset/cost-event provenance. This preserves both
  invariants: route resolution happens **once at creation, never in the worker**,
  and because every fallback is same-price the **single existing cost reservation
  stays valid — there is no re-reservation**. **No migration** (fallbacks ride on
  the existing payload + tables). (2) **Outbound webhooks (MVP-tight)** — one
  signed endpoint per tenant (`webhook_endpoints`), HMAC-SHA256 request signing,
  three job-lifecycle events (`generation_job.preview_ready|completed|failed`),
  an asynq-backed deliverer with bounded retry/backoff (`asynq.MaxRetry(5)`,
  exponential), and a per-event delivery-attempt log (`webhook_deliveries`). The
  worker emits **after** each durable transition (single-phase completed; two-
  phase preview_ready + completed; all terminal failures via
  `failJobOnFinalAttempt`) — best-effort, never failing the job; not emitted for
  admin cancel / preflight-deny / enqueue-failure (documented MVP limit). The
  config surface is `PUT`/`GET /v1/admin/webhook-endpoint` (admin:jobs scope;
  tenant from the principal; server-generated secret returned on PUT). Event
  body: `{event, job_id, tenant_id, data, occurred_at}`; headers
  `X-DreamChat-Event` + `X-DreamChat-Signature: sha256=…`. (3) **RLS continuity
  (7C-3)** — the two new tables are directly tenant-scoped and get the SAME
  ENABLE + FORCE RLS + canonical deny-by-default `tenant_isolation` policy as the
  7C-3 tables (migration `0010`); the config path runs on the RLS-enforced tenant
  pool via `db.WithTenant`, while the worker emitter/deliverer run on the
  BYPASSRLS system pool like the rest of the worker. **Table count 18 → 20** (the
  first deliberate table growth since 6A3 — webhooks genuinely need persistent
  endpoint config + a delivery log). OpenAPI `0.10.0 → 0.11.0` (strictly
  additive, api + docs mirrored).

- **Provider capability reconciliation + fail-closed routing** (Done):
  implements **PRD 03 §8 (Provider Capability Floor)** and is captured in
  **ADR-016**. Config (`provider_routes`) is no longer trusted to state a
  provider's real capability. A single helper
  (`providers.CapabilitySatisfies` / `CapabilitiesSatisfy`) encodes the §8.3
  hierarchy (`production_capable` ⊇ `pack_capable` ⊇ `identity_capable`;
  `scene_capable`/`draft_only` parallel, satisfy only themselves) and is used
  **only** for provider-satisfies-route validation — **request-to-route matching
  stays exact** on `route.required_capability`, so cheap `scene_capable` work is
  never routed to identity/pack routes. At boot, `routing.Reconcile` checks every
  route against the registered adapters' `Capabilities()` and logs each decision
  (route id, provider id, model id, required capability, provider capabilities,
  decision) plus an identity-readiness summary; invalid routes are disabled by
  exclusion with loud WARN logs (the repo's fail-at-resolution pattern, not a
  boot abort). At resolution the resolver re-applies the check
  (`WithProviderCapabilities`) and fails closed with `route_capability_mismatch`
  (HTTP 422). The provider-satisfies-route check runs **last**, only on routes
  that survived every request-scoped filter (operation, availability, quality,
  exact `required_capability`, preview), so an unrelated invalid route never
  changes the error a request sees. Because resolution runs **before** cost
  reservation in the handler, a fail-closed rejection leaves **no dangling budget
  hold**. A `Synthetic` marker on `ProviderCapabilities` (set by mock) plus the
  `ALLOW_SYNTHETIC_PROVIDERS` env var (**default false in every environment** —
  safety does not key off `ENVIRONMENT`, since prod may run `ENVIRONMENT=dev`; via
  `WithSyntheticIdentityAllowed`) means synthetic providers do **not** back
  identity/pack routes unless explicitly opted in — so character/pack requests fail
  closed instead of resolving synthetic placeholder grids — while mock still backs
  scene routes everywhere and never makes production readiness report a real
  identity-capable provider. Current real provider BFL `flux-pro-1.1` is
  `scene_capable` only (scenes/artifacts, not recurring characters); recurring
  character consistency requires a reference/identity-capable provider and
  prompt-only retries do not solve it. **No migration, no provider integration,
  no cost-model change.** New shared seam `internal/providers/bootstrap` so API
  and worker agree on the provider set. Runbook:
  `docs/runbooks/provider-capability-misconfiguration.md`.

- **Webhook emission completeness + published event contract** (Done): the
  outbound push path from 7C-4 is now **safe for a cross-repo consumer to depend
  on**, and its payload is a machine-readable contract instead of a Go struct.
  (1) **Pack jobs emit.** `worker_pack.go` previously had **zero** emit
  callsites: every pack success and every pack failure — including
  `pack_all_items_failed` and `pack_invalid_job` — ran silently. Since a
  character/place pack is exactly the flow a consumer waits on, the push channel
  was unusable for its main use case. Pack fan-out now emits
  `generation_job.completed` with the delivered assets in `final_asset_ids`
  (a partial pack still completes; completeness is read back from the job) and
  `generation_job.failed` on every terminal pack failure, including the
  `failPackTerminal` branches. (2) **Unrunnable single-image failures emit.**
  `failTerminal` marked a job terminally failed and released its reservation
  **without emitting**, so `invalid_resolved_route`,
  `max_megapixels_exceeded`, `missing_reference_assets`, and
  `invalid_reference_asset` were silent terminal states — a gap that was not in
  the documented MVP-limits list. It now emits `generation_job.failed`.
  (3) **One emit-ordering rule, fixing a silent-loss bug.** Every event is
  emitted immediately after its status transition is durable and **before** cost
  finalization. The pre-existing single-image `completed` emit sat *after*
  `commitReservation`; because a commit error returns and the asynq retry
  short-circuits on the terminal status (re-running only finalization), a commit
  failure **dropped the completed event permanently**. Emission stays
  at-most-once per transition (the terminal short-circuit is what guarantees it)
  and strictly best-effort — an emitter error never fails a job.
  (4) **Contract published.** OpenAPI `0.13.0 → 0.14.0` (strictly additive, api +
  docs mirrored) adds an OAS 3.1 `webhooks` section for the three events, the
  `WebhookEventEnvelope` + per-event schemas, the `X-DreamChat-Event` /
  `X-DreamChat-Signature` header parameters, and a `WebhookFailedEvent`
  `error_code` enum of the **11 codes actually reachable through an emitted
  event** (`enqueue_failed` and `cancelled` are deliberately absent — those paths
  never emit). `info.description` records the two cross-repo invariants: pull
  (`GET /v1/jobs/{job_id}`) is authoritative and push is only a latency hint, and
  IDs are durable while download URLs are minted per read and expire.
  (5) **Tests.** Webhook emission had **no** test coverage in `internal/jobs`,
  which is why (1)–(3) survived. `worker_webhook_emit_test.go` covers pack
  completed / all-items-failed / invalid-job / `failPackTerminal`, single-image
  `failTerminal`, emission surviving a cost-commit failure, no re-emission on a
  terminal retry, and an emitter error never failing the job. All eight fail
  against the pre-change worker and pass after. **No migration** (table count
  stays **20** baseline / **24** with Chunk 1; head stays **19**).
  Cancellation still has no event type, and admin cancel / preflight denial /
  enqueue failure still deliberately do not emit.

- **`/v1/generations` exact-reuse 500 fix + integration quickstart** (Done):
  found by running the documented integration sequence against a real stack
  instead of reading it.
  (1) **Every cache hit on `POST /v1/generations` returned `500`.**
  `GenerationsHandler.respondCacheHit` built `CreateCacheHitParams` **without
  `FallbackPolicy`**, so the column arrived as `""` and the INSERT violated
  `generation_jobs_fallback_policy_check` (which accepts only
  `none|compatible_only|preview_allowed|any_existing`). The primary integration
  endpoint therefore failed on its *second* identical request — exactly the
  retrieval-first reuse the cost model and the "never re-request a portrait"
  contract depend on. The artifact path had always set it; only the combined
  contract omitted it. `WorldID` was omitted too, so a reused job silently lost
  the world lineage a generated job carries. Both now match the miss path
  (`compatible_only`, `identity.WorldID`), verified live: the reused request
  returns `202`, completes with `actual_cost_usd=0`, and resolves to the **same**
  `asset_id`.
  (2) **Why the tests missed it.** The existing cache-hit coverage used a stub
  creator, which accepts any params, and every cache-hit integration test set
  `FallbackPolicy` explicitly. Added `generations_cachehit_lineage_test.go`
  (cache-hit params must match the miss path on policy + world + job type, and the
  emitted policy must be a value the CHECK accepts) and two real-DB tests
  (`TestGenerationsCacheHitJobInserts` inserting the handler's exact params,
  `TestCacheHitEmptyFallbackPolicyRejected` proving the constraint is real so the
  first assertion is load-bearing).
  (3) **Unclassified create failures now log their cause.** `writeJobServiceError`'s
  `default` branch wrote `500 internal_error` and discarded the error, so this bug
  was diagnosable only by reading Postgres' own log. It now logs the cause with the
  request id; the response body stays generic.
  (4) **`docs/api/integration-quickstart.md`** — the verified end-to-end sequence
  for the world backend, with real request/response bodies from that run: the
  governance envelope (all 7 fields; `signature` is accepted as any non-empty
  string because `StubSignatureVerifier` passes unconditionally — use
  `stub-unsigned-v1`), the `log_only` semantics that matter (a `202` does **not**
  mean the envelope was accepted — an unknown issuer or a missing `issued_at` still
  audits `media.eligibility_blocked` and becomes `403` under `enforce`), required
  bounded/jittered polling backoff, the `identity_capable` config prerequisite
  (stock config returns `422 route_capability_mismatch` on **every** generation
  until `ALLOW_SYNTHETIC_PROVIDERS=true` or `FAL_KEY` is set), the idempotency trap
  (the key hashes the whole body, so a redrawn `issued_at` yields `409`), and the
  storage split (the asset's `visual_identity_id` is **NULL** on this path, so the
  consumer must persist `identity_id → asset_id` itself). OpenAPI `0.14.0 → 0.14.1`
  (description + client guidance only; no schema, endpoint, behavior, or
  generated-code change). **No migration** (head stays **19**).

## Cost optimization waves (Wave 3/4)

- **Wave 3 — Measurement + cost-accounting truth** (implemented): generation economics telemetry, planned-call reservation sizing, provider-reported cost reconciliation, reservation-scoped cost events, identity lifetime ledger updates, decoded-byte `max_megapixels` enforcement, and pack fallback parity are wired through the governed paths. Design + verification: `docs/superpowers/specs/2026-08-08-wave3-cost-truth-design.md`.
- **Wave 4 — Amortization** (specification only): no sprite-sheet pipeline, anchor-derive default, or lazy-finalization implementation is shipped. The design and release gates are documented in `docs/superpowers/specs/2026-08-08-wave4-amortization-design.md`.

### Wave 3 validation

- Full unit suite, `go vet ./...`, and `gofmt -l .` clean; `sqlc generate` produces no diff.
- PostgreSQL 15 at migration 19, `POSTGRES_DSN` set so DB-backed tests execute rather than skip:
  `go test -tags=integration ./internal/jobs ./internal/adminjobs ./internal/migrate ./internal/assets ./internal/identities ./internal/http/handlers` — all ok.
- Migration `0019` applies and rolls back; cost events are attributable to the reservation that priced them across a retry reusing the same `generation_job_id`.
- Concurrency and budget semantics match the pre-Wave-3 baseline: the concurrent-job cap, tight-budget denial, budget-window reset, and concurrent idempotency tests all pass unchanged.

## Integration state — world backend is a live consumer

The three-repo integration (`dreamchat-world-backend`, `dreamchat-frontend` — since archived and
superseded by `dream-weaver-visuals`, `workspace:ADR-W003` — and this
platform) is **functionally complete**: the world backend ran a live handshake
against this platform and the frontend renders portraits end to end through it.

**Contract of record: pull, not push.** The world backend creates a generation and
then reads `GET /v1/jobs/{job_id}` followed by `GET /v1/jobs/{job_id}/assets`.
Outbound webhooks are **not** on the integration path — they remain a future
latency hint. This was a deliberate joint decision: the world backend has no async
channel, and a contract where a dropped event degrades latency but never
correctness is the property all three repos wanted. `GET /v1/jobs/{job_id}` is
therefore authoritative, and a consumer that ignores webhooks entirely is correct.

**Shape of the integrated flow** (the world backend's PoC scope is one background
per scene and one portrait per entity — **single image, not the 7-role pack**; a
role taxonomy is not defined product-side, and variation is handled by their
regeneration history rather than seven upfront variants):

```
POST /v1/styles → POST /v1/characters/{id}/visual-identity → POST /v1/generations
  → poll GET /v1/jobs/{id} → GET /v1/jobs/{id}/assets
```

**Provisioned for the live window** (dev environment):

- Tenant `tenant_world_backend`; token `tok_wb_handshake_2hzyj9`, `active`, with
  **exactly four scopes** — `styles:write`, `images:write`, `jobs:read`,
  `images:read`. Delivered to the consumer as `DREAMCHAT_IMAGE_API_TOKEN` +
  `DREAMCHAT_IMAGE_BASE_URL` env vars, never persisted on their side.
- `tenant` currently means **the deployment**: the world backend has no
  customer/account model, so one token → one tenant, and a token maps to no
  particular world. `world_id` remains an ordinary scoping column beneath it.
- `GOVERNANCE_ENFORCEMENT=log_only` with
  `GOVERNANCE_AUTHORIZED_ISSUERS=svc_world_backend`, set in **both** launch paths
  (`scripts/dev.sh`, which is what `make start` runs and what serves the host API,
  and the `docker-compose.yml` api service). API only — the worker never verifies
  envelopes.
- `ALLOW_SYNTHETIC_PROVIDERS=true`, so `mock` backs the `identity_capable` floor
  and the handshake needs no identity anchors.

**What the live window proved.** Health, the four-scope token, the seven-field
governance envelope under `log_only` (auditing `media.eligibility_verified`, not
`unknown_issuer`), a `202` create, terminal `completed`, and a presigned URL
returning a real PNG. It also surfaced two defects and two client-side traps that
are now fixed and documented rather than folklore:

- **Defect — exact reuse returned `500`.** Every cache hit on `POST /v1/generations`
  violated `generation_jobs_fallback_policy_check`; the endpoint failed on its
  *second* identical request. Fixed, with params-parity plus real-DB regression
  tests (see the entry above).
- **Defect — an empty authorized-issuer allowlist looked healthy.** `log_only` let
  requests through while recording `unknown_issuer`, which would have turned into a
  blanket `403` the moment enforcement flipped. Fixed in both launch paths.
- **Client trap — `Idempotency-Key` hashes the whole body**, and `issued_at` moves
  every time an envelope is built, so "resending the same request" yields
  `409 idempotency_conflict`. The consumer pins one `issued_at` per logical request
  and stores it with the key.
- **Client trap — reads share the write rate limit and a denied request still
  increments the counter**, so fixed-interval polling is self-harming. The consumer
  uses bounded full-jitter backoff, honours `Retry-After` on
  `rate_limit_exceeded`, and treats `concurrent_jobs_exceeded` separately (it clears
  at a terminal job state, not on a clock).

Two invariants are now binding on all three repos and are stated in
`info.description` of the OpenAPI contract so they travel with it: **IDs are durable
and download URLs are not** (presigned per read, expiring at `url_expires_at`), and
**reuse is the default** — repeating a request is a zero-cost cache hit returning the
same `asset_id`, so a portrait is never re-requested to "refresh" it.

Consumer-facing walkthrough: `docs/api/integration-quickstart.md` (verified against
a running stack, not written from the code).

## Remaining

- **None for the Phase 7 implementation track.** Phase 7C-3 (RLS / tenant
  isolation) is **Done**, Phase 7C-4 (provider fallback chains + outbound
  webhooks) is **Done**, and there is **no remaining Phase 7 implementation
  work**. Phase 7C-4 closes Phase 7C and the planned phase sequence. Nothing
  below is a new phase or new product scope: the sections after the next one are
  documentation/closure reconciliation, and "Before production (integration
  track)" immediately below is the cross-team readiness list — mostly contracts
  and configuration owned outside this repo, not implementation work queued here.

### Before production (integration track)

The functional integration is done; none of the items below block the dev-stack
handshake, and all four are cross-team, not local code debt.

1. **Real signing contract — required to leave `log_only`.** The envelope
   `signature` is accepted as **any non-empty string** today, because
   `StubSignatureVerifier` passes unconditionally; the consumer sends the sentinel
   `stub-unsigned-v1`. Canonicalization + crypto is a cross-system contract owned by
   core (`TODO(core-signing)` in `internal/governance/signature.go`) and this repo
   must **not** invent the wire format. Until it ships, `enforce` asserts an
   integrity guarantee that is not actually checked — which is why `enforce` + a stub
   verifier is **refused at startup in `live`** (`governance.EnforceWithStubError`)
   and only WARNs in dev/test. Signature binding to
   tenant/subject/operation/request-hash/expiry ships with that contract.
2. **Enforcement-flip checklist.** Flipping `GOVERNANCE_ENFORCEMENT=enforce` turns
   every currently-audited block into a `403 governance_blocked`, so before flipping:
   real signing must be in place (item 1 — otherwise `live` refuses to boot);
   `GOVERNANCE_AUTHORIZED_ISSUERS` must list every calling service **in every
   environment and every launch path** (this repo has two: `scripts/dev.sh` and the
   `docker-compose.yml` api service — an empty allowlist is silent under `log_only`);
   every caller must send a non-zero `issued_at` inside `GOVERNANCE_MAX_AGE`
   (default 24h, ±2min future skew); and `audit_events` should show **zero**
   `media.eligibility_blocked` rows for the traffic you are about to enforce on.
   That last one is the actual go/no-go signal — dry-run enforcement by reading the
   audit table, not by flipping and watching for errors.
3. **Tenant granularity — an open design question, not a schema gap.** `tenant`
   currently means the deployment. Adding tenants needs **no schema change** (ids are
   opaque `TEXT`; measured on a migrated DB at head 19: 20 tables under
   `ENABLE` + `FORCE ROW LEVEL SECURITY` with a deny-by-default `tenant_isolation`
   policy, 14 carrying `tenant_id` directly, the rest policed by parent-join
   `EXISTS`). A per-customer split is a **data re-key plus re-proving cross-tenant
   isolation under the `image_platform_api` role**, and because `world_id` already
   rides on every asset, identity and pack row, **nothing is regenerated**. The
   question to settle first: does a customer map to one tenant with many worlds
   (a straight re-key) or one tenant per world (which multiplies token management)?
   RLS keys on `tenant_id` alone either way.
4. **Webhooks — the future latency hint, now safe to adopt.** Not on the integration
   path today, deliberately. The three gaps that made the push contract unsafe are
   **closed**: pack jobs emit (they previously emitted nothing at all), the
   unrunnable-job failure paths emit `generation_job.failed` (they previously went
   terminal silently), and the event envelope is published as an OAS 3.1 `webhooks`
   section with generated Go types. A silent-loss bug where a cost-commit failure
   dropped the `completed` event was fixed at the same time. Remaining before anyone
   depends on it: no signed timestamp (so no replay protection), no SSRF/private-range
   guard on the endpoint URL, no dead-letter queue or enqueue-failure sweeper,
   at-least-once delivery only, one endpoint per tenant, and no `cancelled` event
   type. Adopt it as an optimization on top of polling — never as the readiness
   mechanism.

## Scope move — RLS and webhooks were deliberately pulled into Phase 7C

`DECISIONS.md` originally listed **row-level security (RLS)** and **outbound
webhooks** under "Deferred to later phases." During Phase 7C they were
**intentionally pulled forward** as production-control hardening: RLS landed in
7C-3 (defense-in-depth tenant isolation under FORCE RLS) and webhooks in 7C-4
(MVP-tight, one signed endpoint per tenant). This was a **deliberate scope move**
made because they are production controls, **not accidental drift** and **not**
an expansion of product scope. The stale "deferred" wording in `DECISIONS.md`
has been annotated to reflect this.

## Post-7C known residue / explicit non-MVP

Everything below is **known and intentional**. None of it is a Phase 7
implementation blocker; none of it is silently broken. These are the honest
edges of the MVP.

- **Product-safety retrieval filter.** Matrix safety (the §2 rule that an
  `invalid_match` candidate is never reused; the exact → compatible → preview
  gating) **is active and conservative** today. The *world-state-aware override*
  (`passesWorldStateSafetyFilter`) is **intentionally deferred** — it would
  reject a matrix-compatible candidate that contradicts known world state, but
  it depends on world-state hints the retrieval call does not yet carry. It is a
  deliberate, documented placeholder that returns `true`; matrix safety is **not**
  a silent no-op.
- **Cost reservation margin.** The configurable safety margin
  (`reserved_amount = estimated_amount × (1 + margin)`) is **not needed for
  MVP** — reservations equal the estimate. Wave 3 reconciles provider-reported
  per-call actuals at terminal finalization and folds late events into the
  exact committed/released reservation. A configurable margin remains future
  operational work. When no actual is reported, the committed reservation
  still falls back to its estimate.
- **Wave 4 evidence.** Amortization remains specification-only. The benchmark,
  quality/economic gates, provider capability evidence, manifest behavior, and
  vision-based pane assessment/targeted regeneration requirements are defined in
  the Wave 4 design spec; no production claim of sprite-sheet savings is made.
- **Admin audit-events endpoint.** The `audit_events` **table exists** and is
  written **internally** in-transaction by the served admin write endpoints
  (price-book / cost-budget changes, etc.). There is **no** public/manual admin
  audit-events endpoint — `POST /v1/admin/audit-events` and
  `GET /v1/admin/audit-events` are **non-MVP / planned**. Docs and runbooks must
  not imply that endpoint exists today. (This PR does **not** implement it.)
- **Worker RLS residual.** The worker runs on the **system / BYPASSRLS** pool.
  Worker tenant safety is therefore **app-level predicates** (explicit
  `tenant_id` scoping in queries), **not** RLS enforcement. RLS enforcement
  covers the tenant request path (the `image_platform_api` role); the worker is
  trusted by construction.
- **API-role grant hardening.** The `image_platform_api` role's grants on
  **global / config tables** (e.g. provider reference tables) are **broader than
  ideal**. Future hardening can tighten API-role grants to **read-only** where
  appropriate. Not an MVP blocker.
- **Same-price fallback limitation.** Provider fallback (7C-4) only fires when
  **at least two same-priced routes exist** for an operation (option A: no
  re-reservation, so every alternate must match the primary's unit price). The
  **default seed data may not provide such parity routes**, so fallback can be a
  no-op on a fresh seed. This is **intended under option A and not a bug**.
- **Webhook MVP limitations.** Outbound webhooks are deliberately minimal:
  **at-least-once** delivery (not exactly-once); **no** dead-letter queue; **no**
  replay UI; **no** signature-rotation endpoint; **no** multiple endpoints per
  tenant; **no** event-subscription management (a receiver gets all three event
  types or none). Receivers **should dedupe** events (by `job_id` + event type /
  `occurred_at`). This is exactly why **polling stays the authoritative readiness
  path** and push is only a latency hint — see the `webhooks` section and
  `info.description` in `docs/api/openapi.yaml`.
- **Webhook signature has no replay protection.** The HMAC-SHA256 signature
  covers the **raw body only** — no timestamp, nonce, method, or path — so a
  captured delivery stays replayable indefinitely. `occurred_at` is inside the
  signed body at second precision, so a receiver *can* enforce freshness, but the
  platform neither sends a timestamp header nor requires it. Documented in the
  published contract; adding a signed timestamp is future hardening.
- **No SSRF guard on the webhook endpoint URL.** `PUT /v1/admin/webhook-endpoint`
  validates only that the URL parses with an `http`/`https` scheme and a non-empty
  host: plain `http://`, private ranges, and `http://127.0.0.1` are all accepted.
  It is an `admin:jobs`-scoped, one-per-tenant config, so this is a trusted-caller
  surface today, but it should get an allow-list / private-range guard before
  untrusted tenants can self-configure endpoints.
- **Webhook delivery residue.** If a `webhook_deliveries` row is inserted but the
  asynq enqueue fails, there is **no sweeper** to re-drive it yet. Documented as
  **future hardening**, not a Phase 7 blocker.
- **Webhook-table RLS test residue.** `webhook_endpoints` / `webhook_deliveries`
  have the **same** ENABLE + FORCE RLS + deny-by-default policy/migration shape
  as the 7C-3 tables (migration `0010`). There is **not yet a dedicated DB
  integration test** proving `webhook_endpoints` / `webhook_deliveries` tenant
  isolation specifically; the policy shape is identical to the 7C-3 tables that
  are covered. (Optional future add: a tiny isolation test that touches no
  runtime behavior.)
- **Token-pepper rotation.** Rotating `API_TOKEN_PEPPER` is noted in ADR-005 and
  remains deferred — there is **no pepper-rotation runbook**. (The existing
  `docs/runbooks/token-rotation.md` covers *API-token* rotation/revocation
  accurately; it does **not** cover pepper rotation.)

## Notes

- Phase numbers here are the **only** authoritative sequencing.
- Each phase is a separate PR. Do not compress phases into one.
