# Cost Optimization Wave 1 — Repairs: references, reuse, policy honesty, expiry

> **Status:** implemented (this commit).
> **Date:** 2026-08-07
> Program context: ADR-P002 (governance + cost routing), ADR-P003 (anchor
> amortization, proposed on `main`), PRD 08 (sprite sheets, proposed on `main`).
> This wave implements the correctness repairs that every later cost lever
> depends on. It was converged by a three-way review panel (two independent
> model panels + moderator) that verified each defect against the code.

## 1. Problem

The combined contract `POST /v1/generations` (Chunk 2) shipped as a validated
scaffold with five defects that made it unusable for real-provider generation
and invisible to the cost/reuse economy:

1. **No reference propagation.** The endpoint's `identity_capable` floor is
   satisfied by fal's `pack_capable` route (hierarchy, PRD 03 §8.3), but the
   single-image worker path built `ProviderGenerateRequest` without
   `ReferenceURLs` — fal fails closed (`ErrReferenceRequired`) on every job.
   The endpoint could not succeed with any real provider.
2. **No reuse participation.** The handler stamped no `prompt_hash` /
   `quality_tier`, so its outputs never entered the exact-reuse cache and it
   never consulted reuse before reserving cost — double spend on the canonical
   endpoint.
3. **Moderation opacity.** BFL `Request Moderated` / `Content Moderated`
   collapsed into generic `provider_failure`; the same-price fallback walk
   could try the identical content on another route — silently circumventing a
   provider's content-policy decision (a non-censorship violation in both
   directions: hidden suppression AND hidden circumvention), and asynq retried
   a deterministic rejection at additional cost.
4. **Idempotency keys never expired in behavior.** `expires_at` was stored but
   `GetIdempotencyKey` ignored it and `DeleteExpiredIdempotencyKeys` had no
   caller — a denied preflight (e.g. `budget_exceeded`) poisoned its key
   forever, replaying the same 422 even after the budget window reset.
5. **Pack delivery broke under RLS.** `GET /v1/jobs/{id}/assets` read
   `asset_pack_items` through pool-bound queries with no tenant GUC; under the
   RLS-enforced `image_platform_api` role the deny-by-default policy returned
   zero rows. The fix existed on the deleted branch
   `claude/zealous-ritchie-84qd37` (`e14153f`) and was never merged.

## 2. Decisions

### D1 — References are gathered per job, chain-aware, fail-closed

`Worker.singleImageReferences` (internal/jobs/worker.go) mirrors the pack
path: when ANY route in the resolved chain (primary + persisted fallbacks) is
backed by an adapter with `RequiresReferenceImage`, the identity's anchors are
loaded tenant-scoped, validated, presigned fresh for the attempt, and threaded
into every provider request (single-phase, and both phases of preview-first).
Missing/invalid anchors fail the job terminally
(`missing_reference_assets` / `invalid_reference_asset`) before any provider
call — a recurring character is never silently rendered as a different person.

The capability hierarchy is intentionally UNCHANGED: `pack_capable` satisfying
an `identity_capable` floor is semantically correct; restricting it would have
left `/v1/generations` with no real route at all. (Panel-converged decision.)

### D2 — Combined-contract exact reuse via a dedicated render hash

`assets.GenerationRenderHash` (internal/assets/generation_hash.go) keys the
slot on `tenant + identity + display_name + anchor + derive_from + intent +
transform` — the fields that determine the rendered pixels today. Provider,
`max_megapixels` (clamped, unenforced) and `lazy` (stored-not-acted) are
excluded; enforcing either later REQUIRES a hash-version bump (`gv`).

Flow (generations handler): replay → governance gate → **reuse lookup**
(`FindReadyGenerationByPromptHash`: tenant + hash + ready + artifact/default,
highest version wins) → hit ⇒ `CreateCompletedCacheHitJob` (`job_type=
"generation"`, zero cost, no resolve/reserve/enqueue) → miss ⇒ resolve, stamp
`payload["prompt_hash"]` + `payload["quality_tier"]` (the resolved route's
tier), reserve, enqueue. The worker's existing `buildArtifactInsertParams`
persists both onto the produced asset, closing the loop: every
`/v1/generations` output is now itself reusable.

