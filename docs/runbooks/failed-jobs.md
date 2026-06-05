# Runbook — Failed Jobs

## Common Causes

```txt
provider_timeout
provider_rate_limited
provider_content_rejected
provider_auth_failed
provider_capacity_error
storage_upload_failed
invalid_prompt_package
unknown_error
```

## Investigation

1. Get job by ID.
2. Check request ID.
3. Check provider request ID.
4. Check cost events.
5. Check worker logs.
6. Check asset records created before failure.

## Retry Rules

Retry if:

- provider timeout
- provider capacity error
- storage temporary failure
- network failure

Do not retry automatically if:

- invalid request
- missing style profile
- provider content rejected
- auth failed

## Manual Repair

For partial success:

- keep completed preview assets
- mark final step failed
- allow regeneration for missing high-res variants

---

## Confidence to Implement

**Score: 78/100 — High**

The retry policy (retry timeouts and capacity errors, do not retry content-policy or auth) is correct. The investigation steps assume the request_id + job_id + provider request_id are all linked in logs (covered by observability spec) — that part is implementable. The "manual repair" for partial success requires admin endpoints (`POST /v1/admin/jobs/{id}/retry` style) that aren't in the API yet. Same gap as provider-failure runbook.
