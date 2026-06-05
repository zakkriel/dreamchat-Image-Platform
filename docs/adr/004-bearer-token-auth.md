# ADR-004 — Use Bearer Tokens with Scoped API Keys

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

All non-public endpoints require Authorization: Bearer <token>. Tokens have explicit scopes.

## Consequences

Positive:

- Internal and future external clients need controlled access, revocation, auditability, and rate limiting.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 90/100 — Very High**

`Authorization: Bearer <token>` + scope checks is a standard middleware pattern. The token prefix/hash lookup flow is described in `docs/architecture/security-and-auth.md` and is straightforward (prefix lookup → constant-time hash compare → scope set check). The scope list (`images:read`, `images:write`, `jobs:read`, `styles:*`, `models:read`, `admin:*`) is already enumerated. Minor uncertainty around scope-to-endpoint mapping conflicts in edge cases (e.g. does `POST /v1/assets/{id}/regenerate` need `images:write` or a new `images:regenerate`?) — choosable.
