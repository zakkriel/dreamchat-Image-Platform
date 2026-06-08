# API Error Format

## Standard Error Shape

Use RFC 7807-style problem details.

```json
{
  "type": "https://docs.dreamchat.ai/errors/invalid-style-profile",
  "title": "Invalid style profile",
  "status": 400,
  "detail": "The requested style profile does not exist.",
  "request_id": "req_123"
}
```

## Common Errors

### 400 Bad Request

Invalid input, missing fields, invalid enum, unsupported style profile.

### 401 Unauthorized

Missing or invalid bearer token.

### 403 Forbidden

Valid token but missing required scope.

### 404 Not Found

Resource not found.

### 409 Conflict

Version conflict, duplicate identity, incompatible idempotency replay.

### 422 Unprocessable Entity

Input is syntactically valid but semantically invalid.

### 429 Too Many Requests

Rate limit exceeded.

### 500 Internal Server Error

Unexpected server error.

### 502 Bad Gateway

Provider failed unexpectedly.

### 503 Service Unavailable

Provider unavailable, queue overloaded, or service is degraded.

## Provider Error Mapping

Provider errors should be normalized:

```txt
provider_timeout
provider_rate_limited
provider_content_rejected
provider_auth_failed
provider_capacity_error
provider_invalid_request
provider_unknown_error
```

---

## Confidence to Implement

**Score: 92/100 — Very High**

Problem Details + the listed HTTP status mapping is standard, and the provider-error normalization vocabulary is complete. The remaining work is purely mechanical: a Go errors package with typed errors per code, an HTTP middleware that converts them to Problem Details, and per-adapter mapping tables. No design decisions left.
