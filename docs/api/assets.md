# Assets API

## Asset Search

```txt
POST /v1/assets/search
```

Required scope:

```txt
images:read
```

Example request:

```json
{
  "owner_type": "character",
  "owner_id": "char_123",
  "variant_key": "angry_expression",
  "version": 1,
  "style_profile_id": "style_dark_cinematic"
}
```

Example response:

```json
{
  "assets": [
    {
      "id": "asset_123",
      "asset_type": "character_portrait",
      "variant_key": "angry_expression",
      "version": 1,
      "low_res_url": "https://cdn.example.com/asset_123_low.webp",
      "high_res_url": "https://cdn.example.com/asset_123_high.webp",
      "thumbnail_url": "https://cdn.example.com/asset_123_thumb.webp",
      "status": "ready"
    }
  ]
}
```

## Get Asset

```txt
GET /v1/assets/{asset_id}
```

Required scope:

```txt
images:read
```

## Regenerate Asset

```txt
POST /v1/assets/{asset_id}/regenerate
```

Required scope:

```txt
images:write
```

Regeneration should create a new job.

It should not overwrite the existing asset unless explicitly requested by a future admin-only operation.

---

## Confidence to Implement

**Score: 85/100 — High**

`POST /v1/assets/search` maps to an indexed query on `visual_assets` (owner_type, owner_id, variant_key, version, style_profile_id). `GET /v1/assets/{id}` is a one-row read. `POST /v1/assets/{id}/regenerate` creates a new generation_job that references the existing asset. Subtracting points because the search endpoint doesn't expose the `match_type` telemetry (exact/variant/fallback) that PRD 05 emphasizes — the search response is a flat list of assets without saying which was an exact hit vs. a fallback. Implementer should add that field before web-app integration.
