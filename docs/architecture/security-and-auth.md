# Security and Authentication Architecture

## Authentication Model

All `/v1` endpoints require bearer token authentication unless explicitly marked public.

Header:

```txt
Authorization: Bearer dci_live_xxxxx
```

## Token Types

Token prefixes:

```txt
dci_test_   test environment token
dci_live_   live environment token
dci_dev_    local development token
```

## Token Storage

Store only token hashes.

Raw tokens are shown only once at creation.

Recommended hash:

- Argon2id or bcrypt for user-created API keys
- HMAC-SHA256 with server-side pepper is acceptable for high-throughput API token lookup if using prefix lookup first

## Token Lookup

Store a short non-secret prefix for lookup:

```txt
prefix = first 12-16 chars after token family prefix
```

Flow:

```txt
1. Extract bearer token
2. Parse token prefix
3. Lookup token record by prefix
4. Verify hash
5. Check status active
6. Check expiry
7. Check required scope
8. Apply rate limit
9. Continue request
```

## Scopes

Initial scopes:

```txt
images:read
images:write
jobs:read
styles:read
styles:write
models:read
admin:tokens
admin:costs
admin:providers
```

## Logging Rules

Never log:

- raw bearer tokens
- provider API keys
- signed URLs with long expiry
- raw secrets

Be careful with prompts. Prompts may contain user/private content. Store only what is needed and protect access.

## Rate Limiting

Apply rate limits by token ID.

Recommended dimensions:

- requests per minute
- generation jobs per hour
- estimated cost per day
- concurrent running jobs

## Admin Endpoints

Admin endpoints require `admin:*` scopes.

Examples:

```txt
POST /v1/admin/tokens
GET /v1/admin/cost-events
GET /v1/admin/provider-status
```

## Docs Endpoint

For local/dev:

```txt
GET /docs
GET /openapi.json
```

For production, docs should be protected or intentionally public with no sensitive examples.
