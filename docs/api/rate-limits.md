# Rate Limits

## Purpose

Rate limits protect cost, providers, and service stability.

## Rate Limit Dimensions

Apply limits by token ID.

Recommended MVP dimensions:

```txt
requests_per_minute
generation_jobs_per_hour
concurrent_running_jobs
estimated_cost_per_day
```

## Headers

Responses may include:

```txt
X-RateLimit-Limit
X-RateLimit-Remaining
X-RateLimit-Reset
```

## Error

When exceeded:

```json
{
  "type": "https://docs.dreamchat.ai/errors/rate-limit-exceeded",
  "title": "Rate limit exceeded",
  "status": 429,
  "detail": "This token has exceeded the allowed generation job rate.",
  "request_id": "req_123"
}
```

## Cost Limits

Generation jobs should be denied or queued when the client exceeds cost budget.

This is more important than simple request count because one request may generate many images.

---

## Confidence to Implement

**Score: 75/100 — High**

The first three dimensions (requests/min, jobs/hour, concurrent jobs) are implementable with Redis token-bucket or sliding-window counters keyed by `token_id`. `X-RateLimit-*` headers are conventional. The fourth dimension, **`estimated_cost_per_day`**, requires the platform to *predict* a job's cost before it runs and atomically reserve against a daily budget — that's a non-trivial sub-system (price book per model × per asset type, pack-size estimation, post-hoc reconciliation when actual cost differs). Implementable, but a meaningful chunk of work that's mentioned but not specified.
