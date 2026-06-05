# ADR-003 — Use OpenAPI-first API Design

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

The OpenAPI contract is the source of truth for request and response shapes.

## Consequences

Positive:

- This avoids API drift, enables Swagger docs, client generation, CI validation, and clearer implementation by Superpowers.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 95/100 — Very High**

`oapi-codegen` or `ogen` generate Go types + handler interfaces from the spec, Swagger UI/Redoc serve `/docs`, CI can fail on schema drift via `spectral` or similar. The spec already exists at `docs/api/openapi.yaml` and is self-consistent. Only friction: the PRD draft yaml diverges from the docs yaml (see `frustration_log.md` entry 6), and the team must pick one as source of truth — likely the docs one — before generating code.
