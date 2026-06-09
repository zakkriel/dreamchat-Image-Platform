# Phase 2 — Confidence Rate Index

Per-action confidence for the Phase 2 deliverable (visual-identity CRUD).
Confidence here means "the implementation matches the contract, the
behavior matches the brief, and the code will survive Phase 3 without
rework."

Rubric (matches the repo-wide rubric):

- 90–100 — **Very High**: Concrete spec, mature primitive, low novel logic.
- 75–89  — **High**: Clear with minor ambiguity or follow-up.
- 60–74  — **Medium**: Material ambiguity or external coupling.
- 40–59  — **Low**: Significant ambiguity or quality risk.
- <40    — **Very Low**: Highly uncertain or out of scope.

| # | Action | Confidence | Explanation (what would raise / lower it) |
|---|--------|-----------:|-------------------------------------------|
| 1 | Update `scripts/seed_dev_token.sh` to include `styles:read`, `styles:write`. | 98 | Tiny shell edit. Verified the SQL insert and the printed `Scopes` line. Only risk is the printed scopes string falling out of sync with the array — both updated together. |
| 2 | Extend `internal/httperr` with `CodeInvalidRequest`, `CodeInvalidStyleProfile`. | 99 | Two new constants, no behavior change. Covered indirectly by every handler test asserting `code=invalid_request` / `code=invalid_style_profile`. |
| 3 | Add `internal/ids` package (`sty_<16hex>`, `vi_<16hex>`). | 95 | Pure function with regex-backed tests. Panics on `crypto/rand` failure rather than threading an error, matching `uuid.NewString` behaviour in this codebase. Would drop a few points if downstream needed time-ordered IDs (KSUID/ULID) — Phase 2 doesn't. |
| 4 | sqlc queries for `style_profiles` (list, create, get-by-id). | 95 | Hand-written, scoped by tenant + status, `world_id IS NULL` per phase brief. `sqlc vet` passes; `make generate` is clean; CI's `git diff --exit-code` will stay green. |
| 5 | sqlc queries for `visual_identities` (six queries — owner lookup, FOR UPDATE, insert, update-with-bump, version insert). | 90 | Avoided a one-statement `ON CONFLICT DO UPDATE` blind upsert as the brief explicitly warned against. Each query is small and tenant-scoped. Confidence isn't 95+ because the `GetVisualIdentityByOwnerAcrossWorlds` query (used by GET) returns the most recently updated row across worlds — a sensible default for MVP but it papers over a contract ambiguity (see frustration entry 19). |
| 6 | sqlc query for `visual_assets` GET by `(asset_id, tenant_id)`. | 97 | Single trivial select. Cross-tenant rows return zero matches → `404 not_found`. Generated row has every column the contract needs. |
| 7 | `internal/styles/repository.go` (list/create/get). | 95 | Thin wrapper over generated queries, maps row → domain. Tests in `handlers_test.go` exercise both list-by-tenant and create paths via a stub of this interface. |
| 8 | `internal/identities/repository.go` (transactional upsert). | 86 | Full 5-step algorithm: validate style → `SELECT ... FOR UPDATE` → branch on insert vs unchanged vs change → version-row write → commit. Integration test (`-tags=integration`) verifies version=1 on insert, version stays on identical re-upsert, version=2 + `canonical_change` row on changed re-upsert. Confidence cap is at 86 because: (a) canonical-trait "did it change?" is JSON-canonicalised via `json.Marshal/Unmarshal` round-trip — semantically robust against key ordering but not against numeric-precision quirks (1e0 vs 1); (b) only one query (`UpdateVisualIdentityWithVersionBump`) is keyed by id without re-checking tenant_id, relying on the prior `FOR UPDATE` lock for safety. Both are intentional for Phase 2 simplicity. |
| 9 | `internal/assets/repository.go` (single read). | 97 | One method, tenant scoping, `pgx.ErrNoRows` → `ErrNotFound`. Phase 3 will grow this with retrieval/search; the surface is intentionally minimal. |
| 10 | `internal/http/handlers/styles_handler.go`. | 92 | Validates `name`, `style_mode`, `positive_prompt`, enum-checks `style_mode`, defaults `default_quality_tier` to `standard`, returns `201` with generated id starting `sty_`. Re-validates the style_mode and quality_tier enums in Go even though the generated types constrain them — that's defensive against the "generated structs ignore unknown values" pattern. |
| 11 | `internal/http/handlers/identities_handler.go`. | 90 | Path/body owner consistency, body tenant_id rejection, required-field validation, `422 invalid_style_profile` from `identities.ErrInvalidStyle`, `404 not_found` from the GET when the owner doesn't exist. Confidence stays at 90 because the GET-across-worlds fallback is a Phase 2 simplification, not a strong long-term contract (see entry 19). |
| 12 | `internal/http/handlers/assets_handler.go`. | 96 | Single GET, tenant scoping, `404` for cross-tenant. The full `apigen.VisualAsset` field mapping is exercised by `TestAssetGetSameTenant`. |
| 13 | `decode.go` — body-level `tenant_id` rejection before decoding into generated types. | 94 | Raw-body inspect via `json.Unmarshal` into `map[string]json.RawMessage`, scans for `"tenant_id"` key, then re-decodes into the generated struct. Tested for both styles and identities. Worth noting the same approach scales to other reserved keys (e.g. body `id`) if the contract gains more. |
| 14 | Route wiring in `internal/http/router.go` with `auth.RequireScopes` per OpenAPI security block. | 95 | All seven Phase 2 paths are wired with the correct scope. The catch-all `/v1/*` still returns 404 for unimplemented paths. Tested with stub repos via the router-level scope tests. |
| 15 | Handler tests with stub repositories (`internal/http/handlers/handlers_test.go`). | 92 | 20+ tests exercising every acceptance criterion. Uses an injectable `NewID` function so generated IDs are deterministic in tests. The single area I'd refine is reducing the JSON-shape assertion noise (`map[string]any` + `.(float64)` casts) by introducing typed responses — Phase 3 candidate. |
| 16 | Repository-level integration test for the upsert transaction (`-tags=integration`). | 88 | Skips when `POSTGRES_DSN` isn't set; the CI `migrations` job sets it. Cleans only the `tenant_id` it owns, so it can run alongside other tests safely. Confidence isn't higher because the test depends on the migrations job already running migrations — coupling that I documented in CI. |
| 17 | OpenAPI surface is unchanged. `docs/api/openapi.yaml` == `api/openapi.yaml`. `openapi-spec-validator` passes both. | 99 | No contract changes were needed; Phase 2 reads existing types. |
| 18 | CI updates: integration step added to the `migrations` job, uses existing Postgres service container. | 90 | New step is small (`go test -tags=integration ./internal/identities/...`) and uses the same DSN the existing step uses. Confidence isn't 95 because the `migrations` job didn't previously install Go — I added `actions/setup-go@v5`, which is a meaningful new step that could fail on cache misses. |
| 19 | `make generate` clean, `git diff --exit-code` clean. | 95 | Verified locally after sqlc + oapi-codegen runs. The OpenAPI-3.1 warning from oapi-codegen is pre-existing and doesn't dirty the working tree. |
| 20 | Phase 2 explicit non-goals stayed un-implemented (no generation, no worker, no provider calls, no S3, no jobs, no idempotency storage). | 99 | I touched only the seven paths in scope. The catch-all `/v1/*` 404 behavior continues to cover everything else. |

## Aggregate

- **Mean across actions**: ~93.5 — **Very High**
- **Floor (lowest single action)**: 86 — transactional upsert, by design (the
  hardest piece of Phase 2 and the only one that warrants caveats).
- **Risks carried into Phase 3**:
  - GET-across-worlds ambiguity (entry 19). Resolve when world-scoped style
    profiles or world-scoped GET semantics land.
  - Idempotency header is currently ignored. Phase 3 brings real storage and
    replay logic.
  - Trait-equality uses JSON canonicalisation; a future move to numeric or
    deeply-nested types may want a structural equality check.
