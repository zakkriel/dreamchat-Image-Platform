# ADR-I004 — Measured cost findings: what the platform actually spends, and the three `ADR-I003` premises that are false

- **Image-platform ADR** (`ADR-I###`). Namespace assigned by `workspace:ADR-W002`;
  cross-repo citation form is `image:ADR-I004`.

**Type:** ADR (decision · rationale · alternatives · consequences). No implementation detail or build
phases — the code this ADR records landed as *Wave 3.5 — measured cost repairs*.
**Status:** **Accepted.**
**Date:** 2026-08-31.
**Repo target:** `docs/adr/` (image-platform repo).
**Relationship to `ADR-I003`:** this ADR **supersedes no whole ADR**. It **corrects three premises**
of `image:ADR-I003` (Proposed) and replaces two of its unfalsifiable deferral triggers with
falsifiable ones. `ADR-I003` stays in place, keeps its id, and remains the record of the structural
cost model (anchor amortization, derive-first). Its own provenance caveat already says its numbers are
provisional and unmeasured; this ADR is the measurement.

---

## What is now measured

Every line below was read in the shipped code, and each is the reason a repair landed or deliberately
did not. **The headline is negative and it reverses the intuitive answer: the platform had built
elaborate cost *accounting* and almost no cost *reduction*, and the most attractive-looking reduction
lever is a verified no-op.**

| # | Finding | Evidence | Outcome |
|---|---|---|---|
| V1 | **Spend was uncapped by default, fail open.** Zero applicable budget rows ⇒ the reservation is admitted. No migration inserts a `cost_budgets` row — only the DDL exists — so the whole price-book / reservation / `422 budget_exceeded` apparatus is inert until an operator creates one. | `internal/cost/cost.go` `reserveBudgets` (`len(toEnforce) == 0 → return true`); `migrations/0001_initial.sql:327` (DDL only) | **Repaired**: the admission now logs `cost_budget_absent_uncapped` with the tenant, and `cmd/seed-token` seeds a tenant-scope daily cap (`SEED_DAILY_BUDGET_USD`, default `25.00`). Semantics unchanged — zero budgets still admits. |
| V2 | **Failure paths multiplied spend against a one-image reservation.** `MaxAttempts = 3`, and each asynq attempt walked the primary plus every persisted same-price fallback with one billable `adapter.Generate` per route. bfl and fal are both `$0.0400`, so the single-image worst case was **6 billable calls, `$0.24`, against a `$0.04` reservation**. The reservation bounds the fallback chain to the same *price class*, never to a *count*. | `internal/jobs/enqueue.go` `MaxAttempts`; `internal/jobs/worker.go` `generateWithFallback`; `internal/jobs/service.go` (same-price-class fallback bound); `migrations/0006_bfl_provider_seed.sql:69`, `migrations/0011_fal_provider_seed.sql:74` (`0.0400` each) | **Repaired**: `MaxBillableCallsPerUnit = 3`, counted from the persisted `provider_attempts` rows so it spans asynq attempts, and terminal when spent. |
| V3 | **Wiring the ADR-009 four-tier retriever into `/v1/generations` is a verified no-op.** The ladder exists and is unwired, but `/v1/generations` reads and writes `variant_key = 'default'`, which classifies as family `unknown` with fallback disabled, and an unknown family scores `invalid()`. Every candidate is `invalid_match` and the ladder collapses to exact-match-or-generate — which is what the endpoint already does. | `internal/assets/retrieval.go` (the ladder); `internal/http/router.go:344` (generations gets only `h.Reuse = deps.AssetsRepo`); `internal/assets/variants.go:469-472`; `internal/assets/compatibility.go:80-81` | **Not built, and must not be.** Widening it requires inventing a variant vocabulary for single-artifact output — new design, not wiring. |
| V4 | **The reuse key missed on precision the database cannot store.** The key formatted `max_megapixels` with full shortest-round-trip float64 precision while the column is `NUMERIC(6, 2)`. A client sending a float32-widened `2.0999999046325684` and one sending `2.1` **persist identically as `2.10`**, render identically, and produced two different cache keys and a second full-price render. A key/store mismatch, not a semantic choice. | `internal/assets/generation_hash.go` (`max_megapixels` field); `migrations/0013_cost_routing.sql:9` | **Repaired**: quantized to `'f', 2`; hash version bumped `4 → 5`. |
| V5 | **`intent` split identical renders.** `intent` (`draft`\|`commit`) is in the reuse key, so a draft and a commit of an identical subject could never share an asset — while draft ranking is *already implemented* (ascending unit price) and **every seeded production route is priced identically**, so both intents resolve to the same paid model today. | `internal/assets/generation_hash.go` (`intent` field); `internal/providers/routing/routing.go` `ranksBefore` (`case "draft"`) | **Repaired one-way**: a draft may be served a ready commit-keyed asset (equal-or-better quality); a commit is **never** served a draft-keyed asset. |
| V6 | **Two inputs are load-bearing and must NOT be normalized.** (a) The hashed display-name field is not caller text: the handler computes the prompt from `identity.CanonicalVisualTraits["appearance"]`, else the identity's display name — a caller cannot vary its case per request, so case-sensitivity is not a miss source. (b) The anchor-id list is deliberately unsorted because it feeds the provider's *ordered* reference list. | `internal/http/handlers/generations_handler.go` (`identityPrompt`); `internal/assets/generation_hash.go` (`IdentityAnchorAssetIDs` doc comment) | **Left alone.** Both would have been "normalizations" that changed behavior for no saving. |
| V7 | **Preview-first is a cost increase, not a draft lever.** It exists only on `/v1/artifacts/{id}/generate` and `/v1/styles/{id}/preview`, never on `/v1/generations`; it makes two full paid renders on the *same* resolved route (the worker never re-resolves), reserves `phases = 2`, and deliberately bypasses reuse. | `internal/jobs/worker.go` `processPreviewFirst`; `internal/jobs/service.go` `worstCaseBillableUnits` (`phases = 2`); `internal/http/handlers/artifacts_handler.go` (reuse bypass) | **Recorded.** Never cite it as the draft flow `ADR-I003`'s lever #5 waits for. |
| V8 | **Area is not priced.** `supportedUnitType = "image"`; any other unit including `megapixel` becomes `no_price_entry` / `422`. The estimate is a flat `price_per_unit * units` with `units` hardcoded `1` for a generation. Fail-closed, so an accounting gap rather than a mispricing bug. | `internal/cost/cost.go` (`supportedUnitType`, unsupported-unit warn); `internal/db/queries/cost.sql` (`EstimateOperationCost`); `internal/http/handlers/generations_handler.go` (`Units: 1`) | **No work.** Recorded so nobody prices by area without changing the unit contract first. |
| V9 | **The mechanisms needed already existed.** `provider_attempts` has an index on `(generation_job_id, attempt_number)`; `POST /v1/admin/cost-budgets` and `GET /v1/admin/cost-events` are **served**; `cost_budgets` has `UNIQUE (tenant_id, scope_type, scope_id, period)`, making an idempotent seed trivial. A `CountProviderAttemptsForJob` query and a repository method were already shipped and unused. | `migrations/0001_initial.sql:340`, `:469`; `internal/http/router.go` (admin cost mounts); `internal/db/queries/provider_attempts.sql:29`; `internal/jobs/repository.go` `CountProviderAttempts` | Wave 3.5 needed **no migration, no OpenAPI change, and no new SQL** — the table count stays 18. |
| V10 | **In-flight coalescing does not exist.** No query looks up a pending job by `prompt_hash`, and `generation_jobs` does not carry one; the asset row is inserted by the worker *after* the provider call. So the known ready-slot race causes duplicate **rows**, and a unique index would not prevent duplicate **spend**. Preventing that needs request-time coalescing. | `internal/db/queries/generation_jobs.sql` (no `prompt_hash` column/lookup); `internal/jobs/worker.go` (asset inserted post-generation) | **Not built** — a real feature, not a repair. Recorded here and corrected in the waves plan's §Known-open. |

