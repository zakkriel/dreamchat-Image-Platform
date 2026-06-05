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
