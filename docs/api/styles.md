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
