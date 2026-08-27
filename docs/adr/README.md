# ADR index — image platform

Twenty decisions in two series. **Statuses below are read from each file's own `Status`, not
restated from memory** — if one disagrees with its file, the file wins and fixing this index is in
scope for whoever found it.

## Numbering

`workspace:ADR-W002` assigns one namespace per repo, because `ADR-P001` once meant two different
decisions in two repos.

| Namespace | Prefix | Home |
|---|---|---|
| World engine (frozen canon set) | `ADR-###` | `dreamchat-world-backend/docs/law/02_world_state_adrs.md` |
| World backend platform | `ADR-P###` | `dreamchat-world-backend/docs/law/adr/` |
| **Image platform (this repo)** | **`ADR-I###`**, plus the pre-existing plain `001`…`017` series | `docs/adr/` |
| Live frontend | `ADR-F###` | `dream-weaver-visuals/docs/adr/` |
| Workspace / cross-repo | `ADR-W###` | the workspace root's `docs/adr/` |

**Cross-repo citation form:** `<slug>:ADR-<id>` — so `image:ADR-016`, `image:ADR-I002`,
`backend:ADR-P021`, `workspace:ADR-W002`. Inside this repo the bare id is fine.

`ADR-I001`…`I003` were renamed from `ADR-P001`…`P003`; `ADR-P###` is the world backend's namespace.
Dated plans and specs under `docs/superpowers/` still say `ADR-P00N` — those are closed records of
what the id was at the time and are deliberately not rewritten. Live code, the OpenAPI specs, the
PRDs, `README.md`, `scripts/dev.sh` and `docker-compose.yml` all cite the new ids.

The plain `001`…`017` series keeps its numbers. Renumbering seventeen files with inbound references
from runbooks, PRDs, CI comments and other ADRs is real breakage risk for no benefit now that the
citation form disambiguates.

## The series

### Plain `001`…`017` — the founding architecture

| ADR | Status | Decision |
|---|---|---|
| [`001`](001-standalone-image-platform.md) | Accepted for initial implementation | The image platform is a standalone HTTP service with its own deployment, Postgres, Redis and S3. A web app is just one client. |
| [`002`](002-go-api-and-workers.md) | Accepted for initial implementation | Go for `cmd/api` and `cmd/worker`, one module. |
| [`003`](003-openapi-first.md) | Accepted for initial implementation | **`docs/api/openapi.yaml` is the single source of truth.** CI fails if the served `/openapi.json` diverges. |
| [`004`](004-bearer-token-auth.md) | Accepted for initial implementation | `Authorization: Bearer` with scoped keys, **AND-ed**. Public endpoints are marked `security: []`. |
| [`005`](005-store-only-hashed-tokens.md) | Accepted for initial implementation | Store prefix + HMAC-SHA256 under a server-side pepper. The raw token is shown once. |
| [`006`](006-async-generation-jobs.md) | Accepted for initial implementation | `202` + `job_id`, poll `GET /v1/jobs/{job_id}`. **Pull is the contract of record.** |
| [`007`](007-provider-adapters.md) | Accepted for initial implementation | One Go interface in `internal/providers`; no provider SDK imported anywhere else; a mock adapter is mandatory. |
| [`008`](008-asset-state-first.md) | Accepted for initial implementation | The unit of persistence is the visual asset, not the prompt. |
| [`009`](009-retrieval-before-generation.md) | Accepted for initial implementation | Four-tier match — exact → variant → fallback → generate — **before** creating a job. |
| [`010`](010-preview-first-delivery.md) | Accepted for initial implementation | Preview-first delivery where the provider supports it. |
| [`011`](011-s3-object-storage.md) | Accepted for initial implementation | Bytes in S3-compatible storage. **The path is not the source of truth** — `visual_assets.*_url` is. |
| [`012`](012-postgres-source-of-truth.md) | Accepted for initial implementation | Postgres is the metadata source of truth. |
| [`013`](013-redis-queue-mvp.md) | Accepted for initial implementation; NATS JetStream a documented future option | Redis + asynq for the MVP queue and short-lived state. |
| [`014`](014-standard-errors.md) | Accepted for initial implementation | RFC 7807 `application/problem+json`, with a seven-value normalized provider-error vocabulary. |
| [`015`](015-serve-api-docs.md) | Accepted for initial implementation | Serve `GET /openapi.json` and `GET /docs` from the service. |
| [`016`](016-provider-capability-reconciliation.md) | Accepted — implements PRD 03 §8, extends `007` | Reconcile configured routes against **adapter-reported** capabilities and **fail closed**. |
| [`017`](017-reference-conditioned-provider.md) | Accepted — implements the recurring-character path `016` anticipated | fal.ai FLUX.1 Kontext as the one real reference-conditioned provider, wired through the pack path. |

### `ADR-I###` — this repo's later decisions

| ADR | Status | Decision |
|---|---|---|
| [`ADR-I001`](ADR-I001-migration-tooling.md) | accepted (2026-06-18) | goose as a Go **library** via `internal/migrate` over an embedded migration FS, with an irreversible baseline floor. Renamed from `ADR-P001`. |
| [`ADR-I002`](ADR-I002-governance-and-cost-routing.md) | accepted (2026-06-24) | Seven sub-decisions around `POST /v1/generations`: an additive chokepoint; a governance gate that **verifies and stores but never interprets** (`backend:D-3` / `E-1`); intent-driven cost routing with an identity capability floor; reservation prices the existing basis; `501` for `transform_only` and `grid.enabled`; prompt assembly **after** the gate; RLS cross-tenant enforcement asserted in CI and in Go. Renamed from `ADR-P002`. |
| [`ADR-I003`](ADR-I003-cost-optimization-strategy.md) | **Proposed** — the only one not accepted | Anchor amortization as the structural cost model ("derive, don't regenerate") plus deferred levers. Carries a provenance caveat: its cost figures are **provisional**; the decided content is the structural model and the deferral triggers, not the numbers. Renamed from `ADR-P003`. |

## Not ADRs, but binding

`DECISIONS.md` is a fourth governance form and carries **no** ADR ids: a table keyed by concern that
locks the stack and the canonical environment-variable names ("There is **one** name per concern. No
aliases."). It locks a stack rather than recording a choice between alternatives, which is why it is
not numbered. `IMPLEMENTATION_STATUS.md` owns phase sequencing. Both are named in `AGENTS.md`'s STOP
list.
