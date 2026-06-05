# ADR-011 — Use S3-Compatible Object Storage

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Image binaries are stored in S3-compatible object storage. Postgres stores metadata only.

## Consequences

Positive:

- Object storage is cheaper and more scalable for binary media.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.
