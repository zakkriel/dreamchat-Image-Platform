# ADR-P002: Governance verification + intent-driven cost routing

- **Status:** accepted (2026-06-24)
- **Platform ADR** (numbered `ADR-P###` per rule D-5).

## Context

Chunk 2 added the combined-contract endpoint `POST /v1/generations` and with it
two architectural decisions that cut across every generation request:

1. **When and how the governance gate runs** — where it sits in the request
   pipeline, what it checks, what it leaves to later stages, and how enforcement
   is phased in before the signing infrastructure exists.
2. **How cost routing maps intent to provider** — cheapest vs. premium,
   what the capability floor is, and what happens when the caller pins a specific
   provider.

These decisions interact (the gate must clear before cost reservation; the floor
must hold regardless of the caller's pin) and both carry follow-up obligations
that must be tracked explicitly.

## Decision 1 — Additive endpoint; resource-scoped endpoints unchanged

`POST /v1/generations` (`internal/http/router.go` line 304) is the single
chokepoint for the combined governance + subject + render + grid + lazy +
idempotency contract. The handler is `internal/http/handlers/generations_handler.go`.
The request body maps to the OpenAPI `GenerationRequest` schema.

The existing resource-scoped endpoints (`/v1/artifacts/{id}/generate`,
`/v1/packs/{id}/generate`, `/v1/style-preview`) are unchanged this chunk.
**See Follow-up 1.**

## Decision 2 — Governance gate: verify and store, never interpret (D-3 / E-1)

### What the gate checks

`internal/governance/governance.go` runs a structural envelope check before
route resolution and cost reservation (handler step 8b):

- **Presence** — `governance.content_class` is required; the handler rejects 422
  if absent (line 128 of the handler). The value is stored and logged as an
  opaque string — the gate never parses or branches on its content.
- **Freshness** — envelope `issued_at` must be within `GOVERNANCE_MAX_AGE`
  (default 24 h; `internal/config/config.go` line 128).
- **Authorized-by allowlist** — `authorized_by` is checked against
  `GOVERNANCE_AUTHORIZED_ISSUERS` (config line 129).
- **Signature** — **STUBBED** via `StubSignatureVerifier` in
  `internal/governance/signature.go`. The stub unconditionally passes every
  signature (`TODO(core-signing): replace with real canonicalization + signature
  verification`). **See Follow-up 4.**

### Prompt isolation

`SubjectMeta` passed to the gate carries only ID references (`IdentityID`,
`PackID`, `StyleID`). The identity's `DisplayName` is fetched at handler step 7
but is **not** passed into `SubjectMeta` (which carries only ID refs); the gate
at step 8b therefore structurally cannot access the identity's display
name/traits or any prompt text. Prompt content is assembled later, at step 11,
strictly after the gate has cleared.

### Enforcement mode

`GOVERNANCE_ENFORCEMENT=log_only|enforce`; default `log_only`
(`internal/config/config.go` line 127).

- `log_only` — gate records what *would* be blocked via an audit event then
  proceeds.
- `enforce` — gate blocks the request.

Running `enforce` while the stub is wired triggers a startup `WARN` log
(Task 9 wiring). Operators who set `GOVERNANCE_ENFORCEMENT=enforce` before core
ships real signing will see this warning on every startup.

### Audit events

`internal/audit/audit.go` (`Emit`) writes to `audit_events`. The governance
package defines two event-type constants
(`internal/governance/governance.go` lines 22–23):

- `media.eligibility_verified` — gate passed (real or log-only).
- `media.eligibility_blocked` — gate would block (log-only mode) or did block
  (enforce mode).

## Decision 3 — Intent-driven cost routing with identity capability floor

### Floor rule

`internal/http/handlers/generations_handler.go` (line 264) sets the capability
floor to `identity_capable` for every `POST /v1/generations` request because
`subject.identity_id` is required by the endpoint. The floor is passed as
`ResolveRequest.RequiredCapability`.

`internal/providers/routing/routing.go` (line 360) treats `RequiredCapability`
as a hierarchy floor when `Intent` is non-empty: a route qualifies only if its
capability satisfies the floor via `providers.CapabilitySatisfies`. This means
an identity-capable floor cannot be silently satisfied by a scene-only route —
the resolver fails closed (`ErrNoRoute`) rather than downgrading.

### Intent → ranking

- `draft` → ascending active unit price (cheapest capability-valid route; nil
  price sorts last).
- `commit` → descending quality-tier rank (`high > standard > draft`), then
  identity-axis-capable routes first (routing.go lines 454–482).

### Provider pin

`render.provider_id`, if supplied, pins resolution to a single provider
(routing.go lines 300–318). The pin is applied *after* the floor filter —
a pinned provider that does not satisfy `identity_capable` returns
`ErrRequestedProviderUnavailable`, not a silent downgrade.

## Decision 4 — Reservation prices the existing basis, not max_megapixels

`max_megapixels` is validated, clamped, and persisted this chunk. It is **not**
used as the reservation basis. Cost reservation uses the existing price-row
basis (unit price × existing quantity) so that reserved ≠ actual drift cannot
arise from unimplemented MP-scaling logic. **See Follow-up 2.**

## Decision 5 — 501 for deferred behaviors

`transform_only=true` and `grid.enabled=true` are rejected with HTTP 501 (Not
Implemented) at handler step 5 (`generations_handler.go` lines 168, 172), before
identity fetch, to avoid a wasted DB round-trip on requests that cannot be
fulfilled this chunk.

`derive_from`, a `transform` block with `transform_only=false`, and `lazy=true`
are accepted, stored in the job payload, and treated as a normal single-image
generation this chunk (the stored fields are available for Chunk 5/7). **See
Follow-up 3.**

## Decision 6 — Prompt assembly after the gate

The generation prompt is derived from `identity.DisplayName` (fetched at handler
step 7), seeded into `payload["description"]` at step 11
(`generations_handler.go` line 314). This mirrors the existing pack flow's
`identity.DisplayName → payload["display_name"]` path. The gate at step 8b
has already cleared by the time any identity description text exists in the
request context.

## Decision 7 — RLS cross-tenant enforcement asserted in CI and Go

`internal/jobs/rls_integration_test.go` (`TestRLSGovernanceColumnsCrossTenantBlocked`,
line 534) asserts that the `image_platform_api` role cannot read another
tenant's governance columns. `.github/workflows/ci.yml` (the `migrations` job,
"assert RLS blocks cross-tenant access to governance-column rows" step, line 398)
covers the same invariant at the SQL level. Both run in CI on every push.