Reuse runs AFTER the governance gate on purpose: eligibility is verified and
audited even for reused output.

### D3 — Provider content-policy rejections are first-class and final

`providers.ErrContentPolicyRejected` classifies BFL moderation statuses
(adapter-level wrap, `internal/providers/bfl/bfl.go`). Worker behavior:

- `generateWithFallback` STOPS the walk on it — never tries the same content
  on another route.
- The job fails terminally with `provider_content_rejected`
  (docs/api/errors.md vocabulary, previously documented-but-unimplemented),
  `retryable=false`, reservation released, webhook emitted; the worker returns
  nil so asynq never re-bills a deterministic rejection.

Non-censorship boundary restated: the platform adds no content judgment of its
own — it surfaces the provider's decision verbatim and refuses to either hide
it or engineer around it. Fallback remains available for infrastructure
failures only.

### D4 — Idempotency expiry is enforced at read, reclaimed at write

`GetIdempotencyKey` now returns only live rows (`expires_at > now()`).
`InsertIdempotencyKey` becomes a conditional upsert: a conflicting EXPIRED row
is taken over in place (`ON CONFLICT ... DO UPDATE ... WHERE expires_at <=
now()`); a live row still yields no-rows → the existing replay path. This
keeps first-writer-wins for live keys, bounds denied-preflight poisoning at
the TTL (24 h), and needs no background sweeper for correctness
(`DeleteExpiredIdempotencyKeys` remains available as optional hygiene).
Recording denials for replay within the TTL is intentional and unchanged.

### D5 — Pack delivery reads through the tenant executor

Ported from stranded commit `e14153f`: `ListAssetPackItemsForTenant` runs
inside `WithTenant` (GUC set) and the job-assets handler uses it with the
job's tenant. The worker keeps the unscoped read on its BYPASSRLS pool.

### D6 — Explicitly NOT in this wave

- Real governance signature crypto: the canonicalization/signing format is a
  cross-system contract with core (`TODO(core-signing)`) — do not invent.
- Governance gating of legacy endpoints (ADR-P002 Follow-up 1): a breaking
  contract change for existing callers; needs its own chunk.
- An identity-capable fal route seed (migration): resolution already works via
  the hierarchy; a dedicated route is seed hygiene for a follow-up migration.
- `any_existing` scope-gating, sprite sheets (PRD 08), lazy finalization,
  cost telemetry counters, ledger writes — see the Wave 2+ plan.

## 3. Invariants (tested)

- fal-routed single-image job with anchors → provider receives presigned
  high-res reference URLs; without anchors → terminal
  `missing_reference_assets`, zero provider calls
  (`worker_singleimage_reference_test.go`, both phases in preview-first).
- Content-policy rejection: fallback provider never called, job
  `provider_content_rejected` + non-retryable, reservation released once,
  `Process` returns nil on a non-final attempt
  (`worker_contentpolicy_test.go`); BFL classification + inverse
  (`bfl_moderation_test.go`).
- `/v1/generations` stamps `prompt_hash` (= `GenerationRenderHash`) and the
  resolved route's `quality_tier`; exact-hash hit skips resolve/reserve and
  lands a completed `generation` cache-hit job with `0.0000` estimate; miss
  generates (`generations_reuse_test.go`); hash determinism/per-field/boundary
  (`generation_hash_test.go`).
- Expired idempotency key: `LookupReplay` not-found; same key + different body
  creates a FRESH job (takeover); the taken-over record replays normally;
  live-key conflict semantics unchanged
  (`idempotency_expiry_integration_test.go`, real Postgres).
- Full pre-existing unit + integration suites pass unchanged (Postgres 15,
  migrations at version 17).

## 4. Rules

D-3/E-1 (governance verifies, never interprets — reuse placed after the gate;
content_class untouched), D-4 (no schema change; JSONB contracts untouched),
D-8 (async only; cache-hit completion mirrors the established 6A2 envelope),
D-9 (this doc cites proving code and tests).
