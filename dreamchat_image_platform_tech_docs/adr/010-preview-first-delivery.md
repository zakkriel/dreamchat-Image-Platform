# ADR-010 — Use Preview-First Delivery

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

The platform produces and serves low-res previews before high-res finals when possible.

## Consequences

Positive:

- The web app should feel responsive even when high-quality generation takes longer.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.
