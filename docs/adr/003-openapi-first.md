# ADR-003 — Use OpenAPI-first (contract-first) API design

## Status

Accepted for initial implementation.

## Context

The Image Platform has multiple clients (web app, admin tooling, benchmark runner, future creator tools) and is built by humans and code-generation agents (Superpowers) in parallel. The API surface is the boundary; if it drifts between docs and code, every client has to chase it.

OpenAPI tooling supports two flows: write the spec first and generate code (contract-first), or write code with annotations and generate the spec (code-first). They produce different failure modes.

## Decision

`docs/api/openapi.yaml` is the **single source of truth**. Handler interfaces and DTOs are generated from it (via `oapi-codegen` or `ogen`), and CI fails if the served `/openapi.json` does not match the file in `docs/`. Client SDKs (web app, admin tooling, scripts) are also generated from the same spec.

## Alternatives considered

- **Code-first OpenAPI generation** (e.g. annotate Go handlers with `swaggo`). Faster to start, no separate spec file to maintain. But the spec drifts behind code as developers ship features and forget annotations, clients can't be built before code is written, and code-gen agents can't operate on a contract that doesn't exist yet.
- **No spec at all** (free-form REST + handwritten docs). Lowest upfront cost, highest long-term cost: clients reimplement parsing, error shapes diverge, and migrations become guesswork.
- **gRPC + protobuf**. Stronger typing, native codegen for many languages. But unfriendly to browser clients without a gateway, less inspectable in curl/Postman, and the team's web app is HTTP+JSON.
- **GraphQL**. Better for client-driven field selection, worse for our shape (we mostly do RPC-style generation requests and resource fetches, not graph traversal).

## Tradeoffs

- **+** Codegen agents (Superpowers) can implement against a known contract.
- **+** Web-app and admin-tool SDKs are generated; type mismatches surface at build time.
- **+** Swagger UI / Redoc at `/docs` is free and always matches the running version (ADR-015).
- **+** CI can lint the spec (`spectral`) and validate response payloads.
- **−** Spec edits become the first step of any API change; PRs touch more files.
- **−** Free-form fields (`additionalProperties: true`) silently bypass validation; discipline required.
- **−** Two-step iteration: edit spec → regenerate → implement.

## Consequences

- `docs/api/openapi.yaml` v0.2.0 is now the canonical contract (see CONFIDENCE_SCORES.md and the deprecated `prds/schemas/image_platform_openapi_draft.yaml`).
- Codegen output lives in a generated package and is never edited by hand (per `docs/guidelines/go-service-guidelines.md`).
- CI step: `openapi-spec-validator docs/api/openapi.yaml` plus a contract test that diffs the served `/openapi.json` against the file.

## Revisit when

- A second API style is genuinely needed (e.g. streaming preview frames via WebSocket — that endpoint may need its own contract artifact alongside OpenAPI).
- The OpenAPI 3.1 ecosystem changes meaningfully (e.g. JSON Schema 2020-12 support in tooling we depend on).

---

## Confidence to Implement

**Score: 95/100 — Very High**

`oapi-codegen` or `ogen` generate Go types + handler interfaces from the spec, Swagger UI/Redoc serve `/docs`, CI can fail on schema drift via `spectral` or similar. The spec already exists at `docs/api/openapi.yaml` and is self-consistent. Drift with the PRD draft yaml has been resolved (see CONFIDENCE_SCORES.md changelog).
