# Idempotency

## Purpose

Generation endpoints are expensive and should not accidentally create duplicate jobs when clients retry requests.

## Header

```txt
Idempotency-Key: <unique-client-generated-key>
```

## Recommended Keys

For initial character pack:

```txt
world_{world_id}_character_{character_id}_initial_pack_v1
```

For initial place pack:

```txt
world_{world_id}_place_{place_id}_initial_pack_v1
```

For explicit user regeneration:

```txt
regen_{asset_id}_{uuid}
```

## Server Behavior

The idempotency record should include:

- token ID
- endpoint
- request body hash
- created job ID
- expiry

If the same key is reused with the same body, return the original job.

If the same key is reused with a different body, return `409 Conflict`.

## Expiry

Recommended retention:

```txt
24 hours for generation requests
7 days for long-running batch jobs if needed later
```

---

## Confidence to Implement

**Score: 85/100 — High**

The pattern is well known: hash the request body, store `(token_id, key, body_hash, job_id)` under a Redis or Postgres key with TTL. The `409 Conflict on same key + different body` rule is correct. Subtracting points because there are subtle race conditions on first-write between two concurrent retries arriving before the row exists — needs an `INSERT ... ON CONFLICT DO NOTHING RETURNING` pattern or Redis `SETNX`. That's standard but worth implementing carefully.
