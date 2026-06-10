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

## Remaining

- **Phase 5B — Variant logic**: expressions, angles, fallback
  compatibility, variant keys, pack-completeness rules.
- **Phase 6A — Retrieval-before-generation**: asset search, exact match,
  compatible match, skip-generation-when-asset-exists, regeneration.
- **Phase 6B — Delivery readiness**: S3 reads, presigned URLs, asset
  retrieval UX, style preview.
- **Phase 7 — Real provider + production controls**: BFL adapter,
  provider routing, capability checks, admin retry/cancel, rate limits,
  period reset, webhooks, RLS.

## Notes

- Phase numbers here are the **only** authoritative sequencing.
- Each remaining phase is a separate PR. Do not compress 5/6 into one.
