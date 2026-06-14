# Runbook — Playground Visual Fixtures (local/dev only)

This document describes how to get **sample visual assets** into a local
environment so the [Image Platform Playground](../../playground/README.md) can
exercise the asset-search and job/asset-gallery panels **without** first running
a real generation job.

> **Scope.** This is a local/dev testing aid. It is **not** a product image
> upload feature and adds **no** runtime API surface. There is no
> user-facing import/upload endpoint in the platform, so fixtures are seeded
> directly through the same local conventions the dev stack already uses
> (Postgres via `psql`, MinIO via the `minio/mc` image). Never run this against
> a shared or production environment.

## Two ways to get assets

### A. Generate them (the real path — preferred)

The honest end-to-end path is to generate assets through the API with the mock
provider, then read them back:

1. `make dev` — brings up Postgres, Redis, MinIO, the API, and the worker, and
   prints a dev token.
2. In the playground: create a style, generate an artifact (or a pack), then
   poll the job in the **Job monitor** panel until `completed`.
3. Fetch `GET /v1/jobs/{job_id}/assets` (the panel's button) — the worker has
   produced real thumbnail/preview/final tiers in MinIO and the response
   carries presigned URLs that render in the gallery.

No fixtures are needed for this path. The mock provider produces deterministic
placeholder bytes, so it works with no provider keys.

### B. Seed static fixtures (shortcut for search/retrieval testing)

When you only want existing assets to **search for** and **render** — without
waiting on generation — seed a few static fixtures:

```bash
make dev                      # stack must be up
./scripts/seed_visual_fixtures.sh
```

This inserts four `visual_assets` rows for `tenant_dev` / `world_dev` and
uploads a tiny PNG to each asset's deterministic object keys in MinIO.

| asset_id          | asset_type          | variant_key   |
| ----------------- | ------------------- | ------------- |
| `asset_fix_blue`  | `character_portrait`| `neutral`     |
| `asset_fix_green` | `place_scene`       | `establishing`|
| `asset_fix_amber` | `artifact`          | `artifact`    |
| `asset_fix_violet`| `expression`        | `smiling`     |

Then, in the playground:

- **Asset search** panel → set `world_id=world_dev`, optionally an
  `asset_type`, and search. Returned assets render their presigned image URLs.
- Or call `GET /v1/assets/asset_fix_blue` directly.

#### Why it works (what the script relies on)

The asset read surface mints presigned `https` URLs at read time from a
**deterministic object key**, never a client-supplied path
(`internal/storage/storage.go`, `internal/http/handlers/assets_handler.go`):

```
assets/<asset_id>/thumb.png   ->  thumbnail_download_url
assets/<asset_id>/low.png     ->  preview_download_url
assets/<asset_id>/high.png    ->  final_download_url
```

The script writes the fixture PNG to all three keys for each asset, so the
presigned URLs resolve. The durable `s3://` provenance columns
(`low_res_url` / `high_res_url` / `thumbnail_url`) are set to the matching
canonical URLs.

#### Configuration

Override defaults with environment variables:

| Env var                 | Default      | Notes                                              |
| ----------------------- | ------------ | -------------------------------------------------- |
| `SEED_TENANT_ID`        | `tenant_dev` | Must match the token you use in the playground.    |
| `SEED_WORLD_ID`         | `world_dev`  | Assets are world-scoped.                           |
| `SEED_STYLE_PROFILE_ID` | _(unset)_    | Set to a real style id for style-filtered search.  |
| `S3_BUCKET`             | `image-platform` | Bucket name (matches docker-compose).          |

#### Limitations (by design)

- Fixtures are **not** linked to a `visual_identity`, so asset searches that
  filter strictly by `owner_type`/`owner_id` may not match them. Search by
  `world_id` + `asset_type` + `variant_key` to find them.
- Re-running the script upserts the same rows (idempotent on `asset_id`).
- `make down` (which runs `docker compose down -v`) wipes the volumes and the
  fixtures with them; re-run the script after bringing the stack back up.

## Fixture images

The PNGs live in [`playground/fixtures/`](../../playground/fixtures/): four tiny
(64×64, ~140 byte) solid-color images, small enough to keep in the repo. Replace
them with your own PNGs of the same names to test different content.
