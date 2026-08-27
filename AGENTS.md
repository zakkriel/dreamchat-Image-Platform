# DreamChat Image Platform — AGENTS.md

> **Workspace harness:** `../AGENTS.md` + `../docs/00_workspace/` govern anything crossing a repo
> boundary. Read it before cross-repo work; this file governs everything inside this repo.

> **Agent-agnostic entry point.** This file is the canonical instruction set for *any* coding agent
> working in this repo. Tool-specific files (`CLAUDE.md`, `.claude/`) are one-line pointers here and
> nothing else — backend rule **D-10**.

This service owns **pictures**: generation, storage, delivery, cost, and provider routing. It owns
nothing about a world. It is handed a subject description and a style, and `docs/README.md` lists what
it must never own — canonical story state, NPC memory, backstage simulation, relationship logic,
in-world time, narration. That boundary is `backend:D-3`: the Image Platform is a separate service
that **never owns world truth** and receives only classified, authorized generation requests. The
world backend calls this service; nothing here calls back into a world.

## STOP — the four things that are not optional

1. **`DECISIONS.md`** — the locked stack and, more importantly, **§Canonical environment variables**.
   It says: *"There is **one** name per concern. No aliases."* Introducing a second name for a thing
   that already has one is the violation, and it is the most common one. `IMAGE_PROVIDER` is the
   single provider switch; there is no `PROVIDER_DEFAULT`.
2. **`IMPLEMENTATION_STATUS.md`** — the **only** authoritative phase sequencing. Its own closing note:
   *"Phase numbers here are the **only** authoritative sequencing. Each phase is a separate PR. Do not
   compress phases into one."* It overrides the roadmaps in `prds/06` and `prds/07`, which use
   different numbering and must not be used for ordering.
3. **`docs/adr/`** — the decisions, indexed in `docs/adr/README.md`. The ones most often violated:
   **`003`** (`docs/api/openapi.yaml` is the single source of truth), **`004`** (bearer tokens,
   AND-ed scopes), **`009`** (retrieval before generation — four-tier match *before* creating a job),
   **`016`** (reconcile against adapter-**reported** capabilities and **fail closed**), and
   **`ADR-I002`** (`POST /v1/generations` is the governance + cost chokepoint, and the gate
   **verifies and stores but never interprets**).
4. **`docs/api/integration-quickstart.md`** — the contract of record for the world backend. Every
   request and response shape in it was executed against a real stack. The backend's client says so
   outright: `dreamchat-world-backend/core/api/imageclient.go:18-20` records that it was written
   against **that document, not the OpenAPI file**. So the quickstart is load-bearing in a way the
   spec is not: breaking a shape the spec still permits will break a live consumer.

### Pre-flight — run it; do not assume

| Check | Why it is here |
|---|---|
| Did I run `make generate`? | CI job `go` runs `make generate` then **`git diff --exit-code`**. Uncommitted `sqlc`/`oapi-codegen` output fails the build. |
| Did I touch an OpenAPI spec? Then touch **both**. | `docs/api/openapi.yaml` is ADR-`003`'s source of truth, but `Makefile` `generate` feeds `oapi-codegen` from **`api/openapi.yaml`** (the `//go:embed` mirror). CI job `openapi` validates both and then runs `diff -q` between them — that diff is the only thing making the split safe. |
| Did I add a migration? | CI job `migrations` asserts ~18 named SQL facts, including that the down-guard **refuses to cross below the baseline** and that RLS is enabled **and forced** on the Chunk 1 tables under the non-superuser `image_platform_api` role. `ADR-I001`'s baseline floor is irreversible on purpose. |
| Did I add a path to the OpenAPI spec? Is a handler mounted? | About half the `/v1/admin/*` paths in `api/openapi.yaml` are documented-but-unserved; `docs/architecture/admin-control-surface.md` is the served/planned split. A spec entry with no handler is a lie in the contract: the 2026-08-09 round close found `/v1/admin/cost-events` declared as served with nothing behind it and graded it *"contract claiming a nonexistent endpoint — **worst**"* (`../docs/00_workspace/evidence/ROUND-CLOSING-REPORT-2026-08-09.md` §4). Nothing gates this. |
| Did I add an env var? | `DECISIONS.md` §Canonical environment variables owns the name. One name per concern. Check before inventing. |
| Did I change a shape another repo reads? | Then it is a cross-repo round: `../docs/00_workspace/round-protocol.md`. The backend's client is hand-written against the quickstart, so it will not fail to compile — it will fail in production. |
| Do my tests fail if I revert the fix? | Nothing forces the revert. Across this workspace 40 of 70 mutation probes survived a fully green run (`../docs/00_workspace/evidence/QA-SPAN-2026-08-11.md`). A guard you have not watched go red is not a guard. |

## Standing answers — do not ask, do not re-derive

