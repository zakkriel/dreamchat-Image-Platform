# Models API

## List Models

```txt
GET /v1/models
```

Required scope:

```txt
models:read
```

Example response:

```json
{
  "models": [
    {
      "id": "bfl_flux_2_klein",
      "provider_id": "bfl",
      "display_name": "FLUX.2 Klein",
      "capabilities": ["text_to_image", "image_to_image", "upscale"],
      "supports_preview": true,
      "supports_high_res": true,
      "status": "active"
    }
  ]
}
```

## Model Selection

Clients should not need to select a model directly in normal use.

They should provide:

- asset type
- style profile
- quality tier
- latency tier

The provider router chooses the backend.
