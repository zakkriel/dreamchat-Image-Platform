# Testing Strategy

## Test Levels

### Unit Tests

Test:

- token validation
- scope checks
- prompt compiler normalization
- provider router decisions
- retrieval-before-generation logic
- asset versioning rules
- job state transitions

### Integration Tests

Test with Postgres + Redis + fake S3/minio:

- create visual identity
- generate job with mock provider
- job status updates
- asset metadata storage
- asset search
- idempotency replay

### Contract Tests

Validate API responses against OpenAPI schema.

### Provider Adapter Tests

Each provider adapter should have:

- mocked HTTP tests
- error mapping tests
- timeout tests
- authentication failure tests

### Security Tests

Test:

- missing bearer token
- invalid bearer token
- revoked token
- expired token
- missing scope
- rate limit exceeded
- raw token not logged

## CI Requirements

CI should run:

```txt
go test ./...
openapi schema validation
migration validation
basic linting
```

## Golden Tests

Prompt compiler should use golden tests.

Same structured input should produce the same normalized prompt package and prompt hash.

---

## Confidence to Implement

**Score: 85/100 — High**

Unit + integration (with testcontainers Postgres/Redis/MinIO) + OpenAPI contract tests + golden tests for the prompt compiler is the right ladder. Provider adapter tests against `httptest` servers cover the most error-prone code. Security test list is complete. The only thing I'd add explicitly is **fuzz tests for the auth/idempotency middleware** (random bearer tokens, replay attacks) and a **load test** for the job queue before phase-3 web app integration. Not blockers, just standard for an API that handles money-shaped requests.
