# Phase 7C-3 Confidence Index — RLS / Tenant Isolation Hardening

**Overall: 90/100 — Very High**

Phase 7C-3 is **slice 3 of 4** of Phase 7C (Production Controls). It makes the
**database enforce** tenant isolation as defense in depth: a missing or wrong
`WHERE tenant_id = $1` in any current or future query can no longer leak jobs,
assets, identities, budgets, packs, tokens, or cost data across tenants. The
existing application-level tenant predicates **remain** — RLS is an additional
layer, not a replacement. Provider fallback chains + webhooks (7C-4) are **not**
in this slice. **No new business table — count stays 18** (migration `0009` adds
only roles, grants, RLS enablement, and policies). **OpenAPI is unchanged**
(`0.10.0`); RLS is an internal enforcement layer with no client-visible change.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | `0009` enables **+ forces** RLS on 10 direct tenant tables; text-safe, deny-by-default `tenant_isolation` policy | `migrations/0009_rls_tenant_isolation.up.sql` | 93 |
| 2 | Parent-join `EXISTS` policies on 5 tenant-owned child tables (no new columns) | `migrations/0009...` | 91 |
| 3 | Global reference tables (`provider_models/_routes/_model_prices`) left un-RLS'd | `migrations/0009...` | 95 |
| 4 | `image_platform_api` (RLS-enforced) + `image_platform_system` (BYPASSRLS) roles + grants, guarded creation | `migrations/0009...` | 90 |
| 5 | `db.WithTenant` tenant executor (tx-local `set_config(...,true)`) | `internal/db/tenant.go` | 92 |
| 6 | `db.SetTenantLocal` for service-owned transactions | `internal/db/tenant.go` | 92 |
| 7 | `db.SystemDB` named type — explicit, un-accidental system executor | `internal/db/system.go` | 90 |
| 8 | Service-owned tx GUC: create + cache-hit + pack-reuse, cost reserve, idempotency replay | `internal/jobs/service.go` | 90 |
| 9 | Service-owned tx GUC: `identities` upsert; `adminjobs` cancel/retry (tenant-local) | `identities/repository.go`, `adminjobs/adminjobs.go` | 90 |
| 10 | Request-path read repos wrap reads in `WithTenant` (styles, identities, assets, jobs read) | `*/repository.go` | 89 |
| 11 | Executor-agnostic `cost.Lifecycle` (`Commit/Release` on pool; `CommitInTx/ReleaseInTx` on caller tx) | `internal/cost/cost.go` | 90 |
| 12 | Two pools in API; system pool for auth + admin-cost + resolver; tenant pool elsewhere | `cmd/api/main.go` | 90 |
| 13 | Worker on system pool (loads job by id pre-tenant) | `cmd/worker/main.go` | 91 |
| 14 | `POSTGRES_SYSTEM_DSN` + `SystemDSN()` fallback | `internal/config/config.go`, `.env.example`, `docker-compose.yml` | 91 |
| 15 | Two-pool test harness (`openTestPool` system + `openAPITestPool` API role) | `internal/jobs/integration_test.go` | 90 |
| 16 | RLS-enforcement + tenant-executor + service-owned-tx + auth + worker + dual-context tests under API role | `internal/jobs/rls_integration_test.go`, `internal/db/tenant_test.go` | 89 |
| 17 | CI provisions API role + asserts enabled/forced/policies + isolation/deny/WITH-CHECK under non-superuser role | `.github/workflows/ci.yml` | 89 |
| 18 | OpenAPI byte-for-byte unchanged; `0009` NOT added to `sqlc.yaml` (no model change) | `sqlc.yaml`, `api/openapi.yaml` | 93 |

## Tenant GUC model (and why TEXT, no uuid cast)

Tenant-scoped DB work sets a Postgres GUC the policies read:

```sql
SELECT set_config('app.current_tenant', $1, true);   -- third arg true => tx-local
```

The canonical policy predicate is **text**:

```sql
tenant_id = NULLIF(current_setting('app.current_tenant', true), '')
```

