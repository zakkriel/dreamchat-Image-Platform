# Styles API

## Style Profile

A style profile controls visual style independently from provider/model.

Examples:

```txt
open_prompt
preset
creator_pack
```

## List Styles

```txt
GET /v1/styles
```

Required scope:

```txt
styles:read
```

## Create Style

```txt
POST /v1/styles
```

Required scope:

```txt
styles:write
```

Example request:

```json
{
  "name": "Dark cinematic realism",
  "style_mode": "preset",
  "positive_prompt": "cinematic realistic dramatic lighting",
  "negative_prompt": "low quality, blurry, distorted anatomy",
  "default_quality_tier": "standard"
}
```

## Style Preview

```txt
POST /v1/styles/{style_id}/preview
```

Creates a small preview generation job to test style behavior.

---

## Confidence to Implement

**Score: 85/100 — High**

CRUD for `style_profiles` is mechanical; the preview endpoint reuses the existing generation pipeline with `quality_tier=draft`. Style modes (`open_prompt`, `preset`, `creator_pack`) map to a strategy switch in the prompt compiler. Subtracting points only because the prompt-compiler contract for *how exactly* `positive_prompt` and `negative_prompt` are spliced into a final provider prompt isn't pinned down — see `docs/architecture/asset-versioning.md` "Prompt Hashing" for hints, but it's still a design call.
