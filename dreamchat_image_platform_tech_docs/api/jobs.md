# Jobs API

## Job Statuses

```txt
queued
running
preview_ready
completed
failed
cancelled
```

## Get Job

```txt
GET /v1/jobs/{job_id}
```

Required scope:

```txt
jobs:read
```

Example response:

```json
{
  "id": "job_123",
  "status": "preview_ready",
  "job_type": "character_pack",
  "visual_identity_id": "vid_123",
  "preview_asset_ids": ["asset_001", "asset_002"],
  "final_asset_ids": [],
  "cost_estimate_usd": "0.0840",
  "actual_cost_usd": null,
  "created_at": "2026-06-05T12:00:00Z",
  "updated_at": "2026-06-05T12:00:08Z"
}
```

## Failure Response

```json
{
  "id": "job_123",
  "status": "failed",
  "error_code": "provider_timeout",
  "error_message": "The selected image provider timed out.",
  "retryable": true
}
```
