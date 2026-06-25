# DreamChat Image Platform PRDs

This package contains the standalone PRDs and implementation guidance for the DreamChat Image Platform.

The Image Platform is intended to be built as an independent API/service that can be tested outside the DreamChat web app and later consumed by the web app as one client.

## Files

1. `01_image_platform_vision_and_scope.md`
   - Defines why the image platform exists, what it supports, and what is out of scope.

2. `02_standalone_image_generation_api_and_job_system.md`
   - Defines the API-first service, job lifecycle, endpoints, response contracts, and backend abstraction.

3. `03_character_and_place_consistency_system.md`
   - Defines persistent visual identity records for characters and places, including anchors, invariants, reference packs, and drift control.

4. `04_asset_packs_variants_and_expressions.md`
   - Defines starter packs for characters and places, expression/angle variants, place states, and artifact generation.

5. `05_storage_retrieval_versioning_and_cache_strategy.md`
   - Defines asset storage, metadata, retrieval-before-generation rules, versioning, cache matching, and invalidation.

6. `06_delivery_pipeline_performance_cost_and_rollout.md`
   - Defines preview/final delivery, performance targets, cost controls, rollout stages, observability, and acceptance gates.

7. `07_superpowers_implementation_prompt.md`
   - A direct implementation prompt for Superpowers.

8. `08_npc_expression_sprite_pipeline.md`
   - Defines the NPC expression sprite-sheet pipeline: one coherent governed sprite-sheet
     generation sliced into reusable, individually addressable expression assets. Supersedes the
     NPC portrait/expression-pack content of `04_asset_packs_variants_and_expressions.md` §4.

9. `schemas/image_platform_openapi_draft.yaml`
   - A draft OpenAPI-style API contract for the standalone service.

10. `schemas/image_platform_data_model.json`
   - Draft data model for assets, jobs, style profiles, visual identities, packs, and provider metadata.

11. `schemas/benchmark_corpus_template.md`
   - Template for evaluating quality, consistency, latency, cost, and failure cases.

## Implementation principle

Do not build this as a hardcoded image button inside the web app.

Build it as a separate, testable service:

- API-first
- async-job based
- provider/model agnostic
- storage/retrieval first
- character/place consistency aware
- preview-first and high-res-final capable
- style-flexible
- observable and cost-controlled

## Initial asset scope

The first version should support only:

- character visual identity and character asset packs
- place visual identity and place asset packs
- artifact/context images

Do not start with:

- video
- animation
- marketplace
- end-user model training
- full media generation layer
- per-message image generation

---

## Confidence to Implement

**Score: 95/100 — Very High**

This is an index/scope document, not a buildable artifact. There is no code surface of its own — it just enumerates the other PRDs and the build principle. The principle ("API-first, async, provider-agnostic, storage/retrieval-first, preview+final, observable, cost-controlled") is unambiguous and maps to well-understood service patterns. The only loose end is the relationship to the already-richer in-repo `docs/` tree (see `frustration_log.md` entry 2) — a future merge step.