## Decision

1. **Bound billable provider calls to what the reservation priced.** `MaxBillableCallsPerUnit = 3`
   per billable unit, counted from persisted `provider_attempts` so the bound survives asynq retries,
   and terminal once spent (a retry cannot bill, so retrying only burns attempts). Per *unit*, not per
   job, because a reservation is `cells × phases` units: a six-role pack legitimately needs six calls,
   and a flat per-job cap would deliver half a pack the caller already paid for.

2. **A cache key may not carry precision its store discards.** `max_megapixels` is hashed at the
   column's two decimals. Both key corrections landed under **one** version bump (`gv=5`), because a
   bump invalidates the whole cache and that is free only pre-traffic.

3. **Quality substitution is one-way.** A draft request may be served a ready commit-keyed render;
   a commit request may never be served a draft-keyed one. The reverse is the silent quality downgrade
   `docs/architecture/cost-control.md` §7 rejects.

4. **The fail-open budget stays fail-open, and stops being invisible.** Zero applicable budgets
   continues to admit — a tenant nobody set a limit for is not blocked, which is a legitimate
   multi-tenant default. But the admission is now logged (`cost_budget_absent_uncapped`) and a dev
   database gets a seeded cap, so "uncapped" is a state an operator can see rather than infer.

## The three falsified premises of `ADR-I003`

Named plainly, because the point of this ADR is that the next agent does not re-derive them.

1. **"Design for cheapness now, build mechanisms later" held for schema but not for spend.** The
   accounting shipped — price book, reservations, holds, cost events, per-identity ledger — and the
   *reduction* did not, while the default configuration left spend **uncapped** and the failure path
   billed up to 6× a one-image reservation (V1, V2). Deferring mechanisms was defensible; deferring
   the *ceiling* was not, and nothing in `ADR-I003` distinguished the two.

2. **The anchor/derive-first structural claim does not make the existing four-tier ladder reusable
   for single-artifact generation output.** `variant_key='default'` classifies as family `unknown` and
   scores `invalid_match`, so "derive, don't regenerate" has **no retrieval path on
   `/v1/generations`** today (V3). The structural seams `ADR-I003` cites (anchor/derive columns) exist;
   the retrieval that would exploit them does not apply to this endpoint's output shape.