## Follow-ups (required; not deferred indefinitely)

1. **GOVERNANCE HOLE — legacy resource-scoped endpoints are ungoverned.**
   `/v1/artifacts/{id}/generate`, `/v1/packs/{id}/generate`, and
   `/v1/style-preview` bypass the gate entirely. A later chunk MUST route them
   through the governance gate or retire them. Until then, governance coverage
   is incomplete for all generation paths except `POST /v1/generations`.

2. **Worker pixel-level MP enforcement.** `max_megapixels` is persisted but the
   worker does not enforce it at image-generation time. A later chunk must add
   worker-side clamping and connect it to the reservation basis.

3. **`derive_from` and non-only `transform` still generate untransformed.**
   Requests carrying these fields produce a standard single-image generation
   this chunk. Real derive / transform execution is deferred to Chunk 5/7.

4. **Real signature crypto + flip default to `enforce`.** Once core ships
   signing, replace `StubSignatureVerifier` with a real implementation
   (`TODO(core-signing)` in `internal/governance/signature.go`) and flip the
   default `GOVERNANCE_ENFORCEMENT` to `enforce`.

## Consequences

- Every `POST /v1/generations` request passes through the governance gate before
  cost reservation, satisfying D-3/E-1 for that endpoint.
- Governance audit trail (`media.eligibility_verified` / `media.eligibility_blocked`)
  is live from day one of the endpoint, even in `log_only` mode.
- Identity-capable floor on all `POST /v1/generations` requests prevents
  silent capability downgrade; operators who pin a non-identity-capable provider
  receive a 422, not a silently degraded result.
- `enforce` mode with the stub seam causes a startup WARN — acceptable until
  core delivers signing; avoids shipping a silent lie about enforcement state.
- Four follow-up obligations are recorded above; none are open-ended deferrals.
