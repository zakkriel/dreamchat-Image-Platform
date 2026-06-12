# Phase 7B Confidence Index — `true_preview` Two-Phase Generation

**Overall: 90/100 — Very High**

Phase 7B adds preview-first delivery. A request can now opt into
`delivery_mode=preview_first`; the resolver imposes a **hard `true_preview`
requirement** (resolved once, before cost), and the worker runs a **two-phase**
lifecycle in a single job with a single charge: it emits a lighter preview
asset, **commits the job to `preview_ready` (with `preview_asset_ids`) before
final generation begins** — so the preview is externally observable — then emits
the final asset and completes the same job. Cost is reserved once and committed
once. Final-only / omitted delivery is **behaviorally unchanged from Phase 7A**.
BFL (`no_preview`) is excluded from preview-first; mock (`true_preview`) serves
it. **No new table — count stays 18.**

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | `delivery_mode` request opt-in (artifact + style preview) | `api/openapi.yaml`, `apigen.gen.go`, `artifacts_handler.go`, `style_preview_handler.go` | 93 |
| 2 | Hard `true_preview` routing on `preview_first` (422 before writes) | `routing.go` handlers, `internal/providers/routing/routing.go` (existing Stage-5 filter) | 92 |
| 3 | Preview capability persisted on payload (worker never re-resolves) | `internal/http/handlers/routing.go` (`applyResolvedRoute`) | 91 |
| 4 | Worker two-phase lifecycle (preview → commit → final) | `internal/jobs/worker.go` (`processPreviewFirst`) | 89 |
| 5 | Preview asset `status=preview_ready` + `preview_safe` tag | `InsertPreviewVisualAsset` query, `assets.InsertPreview` | 91 |
| 6 | Preview-ready job transition (separate, earlier transaction) | `MarkGenerationJobPreviewReady` query, `jobs.MarkPreviewReady` | 91 |
| 7 | Cost reserved once / committed once after final | `internal/jobs/worker.go` + existing `cost.Lifecycle` | 90 |
| 8 | Retry resumes final without duplicating preview / recharging | `internal/jobs/worker.go` (preview_asset_ids resume guard) | 89 |
| 9 | Delivery: preview shown before final; final after completion | `internal/http/handlers/assets_handler.go` (`deliveryOrder`) | 91 |
| 10 | Resolver + handler + worker unit tests; DB/S3 integration tests | `*_test.go` | 89 |
| 11 | Additive OpenAPI `0.7.0 → 0.8.0`, mirrored byte-for-byte | `api/openapi.yaml`, `docs/api/openapi.yaml`, `apigen.gen.go` | 93 |

## Request opt-in (93)

`delivery_mode: final_only | preview_first` (default `final_only`) is added to
`GenerateArtifactRequest` and `StylePreviewRequest`. Validation rejects any
other value with `400 invalid_request`. The handler only persists
`delivery_mode=preview_first` on the payload when opted in (final_only/omitted
keeps the Phase 7A payload shape). **Packs are excluded**: the pack schema does
not expose `delivery_mode`, and the lenient JSON decoder drops the unknown
field, so a `delivery_mode` sent to a pack endpoint is silently ignored — the
pack resolves `pack_capable` with **no** preview requirement and never
two-phases (`TestPackEndpointsIgnoreDeliveryMode`).

## Hard `true_preview` routing (92)

When `delivery_mode=preview_first`, the handler sets
`ResolveRequest.RequiredPreviewCapability=true_preview` alongside the normal
`RequiredCapability=scene_capable`. The resolver's existing Stage-5 filter keeps
only `true_preview` routes; an empty survivor set returns
`ErrUnsupportedCapability` → `422 unsupported_capability`. Because resolution
runs **after the idempotency-replay check and before cost reservation / job
creation / enqueue**, a BFL-only `preview_first` request fails with **no** side
effects (`TestEndToEndPreviewFirstBFLOnlyReturns422`,
`TestArtifactPreviewFirstUnsupportedReturns422BeforeWrites`). There is **no**
downgrade to `final_only` and **no** `derived_preview` fallback — both deferred.
`final_only` leaves `RequiredPreviewCapability` empty, so BFL stays selectable
(`TestFinalOnlyDoesNotConstrainPreviewAndAllowsBFL`).

## Provider two-call model (89)

Phase 7B intentionally does **not** introduce a new provider interface or a
durable async polling lifecycle. The worker proves the platform lifecycle using
the existing `ImageProvider.Generate` surface, calling it twice:

1. **Preview** — `Generate` at `previewRenderEdge=512` (lower than the final's
   `deliveryRenderEdge=1024`), so the preview asset is genuinely lighter where
   the provider honors dimensions (mock does). Upload tiers, insert a
   `visual_asset` `status=preview_ready` tagged `preview_safe`.
2. **Final** — `Generate` at `deliveryRenderEdge=1024`, upload tiers, insert a
   `status=ready` asset.