3. **Deferred lever #5 (draft/commit cheap-model routing) is described as awaiting a draft flow, but
   its ranking is already implemented and buys nothing** — every seeded production route is priced
   identically, so both intents resolve to the same paid model. And preview-first, the closest thing
   in the platform to a draft flow, **increases** spend: two full paid renders on the same route
   (V5, V7).

## Two unfalsifiable triggers, replaced

This is a governance defect, not a code one: a trigger that can never fire defers a decision forever
while looking like a plan.

- **Programmatic transforms (`ADR-I003` #6)** — the old trigger was *"real `transform_only` requests
  appear"*. `transform_only` is `501`-gated at the handler before anything else, and the column is
  written but never read. **A gate that blocks the evidence its own trigger requires is unfalsifiable
  by construction.** New trigger: a **counted `501` rejection rate on the existing telemetry**, or a
  **named consumer commitment from the world backend** — both are evidence the platform can actually
  produce.

- **Sprite-sheet batching (`ADR-I003` #4/lever 5)** — the old trigger treated the benchmark as the
  evidence. `cmd/sprite-sheet-benchmark` **refuses to score** the three things that decide the lever:
  identity consistency, pane separation, and cross-pane bleed are declared human judgements and left
  empty by the tool. Its 30-sample floor is a **warning only**, no gate verdict exists anywhere in the
  repo, and migration `0015`'s schema has zero runtime consumers. New trigger: **a human reviewer is
  committed to score those three fields.** ~30 samples costs ~`$1.20`; unreviewed samples are spend,
  not measurement.

## Lever 4 — a genuinely cheaper draft-tier route: recorded, not seeded

The mechanism needs **zero application code** — draft intent already ranks by ascending unit price. The
change would be seed-only DML in the existing convention: one `provider_model_prices` row
(`unit_type='image'`, the only supported unit, V8) plus one `provider_routes` row with
`required_capability='identity_capable'` (the handler's floor), `quality_tier='draft'`,
`allow_unpriced_provider=false`.

It is **not seeded here** because it depends on one fact nobody has measured: a genuinely cheaper model
that *honestly reports* identity/reference conditioning. `image:ADR-016` reconciles routes against
**adapter-reported** capabilities and fails closed, so seeding a route against an inflated capability
claim produces `422 route_capability_mismatch`, not savings. **Trigger:** a candidate model priced
below `$0.0400` whose adapter reports identity capability.

## Refused at any volume

- **Cross-identity semantic dedup.** Its saving is exactly proportional to how often it serves the
  wrong subject. Already rejected as `ADR-I003` Alternative 4; reaffirmed here.
- **Silent quality downgrade under budget pressure.** Already rejected at
  `docs/architecture/cost-control.md` §7. A denial is synchronous and visible; a downgrade is neither.

## Consequences

**Positive**
- The single-image worst case falls from 6 billable calls to 3, and a pack's from `3 × cells × routes`
  to `3 × cells`.
- Two classes of forfeited cache hit are recovered: budgets the store cannot distinguish, and a draft
  that follows a commit.
- "Uncapped tenant" is observable, and a dev database has a ceiling.

**Negative / cost**
- **The `gv=5` bump invalidates the entire existing reuse cache.** Free pre-traffic, not free later.
- **The draft→commit saving only materialises when a draft is requested *after* a commit of the same
  subject.** Whether that ordering occurs is unmeasured; the shipped cache-hit counter is what will
  prove it. Recorded as a caveat, not designed around.
- **`SEED_DAILY_BUDGET_USD = 25.00` is an assumption, not a measured figure** (~625 images at the
  seeded `$0.0400`). It is a dev guardrail and env-overridable precisely so it needs no decision now.
- **`MaxBillableCallsPerUnit = 3` equals `MaxAttempts`.** If legitimate transient provider failures
  routinely need more than three calls per unit to succeed, raise the constant — do not remove the
  cap. An unbounded fan-out against a priced reservation is the defect being fixed.

## Debate record

The findings above came out of an adversarial debate run for this decision: two adversaries — `hawk`
(cost-aggressive) and `warden` (correctness-first) — ran three rounds on `claude-fable-5`, and every
surviving claim was then verified against the code by the orchestrator. Three claims the debate
**killed**, recorded so they are not re-derived:

1. **Wiring the four-tier retrieval ladder into `/v1/generations`** — `hawk`'s top-ranked lever, and a
   verified no-op (V3).
2. **Normalizing `display_name` case in the reuse key** — not a miss source at all, because the hashed
   value is server-held, not caller text (V6a).
3. **Treating preview-first as the draft lever** — it doubles spend on the same route (V7).

---

## Source

Wave 3.5 measured-cost round (this program), verified against shipped code at commit-time. Corrects
three premises of `image:ADR-I003`; builds on `image:ADR-I002` (the chokepoint), `image:ADR-016`
(capability reconciliation, fail closed), and `image:ADR-009` (retrieval before generation). Related:
`docs/architecture/cost-control.md`, `docs/superpowers/plans/2026-08-07-cost-optimization-waves.md`.