`tenant_id` in this repo is `TEXT NOT NULL` (e.g. `tenant_it_jobs`), **not** uuid.
A uuid cast (`::uuid`) would raise at runtime on these ids, so the policy compares
text. The expression is **deny-by-default**: `current_setting(..., true)` returns
`NULL` when the GUC is unset (the `true` = missing_ok), `NULLIF(...,'' )` maps an
empty string to `NULL` too, and `tenant_id = NULL` matches no rows. A request that
never set the GUC therefore sees **zero** tenant-owned rows.

**Why `SET LOCAL` / `set_config(..., true)` is preferred:** the setting is local
to the current transaction and is discarded when the transaction ends, so it can
never leak onto the next checkout of a pooled connection. A plain session-level
`SET app.current_tenant = ...` on a shared `*pgxpool.Pool` is forbidden — it would
bleed across requests. This PR uses **only** the transaction-local model
(`WithTenant` and `SetTenantLocal`); no dedicated-connection/session-reset design
was needed. The no-leak property is tested directly (commit, rollback, and
next-checkout all show an empty GUC).

## Role split (and why ownership is not enough under FORCE RLS)

| Role | superuser | BYPASSRLS | subject to RLS | used by |
|---|---|---|---|---|
| `image_platform_api` | no | no | **yes** | tenant request path + service-owned txns |
| `image_platform_system` | no | **yes** | no | auth pre-tenant, worker, system cost lifecycle, admin-cross-tenant, migrations/seed |

Table **owners normally bypass RLS**, so the migration `ALTER TABLE ... FORCE ROW
LEVEL SECURITY` subjects the owner to policies too — otherwise the app (which has
historically connected as the owner) would silently bypass the whole layer. Once
FORCE is on, **ownership alone is not a valid bypass**; the system path needs an
explicit `BYPASSRLS` role. **Superusers still bypass RLS even under FORCE**, which
is exactly why CI proves enforcement under the **non-superuser** `image_platform_api`
role — an owner/superuser-only check would pass for the wrong reason and prove
nothing.

## Executors and their boundaries

- **Tenant executor** (`db.WithTenant`, `db.SetTenantLocal`) — request-path tenant
  work. `WithTenant(pool, tenant, fn)` begins a tx, sets the GUC tx-locally, runs
  `fn(tx)`, commits/rolls back. `SetTenantLocal(tx, tenant)` sets the GUC inside a
  service that already owns its tx. Empty tenant ⇒ `ErrNoTenant` (loud, never a
  silent zero-row read).
- **System executor** (`db.SystemDB`) — a distinct named type wrapping the
  BYPASSRLS pool. It is reachable only where deliberately wired; a normal tenant
  handler holds the tenant pool and cannot obtain a `SystemDB`. Used for: auth
  token lookup before a principal exists, the async `TouchAPITokenLastUsed`, the
  worker (job lookup by id), the route resolver (global reference data), the admin
  cost surface (admin-cross-tenant after an `admin:costs` scope check), and
  migrations/seed.

### `api_tokens` auth ordering

Auth must read `api_tokens` by prefix **before** the tenant is known. Rather than
weaken the `api_tokens` policy to allow a pre-tenant prefix lookup (which would let
an unset GUC read tokens through the API role), the **auth repository uses the
system executor**. After auth resolves the token and builds the `Principal`, all
normal tenant-scoped request work uses the tenant executor. A test proves the same
prefix lookup through the API role with no GUC returns `ErrTokenNotFound`.

### `TouchAPITokenLastUsed`

The async last-used touch runs after auth, around-tenant, on a goroutine — it is
auth infrastructure, not a tenant handler — so it also uses the system executor. A
test exercises it under the system executor.

### Worker / system bypass decision

The worker receives only a `job_id` and must read the job row to discover its
tenant, so it **cannot** set `app.current_tenant` before its first DB call. It
connects on the system (BYPASSRLS) pool and continues to rely on the existing
app-level tenant predicates (every worker query already passes the job's
`tenant_id`). Adding tenant GUC plumbing to the worker would require a refactor that
makes the tenant known before every DB call — intentionally **out of scope** for
this PR and recorded as deferred. A test loads a job by id under the system
executor and proves the API role cannot do the same unchecked read.

