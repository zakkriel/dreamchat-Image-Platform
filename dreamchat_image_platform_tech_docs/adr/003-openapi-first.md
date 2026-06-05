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
