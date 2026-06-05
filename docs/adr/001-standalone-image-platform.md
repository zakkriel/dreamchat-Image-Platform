# ADR-001 — Image Platform is a standalone API

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

The DreamChat image system is implemented as a separate API service, not embedded directly into the web app.

## Consequences

Positive:

- This enables independent testing, clean contracts, separate scaling, provider switching, and future API reuse.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.
