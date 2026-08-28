# image-service · tech

**Repo:** `dreamchat-Image-Platform` · **Cluster:** IP-1 · The service ·
**Parent bounded context:** Image Platform

This file holds how the domain is built — architecture, the write and read paths, validation,
traps. `image-service.product.md` holds what it means; `image-service.seams.md` holds what
crosses its boundary.

Line numbers below are as of 2026-08-28; re-locate by grep before relying on one.

---

## Architecture

- **Two binaries, one image.** `cmd/api` and `cmd/worker`, selected by the Docker `BINARY` build
  arg. Deployed images carry no Go toolchain — `cmd/migrate`/`cmd/seed-token` run locally or as
  one-off builds (`docs/runbooks/railway-real-image-smoke-test.md`).
- **Postgres is the metadata source of truth** (`image:ADR-012`) — migration head 19, goose with
  an **irreversible baseline floor at 11** (`image:ADR-I001`). Bytes in S3 (`image:ADR-011`);
  queue and short-lived state in Redis via asynq (`image:ADR-013`). Durable provenance is the
  `s3://` columns on `visual_assets`; download URLs are minted per read, never persisted.
- **RLS on everything tenant-shaped** (migration `0009`): ENABLE + FORCE, deny-by-default text
  predicate on `app.current_tenant`. `image_platform_api` (no BYPASSRLS) serves requests;
  `image_platform_system` (BYPASSRLS) serves auth lookup, the worker, and admin. The worker is
  trusted by construction — documented, accepted residue (`IMPLEMENTATION_STATUS.md`).
- **Code layout:** `internal/{auth,assets,identities,jobs,providers,styles,storage,imaging,
  ratelimit,governance,telemetry,db,http,config,migrate}`. Provider SDK code never leaves
  `internal/providers/*` (`image:ADR-007`). `docs/api/openapi.yaml` is the contract's source of
  truth, with a CI-enforced byte-identical mirror at `api/openapi.yaml` (`image:ADR-003`).

## The write path

`POST /v1/generations`, in the chokepoint order that Chunk 2 declared non-negotiable:

```
decode (DisallowUnknownFields) → 501 check (transform_only, grid) → replay
→ governance gate → reuse lookup → resolve route → reserve cost → enqueue
```

- **The gate verifies and stores, never interprets** — the package comment at
  `internal/governance/governance.go:2` is the rule. `SubjectMeta` carries IDs only; prompt
  content is assembled *after* the gate, from the validated identity.
- **Reuse runs after the gate on purpose** — eligibility is audited even for reused output. A hit
  is a zero-cost completed job returning the same `asset_id`. The reuse key
  (`internal/assets/generation_hash.go:21`) **deliberately excludes provider/model** — see Traps.
- **Routes resolve once, at creation**, persisted in the job payload; the worker never
  re-resolves. `routing.Reconcile` (`internal/providers/routing/reconcile.go:75`) disables
  capability-lying routes at boot; the resolver re-checks last, fail-closed (`image:ADR-016`,
  including its 2026-08-10 amendment).
- **Cost is reserved before enqueue**, so denials are synchronous `422`s — never a silent
  downgrade. The concurrency cap (`internal/auth/principal.go:10`, `DefaultMaxConcurrentJobs =
  5`) is checked under a per-token advisory lock inside the create transaction; idempotency
  replay always wins over the cap.
- The worker generates, enforces `max_megapixels` on **decoded** bytes (never clamps), uploads
  three tiers, and finalizes cost from provider-reported actuals where they exist (Wave 3,
  `docs/superpowers/specs/2026-08-08-wave3-cost-truth-design.md`).

## The read path

`GET /v1/jobs/{job_id}` is authoritative; `GET /v1/jobs/{job_id}/assets` returns delivered assets
in deterministic order and is deliberately not restricted to `status='ready'` — archived assets
stay displayable. `GET /v1/assets/{id}` mints presigned per-tier URLs (`thumb/low/high`) at read
time from derived keys. Cross-tenant access is `404 not_found`, never `403` — absence is the
answer. Webhooks exist but are a latency hint only (`image:ADR-006` in-file note).

## Technical decisions already made

| Id | What it settles | What breaks if you ignore it |
|---|---|---|
| `image:ADR-006` | Async jobs; pull is authoritative, push a hint. | A consumer whose readiness depends on webhooks breaks on the first lost event. |
| `image:ADR-007` | One provider interface, total isolation, mandatory mock. | Provider churn leaks into every client. |
| `image:ADR-014` | Error contract: compact `{code, message, request_id}` — the in-file note, not the 7807 headline. Clients key on `code`. | Parsing `message` or expecting `detail`. |
| `image:ADR-016` + amendment | Capability floor; reconcile against adapter-reported capabilities; fail closed. `ALLOW_SYNTHETIC_PROVIDERS=false` everywhere, every axis. | Placeholder grids served as real art — observed live pre-amendment. |
| `image:ADR-017` | fal Kontext multi is the reference-conditioned provider; anchors presigned in the worker; fails closed without them (`missing_reference_assets`). | Prompt-only retries do not solve recurring identity. |
| `image:ADR-I001` | goose; baseline 1–11 irreversible; expand → backfill → contract; explicit-column queries. | sqlc emits per-query `*Row` types and the build breaks. |
| `image:ADR-I002` | The chokepoint and the gate (see write path). | Re-opening what a later wave already closed. |
| `workspace:ADR-W002` | Two legitimate ADR series here: plain `001–017` + `ADR-I###`. Cite `image:ADR-016` vs `image:ADR-I002`; never a bare `ADR-P00N` in this repo. | The dated plans/specs still cite `ADR-P00N`; those are closed records, not current ids. |
| `DECISIONS.md` | The locked stack and the canonical env-var names — *"one name per concern. No aliases."* | `TOKEN_PEPPER` and `EXPOSE_API_DOCS` in older ADRs are the superseded names. |
| `IMPLEMENTATION_STATUS.md` | The only authoritative phase sequencing; one phase = one PR. | `prds/06`/`prds/07` numbering is explicitly excluded. |

