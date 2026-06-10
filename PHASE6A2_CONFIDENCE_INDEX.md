# Phase 6A2 Confidence Index — Single-Artifact Exact Reuse

**Overall: 90/100 — Very High**

Phase 6A2 wires retrieval-before-generation into the **single-artifact**
generate path only (`POST /v1/artifacts/{artifact_id}/generate`). Before any
cost reservation or enqueue, the handler computes a deterministic artifact
render hash and looks for an existing ready artifact with that hash; a hit
becomes an already-completed cache-hit job that does no provider work, a miss
generates as before. Artifact reuse is **exact-hash only** — no
compatible/preview/matrix/embedding fallback and no artifact visual identities
(those do not exist in the generation path). Pack reuse-first stays 6A3.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | Deterministic artifact render hash (incl. `artifact_id`) | `internal/assets/artifact_hash.go` | 93 |
| 2 | Narrow exact-reuse SQL `FindReadyArtifactByPromptHash` | `internal/db/queries/visual_assets.sql` | 93 |
| 3 | Completed cache-hit job insert `InsertCompletedCacheHitJob` | `internal/db/queries/generation_jobs.sql` | 92 |
| 4 | Repository method + `ArtifactLookup` | `internal/assets/repository.go` | 93 |
| 5 | `Service.CreateCompletedCacheHitJob` (no reserve/attempt/enqueue) | `internal/jobs/service.go` | 90 |
| 6 | Handler retrieval-before-generation wiring | `internal/http/handlers/artifacts_handler.go`, `internal/http/router.go` | 90 |
| 7 | Worker provenance fix (render hash, request quality tier, provider hash) | `internal/jobs/worker.go` | 91 |
| 8 | Unit + handler + integration tests | `internal/assets/artifact_hash_test.go`, `internal/http/handlers/artifacts_handler_test.go`, `internal/jobs/worker_test.go`, `internal/jobs/integration_test.go` | 90 |

## Artifact render hash (93)

`ArtifactRenderHash(ArtifactHashInput) string` — SHA-256 hex over a labeled,
newline-delimited canonical form of:

```
v (hash-format version) · tenant_id · world_id · artifact_id ·
normalized description · style_profile_id · style_profile_version (if present) ·
quality_tier · variant_key (= "default")
```

- **`artifact_id` is part of the key.** Artifacts have no durable
  `visual_identity_id` in the generation path, so without `artifact_id` two
  different logical artifacts with similar descriptions could collide and reuse
  each other's visual. Asserted by a unit test and the integration
  "different artifact_id, same description → generates" case.
- **Provider/model identity is deliberately excluded.** Real provider/model
  routing does not exist yet (Phase 7); the current path resolves to one fixed
  mock route as a placeholder, not a deterministic routing decision. Baking a
  placeholder model id into a durable cache key would silently invalidate every
  cached artifact the day routing lands, or be a guessed id we were told not to
  include. A `v` field namespaces the hash so routing can be folded in later
  without colliding with today's keys. Documented in the file header.
- **Description normalization** collapses all runs of whitespace and trims
  (`strings.Fields` join), so cosmetic whitespace differences reuse; case is
  preserved (a different case is a different prompt).
- Stored in `visual_assets.prompt_hash`. The provider's own returned hash goes
  in `metadata.provider_prompt_hash`, never the primary key.

## SQL (93 / 92)

- `FindReadyArtifactByPromptHash`: `tenant_id + world_id + asset_type='artifact'
  + variant_key='default' + style_profile_id + quality_tier + prompt_hash +
  status='ready'`, optional `style_profile_version` exact-match (the hash
  already folds it in — belt-and-suspenders), `ORDER BY id ASC LIMIT 1` for a
  deterministic single row. **No matrix/compatibility logic.** Served by the
  existing `idx_visual_assets_tenant_world` (and `status`) indexes.
- `InsertCompletedCacheHitJob`: inserts a job already at `status='completed'`,
  `cache_result='exact_match'`, `final_asset_ids=[asset]`,
  `cost_estimate_usd=0`, `actual_cost_usd=0`, `requested_outputs` (default
  `['default']`), **no** `cost_reservation_id`. Never enqueued → the worker
  never processes it → the terminal-job cost finalizer is never invoked on it
  (this avoids the "completed job with a released reservation" hazard called
  out in the spec).
