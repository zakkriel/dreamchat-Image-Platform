# Wave 2 — Governance completion + interface hygiene

> **Status:** implemented (this commit).
> **Date:** 2026-08-07
> Program: `docs/superpowers/plans/2026-08-07-cost-optimization-waves.md`
> (Wave 1 spec: `2026-08-07-cost-optimization-wave1-design.md`). OpenAPI
> `0.12.0 → 0.13.0` (additive), migrations head `17 → 18`.

## 1. Problem

After Wave 1 the platform still had four interface/governance gaps:

1. **Legacy generation endpoints bypassed governance** (ADR-P002 Follow-up 1):
   only `POST /v1/generations` ran the media-eligibility gate; artifact, pack,
   and style-preview generation had no gate at all, so "no generation without
   governance" was not actually true.
2. **`enforce` was a silent lie in production:** enforce mode with the stub
   signature verifier only logged a WARN, asserting an integrity guarantee
   that never runs.
3. **`any_existing` was reachable by ordinary callers** despite being
   documented admin/debug-only — it deliberately bypasses the compatibility
   matrix and can return (and, via pack creation, persist) matrix-invalid
   assets.
4. **Idempotency carriers diverged:** docs said the `Idempotency-Key` header
   is canonical; `/v1/generations` required a body field and rejected
   header-only requests.

Plus one route-table gap: fal had no explicit `identity_capable` route —
single-image identity resolution worked only implicitly through the
capability hierarchy (`pack_capable ⊇ identity_capable`).

## 2. Decisions

### D1 — One gate, phased onto the legacy endpoints (additive)

`handlers.GovernanceGate` (internal/http/handlers/governance_gate.go) is the
shared gate for artifact, pack, and style-preview generation, wired from the
same deps as the combined endpoint (`router.governanceGateFromDeps`). The
envelope is an OPTIONAL new field on the four legacy request schemas
(v0.13.0): an absent envelope flows through the verifier as a zero envelope
and fails its presence checks, so:

- `log_only` (default): verdict audited (`media.eligibility_blocked`,
  `envelope_present:false`), request proceeds — **existing callers are
  unaffected**.
- `enforce`: missing/invalid envelope → `403 governance_blocked` before
  reuse, route resolution, or cost reservation (no side effects).
- Envelope present + verified: persisted onto the job (envelope JSONB +
  scalar columns + `governance_verified_at`) exactly like `/v1/generations`.

Gate order on every endpoint: replay → **gate** → reuse → resolve → reserve.
The gate sees only the envelope (`SubjectMeta` empty on legacy contracts) —
never prompt text; `content_class` stays opaque (D-3/E-1). This is the same
phased-enforcement model Chunk 2 shipped, now covering every generation door.

### D2 — enforce + stub is refused at startup in live

`governance.EnforceWithStubError`: `ENVIRONMENT=live` +
`GOVERNANCE_ENFORCEMENT=enforce` + `StubSignatureVerifier` → `cmd/api`
refuses to start. Dev/test keep the WARN-only posture so enforcement flows
can be exercised before core ships real signing (which remains blocked on
the cross-system canonicalization contract, `TODO(core-signing)` — still not
invented here).

### D3 — `any_existing` requires `admin:read`

`requireAdminForAnyExisting` gates `fallback_policy: any_existing` on
artifact generation, pack generation, and `POST /v1/assets/search`: 403
`forbidden` without the `admin:read` scope. It remains a debug facility; the
compatibility matrix stays the only path to substitution for normal callers.

### D4 — Idempotency-Key header is canonical on `/v1/generations`

`GenerationRequest.idempotency_key` is no longer required (schema-relaxing,
backward compatible). Exactly one key must arrive via header (canonical) or
body; neither → 422; both-present mismatch → 422 (unchanged). The effective
key feeds replay, reuse cache-hit, and job creation identically. Legacy
endpoints are unchanged (header optional ⇒ non-idempotent create).

### D5 — Explicit fal identity route (migration 0018)

Seed-only DML adding `route_fal_text_to_image_identity`
(`identity_capable`, reuses the fal model + existing text_to_image price).
ADR-017's revisit-condition ("a dedicated single-image identity path is
added → seed an identity_capable fal route and wire references") fired when
Wave 1 wired single-image references. CI head assertions bumped 17 → 18.

### D6 — Documentation reconciliation

- `docs/architecture/cost-control.md` §4.1/§5: `503 provider_unpriced` /
  `429 budget_exceeded` corrected to the implemented `422 no_price_entry` /
  `422 budget_exceeded`.
- `README.md`: Phase-0 skeleton wording replaced with current status; real
  BFL/fal adapters and the synthetic-provider policy documented.
- `DECISIONS.md`: canonical env-var list gains `POSTGRES_SYSTEM_DSN`,
  `S3_PRESIGN_TTL`, `FAL_KEY`, `ALLOW_SYNTHETIC_PROVIDERS`, `GOVERNANCE_*`;
  `IMAGE_PROVIDER` is `mock | bfl | fal`.
- `docs/adr/014-standard-errors.md`: implementation note reconciling the
  claimed RFC 7807 field set with the shipped `{code, message, request_id}`
  body.
- `docs/api/idempotency.md`: canonical-header contract.

## 3. Invariants (tested)

- Enforce + no envelope → 403 on artifact, pack, and style preview; zero
  service calls; blocked verdict audited
  (`wave2_governance_gate_test.go`).
- log_only + no envelope → 202, verdict audited, nothing stamped.
- Valid envelope under enforce → 202 with envelope JSON, scalars, and
  `governance_verified_at` persisted on the create params; verified verdict
  audited.
- `any_existing` without `admin:read` → 403 on generate and search; with the
  scope → proceeds.
- Header-only idempotency on `/v1/generations` → 202, key threaded to the
  service, replay across carriers returns the same job; neither carrier →
  422 (`generations_handler_test.go`).
- live + enforce + stub → startup error; dev/test → warning only
  (`enforce_stub_error_test.go`).
- Full unit + `-tags=integration` suites pass; migration 0018 applies and
  rolls back (goose Down) with CI head assertions updated.

## 4. Explicitly NOT in this wave

Real signature crypto (cross-system contract with core), retiring the legacy
endpoints (they are now governed, so retirement is product timing, not a
safety hole), Wave 3 measurement/cost-accounting, Wave 4 amortization.
