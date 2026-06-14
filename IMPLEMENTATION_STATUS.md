# Implementation Status

Canonical phase list for the implementation track. This is the source of
truth for "what's done / what's next" — the roadmaps in `prds/06` and
`prds/07` use different numbering and should not be used for sequencing.

Rule of thumb: **~3 product buckets left, but ~5 implementation phases
before this is production-ready.**

## Done

- **Phase 0** — skeleton: health, config, docker, migrations.
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

## Remaining

- **Phase 7C-3 — RLS / tenant isolation hardening**: row-level security for
  tenant isolation (`ENABLE/FORCE ROW LEVEL SECURITY`, policies, `app.current_tenant`
  GUC, connection/session tenant plumbing). Deferred from 7C-2 on purpose — it
  needs deliberate DB connection/session plumbing and must not be bundled with
  request throttling.
- **Phase 7C-4 — Provider fallback chains + webhooks**: multi-provider fallback
  on failure and outbound webhooks for job lifecycle events.

## Notes

- Phase numbers here are the **only** authoritative sequencing.
- Each remaining phase is a separate PR. Do not compress 5/6 into one.
