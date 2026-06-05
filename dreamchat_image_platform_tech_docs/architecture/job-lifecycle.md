# Generation Job Lifecycle

## Purpose

Image generation must be asynchronous.

Generation can take seconds or fail due to provider capacity. The API should not block web requests until full image generation completes.

## Statuses

```txt
queued
running
preview_ready
completed
failed
cancelled
```

## Standard Flow

```txt
1. Client calls POST /v1/characters/{id}/generate-pack
2. API validates bearer token and scopes
3. API checks idempotency key
4. API checks existing reusable assets
5. API creates generation_job
6. API enqueues worker task
7. API returns 202 Accepted with job_id
8. Worker builds prompt package
9. Worker routes to provider
10. Worker stores preview assets
11. Job becomes preview_ready
12. Worker generates/upscales high-res assets
13. Worker stores final assets
14. Job becomes completed
```

## Job Transitions

```txt
queued -> running
running -> preview_ready
preview_ready -> completed
running -> failed
preview_ready -> failed
queued -> cancelled
running -> cancelled
```

## Preview-First Flow

The first usable output should be a low-res preview.

This allows the DreamChat UI to show something quickly while high-res finishes later.

## Retry Rules

Safe to retry:

- Provider timeout before provider job accepted
- Network failure before response
- 5xx provider error
- Temporary provider rate limit, after delay

Not safe to blindly retry:

- Provider accepted job but response was lost
- Provider returned content-policy failure
- Input validation failure
- Insufficient balance or unauthorized provider response

## Idempotency

Generation endpoints should accept:

```txt
Idempotency-Key: <client-generated-key>
```

The same key + token + endpoint + body hash must return the same job when possible.

## Job Polling

Clients can poll:

```txt
GET /v1/jobs/{job_id}
```

The response should include:

- status
- progress stage
- preview asset IDs if available
- final asset IDs if available
- errors if failed
- cost estimate and actual cost if available

## Future Webhooks

Later, the platform may emit webhooks:

```txt
generation_job.preview_ready
generation_job.completed
generation_job.failed
```

Not required for MVP.
