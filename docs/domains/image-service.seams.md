# image-service · seams

**Repo:** `dreamchat-Image-Platform` · **Cluster:** IP-1 · The service ·
**Parent bounded context:** Image Platform

A seam belongs to two domains, so each row declares an expectation: one side owns a fact, the
other consumes it and must not re-derive or re-decide it. The mirror rows live in
`dreamchat-world-backend/docs/domains/art-and-image-seam.seams.md` (IP-2) and
`content-governance.seams.md` (CG-1); differences between the mirrors are for the moderator, not
for either writer to resolve alone.

---

## What this domain consumes

| Direction | Domain | What crosses | The expectation |
|---|---|---|---|
| consumes | **Content Governance** (CG-1, backend) | the seven-field governance envelope | Classification and authorization happen in the core *before* any generation request (`E-1`, `E-2`). This service's gate verifies presence, freshness, and issuer, and stores `content_class` opaque — it never parses or branches on it (`image:ADR-I002`; `internal/governance/governance.go:2`). The values currently arriving in `content_class` are this platform's own `AssetType` strings — an unowned vocabulary, recorded, not a negotiated enum (CG-1's row states the same). Under `log_only` a `202` is not envelope acceptance — the caller must not treat it as governance proof. |
| consumes | **art-and-image-seam** (IP-2, backend) | the generation request: subject descriptor, style, anchor, envelope | The platform is handed a subject description and a style and never learns what a world is (`D-3`). It must not re-derive perception, canon, or entity state; `world_id` is a scoping column it stores and never interprets. `subject.anchor_asset_id` folds into the reuse key, so the caller sends it or gets the portrait drawn from a replaced anchor. |

## What this domain provides

| Direction | Domain | What crosses | The expectation |
|---|---|---|---|
| provides | **art-and-image-seam** (IP-2, backend) | `job_id`, then job state and asset ids by polling | **Pulled, never pushed** (`image:ADR-006`): `GET /v1/jobs/{id}` then `/assets` is authoritative; webhooks are a latency hint, and a consumer ignoring them entirely is still correct. IDs are durable; download URLs expire — never persisted. Reuse is the default (one home: `tech.md` §The write path). The consumer owns the `identity_id → asset_id` mapping (assets from `/v1/generations` carry NULL `visual_identity_id`) and handles the two 429s differently — `Retry-After` authoritative on `rate_limit_exceeded`, deliberately absent on `concurrent_jobs_exceeded`. The consumer never re-derives routing, cost, or storage; this side never pushes and never invents a readiness channel. Contract of record: `docs/api/integration-quickstart.md`, load-bearing over the OpenAPI — the backend's client says so (`dreamchat-world-backend/core/api/imageclient.go:22-24`). |
| provides | **Compendium & Play UX** (via the backend, never directly) | asset ids only | The frontend stores nothing durable and treats every URL as expiring (quickstart §5's suggested split). No frontend calls this service; a direct call would bypass both the governance seam and `D-3`. |

## The seams that do not exist

- **The signing contract.** `StubSignatureVerifier` passes every signature because the
  canonicalization is core's to design (`TODO(core-signing)`); this repo will not invent the wire
  format. Until it ships, `enforce` + stub is refused at startup in live. An agent asked to "add
  real signing" here is being asked to build the wrong side of the seam.
- **World-state hints for the retrieval safety filter.** The matrix's world-state override
  (`passesWorldStateSafetyFilter`) is a documented placeholder returning `true`; the candidates
  named are `scene_mood` and `recent_canonical_events`
  (`docs/architecture/variant-compatibility-matrix.md` §2/§11). This is the one place the platform
  would be handed something world-shaped, so it touches the `D-3` boundary — a founder question,
  not a local decision.
- **Webhooks as readiness.** Deliberately not a seam: at-least-once, no dead-letter queue, some
  transitions never emit, bodies carry ids only. Adopt as an optimization on top of polling,
  never as the mechanism.
- **A second product as a client.** `image:ADR-001` names it a revisit trigger (multi-tenant work
  beyond token-per-tenant), not a current surface. Today tenant = the deployment.
