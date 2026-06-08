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

---

## Confidence to Implement

**Score: 88/100 — High**

Listing rows from a `provider_models` table is trivial. The interesting part — the router that consumes capability metadata to pick a backend — lives behind ADR-007 and `docs/architecture/provider-adapters.md`, both of which sketch but don't fully specify the policy. Sufficient for a first-pass router with simple rules (e.g. "prefer fast/cheap when latency_tier=fast"); refinement comes from the benchmark corpus.
