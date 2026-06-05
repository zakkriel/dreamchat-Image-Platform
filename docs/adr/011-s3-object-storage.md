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

## Confidence to Implement

**Score: 95/100 — Very High**

`aws-sdk-go-v2` (or `minio-go`) plus a thin storage interface (`PutObject`, `GetObject`, `PresignURL`) covers everything needed. The key layout in PRD 05 is sensible. MinIO works locally and in CI. The only thinking left is signed-URL TTL choice and whether to fan out three derivatives (thumbnail/preview/final) at write time or generate on demand — both are easy.
