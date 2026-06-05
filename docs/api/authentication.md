# API Authentication

## Authentication Method

The API uses bearer tokens.

```txt
Authorization: Bearer dci_test_xxxxxxxxx
Authorization: Bearer dci_live_xxxxxxxxx
```

## Token Environments

```txt
dci_test_ = test token
dci_live_ = live token
dci_dev_ = local development token
```

## Scopes

Endpoints require scopes.

Initial scope list:

```txt
images:read
images:write
jobs:read
styles:read
styles:write
models:read

admin:tokens       # issue, list, revoke API tokens
admin:providers    # view, disable, re-enable provider models
admin:routes       # view, disable, re-enable provider routes (PLANNED)
admin:jobs         # view, retry, cancel generation jobs (PLANNED)
admin:costs        # view/edit price book and cost budgets, view cost events (PLANNED)
```

Admin scopes mapped to runbooks:

- `admin:providers` → provider-failure runbook (`docs/runbooks/provider-failure.md`)
- `admin:routes` → narrower route-level disabling (provider-failure runbook)
- `admin:jobs` → failed-jobs runbook (`docs/runbooks/failed-jobs.md`)
- `admin:costs` → cost-spike runbook (`docs/runbooks/cost-spike.md`)

Endpoints requiring `admin:routes`, `admin:jobs`, and `admin:costs` are
**PLANNED — required admin surface for implementation, not yet served.**
See `docs/architecture/admin-control-surface.md` for the planned surface
and implementation order.

## Example Request

```bash
curl -X POST "https://image-api.dreamchat.ai/v1/assets/search" \
  -H "Authorization: Bearer dci_test_xxxxx" \
  -H "Content-Type: application/json" \
  -d '{"owner_type":"character","owner_id":"char_123"}'
```

## Tenant Inference

For normal API calls, `tenant_id` is **inferred from the bearer token**. Every API token belongs to exactly one tenant, and the auth middleware resolves token → tenant on every request before any handler runs.

Clients must **not** send `tenant_id` in request bodies for generation, asset search, style, or job endpoints. Doing so should be rejected as `400 Bad Request` or silently ignored, depending on policy.

Admin endpoints (`admin:*` scopes) may accept `tenant_id` as an **optional filter** for cross-tenant operations.

## Token Storage

Tokens are shown only once.

The service stores only token hashes.

## Token Revocation

Admin users can revoke tokens.

A revoked token must immediately stop working.

---

## Confidence to Implement

**Score: 90/100 — Very High**

Token prefix conventions (`dci_test_`, `dci_live_`, `dci_dev_`), the scope list, and the bearer header are all concrete. "Revoked token immediately stops working" implies no in-memory caching of token records or a cache invalidation step — that's a small but real correctness detail that should be called out in the auth middleware. Otherwise standard.
