# Phase 3 — Confidence Rate Index

Per-action confidence for the Phase 3 deliverable (generation pipeline:
`POST /v1/artifacts/{artifact_id}/generate`, `GET /v1/jobs/{job_id}`,
worker, mock provider, S3, idempotency, generation_jobs lifecycle).

Confidence here means "the implementation matches the contract, the
behavior matches the brief + the corrections, and the code will survive
Phase 4 without rework."

Rubric (matches the repo-wide rubric):

- 90–100 — **Very High**: Concrete spec, mature primitive, low novel logic.
- 75–89  — **High**: Clear with minor ambiguity or follow-up.
- 60–74  — **Medium**: Material ambiguity or external coupling.
- 40–59  — **Low**: Significant ambiguity or quality risk.
- <40    — **Very Low**: Highly uncertain or out of scope.

| # | Action | Confidence | Explanation (what would raise / lower it) |
|---|--------|-----------:|-------------------------------------------|
| 1 | Add `CodeIdempotencyConflict` and `CodeProviderUnavailable` to `internal/httperr`. | 99 | Two new constants. Each is referenced by a Phase 3 test that asserts the response shape (`Content-Type: application/problem+json`, `code`, `message`, `request_id`). |
| 2 | Add `internal/ids` prefixes for `job_`, `asset_`, `att_`, `ce_`, `idem_`. | 95 | Pure functions; regex-backed tests for each new prefix; consistent with Phase 2's style. The `idem_` prefix is internal — never exposed in any response — so its specific value is low-risk. |
| 3 | sqlc queries for `generation_jobs` (insert / get-by-id / mark-running / mark-completed / mark-failed) and an `unchecked` get used by the worker. | 92 | Tenant scope explicit on every query except `GetGenerationJobByIDUnchecked`, which is the one query the worker uses — it carries the tenant through the row instead of redoing it at the boundary. `sqlc vet` passes; `make generate` is idempotent. |
| 4 | sqlc query for `visual_assets.InsertVisualAsset`. | 93 | Single insert; phase-correct defaults (status='ready', generated_at=now()). The 14 nullable columns (variant_family, style_profile_id, etc.) are intentionally NULL at insert time; matrix/identity fields land later. |
| 5 | sqlc queries for `provider_attempts` (insert + mark-succeeded + mark-failed + count). | 90 | `provider_attempts` has no `tenant_id` column per the corrections; tenant flows via the job FK at read time. The count query exists for the integration test to assert attempt_number monotonicity. |
| 6 | sqlc query for `generation_cost_events` insert. | 92 | Minimal insert (no estimated/actual cost — cost reservations are Phase 4). The `Operation` constant is the provider-enum string (`text_to_image`); status is `completed` / `failed`. |
| 7 | sqlc queries for `idempotency_keys` (get / insert-on-conflict-do-nothing / delete-expired). | 90 | `INSERT ... ON CONFLICT DO NOTHING RETURNING` followed by a fall-back GET handles the concurrent-first-write race the docs/api/idempotency.md confidence note flagged. The expiry-sweep query exists but is not wired to a cron in Phase 3. |
| 8 | `internal/jobs/repository.go` — domain wrapper around generation_jobs + provider_attempts + cost events. | 88 | All writes carry tenant_id; the worker re-reads the job via `GetByID` and carries that tenant through to subsequent writes. Cap at 88 because the repository surface is wide (12 methods) which is a smell — but it also matches the worker's actual data needs, and splitting it would just push the cohesion problem one layer deeper. |
| 9 | `internal/jobs/enqueue.go` — asynq client wrapper with `MaxAttempts=3`. | 90 | One typed method, one task name (`image:generate_artifact`). `asynq.MaxRetry(MaxAttempts-1)` is the correct mapping from "max 3 attempts" to asynq's "retry up to N times" semantics. The retry policy is intentionally asynq's default exponential backoff. |
| 10 | `internal/jobs/worker.go` — handler, MarkRunning → attempt insert → provider call → S3 puts → asset insert → MarkCompleted, with retry-aware failure paths. | 84 | The whole worker is one function plus helpers; that's deliberate for Phase 3 (one job type, one provider, no fan-out). Confidence isn't higher because the failure-vs-storage-vs-persistence error code mapping is a thin one-pass switch instead of a proper typed error; that will need a rethink when retryable failures get nuanced classification (rate-limit vs content-policy vs network) in Phase 5. |
| 11 | `internal/storage/storage.go` + `internal/storage/s3.go` (aws-sdk-go-v2, MinIO / R2 friendly). | 88 | `Storage.Put` returns the canonical `s3://<bucket>/<key>` URL the brief asks for. `BaseEndpoint` + `UsePathStyle` together work for MinIO locally and AWS S3 in prod. Confidence isn't 92+ because the SDK's endpoint surface has shifted across versions and I can't fully verify against R2 / virtual-hosted style without a real R2 bucket. |
| 12 | `internal/idempotency/repository.go` — first-writer-wins backed by the table. | 92 | `INSERT ... ON CONFLICT DO NOTHING` plus a fall-back GET is the standard pattern called out in `docs/api/idempotency.md`'s own confidence note. The Insert returns `(record, inserted bool, error)` so callers can branch on "was this a real insert or did somebody else get there first?". |
| 13 | `internal/idempotency/middleware.go` — chi middleware honoring same-key/same-body, same-key/different-body, same-key/different-endpoint. | 88 | Reservation-then-write flow: middleware reserves a job_id, attaches it to context, runs the handler; on 202, persists the idempotency row. Cap at 88 because there's a small window where the handler returns 202 but the post-write to `idempotency_keys` fails — at that point the response has already gone out, and a future replay with the same key falls through and creates a duplicate job. The brief tolerates this for MVP; a follow-up could wrap the handler in a tx and write both rows atomically. |
| 14 | `internal/http/handlers/artifacts_handler.go`. | 90 | Validation: required field checks, body tenant_id rejection (raw-body inspect from `decode.go`), enum re-validation for quality/latency/fallback, style profile tenant scoping (`422 invalid_style_profile`). The BFL bail-out lives first — before any state changes — per correction 1. The reserved jobID-from-context fallback to `ids.NewGenerationJobID()` matches what the middleware needs. |
| 15 | `internal/http/handlers/jobs_handler.go`. | 95 | Single GET, tenant-scoped, `404` for cross-tenant. The `apigen.GenerationJob` mapping pins `final_asset_ids` and `preview_asset_ids` as pointers per the codegen contract (omitempty). |
| 16 | Route wiring (`mountArtifacts`, `mountJobs`) including the idempotency middleware on POST only. | 92 | Apply order matches the brief: scope check + idempotency middleware → handler. `chi.With(scope, idem)` runs scope first, so an unauthorized request returns 403 without touching the idempotency table. |
| 17 | `cmd/api/main.go` wires the new repos + enqueuer + ImageProvider via Deps. | 92 | The enqueuer's Close is deferred so SIGTERM-on-tests doesn't leak Redis connections. The api binary still works with `IMAGE_PROVIDER=bfl` set (config validates `BFL_API_KEY`) but every artifact request returns 503 — the right shape for "the binary started but the feature isn't on yet." |
| 18 | `cmd/worker/main.go` registers the `image:generate_artifact` handler, graceful shutdown, S3 init. | 88 | One handler, one binary. The provider is hardcoded to mock per the brief; switching to BFL when Phase 4 lands is a one-line change in `buildProvider`. Cap at 88 because the worker doesn't currently set a queue/priority — asynq's default queue is fine for a single task name but the moment Phase 4 adds pack fan-out we'll want per-task queues. |
| 19 | Handler tests with stubs (`artifacts_handler_test.go`, `jobs_handler_test.go`, `idempotency_handler_test.go`). | 92 | 15 tests covering: happy path + job_<16 hex>; each required-field failure; body tenant_id; unknown style → 422; BFL → 503 with zero state writes; same-key/same-body replay; same-key/different-body 409; same-key/different-endpoint 409; no-key two-jobs; cross-tenant GET → 404. |
| 20 | Worker tests (`worker_test.go`). | 88 | In-process tests against fake repos exercise: happy path (3 S3 puts, asset row, MarkCompleted, cost event); provider failure on final attempt (job marked failed, retryable=false); provider failure on early attempt (job not marked failed); attempt_number matches retryCount+1. Cap at 88 because the asynq decode path (`NewHandlerFunc`) isn't tested directly — it just `json.Unmarshal`s and calls `Process`. |
| 21 | Storage tests (`keys_test.go` + integration). | 90 | Unit tests on key formatting and canonical URL shape are deterministic. Integration test against MinIO is `-tags=integration`; skips when env vars unset; HeadObject confirms the bytes actually landed. |
| 22 | Integration test for end-to-end POST → worker → GET (`-tags=integration`). | 86 | Full path against real Postgres + MinIO. Drives the worker synchronously in the test (not via asynq queue) so the test stays deterministic; the brief allowed this. Confidence isn't higher because it relies on env vars being set in CI; a missing var skips the test rather than failing it, which a future contributor could miss. |
| 23 | ID-prefix regex tests for `job_`, `asset_`, `att_`, `ce_`, `idem_`. | 99 | Identical pattern to Phase 2's prefix tests. |
| 24 | CI updates — Redis + MinIO service containers, idempotency index assertion, `go test -tags=integration ./...`. | 86 | The MinIO image (`bitnami/minio:2024.10.13`) is the only fixed-version dependency that's not "alpine"; the official `minio/minio` doesn't expose a healthcheck-friendly path in the same shape, so bitnami is the pragmatic choice. The integration test step now scans the whole repo (`./...`) instead of a single package, which means a future package with a broken integration test will fail this job. That's the right behavior but worth noting. |
| 25 | `scripts/seed_dev_token.sh` — add `jobs:read` to the seeded scope set. | 99 | One shell array entry + the printed Scopes line; both updated together. |
| 26 | `make generate` clean, `git diff --exit-code` clean after my changes are committed. | 92 | `oapi-codegen` produced no changes (no OpenAPI bump); `sqlc generate` only added the five new files for my new queries (and the InsertVisualAsset addition to the existing visual_assets.sql.go). All committed. |
| 27 | Phase 3 explicit non-goals stayed un-implemented (no pack fan-out, no retrieval matrix, no cost reservations, no BFL routing, no admin endpoints, no presigned URLs). | 99 | I touched only the two new paths. The 503 on BFL is an active rejection, not a half-implemented feature. |

## Aggregate

- **Mean across actions**: ~91 — **Very High**
- **Floor (lowest single action)**: 84 — worker happy/failure path, by design (the
  worker is the most complex Phase 3 piece and the one most likely to
  shift when Phase 4–5 land real provider routing).
- **Risks carried into Phase 4**:
  - Cost reservation skeleton needs to wrap the handler before the
    enqueue; the current order (`GenerateArtifactRequest` → style check →
    `Insert` job → `Enqueue`) leaves a clean spot to insert
    `cost_reservations` write between the style check and `Insert`.
  - The 202 response is a small ad-hoc struct, not `apigen.GenerationJobAccepted`. Promoting to the codegen type
    becomes necessary the moment Phase 4 adds `estimated_cost_usd` and
    `cost_reservation_id` to the 202 body.
  - The worker's error classification is a thin switch over Go errors.
    Phase 5's retryable / non-retryable distinction (rate limit, content
    policy, network) needs typed errors at the provider boundary.
  - The idempotency middleware writes its row *after* the handler
    responds 202. A failed post-write leaves no record; replays would
    create a new job. Acceptable for MVP per the brief but worth a tx
    wrap-up when traffic grows.