No new queue job, no provider webhooks, no `PollStatus`-driven runtime.
`TestPreviewFirstTwoPhaseLifecycle` asserts the preview render requests smaller
dimensions than the final.

## Observable preview-ready, committed before final (91)

The worker persists the preview in transactions **separate from and before**
final generation:

```
generate preview → upload → InsertPreview (commit)
                          → MarkPreviewReady(preview_asset_ids) (commit)
   ── only then ──→ generate final → upload → Insert (commit)
                                            → MarkCompleted(final_asset_ids) (commit)
                                            → Finalizer.Commit
```

These are distinct auto-commit statements; the long-running final `Generate`
never shares a DB transaction with the preview persistence. Two proofs:

- **Unit**: `TestPreviewFirstTwoPhaseLifecycle` uses a provider that, on its
  *final* call, reads the job back and asserts it already shows
  `status=preview_ready` with one `preview_asset_id` — i.e. the preview was
  committed before final generation started.
- **Integration**: `TestEndToEndPreviewFirstArtifact` runs the worker in a
  goroutine with a provider that **blocks** on the final call, then issues real
  `GET /v1/jobs/{job_id}` and `GET /v1/jobs/{job_id}/assets` and observes
  `preview_ready` + the delivered preview asset **before** releasing final.

## Cost once (90)

The reservation is created once at job creation (Phase 7A path). The worker
commits it **once**, only after final success — no separate preview charge. On
any terminal failure it is released. `TestPreviewFirstTwoPhaseLifecycle` asserts
exactly one `Finalizer.Commit` and zero releases; the integration test asserts a
single `cost_reservations` row, `status=committed`, and `actual_cost_usd=0.0100`.

## Retry behavior (89)

`preview_ready` is **not** a terminal status, so the Phase 7A
completed/failed short-circuit does not catch it — a retried preview-ready job
falls into `processPreviewFirst`, where a **non-empty `preview_asset_ids` skips
the preview phase entirely** and resumes at final. This guarantees a retry never
duplicates the preview and never re-reserves/recharges
(`TestPreviewFirstRetryResumesFinalWithoutDuplicatingPreview`). A
completed/failed job still only finalizes cost.

## Failure after preview (90)

If final generation fails after the preview was delivered, the terminal-attempt
path marks the job `failed` and **releases** the reservation; `final_asset_ids`
stays empty and the preview asset stays `preview_ready` (not archived, not
superseded — it is the last useful output). `GET /v1/jobs/{job_id}/assets` then
returns the preview, because `deliveryOrder` falls back to `preview_asset_ids`
when `final_asset_ids` is empty
(`TestPreviewFirstFinalFailureKeepsPreviewReadable`,
`TestEndToEndPreviewFirstFinalFailureKeepsPreview`,
`TestJobAssetsFailedAfterPreviewReturnsPreview`).

## Reuse decision (documented)

Per the task's acceptable simpler 7B rule: **preview-first bypasses exact
reuse** and always generates a fresh preview + final. A final-only ready asset
has no preview, so reusing it could never satisfy the preview-first contract.
Final-only reuse is unchanged. Forced regeneration still supersedes the prior
ready **final** (Phase 6A4); the preview tier is a different status and is never
superseded.

## Delivery contract (91)

`GET /v1/jobs/{job_id}/assets` for an artifact job:
`final_asset_ids` when present, else `preview_asset_ids`, else empty. Therefore
`preview_ready` → preview asset; `completed` → final asset; failed-after-preview
→ preview asset. Deterministic and unit-tested
(`TestJobAssetsPreviewReadyReturnsPreviewAsset`,
`TestJobAssetsCompletedPrefersFinalOverPreview`). `GET /v1/jobs/{job_id}`
exposes `status` and `preview_asset_ids` directly (already mapped). No new
delivery endpoint; existing presigned-URL machinery is reused.

## Schema / migration decision

**No schema change, no migration.** All primitives already existed
(`generation_jobs.status='preview_ready'`, `generation_jobs.preview_asset_ids`,
`visual_assets.status='preview_ready'`). Two **additive sqlc queries** were
added (`InsertPreviewVisualAsset`, `MarkGenerationJobPreviewReady`) and
`sqlc generate` was re-run. Table count remains **18**.

## Risks / residue

- The worker runs both phases in one task invocation; a true async provider
  preview lifecycle (`PollStatus`/webhooks) is deferred to a later phase.
- `derived_preview` fallback is explicitly deferred — `preview_first` is a hard
  `true_preview` requirement in 7B.
- Provider preview routing beyond mock `true_preview` is out of scope.
- The preview-ready resume relies on `preview_asset_ids` already being committed;
  a crash between the preview asset insert and `MarkPreviewReady` leaves the job
  non-`preview_ready` with no `preview_asset_ids`, so a retry regenerates the
  preview (correct, no duplicate observable preview).
