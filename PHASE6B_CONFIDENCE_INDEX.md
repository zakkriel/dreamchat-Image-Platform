# Phase 6B Confidence Index — Delivery Readiness

**Overall: 90/100 — Very High**

Phase 6B makes finished assets deliverable. Before it, a client that generated
(or reused) an asset received ids and unfetchable `s3://` references, and the
three "tiers" were identical full-size PNGs. Now: (1) the platform mints
short-lived authenticated `https` GET URLs from the deterministic object keys,
(2) the worker writes three genuinely distinct downscaled tiers, (3) the asset
and a new job-assets read surface those fetchable per-tier URLs, and (4) a
style-preview endpoint renders a sample image deliverable through the same
machinery. `true_preview` provider routing is explicitly deferred to Phase 7;
this phase delivers `derived_preview` honestly. No migration — the table count
stays **18**.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | `Storage.Presign` (AWS SDK v2 presign, endpoint/path-style honored) | `internal/storage/storage.go`, `internal/storage/s3.go` | 92 |
| 2 | Deterministic downscale tiers (fixed Catmull-Rom, no upscale) | `internal/imaging/imaging.go` | 91 |
| 3 | Worker writes three distinct tiers; requests a delivery-res image | `internal/jobs/worker.go`, `worker_pack.go` | 91 |
| 4 | Asset read enrichment: presigned per-tier URLs + `url_expires_at` | `internal/http/handlers/assets_handler.go` | 91 |
| 5 | `GET /v1/jobs/{job_id}/assets` (deterministic delivery order) | `internal/http/handlers/assets_handler.go` | 90 |
| 6 | Style preview `POST /v1/styles/{style_id}/preview` (requires `world_id`) | `internal/http/handlers/style_preview_handler.go` | 90 |
| 7 | DI/config: `S3_PRESIGN_TTL`, storage read side into API | `internal/config/config.go`, `internal/http/router.go`, `cmd/api/main.go` | 92 |
| 8 | Additive OpenAPI `0.5.4 → 0.6.0`, mirrored byte-for-byte | `api/openapi.yaml`, `docs/api/openapi.yaml`, `internal/http/apigen/apigen.gen.go` | 93 |
| 9 | Unit + handler tests (presign, tiers, asset/job-assets, preview) | `*_test.go` (storage, imaging, handlers) | 91 |
| 10 | Integration tests (presigned GET 200 + distinct tiers, preview e2e) | `internal/jobs/delivery_integration_test.go` | 88 |

## Presign wiring point — read time, never persisted (92)

`Presign(ctx, key, ttl)` lives on `storage.Storage` and is implemented on
`s3Storage` with `s3.NewPresignClient(...).PresignGetObject(..., WithPresignExpires(ttl))`.
Signing is **purely local** (no network round-trip), so it does not slow a read
and cannot fail on connectivity. The same `S3_ENDPOINT` / `S3_USE_PATH_STYLE`
settings drive it as `Put`, so MinIO (path-style: bucket in the path) and R2/S3
(virtual-host https) both produce working URLs.

A presigned URL is **never persisted**: the durable provenance on
`visual_assets` stays the `s3://` canonical URL (`storage.CanonicalURL`).
Persisting a presigned URL would bake in an expiry and leak a signature into the
DB. The URL is computed per request, in the read handler, from
`storage.ObjectKey(asset_id, variant, "png")` — a **derived** key, never a
client-supplied path — so the read surface can't be coerced into signing an
arbitrary object. A URL is only minted **after** the tenant-scoped row lookup
succeeds (`GetByIDForTenant`); a cross-tenant miss 404s and presigns nothing
(`TestAssetGetCrossTenantNeverPresigns`).

## Tier downscale rule & determinism (91)

`imaging.EncodeTiers(src)` decodes the provider PNG once and emits three PNGs:

- **final** = the provider output re-encoded as PNG (full resolution).
- **preview** = short edge downscaled toward `PreviewShortEdge` (768px).
- **thumbnail** = short edge downscaled toward `ThumbnailShortEdge` (256px).

Rules: `thumbnail ≤ preview ≤ final`; a tier target larger than the source short
edge is **never upscaled** (tier = `min(target, source)`), so a small source
yields identical-size tiers and dimensions only diverge when the source is large
enough. Downscaling uses a fixed **Catmull-Rom** kernel (`golang.org/x/image/draw`,
the one new dep), and PNG encoding uses fixed settings, so the same input bytes
always produce byte-for-byte identical tiers (`TestEncodeTiersDeterministic`) —
a regenerate/reupload is reproducible. The worker asks the provider for a
1024px square (`deliveryRenderEdge`) so the three delivered tiers are genuinely
distinct (1024 / 768 / 256).

## Read-UX surface & scoping (90)

- `GET /v1/assets/{asset_id}` — unchanged metadata plus the additive
  `thumbnail/preview/final_download_url` + `url_expires_at`. Tenant-scoped via
  `Repo.GetByIDForTenant`, `images:read`-gated by the router.
