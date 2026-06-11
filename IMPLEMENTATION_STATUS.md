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

## Remaining

- **Phase 6B — Delivery readiness**: S3 reads, presigned URLs, asset
  retrieval UX, style preview.
- **Phase 7 — Real provider + production controls**: BFL adapter,
  provider routing, capability checks, admin retry/cancel, rate limits,
  period reset, webhooks, RLS.

## Notes

- Phase numbers here are the **only** authoritative sequencing.
- Each remaining phase is a separate PR. Do not compress 5/6 into one.
