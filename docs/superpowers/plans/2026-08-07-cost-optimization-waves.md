# Cost Optimization — Wave Plan (post-Chunk-2)

> **Goal:** reduce image-generation cost without degrading quality/identity
> consistency and without compromising the non-censorship boundary.
> **Wave 1 is implemented** (see
> `docs/superpowers/specs/2026-08-07-cost-optimization-wave1-design.md`).
> Waves 2+ are planned work, each its own chunk/PR.

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

## Wave 1 — Repairs (DONE, this commit)

| # | Change | Where |
|---|---|---|
| 1 | Single-image reference propagation (fail-closed) | `internal/jobs/worker.go` (`singleImageReferences`) |
| 2 | `/v1/generations` exact reuse: hash, lookup, cache-hit, stamping | `internal/assets/generation_hash.go`, `repository.go`, `visual_assets.sql`, `generations_handler.go`, `router.go`, `routing.ResolvedRoute.QualityTier` |
| 3 | `provider_content_rejected` classification; no fallback/retry past it | `internal/providers/provider.go`, `bfl/bfl.go`, `jobs/worker.go` |
| 4 | Idempotency expiry on read + expired-key takeover on write | `internal/db/queries/idempotency_keys.sql` |
| 5 | Pack delivery under RLS (port of stranded `e14153f`) | `internal/jobs/repository.go`, `handlers/assets_handler.go` |

Verified: `go build`, `go vet`, full unit suite, full `-tags=integration`
suite against Postgres 15 (migrations v17). OpenAPI mirrors untouched.

## Wave 2 — Governance completion + interface hygiene

1. **Real signature verification** (blocked on core's signing contract):
   replace `StubSignatureVerifier`; production MUST fail startup when
   `GOVERNANCE_ENFORCEMENT=enforce` with a stub verifier; bind the signature
   to tenant + subject + operation + request hash + expiry.
2. **One governed chokepoint:** route artifact/pack/style-preview through the
   governance gate or deprecate them in favor of `/v1/generations`
   (ADR-P002 Follow-up 1). Contract change — coordinate with backend callers.
3. **`any_existing` becomes admin/debug-only** (scope-gated): it can return
   matrix-invalid assets and must not be reachable by ordinary `images:read`
   callers, nor persist invalid assets as pack items.
4. **Seed an `identity_capable` fal route** (DML migration, mirrors 0011) now
   that references are wired; keeps route semantics legible.
5. **Idempotency-Key header canonicalization** across endpoints (docs vs
   `/v1/generations` body-key divergence).
6. Docs reconciliation: `cost-control.md` §5 status codes (503/429 → 422),
   README phase status, `DECISIONS.md` fal/env drift, ADR-014 vs actual error
   body shape.

## Wave 3 — Measurement + cost accounting truth

1. Telemetry: cache-hit rate, $/usable image, policy-reject rate,
   estimated-vs-actual variance, fallback frequency (structured events or
   counters; referee for every later lever).
2. Billable-operation accounting: preview call, final call, per-route failed
   attempt, pack cell — reserve for the worst-case billable plan (matters
   before any real `true_preview` provider exists; today only mock).
3. Provider-reported actual cost capture + reconciliation;
   `identity_cost_ledger` updates from committed cost events (ADR-P003 seam).
4. Enforce `max_megapixels` at the worker or reject it — stop silently
   clamping (bump `GenerationRenderHash` version when it becomes behavioral).
5. Pack fallback execution parity: `ProcessPack` walks persisted same-price
   fallbacks like the single-image path (they are currently persisted but
   unused), with the same content-policy stop rule.

## Wave 4 — Amortization (ADR-P003 / PRD 08)

1. **Sprite-sheet benchmark FIRST** (no platform code): fal Kontext 2×2 / 2×5
   sheets from an anchor; measure per-pane identity consistency, pane
   separation, usable-pane rate, true $/usable image vs N singles. Gate: only
   lift the `grid.enabled` 501 if the benchmark clears the quality bar.
2. Sprite-sheet pipeline on the Chunk-1 schema (`sprite_sheet_contract` /
   `sprite_sheet_slice`): one governed generation, deterministic slicing,
   per-cell validation, targeted regeneration of failed cells only.
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
- Worker RLS posture (BYPASSRLS by construction) — documented, accepted.
