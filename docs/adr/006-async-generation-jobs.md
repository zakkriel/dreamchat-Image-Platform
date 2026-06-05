# ADR-006 — Use Async Jobs for Generation

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Generation requests create jobs and return immediately.

## Consequences

Positive:

- Image generation is slow, variable, retryable, and should not block HTTP requests.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 90/100 — Very High**

202 Accepted + `job_id` + poll-or-webhook is a standard pattern. The job state machine in `docs/architecture/job-lifecycle.md` is explicit and finite. Redis-based queue (ADR-013) is enough for MVP. Mild uncertainty only around retry policy edge cases (provider accepted but response lost = ambiguous) and the future webhook surface — both are noted as MVP-deferable in the supporting docs.
