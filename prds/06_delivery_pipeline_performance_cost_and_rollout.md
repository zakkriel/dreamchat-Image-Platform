# PRD 06 — Delivery Pipeline, Performance, Cost, and Rollout

## 1. Purpose

This document defines how the DreamChat Image Platform should deliver images quickly, control cost, and roll out safely.

The platform should support a play-first experience.

Images should improve immersion without making the user wait too long before continuing the world.

## 2. Core Principle

Preview first. Final later.

The service should provide a fast usable image first, then upgrade to a higher-quality image in the background when requested.

This lets DreamChat remain responsive while still supporting higher visual quality.

## 3. Delivery Pipeline

### 3.0 Provider preview capability (precondition for preview-first UX)

Preview-first delivery is not free for every provider. Whether the user
actually experiences a fast preview followed by a final upgrade depends
on what the chosen provider can do, classified into three modes
(exposed as `ProviderModel.preview_capability` in
`docs/api/openapi.yaml`):

| Mode | Provider behavior | Platform delivery semantics |
|---|---|---|
| `true_preview` | Provider has a separate fast / cheap preview generation path that returns before the final asset is ready. | The platform delivers a preview asset early, then upgrades to the final asset when it lands. **Preview-first UX wait-time win is real.** |
| `derived_preview` | Provider returns only one final image. The platform downscales it for thumbnail / preview tiers. | Low-res delivery improves browser rendering and bandwidth, but does **not** reduce generation wait time. The UI should not promise "preview coming soon"; it should show progress/loading state until the single image lands. |
| `no_preview` | Provider has no preview behavior and the platform cannot manufacture a useful preview before the final asset exists (e.g. content is illegible until full resolution). | The API returns job-progress state and a placeholder; no preview asset is created until final. |

Route rules enforced by the router (ADR-007):

- **Character / place starter pack generation** may use `derived_preview`
  providers — pack jobs aren't on the interactive critical path; the
  multiple-asset latency dominates either way.
- **Interactive scene generation** (UI-driven, single image, must feel
  responsive) should prefer `true_preview` routes. If only
  `derived_preview` or `no_preview` is available, the router either
  rejects the request with `503 preview_unavailable` *or* downgrades
  expectations and exposes that in the response — never silently
  promises preview-first UX it can't deliver.
- The `Idempotency-Key` and retrieval-before-generation flows are
  unchanged across all three modes.

This rule replaces any implicit "preview-first works everywhere"
assumption earlier in this PRD.

### 3.1 Standard Pipeline

1. API receives request.
2. Service checks whether suitable assets already exist.
3. If not, service creates a generation job.
4. Preview generation runs first.
5. Preview asset is stored and returned.
6. Final generation/upscaling/refinement runs later.
7. Final asset is stored and linked to same job/asset group.
8. Web app updates UI when final is ready.

### 3.2 Retrieval Pipeline

1. Web app requests asset for character/place/artifact.
2. Image Platform checks exact and variant matches.
3. If preview exists, return immediately.
4. If final exists, return final if requested.
5. If missing, return fallback and optionally trigger generation.

### 3.3 Pack Pipeline

For pack generation:

1. Plan required pack roles.
2. Reuse existing roles where possible.
3. Generate missing preview roles in batch.
4. Return partial preview pack.
5. Generate final versions in background.
6. Mark pack complete or completed with warnings.

## 4. Resolution Tiers

### 4.1 Thumbnail

Used for:

- small avatar lists
- galleries
- admin tools
- previews

Suggested initial size:

- 128–256 px on short edge

### 4.2 Preview

Used for:

- immediate web display
- scene canvas placeholder
- participant avatar
- context card image

Suggested initial sizes:

- portraits: around 512–768 px
- scene canvas: around 768–1024 px wide
- artifacts: around 512–768 px

### 4.3 Final

Used for:

- high-quality scene display
- creator gallery
- export
- premium/high-res view

Suggested initial sizes:

- portraits: 1024–1536 px
- scene canvas: 1600–2048 px wide
- artifacts: 1024–1536 px

Exact sizes should be tested against web layout and generation cost.

## 5. Performance Targets

These are directional initial targets, not hard promises.

### 5.1 Preview Targets

- target preview readiness: 2–8 seconds
- acceptable preview readiness: up to 12 seconds for heavier backends
- preview should never block text interaction if avoidable

### 5.2 Final Targets

- target final readiness: 10–30 seconds
- acceptable final readiness: up to 60 seconds for premium generation
- final generation should run async

### 5.3 Retrieval Targets

- exact asset retrieval should feel immediate
- target API metadata response: under 300 ms from app perspective when cached/in-region
- signed URL generation should not dominate latency

## 6. Cost-Control Strategy

### 6.1 Retrieve Before Generate

The cheapest image is the one already generated.

The system should treat cache hit rate as a first-class metric.

### 6.2 Generate Packs Only for Important Entities/Places

Do not generate full packs for every background character or temporary location.

Only generate packs when importance/relevance crosses a threshold.

### 6.3 Use Tiered Quality

Recommended quality tiers:

- `draft`: fast, cheap, internal/testing/fallback
- `standard`: normal product quality
- `premium`: higher quality, slower/costlier

### 6.4 Use Asset-Type Routing

Different asset types may use different quality/latency defaults.

Examples:

- artifact thumbnails can use cheaper generation
- major NPC portraits may use stronger generation
- scene backgrounds may use balanced generation
- high-res finals can use premium only when needed

### 6.5 Limit Per-Session Generation

The product should not generate endlessly if a user repeatedly presses Continue.

Generation should be tied to meaningful world events:

- new important character
- new important place
- meaningful visual state change
- user-requested regeneration
- artifact inspection

### 6.6 Budget Controls

The service should support:

- per-user budget
- per-world budget
- per-session budget
- per-tenant budget
- soft warnings
- hard limits
- quality downgrade after threshold
- queue delay after threshold

## 7. Observability and Telemetry

The service must track:

- jobs created
- jobs completed
- jobs failed
- preview latency
- final latency
- cache hit rate
- exact/variant/fallback match rates
- cost per job
- cost per asset type
- cost per provider/model
- cost per world/session/user
- regeneration reasons
- provider error rate
- identity drift reports
- user rejection rate

## 8. Benchmarking

Before deep web app integration, run a fixed benchmark corpus.

The benchmark should include:

- 25 character prompts
- 25 place prompts
- 25 artifact prompts
- 25 hard cross-genre prompts

Each model/provider should be measured on:

- quality
- latency
- cost
- character consistency
- place consistency
- style adherence
- prompt adherence
- failure rate
- unsafe/unwanted output rate
- preview usefulness
- final quality improvement

## 9. Provider Routing Strategy

Initial version can use one provider/backend.

But the architecture should support routing later by:

- quality tier
- latency tier
- asset type
- style profile
- backend capability
- cost threshold
- fallback need
- policy mode

Example routing:

- fast preview model for draft/preview
- higher-quality model for final key assets
- self-hosted backend for high-volume standard generation later
- fallback provider when primary fails

## 10. Rollout Plan

### Phase 0 — Contracts and Benchmark Corpus

Outputs:

- API contract
- data model
- style profile schema
- visual identity schema
- asset pack templates
- benchmark corpus

Success:

- developers can implement without the web app
- benchmark can be run repeatedly

### Phase 1 — Standalone Alpha Service

Outputs:

- standalone service
- auth placeholder or internal API key
- job creation
- job status
- character pack generation
- place pack generation
- artifact generation
- storage
- preview/final metadata

Success:

- developer can generate and retrieve assets through API only

### Phase 2 — Retrieval and Reuse

Outputs:

- asset retrieval endpoints
- cache-first logic
- asset metadata
- exact/variant/fallback match
- versioning basics
- rejection/superseding basics

Success:

- service reuses assets instead of always generating

### Phase 3 — DreamChat Web App Integration

Outputs:

- scene canvas uses place assets
- participants area uses character assets
- aux context sidebar uses artifact assets
- preview updates to final when ready
- no web app direct provider calls

Success:

- UI can consume image assets without knowing backend/provider details

### Phase 4 — Optimization

Outputs:

- provider fallback
- queue tuning
- rate limits
- cost dashboards
- improved style presets
- regeneration flow
- drift feedback

Success:

- predictable cost and latency under expected load

### Phase 5 — Later Expansion

Outputs may include:

- creator style packs
- LoRA/adapters
- self-hosted model path
- advanced asset inspection
- batch world asset generation
- public creator/media workflows
- external API access

These are not required for the first implementation.

## 11. MVP Build Sequence

Recommended MVP implementation order:

1. Create service skeleton.
2. Implement job model.
3. Implement storage and metadata tables.
4. Implement provider abstraction.
5. Implement character pack endpoint.
6. Implement place pack endpoint.
7. Implement artifact endpoint.
8. Implement retrieval endpoints.
9. Implement preview/final distinction.
10. Implement benchmark runner.
11. Integrate with DreamChat web app.

## 12. Acceptance Criteria

This PRD is implemented when:

- preview and final outputs are supported
- generation jobs are async
- retrieval is fast and preferred over generation
- costs and latency are logged
- pack generation supports partial completion
- provider routing is abstracted
- service can be benchmarked independently
- web app integration does not require provider-specific logic
- generation remains bounded and does not happen every message

---

## Confidence to Implement

**Score: 85/100 — High** *(was 75; +10 after preview capability classification added)*

The three pipelines (standard, retrieval, pack), the resolution tiers, and the rollout phases (0→5) are concrete and sequenceable. Cost-control levers (budget caps, route-level disable) are now backed by the cost-control spec (`docs/architecture/cost-control.md`) and the admin surface (`docs/architecture/admin-control-surface.md`). The preview-first promise is now honest: §3.0 classifies providers into `true_preview` / `derived_preview` / `no_preview` and the router enforces the route rules so the platform never silently promises a UX it can't deliver. The benchmark corpus is populated (`prds/schemas/benchmark_corpus_template.md`) so provider routing decisions are testable. Remaining 15 points reflect the runner itself (orchestration script) and the LLM-judge augmentation being follow-ups, not blockers.

