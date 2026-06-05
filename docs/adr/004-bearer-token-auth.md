# ADR-004 — Use bearer tokens with scoped API keys

## Status

Accepted for initial implementation.

## Context

The Image Platform starts with a small set of internal clients (DreamChat web app, admin tooling, benchmark runners) and needs to add external/creator/partner clients later. Every client needs: identity, scoped permissions, immediate revocation when a key leaks, per-token rate limits, and an audit trail.

Auth on a per-request basis happens before any handler logic, so the chosen scheme dictates the shape of every request, the middleware stack, and the operational tooling for issuing/revoking credentials.

## Decision

All non-public endpoints require `Authorization: Bearer <token>`. Tokens carry explicit scopes (`images:read`, `images:write`, `jobs:read`, `styles:read`, `styles:write`, `models:read`, `admin:tokens`, `admin:providers`, `admin:routes`, `admin:jobs`, `admin:costs`). Token records live in Postgres and are looked up on every request (ADR-005 covers storage). Public endpoints (`/health`, `/openapi.json`, `/docs`) are explicitly marked with empty `security: []`.

## Alternatives considered

- **OAuth2 / OIDC (full standard).** Right answer for external developer ecosystems. Premature for an internal-first service with three clients and no user-facing OAuth consent flow. We adopt it later if/when external developers self-serve.
- **JWT (stateless tokens).** No DB lookup per request; fast. But immediate revocation requires either a denylist (now we have DB lookup again) or short TTL + refresh tokens (more moving parts). We want immediate revocation as a baseline.
- **mTLS** between clients and the platform. Strongest auth, but every client (including a CLI script and the browser-based admin UI) needs a managed cert. Operationally too heavy.
- **Session cookies.** Tightly coupled to the browser, doesn't fit CLI/script/benchmark-runner clients.
- **HMAC-signed requests (Stripe-style: `Stripe-Signature` over the body).** Strong replay protection. But every client must implement the canonicalization + signing, which we don't want to push on partners or scripts.

## Tradeoffs

- **+** Simple to implement; every HTTP client supports `Authorization` headers.
- **+** Per-call DB lookup enables immediate revocation (ADR-005).
- **+** Scopes provide fine-grained access without per-endpoint key issuance.
- **+** Token prefix gives a non-secret lookup key; hashed body protects against DB leak.
- **−** One DB hit per request (mitigated by a short in-process cache with TTL ≤ revocation latency target).
- **−** No first-class delegation; admin actions need their own admin-scoped tokens.
- **−** Bearer tokens are bearer credentials; loss = full access until revoked.

## Consequences

- Auth middleware shape: extract token → parse prefix → DB lookup by prefix → constant-time hash compare → check status/expiry → load scopes → check required scope for endpoint → attach `tenant_id` and `token_id` to request context → continue.
- `tenant_id` is always resolved from the token; clients must not send it (see `docs/api/authentication.md`).
- Scope checks declared in OpenAPI via `security: [BearerAuth: [scope]]` and enforced by middleware that reads the generated route's required scopes.

## Revisit when

- External developers need self-serve OAuth (move to OIDC + machine-to-machine clients alongside bearer for internal).
- Rate of token issuance exceeds what manual admin endpoints can support (need self-serve key management UI).
- Cross-service S2S call patterns proliferate (consider SPIFFE/SPIRE or mTLS for service identity, keep bearer for human-issued keys).

---

## Confidence to Implement

**Score: 90/100 — Very High**

`Authorization: Bearer <token>` + scope checks is a standard middleware pattern. The token prefix/hash lookup flow is described in `docs/architecture/security-and-auth.md` and is straightforward (prefix lookup → constant-time hash compare → scope set check). The scope list is enumerated. Minor uncertainty around scope-to-endpoint mapping conflicts in edge cases (e.g. does `POST /v1/assets/{id}/regenerate` need `images:write` or a new `images:regenerate`?) — choosable.
