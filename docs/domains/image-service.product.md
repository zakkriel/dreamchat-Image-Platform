# image-service · product

**Repo:** `dreamchat-Image-Platform` · **Cluster:** IP-1 · The service ·
**Parent bounded context:** Image Platform

This file holds what the domain *means* — its job, its language, its product rules, what is
deliberately not built. `image-service.tech.md` holds how it is built; `image-service.seams.md`
holds what crosses its boundary.

---

## What this domain is for

**One job: pictures — generation, storage, delivery, cost, and provider routing.**

A standalone service that is handed a subject description and a style and never learns what a
world *is* (`D-3`). It keeps recurring subjects recognizable across renders, retrieves before it
generates, accounts for every cent, and refuses to let a wrong result look like a right one. It
carries `world_id` as an ordinary scoping column — the boundary is about *truth*, not
identifiers (`docs/README.md` "What This Is Not" lists what it must never own).

## Ubiquitous language

| Term | Means, precisely |
|---|---|
| **Visual identity** | The durable description of a recurring subject: canonical traits, anchors, consistency keys. The unit of persistence is the asset, not the prompt (`image:ADR-008`). |
| **Version** | A meaningful visual *state* change — a scar, a building burned. Strict both directions: old state never substitutes for new, nor new for old (`docs/architecture/asset-versioning.md`). |
| **Variant** | A different view of the same state — expression, angle, weather. Substitution is governed by the compatibility matrix, never by similarity. |
| **Anchor** | The canonical reference image a reference-conditioned provider conditions on. Reference provenance, not a display asset; attaching one does not bump the version (`image:ADR-017`). |
| **Asset** | One stored image with provenance: provider, model, prompt hash, seed, variant, version. IDs are durable; download URLs are presigned per read and expire. |
| **Generation job** | The async unit: `202` + `job_id`, polled to terminal (`image:ADR-006`). |
| **Tenant** | The isolation boundary, resolved server-side from the bearer token — never a request field. Today tenant = the deployment. |
| **Match type** | Retrieval's four-tier verdict: `exact_match \| compatible_match \| preview_fallback \| generated_required` (`image:ADR-009`; API field `match_type`, DB column `cache_result`, CHECK in `migrations/0001_initial.sql`). |

## What this domain is not

- **Not world truth.** Canon, perception, simulation are the World Engine's (`D-3`).
- **Not content policy.** Classification happens in the core *before* any generation request
  (`E-1`, `E-2`); this service verifies the envelope arrived, fresh, from an allowed issuer —
  never what it means (`image:ADR-I002`; see `seams.md`).
- **Not the consuming seam.** How the engine calls this service — reconcilers, slots, polling —
  is IP-2 · art-and-image-seam, a backend package.
- **Not presentation.** The frontend stores nothing durable and receives ids through the backend.

## Product rules — decisions already made

Ids only; the law lives where the id resolves.

| Id | What it settles | What breaks if you ignore it |
|---|---|---|
| `D-3` | Separate service; never owns world truth; receives only classified/authorized requests. | Teaching the platform world state re-merges what was deliberately split. |
| `E-1`, `E-2` | Classification (private/shareable/public/…) happens upstream, in the core. | A platform that branches on `content_class` is deciding policy it must only store. |
| `image:ADR-008` | Asset-state-first: identity → intent → prompt package → job → asset. | Prompt-first makes recurring characters, retrieval, and versioning impossible. |
| `image:ADR-009` | Retrieval before generation, deterministic via the compatibility matrix. Its §2 overrides everything: *"Fallback must never visually contradict known world state."* | A cheap substitute that lies about world state — a burned letter shown intact. |
| `image:ADR-I002` | `POST /v1/generations` is the governance + cost chokepoint; the gate verifies and stores, never interprets. | An ungoverned door, or a gate that reads prompts. |
| — (`docs/architecture/cost-control.md` §7, REJECTED item) | No silent quality downgrade under budget pressure; denial is an explicit `422 budget_exceeded`. | A degraded render indistinguishable from a good one. |
| — (Wave 1 D3, `docs/superpowers/plans/2026-08-07-cost-optimization-waves.md`) | Provider content-policy rejections surface verbatim as `provider_content_rejected` — never hidden, never fallback-walked. | Hidden suppression or hidden circumvention; both are non-censorship violations. |
| Reuse is the default (`docs/api/openapi.yaml` `info.description`; quickstart §0) | Repeating a request is a zero-cost cache hit returning the same `asset_id` (one home: `tech.md` §The write path). | Consumers "refreshing" portraits burn money and drift identity. |

## What is deliberately not built here

Each absence has a stated reason. Building one is reopening a decision.

- **No asset-upload endpoint.** The first `ready` asset must be *generated*; fixtures are a
  dev-only seeding script, *"not a product image upload feature"*
  (`docs/runbooks/playground-fixtures.md`).
- **No `force_regenerate` on `POST /v1/generations`** — and `DisallowUnknownFields` makes sending
  one a hard `422`. Stale-asset recovery is archive-then-miss, an operator action, not a client
  knob (`docs/api/integration-quickstart.md` §1.1.1).
- **No real signature crypto.** One home: `seams.md` §The signing contract.
- **No Wave 4 amortization.** Sprite-sheet batching, anchor-derive defaults, lazy finalization are
  specification-only behind five numeric release gates; *"no production claim of sprite-sheet
  savings is made"* (`IMPLEMENTATION_STATUS.md`).
- **No list-jobs endpoint, no `since` cursor, no ETag.** Every poll is one request per job —
  which is why bounded jittered backoff is a client *requirement* (quickstart §4).
- **No admin audit-events write endpoint.** Served admin writes audit in-transaction; MANUAL
  actions go in the incident ticket — *"runbooks must not imply one exists"* (`DECISIONS.md`).
- **No Python/LangGraph.** The platform is a Go asset/job/storage/retrieval service; Python only
  ever for future self-hosted inference or ML evaluation (`docs/guidelines/implementation-guidelines.md`).
