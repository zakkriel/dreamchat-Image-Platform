# ADR-014 — Use Standardized Error Responses

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Use a single RFC 7807-style error shape across the API.

## Consequences

Positive:

- This improves client handling, documentation, and debugging.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 95/100 — Very High**

RFC 7807 problem-details with a `request_id` is well-supported in Go via custom error types + an HTTP middleware that maps domain errors to status + JSON. `docs/api/errors.md` already enumerates the error codes and provider-error normalization (`provider_timeout`, `provider_rate_limited`, etc.). Nothing here is novel.