## Service-owned transaction GUC handling

Several hot paths own their own transaction; each sets the GUC **inside** that
transaction (not via a handler wrapper):

- `jobs.Service.CreateAndEnqueue` — `SetTenantLocal` right after `BeginTx`; the
  job, reservation, budget holds, pack, pack items, and idempotency key all land
  under the tenant GUC. The post-commit enqueue-failure cleanup (mark failed, mark
  pack failed, **release the reservation via `ReleaseInTx`**) runs in one fresh
  `WithTenant` tx — never the system executor.
- `CreateCompletedCacheHitJob` / `CreateCompletedPackReuseJob` — `SetTenantLocal`
  for the tenant-owned completed job (+ pack + items).
- The request-path **cost reserve** runs inside the create tx, so it inherits the
  GUC the create set.
- **Idempotency replay** (`LookupReplay` / `replayExisting`) reads
  `idempotency_keys` (child) + `generation_jobs`, so it runs inside `WithTenant`
  (tolerating an empty tenant only for BYPASSRLS-pool callers/tests).
- `identities.Upsert` — `SetTenantLocal` for `visual_identities` +
  `visual_identity_versions`.
- `adminjobs.CancelJob` / `RetryJob` — **tenant-local** (tenant from the principal,
  not cross-tenant), so `SetTenantLocal` scopes the lock + cancel/reset + cost
  movement; the enqueue-failure cleanup uses `WithTenant`. These are deliberately
  **not** routed through the system executor.

## cost.Lifecycle dual-context (executor-agnostic)

`cost.Lifecycle` is invoked from two contexts and must not choose its own pool or
hardcode the system executor:

- **Worker (system/bypass):** standalone `Commit(jobID)` / `Release(jobID)` open a
  tx on the pool the Lifecycle was constructed with — for the worker that is the
  system pool, so it finalizes a job it knows only by id, with no tenant GUC.
- **Request-path admin cancel/retry (tenant-local):** `CommitInTx(tx, jobID)` /
  `ReleaseInTx(tx, jobID)` operate purely on the caller's transaction, which the
  admin service has already scoped with `SetTenantLocal`. The cost movement is
  therefore RLS-compliant without bypass.

`TestCostLifecycleDualContext` proves both: a release through the system executor
and a release composed into a tenant-scoped `WithTenant` tx, both succeeding.

## Direct tenant table policy list

`ENABLE` + `FORCE ROW LEVEL SECURITY` + `tenant_isolation` (USING + WITH CHECK,
text-safe deny-by-default) on:

`api_tokens`, `style_profiles`, `visual_identities`, `visual_assets`,
`generation_jobs`, `asset_packs`, `cost_budgets`, `cost_reservations`,
`generation_cost_events`, `audit_events`.

`audit_events` carries a (nullable, for global events) `tenant_id`; the same policy
protects its tenant rows, and its global NULL-tenant rows are reachable only via
the BYPASSRLS system role (the admin/system audit surface) — which is correct. It
is currently unused by the app (no generated queries), so this is pure hardening.

## Child table coverage matrix

| table | ownership path | direct tenant_id? | policy strategy | covered? | residue |
|---|---|---|---|---|---|
| `visual_identity_versions` | `visual_identities` (`visual_identity_id`) | no | parent-join `EXISTS` | yes | — |
| `asset_pack_items` | `asset_packs` (`asset_pack_id`) | no | parent-join `EXISTS` | yes | — |
| `provider_attempts` | `generation_jobs` (`generation_job_id`) | no | parent-join `EXISTS` | yes | — |
| `idempotency_keys` | `api_tokens` (`token_id`) | no | parent-join `EXISTS` | yes | — |
| `cost_reservation_budget_holds` | `cost_reservations` (`cost_reservation_id`) | no | parent-join `EXISTS` | yes | — |