### What you may not decide alone

1. **The signature wire format.** Core-owned (`TODO(core-signing)`); this repo must not invent it.
2. **Flipping `GOVERNANCE_ENFORCEMENT=enforce`.** Gated on real crypto plus zero
   `media.eligibility_blocked` audit rows for the traffic — dry-run by reading the audit table.
3. **Bumping `provider_models.capabilities`** without PRD 03 §8.5 acceptance evidence — the exact
   silent-drift failure `image:ADR-016` exists to prevent.
4. **Tenant granularity** (one tenant many worlds vs one per world) — posed, unanswered
   (`docs/api/integration-quickstart.md` §7).
5. **Accepting `image:ADR-I003`.** It is Proposed — the only non-accepted decision of the twenty —
   and its Wave 4 is specification-only. Not a settled row anywhere.

## Validation for this domain

- `go test ./...`; integration suites behind `//go:build integration` with `POSTGRES_DSN`
  (+ `POSTGRES_API_DSN` for the two-pool RLS harness that proves cross-tenant *blocking*).
- `make generate && git diff --exit-code` — CI fails on uncommitted generated output.
- CI migration gates: head/table-count asserts, `up → down-to 11 → up` round trip,
  below-baseline refusal, RLS forced on the Chunk 1 tables.
- **What counts as evidence here:** this domain fails *green* — a mock placeholder returns a
  normal `202` and a normal `ready` asset. Provenance is the check: `GET /v1/assets/{id}` must
  stamp the provider you meant (`mock` = placeholder); the boot log's `provider capability
  readiness` line is the one-line verdict. Never trust the status code.
- **What counts as ceremony here:** stub-backed creator tests. The cache-hit `500` shipped
  because every test either used a stub creator or set `FallbackPolicy` explicitly
  (`IMPLEMENTATION_STATUS.md`, live-handshake defect 1); webhook emission had zero coverage in
  `internal/jobs` and three silent-loss gaps survived. A guard you have not watched go red is
  not a guard.

## Traps, with receipts

| The trap | The receipt |
|---|---|
| **Route priority is lower-is-preferred; mock is 100, real providers 200.** With both configured, mock wins and every portrait is a placeholder — nothing in the response says so. | `migrations/0001_initial.sql:134` (*"lower = preferred"*); `0006_bfl_provider_seed.sql:14`; quickstart §1.1.1 — "the single most common 'why are my images grey squares' cause". |
| **The reuse key excludes the provider**, so flipping to real providers does not retire existing placeholders — stale grids are served as zero-cost hits forever. Recovery is archive-then-miss, in the same window as the flip. | `internal/assets/generation_hash.go:21`; quickstart §1.1.1. |
| **`Idempotency-Key` is bound to the whole-body hash, including `issued_at`.** Rebuilding the envelope per retry yields `409 idempotency_conflict`. Pin one `issued_at` per logical request. | quickstart §3 (verified live); `internal/jobs/service.go` (grep `sha256`). |
| **Under `log_only`, a `202` does not mean the envelope was accepted** — unknown issuer still audits `media.eligibility_blocked` and becomes `403` under `enforce`. | quickstart §2; the live-handshake found an empty allowlist looking healthy. |
| **Half the `/v1/admin/*` paths are documented-but-unserved.** The served/planned split lives in one place; a spec entry with no handler is the workspace's worst-graded contract failure. | `docs/architecture/admin-control-surface.md` header; `../docs/00_workspace/failure-log.md` row 11 (`/v1/admin/cost-events`). |
| **The ADRs' headline decisions are not always current** — corrections live in in-file notes, amendments, or later phase entries. Three docs still describe the pre-amendment synthetic-provider policy. | S12a contradiction table #1 (`digest/S12a_image_platform_decisions_and_architecture.md`); `docs/adr/016-…md:73` (the amendment). Read every ADR to the bottom. |
| **Both provider keys go to API *and* worker** — only the worker calls providers, so a key set on one process resolves a route that cannot execute. | `docs/runbooks/local-development.md`. |

## Open questions

1. **Tenant granularity** — see "may not decide alone" 4.
2. **Enforcement-flip sequencing** — who declares allowlist → `issued_at` → crypto → flip done
   (`IMPLEMENTATION_STATUS.md` "Before production" 1–2)?
3. **`image:ADR-I003`** — accept as-is with the provisional-numbers caveat, or only with Wave 4
   evidence?
4. **`style_mode` vocabulary** — `styles.md` says `open_prompt|preset|creator_pack`; the OpenAPI
   and a verified run say `open_prompt|preset_style|creator_style|provider_native` (S12b
   contradiction #1). A ruling picks one.
5. **The three admin runbooks mark served endpoints PLANNED** (cost writes, job retry/cancel) —
   an operator reaching for MANUAL SQL loses the in-transaction audit row. Correct now, or is
   the surface still moving?
6. **The engine pins scene routing via `DREAMCHAT_IMAGE_SCENE_PROVIDER`** (backend
   `core/api/imageclient.go:64`) — a disclosed consumer-side deviation whose real fix is
   platform-side routing. Recorded from the IP-2 seam; not resolved here.