All of these are pinned by `docs/api/integration-quickstart.md` or `DECISIONS.md`, and every one has
cost somebody time.

- **Route priority is lower-is-preferred.** Mock is 100, bfl and fal are 200, so an **unpinned**
  request resolves to the **mock**. A "why is my image a mosaic" question is almost always this.
- **`ALLOW_SYNTHETIC_PROVIDERS` is `false` everywhere by default** — including local.
- **The reuse key deliberately excludes the provider.** It is `tenant_id + prompt_hash + asset_type +
  variant_key + status='ready'` (`internal/assets/generation_hash.go`), so flipping providers does
  **not** retire existing placeholders.
- **`POST /v1/generations` enforces a hard `identity_capable` floor.** Stock config returns `422
  route_capability_mismatch` — that is `016` working, not a bug. It decodes with
  `DisallowUnknownFields` and has **no** `force_regenerate`.
- **`Idempotency-Key` is bound to a hash of the whole body, including `issued_at`.** Pin one
  `issued_at` per logical request and store it with the key, or you get `409 idempotency_conflict`.
- **Under `GOVERNANCE_ENFORCEMENT=log_only`, a `202` does not mean the envelope was accepted.**
  `StubSignatureVerifier` currently passes **every** signature (`TODO(core-signing)`), so the
  prescribed sentinel is `"signature": "stub-unsigned-v1"`. Before enforcement ever flips, a real
  signing contract is required or every generation `403`s at once.
- **`tenant_id` is derived server-side from the token and must never appear in a request body.**
  Cross-tenant access returns **`404`**, never `403` — the absence is the answer.
- **The request-rate limiter fails OPEN if Redis is down;** the Postgres-backed
  `max_concurrent_jobs = 5` cap always holds. A denied request still increments the counter.
- **A portrait carries its anchor** (`subject.anchor_asset_id`) and `/v1/generations` has no prompt of
  its own — the anchor **is** the description. Omit it and a re-anchored character is served the
  portrait drawn from the anchor you just replaced.
- **Pull is the contract of record** (`006`). Webhooks are a latency hint; a consumer that ignores
  them entirely is still correct.
- **Do not start from the model provider** (`docs/README.md`). Start from the platform contract.

## Ports

This repo's, all offset to avoid the world backend's `8080`/`5432`:

| Port | What |
|---|---|
| `8081` | API — published from in-container `8080` (`APP_PORT`) |
| `5433` | Postgres |
| `6379` | Redis (asynq) |
| `9000` / `9001` | MinIO / MinIO console |
| `5174` | vite playground — **dev-only, never deployed** |

`5174` was originally chosen because `5173` was taken by `dreamchat-frontend`. **That repo is archived
and `5173` is retired with it** (`workspace:ADR-W003`); the live frontend is `dream-weaver-visuals` on
**`5273`**. 5174 stays, because the playground and the live frontend still must run concurrently.

## Process

- **Bring-up is `make start`** (`scripts/dev.sh`): compose infra → a tunnel *only* when a real
  provider key is present (fal fetches reference images from its own servers, so a localhost
  presigned URL fails `file_download_error`) → goose migrate → token seed → **`go run` api and worker
  on the host**, not in docker, because a code change then costs a ~5s restart instead of a ~60s image
  rebuild. It stops the compose `api`/`worker` containers first: two copies double-consume the asynq
  queue and fight for the port.
- Tests: `go test ./...`. Integration tests are behind `//go:build integration` — `go test
  -tags=integration ./...`, skipped when `POSTGRES_DSN` is unset.
- **One phase = one PR** (`IMPLEMENTATION_STATUS.md`). Do not compress phases.
- Deployed images contain a **single selected binary and no Go toolchain**, so `cmd/migrate` and
  `cmd/seed-token` run locally or as a one-off `BINARY=` build. See
  `docs/runbooks/railway-real-image-smoke-test.md`.

## What is not governance

Four things in this repo are governance: **`DECISIONS.md`**, **`IMPLEMENTATION_STATUS.md`**,
**`docs/adr/`** and **`docs/api/integration-quickstart.md`**.

Everything else at the root is history and **must not be read as law** — `frustration_log.md` (133 KB),
the fifteen `PHASE*_CONFIDENCE_INDEX.md` files, and `CONFIDENCE_SCORES.md`. They record how decisions
were reached, not what was decided. A confidence score is not a rule.

## Do not invent constraints

If you cannot cite a rule ID, an ADR, a line of code, or a logged incident, you do not have a
constraint — you have a preference, and it does not belong in a plan, a PR, or a refusal. Absence of a
rule is not a prohibition. A listed gap — `DECISIONS.md` §Deferred, the PLANNED rows in
`docs/architecture/admin-control-surface.md`, the not-enforced table in
`../docs/00_workspace/contracts.md` — is ordered work, not a question.
