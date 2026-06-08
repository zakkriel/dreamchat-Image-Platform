# Confidence to Implement — `generation_job.schema.json`

**Score: 90/100 — Very High**

Status enum matches the job lifecycle in `docs/architecture/job-lifecycle.md` exactly. `preview_asset_ids` + `final_asset_ids` arrays correctly capture the two-tier delivery shape. `retryable: bool` is a nice telemetry hook. `cost_estimate_usd` and `actual_cost_usd` typed as `string` is unusual but correct for fixed-decimal money in JSON.

Why not higher: no separate `progress_stage` or `current_attempt` field — useful for long-running pack jobs where the client wants to show "3 of 7 previews ready". Could be added as an optional `progress` object.

(Sibling file because JSON Schema files can't carry comment blocks — see `frustration_log.md` entry 8.)