- `GET /v1/jobs/{job_id}/assets` — the new job/pack delivery read. Tenant-scoped
  job lookup gates everything; then assets are returned in **deterministic
  delivery order**: pack jobs by `asset_pack_items.sort_order` (via
  `ListAssetPackItems`), artifact jobs by `generation_jobs.final_asset_ids`
  order. Delivery is **not** restricted to `status='ready'` — archived assets
  the tenant owns stay displayable (`TestJobAssetsArtifactDeliveryOrder` seeds an
  archived asset and asserts it is returned with a URL). A referenced asset that
  is missing/cross-tenant is skipped rather than failing the whole read. The
  response is a `{ "assets": [...] }` object (`JobAssetsResponse`), consistent
  with the existing `AssetSearchResponse`.

Both surfaces are nil-safe: without a wired signer the URL fields are simply
omitted (the pre-6B behavior — `TestAssetGetWithoutSignerOmitsURLs`), so the
change is strictly additive.

## Style preview path (90)

`POST /v1/styles/{style_id}/preview` requires `world_id` (generated assets are
world-scoped). It validates the style for the tenant (422 on miss), then rides
the **normal artifact generate path** (`jobs.Creator.CreateAndEnqueue`,
`job_type=artifact`, one image) with a payload carrying the style's positive
prompt as the description, the style id, an effective quality tier, a render
hash, and a `preview_kind=style_preview` provenance marker. The worker produces
an ordinary delivered `visual_asset` through the same storage + presigned-tier
machinery, so the sample is read back via `GET /v1/jobs/{job_id}/assets`. No
schema change was needed — the preview asset is found via job → asset, so there
is **no** `style_profiles.preview_asset_id` column (table count stays 18).

## `true_preview`-deferred boundary (explicit) — 93

This phase delivers `derived_preview` only: real downscaled tiers + the read UX.
Out (Phase 7): a real latency-saving preview-first path (a provider with a
separate fast preview route, ADR-010 / PRD 06 §3.0), provider routing, the BFL
adapter, `503 preview_unavailable`, and a preview/final two-phase generation
job. The existing `preview_ready` status columns and
`generation_jobs.preview_asset_ids` are untouched (no new write behavior).

## Schema choice — none (93)

Presigning is runtime; the tiers reuse the existing three URL columns; the
job-assets read uses existing queries (`ListAssetPackItems`, `GetVisualAssetByID`);
the preview asset is reachable via job → asset. So **no migration** — `sqlc
generate` produces no `dbgen` diff and the CI table-count assertion stays at 18
(the migrations job is unchanged).

## OpenAPI change — strictly additive, mirrored (93)

`0.5.4 → 0.6.0`. Added: four optional `VisualAsset` fields
(`thumbnail/preview/final_download_url`, `url_expires_at`); the new
`GET /v1/jobs/{job_id}/assets` path + `JobAssetsResponse`; a `StylePreviewRequest`
body (required `world_id`) on the already-declared style-preview path. No field
removed or made required. `api/openapi.yaml` and `docs/api/openapi.yaml` are
byte-identical (`diff -q`), the spec validates, and `apigen.gen.go` was
regenerated (`make generate` clean).

## Tests (91 / integration 88)

- **Presign** (`internal/storage/s3_presign_test.go`): https URL for a derived
  key, `X-Amz-Expires` honors the TTL (and zero-ttl falls back to 900s),
  path-style puts the bucket in the path (MinIO), distinct keys → distinct URLs.
- **Downscale** (`internal/imaging/imaging_test.go`): three distinct sizes for a
  large source (thumb < preview < final), aspect preserved, never upscales a
  small source, deterministic, rejects non-PNG.
- **Asset read / job-assets** (`internal/http/handlers/delivery_handler_test.go`):
  presigned URLs + `url_expires_at`, only derived keys signed, provenance
  preserved; cross-tenant 404 mints nothing; artifact + pack delivery order;
  archived asset still delivered; empty job; signer-less omission.
- **Style preview** (`internal/http/handlers/style_preview_handler_test.go`):
  202 + exactly one enqueue with the right payload; unknown/cross-tenant style
  → 422; missing `world_id` → 400; BFL provider → 503 before any enqueue.
- **Integration** (`internal/jobs/delivery_integration_test.go`, Postgres +
  MinIO): generate → run worker → the presigned URLs actually `GET 200` from
  MinIO and the three tiers are distinct sizes; style preview renders and is
  retrievable through the presigned read.

## Commands run

`go vet ./...`, `go build ./...`, `go test ./...`, `go vet -tags=integration ./...`,
`golangci-lint run`, `golangci-lint run --build-tags integration`, `sqlc vet`,
`sqlc generate` (no diff), `make generate` (apigen regenerated), OpenAPI
validation + mirror `diff -q`. Integration tests (`go test -tags=integration`)
skip locally without Postgres/MinIO and run in CI.

## Residual risk (why not higher)

- The end-to-end MinIO fetch + distinct-tier assertions run only in CI here
  (no local Docker daemon); the harness mirrors the existing, passing
  integration tests, but the live presigned-GET path was not exercised in this
  environment. (−)
- The worker pins a 1024px delivery render so tiers diverge; a future real
  provider that returns a smaller image would collapse preview→final to equal
  sizes (still valid per the "distinct only when large enough" rule, but worth
  re-checking when provider routing lands in Phase 7). (−)
