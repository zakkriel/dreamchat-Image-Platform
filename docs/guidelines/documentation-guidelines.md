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
