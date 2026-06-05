# ADR-007 — Use Provider Adapters

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

All image providers implement a common adapter interface.

## Consequences

Positive:

- This avoids vendor lock-in, enables fallback routing, supports benchmarking, and allows self-hosting later.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.
