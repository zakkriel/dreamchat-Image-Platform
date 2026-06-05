# ADR-015 — Serve API docs from the service

## Status

Accepted for initial implementation.

## Context

The platform is OpenAPI-first (ADR-003), and the team wants every environment (local, staging, production) to be self-documenting against the version of the code actually running there. The risk being prevented is "the docs say one thing, the service does another" — which happens whenever docs and code are deployed separately.

## Decision

The Go service serves the canonical OpenAPI document at `GET /openapi.json` and an interactive viewer (Swagger UI or Redoc) at `GET /docs`. Both are unauthenticated in local/dev. In production they are either gated behind admin auth or deliberately exposed with no sensitive examples.

## Alternatives considered

- **External static docs site** (e.g. Docusaurus / Mintlify hosted separately, built from the spec in CI). Better long-form docs, prettier landing pages. Doesn't solve "the running service might not match" because the static site is built from `main`, not from what's deployed. Worth adding *in addition* later for the public-facing developer portal.
- **CI-generated downloadable artifact** (publish `openapi.yaml` as a release asset). Useful for SDK pipelines, not useful for "I want to poke at the API right now."
- **No served docs; Postman collection only.** Loses in-browser exploration; the collection drifts unless built from spec.
- **Generated docs at deploy time pushed to a CDN.** Solves the drift problem partially (docs match deploy) but adds CDN ops and TTL invalidation complications. The single-binary approach has no TTL question.

## Tradeoffs

- **+** Docs always match the running version (the spec served is the spec the service uses for routing/validation).
- **+** Local dev gets Swagger UI for free at `:8080/docs`.
- **+** Public docs hostable behind a feature flag with no extra deploy step.
- **+** Single binary stays self-contained — no separate "docs bundle" to keep in sync.
- **−** Embedded static assets (Swagger UI's JS/CSS) bloat the binary by a few hundred KB.
- **−** Production exposure must be gated; accidental public exposure leaks internal endpoint shapes (admin paths, capability hints).
- **−** Doesn't replace long-form documentation (guides, tutorials, conceptual docs) — that still needs a separate site eventually.

## Consequences

- `/openapi.json` returns the canonical contract (the same file that codegen consumed) as JSON.
- `/docs` returns Swagger UI (or Redoc) HTML pointing at `/openapi.json`.
- Production config gates both behind a flag (e.g. `EXPOSE_API_DOCS=false`) or behind admin-scope auth.
- A CI contract test diffs the served `/openapi.json` against `docs/api/openapi.yaml` to detect drift.

## Revisit when

- A public developer portal becomes a product (then the served `/openapi.json` becomes the data source for a richer external docs site, and `/docs` may be retired from production).
- Embedded asset size becomes a meaningful fraction of binary size (unlikely; current Swagger UI is small).
- Multiple API versions are served simultaneously; we'll need `/v1/openapi.json` and `/v2/openapi.json` patterns.

---

## Confidence to Implement

**Score: 95/100 — Very High**

Embedding `swagger-ui` or `redoc` as a static asset and serving `/openapi.json` from the same Go binary takes ~50 LoC. The only production concern (gating docs behind admin auth or a flag) is a single conditional in the router. Done.