- `sqlc generate` / `sqlc vet` clean (v1.27.0, no `dbgen` hand-edits).
- **No optional index migration added.** CI applies migrations by explicit
  filename and `sqlc` reads only `0001`/`0003`, so a new file would be dead; the
  lookup is adequately served by existing indexes. **No new table — count stays
  18.**

## Where retrieval is wired (90)

In `ArtifactsHandler.Generate`, **after** validation and **before** the
`CreateAndEnqueue` (reserve + enqueue) call:

1. Resolve the effective `quality_tier` once (default `standard`) — the same
   value feeds the render hash, the lookup, and the stored asset.
2. Compute the render hash; carry it in `input_payload.prompt_hash` so the
   worker persists it on a miss.
3. `Reuse.FindReadyArtifactByPromptHash(...)`:
   - **hit** → `CreateCompletedCacheHitJob` → `202` (no reserve/attempt/enqueue);
   - **miss** (`ErrNotFound`) → fall through to the normal generate path with
     `cache_result=generated_required`;
   - **error** → `500` (fail closed; reuse is a correctness guarantee).

A cache hit creates **no** cost reservation at all (not reserve-then-release),
exactly as the spec requires. `Reuse` is nil-safe (handler skips reuse when
unset). Exact reuse runs for **every** `fallback_policy` including `none` —
`fallback_policy` gates compatible/preview fallback, not exact reuse.

## Worker provenance fix (91)

The single-artifact worker now:

- writes `prompt_hash` = the render hash from the payload (so an identical
  repeat is found), falling back to the provider hash only for pre-6A2 jobs;
- writes `metadata.provider_prompt_hash` = the provider's returned hash;
- writes `quality_tier` from the request payload (was hardcoded `standard`),
  so the stored tier matches what the reuse lookup queries on.

Pack worker untouched (pack reuse is 6A3).

## Cost behavior on a hit (verified)

Reserved unchanged · spent unchanged · **no** `cost_reservation` row · **no**
`generation_cost_event` · **no** `provider_attempt` · `actual_cost_usd=0`. The
integration test snapshots `provider_attempts`, `visual_assets`,
`cost_reservations`, and the tenant budget's `spent_amount`/`reserved_amount`
before the repeat and asserts none move, plus zero reservations/cost-events
point at the cache-hit job.

## OpenAPI (no change)

The `202` stays an **acceptance envelope** (`status: queued`, the schema's only
accepted-status value) with `estimated_cost_usd: "0.0000"` to signal the reuse
is free. The synchronously-completed state, `cache_result=exact_match`, and
`final_asset_ids` are observed via `GET /v1/jobs/{id}` (whose schema already
carries them). The response schema can represent a cache-hit job, so **no
OpenAPI change** — mirror diff stays clean.

## Tests (90)

- **Hash unit** (`artifact_hash_test.go`): determinism; description
  normalization (whitespace variants → same hash, case preserved); variant-key
  default; different `artifact_id` / `style_profile_id` / `quality_tier` each
  differ; `style_profile_version` participates when present.
- **Handler** (`artifacts_handler_test.go`): exact hit creates a completed
  cache-hit job and does **not** call the reserve/enqueue path; hit records
  `exact_match` + reuses the existing asset id; miss goes through the normal
  generate path with `generated_required` and carries the render hash;
  `fallback_policy=none` still reuses an exact hit; different `artifact_id`
  misses even with the same description.
- **Worker** (`worker_test.go`): persists the request `quality_tier`, the render
  hash as `prompt_hash`, and the provider hash under
  `metadata.provider_prompt_hash`.
- **Integration** (`integration_test.go`, Postgres + MinIO):
  `TestEndToEndArtifactExactReuse` (generate → worker → identical repeat is a
  completed `exact_match` reusing the first asset, with no second provider
  attempt / asset / reservation / enqueue / cost event and no budget spend);
  `TestEndToEndArtifactReuseMisses` (different description / quality tier /
  artifact_id each generate; identical repeat reuses). All prior phases'
  unit + integration tests stay green.

## Explicit deferrals (out of scope for 6A2)

Pack reuse-first, misses-only pack pricing, reused pack items, all-hits pack
completion, pack-completeness storage, compatible/preview artifact fallback,
artifact visual identities, `force_regenerate`, regeneration endpoint, S3 read
APIs / presigned URLs, BFL/provider routing, real world-state safety filtering,
embedding/similarity retrieval, and new tables — all remain 6A3 / 6B / 7.
