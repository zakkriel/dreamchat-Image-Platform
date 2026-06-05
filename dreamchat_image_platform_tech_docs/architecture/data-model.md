# Data Model

## Core Entities

```txt
api_token
style_profile
provider_model
visual_identity
visual_asset
generation_job
generation_cost_event
audit_event
idempotency_key
```

## Visual Identity

A `visual_identity` represents the persistent visual identity of a character, place, or artifact.

It is the anchor for visual consistency.

```json
{
  "id": "vid_123",
  "world_id": "world_123",
  "owner_type": "character",
  "owner_id": "char_456",
  "display_name": "Mara Vey",
  "canonical_visual_traits": {
    "age_range": "early 30s",
    "hair": "black wavy shoulder-length hair",
    "eyes": "dark green eyes",
    "build": "lean",
    "signature_features": ["thin scar over left eyebrow", "silver pendant"]
  },
  "style_profile_id": "style_dark_cinematic",
  "consistency_key": "vk_abc123",
  "anchor_asset_ids": ["asset_001", "asset_002"],
  "current_version": 1,
  "status": "active"
}
```

## Visual Asset

A `visual_asset` is one generated or uploaded image file plus metadata.

```json
{
  "id": "asset_001",
  "visual_identity_id": "vid_123",
  "world_id": "world_123",
  "asset_type": "character_portrait",
  "variant_key": "neutral_front",
  "version": 1,
  "status": "ready",
  "low_res_url": "s3://bucket/assets/asset_001/low.webp",
  "high_res_url": "s3://bucket/assets/asset_001/high.webp",
  "thumbnail_url": "s3://bucket/assets/asset_001/thumb.webp",
  "provider_id": "bfl",
  "model_id": "flux-2-klein",
  "prompt_hash": "ph_abc",
  "seed": "123456",
  "metadata": {
    "expression": "neutral",
    "angle": "front",
    "quality_tier": "standard"
  }
}
```

## Generation Job

A `generation_job` represents asynchronous generation work.

```json
{
  "id": "job_123",
  "job_type": "character_pack",
  "status": "preview_ready",
  "requested_by_token_id": "tok_123",
  "world_id": "world_123",
  "visual_identity_id": "vid_123",
  "input_payload": {},
  "result_asset_ids": ["asset_001", "asset_002"],
  "cost_estimate_usd": "0.0840",
  "actual_cost_usd": "0.0791"
}
```

## Style Profile

A `style_profile` defines visual styling independent of model/provider.

```json
{
  "id": "style_dark_cinematic",
  "name": "Dark cinematic realism",
  "style_mode": "preset",
  "positive_prompt": "cinematic realistic dramatic lighting",
  "negative_prompt": "low quality, blurry, distorted anatomy",
  "allowed_asset_types": ["character_portrait", "place_scene", "artifact"],
  "default_quality_tier": "standard"
}
```

## Provider Model

A `provider_model` defines one backend model route.

```json
{
  "id": "bfl_flux_2_klein",
  "provider_id": "bfl",
  "model_name": "flux-2-klein",
  "capabilities": ["text_to_image", "image_to_image", "upscale"],
  "supports_preview": true,
  "supports_high_res": true,
  "status": "active"
}
```

## Cost Event

Every provider call should create cost telemetry.

```json
{
  "id": "cost_123",
  "job_id": "job_123",
  "asset_id": "asset_001",
  "provider_id": "bfl",
  "model_id": "bfl_flux_2_klein",
  "operation": "generate_preview",
  "estimated_cost_usd": "0.0140",
  "actual_cost_usd": "0.0140",
  "duration_ms": 4100,
  "status": "success"
}
```
