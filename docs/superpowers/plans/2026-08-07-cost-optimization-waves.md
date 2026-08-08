# Cost Optimization — Wave Plan (post-Chunk-2)

> **Goal:** reduce image-generation cost without degrading quality/identity
> consistency and without compromising the non-censorship boundary.
> **Waves 1–3 are the implementation scope for this pass.** Wave 4 is
> specification-only and remains behind explicit benchmark, quality, economic,
> and operational gates. See `docs/superpowers/specs/2026-08-08-wave4-amortization-design.md`.

## Non-negotiable constraints (apply to every wave)

- Governance is authorization/integrity only; it never reads or rewrites
  prompt/content; `content_class` stays opaque (D-3/E-1).
- Provider content-policy rejections surface verbatim as
  `provider_content_rejected`; never sanitized, hidden, retried around, or
  fallback-walked (implemented, Wave 1).
- No silent quality downgrades under budget pressure: budget denial stays an
  explicit `422 budget_exceeded`. The `cost-control.md` §7 "auto-downgrade at
  80%" open item is REJECTED.
- Reuse substitutes only quality-equivalent-or-better assets; compatible
  substitution for identity-critical output requires an explicit caller
  policy (`fallback_policy`).

## Wave 1 — Repairs (DONE — PR #35)

| # | Change | Where |
|---|---|---|
| 1 | Single-image reference propagation (fail-closed) | `internal/jobs/worker.go` (`singleImageReferences`) |
| 2 | `/v1/generations` exact reuse: hash, lookup, cache-hit, stamping | `internal/assets/generation_hash.go`, `repository.go`, `visual_assets.sql`, `generations_handler.go`, `router.go`, `routing.ResolvedRoute.QualityTier` |
| 3 | `provider_content_rejected` classification; no fallback/retry past it | `internal/providers/provider.go`, `bfl/bfl.go`, `jobs/worker.go` |
| 4 | Idempotency expiry on read + expired-key takeover on write | `internal/db/queries/idempotency_keys.sql` |
| 5 | Pack delivery under RLS (port of stranded `e14153f`) | `internal/jobs/repository.go`, `handlers/assets_handler.go` |

Verified: `go build`, `go vet`, full unit suite, full `-tags=integration`
suite against Postgres 15 (migrations v17). OpenAPI mirrors untouched.

## Wave 2 — Governance completion + interface hygiene (DONE)

> Spec: `docs/superpowers/specs/2026-08-07-wave2-governance-interfaces-design.md`

1. ~~Startup guard~~ DONE: live + `enforce` + stub verifier refuses startup
   (`governance.EnforceWithStubError`). **Real signature crypto remains
   blocked** on core's signing contract (`TODO(core-signing)`); signature
   binding to tenant/subject/operation/request-hash/expiry ships with it.
2. ~~One governed chokepoint~~ DONE (additive, not retirement): artifact,
   pack, and style-preview run the shared `GovernanceGate` with an optional
   envelope (v0.13.0) — log_only audits and proceeds, enforce blocks
   missing/invalid envelopes 403. Retiring the legacy endpoints is now
   product timing, not a safety hole.
3. ~~`any_existing` admin/debug-only~~ DONE: requires `admin:read` on
   artifact/pack generation and asset search.
4. ~~Seed `identity_capable` fal route~~ DONE: migration 0018 (head 17→18,
   CI assertions updated).
5. ~~Idempotency-Key header canonicalization~~ DONE on `/v1/generations`
   (header canonical, body still accepted, neither → 422).
6. ~~Docs reconciliation~~ DONE: cost-control §4.1/§5 status codes, README
   status, DECISIONS env vars, ADR-014 implementation note,
   idempotency.md canonical header.

## Wave 3 — Measurement + cost accounting truth (DONE — PR pending)

> Design + verification: `docs/superpowers/specs/2026-08-08-wave3-cost-truth-design.md`.

1. Telemetry: cache-hit rate, $/usable image, policy-reject rate,
   estimated-vs-actual variance, fallback frequency (structured events or
   counters; referee for every later lever).
2. Billable-operation accounting: the reservation covers the calls a run
   *plans* to make — one per missing pack cell, doubled for a true preview's
   preview + final. Retries and fallback routes are failure paths: they are
   billed as reservation-scoped cost events and reconciled against the hold,
   never pre-charged onto it (pre-charging the retry cap denies requests that
   are inside their budget).
3. Provider-reported actual cost capture + reconciliation;
   `identity_cost_ledger` updates from committed cost events; provider events are
   attributed to the active cost reservation so retries cannot double-charge.
4. Enforce `max_megapixels` at the worker or reject it — stop silently
   clamping (bump `GenerationRenderHash` version when it becomes behavioral).
5. Pack fallback execution parity: `ProcessPack` walks persisted same-price
   fallbacks like the single-image path (they are currently persisted but
   unused), with the same content-policy stop rule.

## Wave 4 — Amortization (SPEC ONLY — NOT IMPLEMENTED)

1. **Sprite-sheet benchmark FIRST** (no platform code): fal Kontext 2×2 / 2×5
   sheets from an anchor; measure per-pane identity consistency, pane
   separation, usable-pane rate, true $/usable image vs N singles. Gate: only
   lift the `grid.enabled` 501 if the benchmark clears the quality bar.
2. Sprite-sheet pipeline on the Chunk-1 schema (`sprite_sheet_contract` /
   `sprite_sheet_slice`): one governed generation, deterministic slicing,
   malformed-sheet fallback to separately governed expression calls, read-path
   manifests for missing cells, and structural per-cell validation. This is a
   design target only; no runtime sprite-sheet pipeline is shipped in Wave 3.
3. Anchor-derive as the default recurring-identity path: premium tier for
   anchors, standard reference-conditioned tier for derivations; never
   regenerate an anchor to produce a variant.
4. Explicit lazy finalization (product-opt-in only) on the preview-first
   machinery: render the final only when requested.

## Deferred with triggers (do not build early)

- **Hosted LoRA:** one character projected past ~hundreds of lifetime
  generations. **Self-hosted GPU:** sustained ~1–2k images/day. **Cross-
  identity semantic dedup:** never by default (correctness hazard); within
  identity+variant-family only. **Batch API pricing:** does not exist for
  image providers today.

## Known-open items NOT addressed by any wave yet

- Ready-slot uniqueness race for concurrent non-forced inserts (no unique
  index; forced path locks, ordinary inserts don't).
- Replay-after-validation ordering (identity/style deletion can change a
  retry's response).
- Anchor updates don't bump identity versions; packs don't snapshot
  `visual_identity_version`.
- Wave 4 amortization is specification-only. Real-provider billing/quality
  evidence, provider capability gating, vision-based identity/separation
  assessment, targeted regeneration, anchor-derive defaults, and lazy finalization
  remain deferred behind the release gates in the Wave 4 design spec.
- Worker RLS posture (BYPASSRLS by construction) — documented, accepted.
