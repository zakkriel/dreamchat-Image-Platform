# Wave 4 — Amortization Design Specification

Status: proposed specification only. Wave 4 is not implemented by the Wave 3 change set.

This document defines the release-gated work that may follow Wave 3. It does not authorize enabling grid generation, sprite-sheet persistence, anchor-derive defaults, or lazy finalization in production.

## 1. Objective and boundaries

Wave 4 may reduce provider cost by amortizing related outputs while preserving:

- identity and scene quality;
- provider content-policy decisions and the no-fallback-around-rejection rule;
- governed authorization and tenant isolation;
- truthful per-operation cost accounting;
- deterministic retrieval and retry behavior.

The existing request/schema seams (`grid`, `anchor_asset_id`, `derive_from`, and `lazy`) remain deferred contracts. Until the gates below pass, grid requests remain unsupported, anchor/derive fields do not change lineage behavior, and lazy requests use the existing non-lazy behavior described by the current request-contract specification.

## 2. Release gates

No production implementation starts before these gates are recorded. Run the benchmark with `go run ./cmd/sprite-sheet-benchmark`; its JSON records and printed aggregates are the evidence. Each gate is pass/fail against a number, not a judgement call.

1. **Sample size.** At least 30 sheets per candidate (provider, model, grid shape), drawn from at least 3 distinct visual identities. Record for every sample: provider request ID, model, grid shape, anchor input, returned dimensions, per-cell validity, latency, and provider-reported cost. Fewer samples, or a single identity, is not evidence.
2. **Quality floor.** Measured against the same prompts rendered as single images:
   - usable-pane rate **>= 90%** (a pane is usable only if it decodes, is non-empty, and matches the declared cell geometry);
   - reviewer-scored identity consistency **no worse than the single-image baseline on >= 90% of paired comparisons**;
   - cross-pane bleed in **0%** of accepted sheets — any bleed makes the whole sheet malformed, not a partial success.
   Identity consistency and pane separation are human judgements. The tool records artifacts and leaves those fields blank; it must never synthesize a score.
3. **Economic floor.** True cost per usable image **<= 70% of the single-image baseline**, computed including the parent-sheet call, validation failures, targeted regeneration, and every fallback call. A smaller saving does not justify the pipeline's complexity. Estimated price alone is never evidence — only provider-reported actuals count.
4. **Operational floor.** Against PostgreSQL, with passing tests: tenant-scoped retrieval, idempotent retries, cancellation safety, and reservation-scoped cost events.
5. **Provider capability floor.** The adapter itself advertises the governed grid contract through `Capabilities()`. A `provider_routes` row alone is insufficient — route configuration cannot grant a capability the adapter does not implement.

The benchmark output and gate decision are durable review artifacts. A failed gate leaves the API behavior unchanged.

## 3. Sprite-sheet contract

A future grid request contains a versioned contract with `row_count`, `column_count`, ordered unique `cell_keys`, inner margins, expected output dimensions, the identity/reference inputs used for the parent call, the request render hash, and the reservation ID. The contract is immutable after authorization.

**Safety boundaries (platform-verified, never taken on trust):** the decoded byte dimensions of the returned sheet, per-cell decodability and geometry, the cell count matching `row_count × column_count`, tenant ownership of every referenced asset, and the reservation the call is billed against.

**Provenance only (provider-reported, recorded but never load-bearing):** provider-declared width/height, provider request/job IDs, seeds, and provider-reported cost. A provider claiming a correct grid does not make it one — the worker decodes the bytes it is about to persist and validates against the contract.

Each declared cell gets a durable slice/manifest row. Valid cells point to persisted visual assets. Invalid or unavailable cells remain explicitly missing; a read must never imply that a missing cell was generated successfully.

## 4. Fallback and cost behavior

The reservation is made before provider work and covers the conservative worst-case plan: one parent-sheet operation, one single-image operation per cell for the persisted same-price fallback chain, and any explicitly supported targeted-regeneration operations. This is the one place a worst-case multiplier is correct, because every one of those calls is part of the *planned* recovery path for a single request — unlike retries, which Wave 3 deliberately excludes from the hold.

A valid sheet does not automatically justify charging the worst-case estimate as actual spend. Provider-reported actuals are reconciled to the reservation exactly as in Wave 3; when no actual exists, the reservation estimate is the documented fallback.

If the parent response is malformed, grid-ignoring, or fails structural validation, the worker may execute separately governed single-image calls for missing cells. Each call gets its own provider-attempt and cost-event record tied to the active reservation. A content-policy rejection stops fallback for that cell and is surfaced verbatim as `provider_content_rejected`; it must not be retried on another route, and the platform never inspects or rewrites the rejected prompt.

The parent and each child are independently idempotent. A retry may generate only missing cells and must not create a second ready asset for an already delivered cell.

## 5. Anchor-derive lineage

Anchor-derive is opt-in until benchmark and product-policy approval:

- canonical identity anchors are created through an explicit premium/anchor operation;
- derived variants use the approved reference-conditioned route and retain the source anchor/parent lineage;
- a variant request never regenerates or overwrites an anchor;
- lineage is tenant-scoped and included in the retrieval/reuse predicate;
- changing the source anchor set invalidates affected derived outputs through a versioned identity contract.

The implementation must define whether `anchor_asset_id` selects one source or an ordered conditioning set before enabling the default path. Ambiguous or foreign sources fail closed.

## 6. Lazy finalization

Lazy finalization is product opt-in and must be implemented as an explicit state machine, not inferred from a payload flag:

1. reserve the full governed plan and persist a committed preview;
2. expose `preview_ready` without committing final-generation cost;
3. authorize and enqueue a single finalization command;
4. atomically claim the final phase with an idempotency guard;
5. generate, persist, and validate the final output;
6. commit/reconcile the reservation only after final success, or release the unused remainder after terminal failure.

Duplicate finalize commands must be no-ops. A stale preview task must not start a final provider call, and a stale worker must not finalize a replacement reservation.

## 7. Required implementation evidence

Before Wave 4 can be marked implemented, the change set must include:

- benchmark artifacts and the explicit gate decision;
- OpenAPI and generated-code updates for the enabled contract;
- migration/query/repository tests for parent, slice, manifest, and lineage rows;
- worker tests for malformed sheets, missing cells, targeted regeneration, policy rejection, cancellation, and retries;
- cost tests proving parent/child actuals are summed once per reservation;
- retrieval tests proving incomplete manifests do not masquerade as complete packs;
- an updated runbook describing rollback to single-image generation.

## 8. Explicitly out of scope

Hosted LoRA and self-hosted GPU (deferred behind their own volume triggers), cross-identity semantic dedup (a correctness hazard, never on by default), batch API pricing (does not exist for image providers today), any automated identity/quality scoring, and any change to the non-censorship boundary.

Until all evidence exists, Wave 4 remains this specification and the current API's deferred behavior; no production path may depend on it.
