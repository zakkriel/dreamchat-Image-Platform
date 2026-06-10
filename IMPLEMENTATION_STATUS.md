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

## Remaining

- **Phase 6A2 — single-artifact retrieval-before-generation**: call the 6A1
  retrieval layer before creating an artifact generation job;
  skip-generation-when-an-asset-exists; record the cache result on the job.
- **Phase 6A3 — pack reuse-first + completeness storage**: retrieval-first
  pack fan-out (reused vs. missing items), misses-only pricing, all-hits
  completion, and pack-completeness storage (delivered-vs-missing required
  roles) — likely a small schema phase.
- **Phase 6A (remainder) — Retrieval-before-generation**: regeneration
  endpoint / `force_regenerate`.
- **Phase 6B — Delivery readiness**: S3 reads, presigned URLs, asset
  retrieval UX, style preview.
- **Phase 7 — Real provider + production controls**: BFL adapter,
  provider routing, capability checks, admin retry/cancel, rate limits,
  period reset, webhooks, RLS.

## Notes

- Phase numbers here are the **only** authoritative sequencing.
- Each remaining phase is a separate PR. Do not compress 5/6 into one.
