# ADR-P003 — Cost-Optimization Strategy: Anchor Amortization + Deferred Levers

> **Number:** ADR-P003 — next free `ADR-P###` in the image-platform repo at proposal time (highest present was `ADR-P002`, D-5).

**Type:** ADR (decision · rationale · alternatives · consequences). No implementation detail, acceptance criteria, or build phases — those live in chunk specs/plans.
**Status:** **Proposed.**
**Date:** 2026-06-25.
**Repo target:** `docs/adr/` (image-platform repo).
**Provenance caveat:** the cost figures in this ADR originate from a cost-optimization research synthesis, not from production measurement or a prior ratified doc. They are **provisional** (image-model pricing and break-evens move fast). The *decided* content here is the structural model and the deferral triggers — not the specific numbers.

---

## Context

The platform's workload is **recurring-subject generation**: the same characters, locations, and items are rendered repeatedly with a hard identity-consistency requirement. The naive model — one prompt = one paid provider call — is most wasteful exactly here, because it re-pays full generation cost for every depiction of an already-established subject and lets identity drift across independently generated images.

Several cost levers exist (anchor reuse, grid/sprite-sheet batching, draft/commit model routing, programmatic transforms, lazy generation, per-identity cost tracking, and the heavier options: per-character LoRA, self-hosted GPU, semantic dedup). They differ enormously in payoff-per-effort and in when they pay off. The platform is pre-traffic (solo build), so building optimizations speculatively risks engineering a system with no users against prices that will shift before launch.

Two seams already exist in shipped code: the request contract carries `subject.anchor_asset_id` / `derive_from` (Chunk 1 schema, Chunk 2 validation), and per-identity lifetime cost has a home (`identity_cost_ledger`, Chunk 1). This ADR ratifies the principle those seams imply and sets the boundary for what is built now vs. later.

## Decision

1. **Anchor amortization is the structural cost model.** Each visual identity has one canonical, high-quality **anchor**, generated once. Downstream assets (expression sheets, location variants, derivations) are produced **from that anchor** — by reference conditioning, batching, or transformation — rather than independently regenerated. "Derive, don't regenerate" is the default path; a fresh full generation is the exception.

2. **Lifetime cost is tracked per identity** via `identity_cost_ledger`, so the economics of any single recurring subject are observable (is it cheap to keep reusing, or expensive?).

3. **Design for cheapness now; build the heavy machinery later, on triggers.** The structural decisions that are expensive to retrofit are made now (anchor = source of truth; derive-first; per-identity cost tracking; per-request model choice — the last already shipped in Chunk 2). The optimization *mechanisms* are cheap to add later and are **deferred behind measured triggers** (below), not built speculatively.

4. **The grid/sprite-sheet batching lever is promoted to active work** (highest payoff-per-effort, ~5–10× on expression/variant packs, *provisional*) and is specified in its own PRD (NPC Expression Sprite-Sheet Pipeline). It is named here as the first amortization mechanism but governed by that PRD, not this ADR.

## Rationale

- **The workload rewards amortization specifically.** For a subject rendered hundreds of times, anchor-derive collapses lifetime cost (illustrative/provisional: an all-reference-conditioned character at ~$0.04/image over 500 renders ≈ $20; anchor + cheap derivation ≈ a fraction of that). It also *improves* identity consistency, because everything traces to one coherent source instead of N independent generations.
- **Premature optimization is the larger risk pre-traffic.** Per-character LoRA only pays off past roughly hundreds of generations per character; self-hosted GPU only past sustained thousands of images/day (*provisional*). Building either now spends engineering and ops budget on savings that don't yet exist, against prices that change.
- **The cheap-to-set-now / expensive-to-retrofit asymmetry** is the real decision. Anchor-as-source-of-truth and derive-first must be structural; bolting them on after assets are modeled as independent would be a painful migration. The optimizations themselves are additive later.

## Alternatives considered

1. **No anchor concept — per-call reference conditioning for everything.** Simplest. Rejected as the default: lifetime cost scales linearly with render count and identity drifts across independently conditioned calls, violating the consistency requirement at scale.
2. **Build all cost levers now (eager).** Maximum theoretical savings captured early. Rejected: optimizes a system with no users against volatile pricing; high effort, low present payoff; several levers (LoRA, self-hosting) are net-negative below volume thresholds not yet reached.
3. **Per-character LoRA / self-hosted inference as the primary model now.** Best per-image cost at high volume. Deferred (not rejected): correct later, wrong pre-traffic — break-evens unmet and ops burden unjustified. Recorded as triggered downstream decisions.
4. **Broad cross-identity semantic/perceptual dedup.** Tempting cost saver. Rejected as a default: serving a "close enough" wrong subject is a correctness failure against the hard identity requirement. Any dedup is confined to within a single identity's variant family.

## Consequences

**Positive**
- Lifetime cost per identity is bounded and observable; identity consistency improves.
- The structural seams already exist in shipped schema/contract, so this ratifies direction rather than requiring rework.
- Future optimizations slot in additively when their triggers fire, without re-architecting.

**Negative / cost**
- Requires discipline: every variant-producing path must check derive-first before paying for a fresh generation.
- The anchor becomes a **critical asset** — its quality and regeneration policy now matter more than any single downstream image.
- The cost figures justifying the triggers are provisional and must be re-validated against live pricing before any triggered lever is built.

**Scope boundary — what this ADR does NOT decide.** The mechanisms below are explicitly *not* specified here. Each becomes its own `ADR-P###` (or PRD where it adds user-facing capability) **when its trigger fires**, with implementation specifics that do not exist yet and must not be invented now.

## Downstream decisions (deferred — each its own future ADR, with unlock trigger)

- **Draft/commit cheap-model routing (#5).** The commit/premium half shipped (Chunk 2 intent routing). The deferred half is routing *drafts/exploration* to a cheap distilled model. **Trigger:** a user-facing draft/preview flow exists, or exploration volume is large enough that distilled-model savings are material.
- **Programmatic transforms (#6).** Producing variants (crop, recolor, relight, composite, low-strength img2img) by deterministic image ops instead of paid regeneration; `transform_only` performs no provider call. **Trigger:** variant demand on already-generated identities is high enough that regeneration is a dominant cost line, or real `transform_only` requests appear.
- **Lazy generation (#7).** Generate on first real encounter, never speculatively. **Trigger:** worlds contain enough never-viewed subjects that speculative pre-generation cost is material.
- **Cost-ledger reconciliation (#8).** Reconcile actual vs. estimated provider cost. **Trigger:** real spend diverges from estimates enough to affect budgets. **Contingency:** depends on whether BFL/fal return per-call actual cost; if they do not, this degrades to estimated-only and that limitation is recorded rather than faked.
- **Traps — explicitly out until volume justifies.** Per-character **LoRA** (unlock: a single character projected past ~hundreds of lifetime generations), **self-hosted GPU inference** (unlock: sustained ~thousands of images/day / GPU utilization past the serverless break-even), **broad cross-identity dedup** (no unlock — confined to within-identity variant families, correctness hazard otherwise). All thresholds provisional.

---

## Source

Cost-optimization research synthesis (this program). Builds on shipped seams: Chunk 1 (`identity_cost_ledger`, anchor/derive columns) and Chunk 2 (per-request intent routing). Related: NPC Expression Sprite-Sheet Pipeline PRD; Rules Register (D-3, E-1, D-5, D-6, D-9).
