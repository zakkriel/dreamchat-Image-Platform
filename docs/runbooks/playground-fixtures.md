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

This seeds a self-consistent graph for `tenant_dev` / `world_dev`: one style
profile, two visual identities, and four `visual_assets` rows **attached to
those identities**, then uploads a tiny PNG to each asset's deterministic
object keys in MinIO. The assets are attached to identities precisely so the
asset-search exact-match predicate (which keys on `visual_identity_id` +
`variant_key` + `state_version` + `style_profile_id`) can find them.

Seeded objects:

| id                | kind            | details                                   |
| ----------------- | --------------- | ----------------------------------------- |
| `style_fixture`   | style profile   | `open_prompt`                             |
| `vi_fix_character`| visual identity | `owner_type=character`, `owner_id=character_fix_1` |
| `vi_fix_place`    | visual identity | `owner_type=place`, `owner_id=place_fix_1`|

| asset_id                 | visual_identity_id | asset_type           | variant_key    |
| ------------------------ | ------------------ | -------------------- | -------------- |
| `asset_fix_char_neutral` | `vi_fix_character` | `character_portrait` | `neutral`      |
| `asset_fix_char_smiling` | `vi_fix_character` | `expression`         | `smiling`      |
| `asset_fix_place_estab`  | `vi_fix_place`     | `place_scene`        | `establishing` |
| `asset_fix_place_night`  | `vi_fix_place`     | `place_scene`        | `night`        |

#### Exact Asset Search inputs that work

In the playground **Asset search** panel (panel 7), these inputs return an
`exact_match` with a rendered image:

```
world_id           = world_dev
owner_type         = character
visual_identity_id = vi_fix_character
variant_key        = neutral          # or: smiling
style_profile_id   = style_fixture
state_version      = 1
```

```
world_id           = world_dev
owner_type         = place
visual_identity_id = vi_fix_place
variant_key        = establishing     # or: night
style_profile_id   = style_fixture
state_version      = 1
```

You can also read an asset directly: `GET /v1/assets/asset_fix_char_neutral`.

> `POST /v1/assets/search` rejects `owner_type=artifact` (`400`,
> "owner_type must be character or place"). Artifact retrieval is out of scope
> for search; only character/place identities are retrievable.

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

| Env var          | Default          | Notes                                           |
| ---------------- | ---------------- | ----------------------------------------------- |
| `SEED_TENANT_ID` | `tenant_dev`     | Must match the token you use in the playground. |
| `SEED_WORLD_ID`  | `world_dev`      | Assets and identities are world-scoped.         |
| `S3_BUCKET`      | `image-platform` | Bucket name (matches docker-compose).           |

The style profile id (`style_fixture`) and identity ids (`vi_fix_character`,
`vi_fix_place`) are fixed so the search inputs above stay stable.

#### Limitations (by design)

- Only **character** and **place** identities are seeded, because
  `/v1/assets/search` only retrieves those owner types.
- Re-running the script is idempotent: identities/style use
  `ON CONFLICT DO NOTHING` and assets upsert on `asset_id`.
- `make down` (which runs `docker compose down -v`) wipes the volumes and the
  fixtures with them; re-run the script after bringing the stack back up.

## Fixture images

The PNGs live in [`playground/fixtures/`](../../playground/fixtures/): four tiny
(64×64, ~140 byte) solid-color images, small enough to keep in the repo. Replace
them with your own PNGs of the same names to test different content.