All five tenant-owned child tables are covered in this PR. Parent-join `EXISTS` was
chosen over a denormalized `tenant_id` column for every child because (a) it keeps
the table count at 18 with **no schema/sqlc-model churn**, and (b) these child
tables are written by system/bypass paths (worker) or inside the same tenant tx as
their parent (idempotency, budget holds), so the per-row parent lookup is not on a
hot read path. The `EXISTS` predicate is deny-by-default for the same reason as the
direct policy: an unset GUC compares the parent against `NULL`.

## Test-harness two-pool split (§22.6a)

RLS changes the meaning of "which role the tests connect as". The harness exposes
**two** pools:

- **System/bypass pool** — `openTestPool` (`POSTGRES_DSN`, the superuser/owner in
  CI). Used by **all** fixture seed/cleanup and **every** pre-existing integration
  test (worker, cost lifecycle, delivery, idempotency, rate-limit/concurrent-cap,
  …). These keep working unchanged and continue to rely on the app-level tenant
  predicates; the superuser bypasses RLS even under FORCE, so they are not affected.
- **Non-superuser API-role pool** — `openAPITestPool` (`POSTGRES_API_DSN`,
  `image_platform_api`). Used **only** by the new tests that must observe
  enforcement: RLS deny-by-default, tenant-A-vs-B visibility, WITH CHECK rejection,
  the tenant-executor / no-leakage tests, the service-owned-tx test, the auth
  deny-without-GUC test, and the request-path read tests.

Fixtures are seeded/torn down via the system pool so setup is not itself subject to
RLS, then enforcement is exercised via the API-role pool. CI provisions the API
role (the migration creates it; the integration step passes its DSN) while the rest
of the suite keeps using the system/owner DSN.

## sqlc handling decision for RLS DDL

`0009` is **not** added to `sqlc.yaml`. RLS DDL (`CREATE POLICY`, `FORCE ROW LEVEL
SECURITY`, role/grant statements) does not change any query's result shape, and **no
columns were added** (child tables use parent-join `EXISTS`, not a denormalized
`tenant_id`), so the generated `dbgen` models are unaffected. `sqlc generate`
produces no diff and `sqlc vet` passes with `0009` excluded — verified locally.

## OpenAPI unchanged decision

RLS is an internal enforcement layer. Client-visible behavior is identical:
in-tenant access works, cross-tenant access still behaves like `404 not_found`
(the row is invisible at the DB layer, exactly as a missing-tenant-predicate read
returned before), and there is **no** new public error shape (no `rls_forbidden`
code). `api/openapi.yaml` and `docs/api/openapi.yaml` remain byte-for-byte
identical; the version stays `0.10.0`.

## Error behavior

| condition | response |
|---|---|
| cross-tenant read/write | `404 not_found` (row invisible under RLS) |
| missing auth | `401 unauthorized` |
| missing scope | `403 forbidden` |
| internal RLS bug (tenant-scoped statement with no GUC) | surfaced loudly: `db.ErrNoTenant` from the executor, or `500 internal_error` + log |

A tenant-scoped statement that reaches the executor with an empty tenant id returns
`ErrNoTenant` rather than silently reading zero rows, so the bug is obvious in tests
and logs instead of masquerading as "not found".

## Why not 100

- The read-path GUC seam wraps each request-path repository read in its own
  `WithTenant` transaction (using the `tenant_id` the method already receives)
  rather than threading a single request-scoped tenant transaction through every
  handler. This is correct and uniform, but it is one short-lived transaction per
  read instead of one per request; a future refactor could hoist the seam to the
  handler boundary.
- The worker remains system/bypass (it learns the tenant only after reading the
  job). Bringing the worker under a tenant GUC needs a refactor that makes the
  tenant known before every DB call — deferred, documented, and still defended by
  the app-level predicates.
- Full end-to-end S3-backed worker tests still run on the system/owner pool in CI
  (the worker is system-scoped by design), so the API-role pool exercises the
  request/service/executor surface rather than the worker output path.

## Deferred to Phase 7C-4

- Provider fallback chains (multi-provider fallback on failure).
- Outbound webhooks for job lifecycle events.
- Optional later refactor: make the worker tenant-aware (tenant known before its
  first DB call) so it can run under the RLS-enforced role instead of BYPASSRLS.
