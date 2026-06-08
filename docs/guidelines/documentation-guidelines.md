# Documentation Guidelines

## Documentation is Part of the Product

The Image Platform is API-first. Documentation must be maintained as part of the implementation.

## Required Docs

Each endpoint must document:

- purpose
- auth scope
- request body
- response body
- error cases
- idempotency behavior
- example curl

## ADRs

Every major architecture decision needs an ADR.

ADR format:

```txt
Title
Status
Context
Decision
Consequences
Notes
```

## Swagger / OpenAPI

OpenAPI must be served from the service.

Recommended endpoints:

```txt
GET /openapi.json
GET /docs
```

## Runbooks

Any production-critical workflow needs a runbook.

Initial runbooks:

- local development
- provider failure
- failed jobs
- token rotation
- cost spike

---

## Confidence to Implement

**Score: 95/100 — Very High**

Documentation is a policy doc, not a buildable thing — "implementing" it means following the checklist (each endpoint has purpose/scope/body/error/idempotency/curl example, each major decision has an ADR, OpenAPI is served, runbooks exist). The current `docs/api/*.md` files already follow this template well.
