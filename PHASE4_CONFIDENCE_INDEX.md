# Phase 4 — Confidence Rate Index

Per-action confidence for the Phase 4 deliverable (cost-control pre-flight:
price book lookup, estimation, atomic budget reservation, failed-pre-flight
idempotency, mock-provider seed migration), built from
`docs/architecture/cost-control.md` as the base spec with the seven Phase 4
corrections overriding where they conflict.

Confidence here means "the implementation matches the contract, the behavior
matches the spec + the corrections, and the code is verified against a real
Postgres."

Rubric (matches the repo-wide rubric):

- 90–100 — **Very High**: Concrete spec, mature primitive, low novel logic.
- 75–89  — **High**: Clear with minor ambiguity or follow-up.
- 60–74  — **Medium**: Material ambiguity or external coupling.
- 40–59  — **Low**: Significant ambiguity or quality risk.
- <40    — **Very Low**: Highly uncertain or out of scope.

| # | Action | Confidence | Explanation (what would raise / lower it) |
|---|--------|-----------:|-------------------------------------------|
| 1 | `migrations/0002_seed_mock_provider.up.sql` — seed mock model/route/price + partial unique index. | 92 | Verified applying cleanly and idempotently (ON CONFLICT DO NOTHING + CREATE INDEX IF NOT EXISTS) against a live PG16. Cap below 95 because `idx_provider_model_prices_one_active` is functionally redundant with 0001's `uq_provider_model_prices_active` (Correction 3 mandates the named index; see frustration_log Entry 45). |
| 2 | Exact mock seed rows (Correction 4). | 96 | Every required column is set; capabilities / aspect-ratio arrays are PG text[] literals; all CHECK constraints satisfied. The price is `0.0100 USD per image`, `is_active=true`. |
| 3 | `CodeNoPriceEntry` / `CodeBudgetExceeded` added to `internal/httperr`; `CodeAdminOnly` / `budget_paused` deliberately absent (Correction 7). | 98 | Two constants, both 422, each asserted by a handler unit test. Trimmed codes documented in frustration_log Entry 47. |
| 4 | `internal/ids` — `resv_` prefix + `NewCostReservationID`; regex test. | 99 | Identical pattern to the existing prefix constructors and tests. |
| 5 | sqlc `cost.sql` — `EstimateOperationCost`, `ListBudgetsForReservation`, `ReserveActiveBudget`, `ReservePausedBudget`, `InsertCostReservation`. | 90 | `sqlc vet` + `make generate` clean. The estimate is computed in SQL (`price_per_unit * units`) to avoid Go float math; the conditional `ReserveActiveBudget` is the atomicity primitive. Cap at 90 because the unit math assumes `unit_type=image` (Correction 6) — other units are routed to no_price_entry before reaching estimation. |
| 6 | `internal/cost` — price → estimate → atomic budget hold, with savepoint rollback on partial denial. | 88 | Verified by 8 integration tests including the deterministic N=8 concurrency test. Cap at 88 because the nested-tx (savepoint) composition is the subtlest part: it relies on pgx's `Tx.Begin` savepoint semantics and READ COMMITTED re-check on the conditional UPDATE — both verified against real PG, but the worker-side commit/release lifecycle that would make budgets reusable is deferred (Entry 48). |
| 7 | Hard, atomic tenant-budget enforcement; paused records-not-denies; exceeded denies; narrower scope (token→world→user) must also permit (Correction 2). | 90 | Each branch has a dedicated passing integration test. The "both tenant and narrowest must permit" rule and the savepoint rollback of the tenant hold on a narrower denial are both asserted. |
| 8 | Failed-pre-flight idempotency: commit failed job + idempotency row + failed reservation; replay re-returns the same 422 (Correction 1). | 90 | Verified: `TestPreflightNoPriceEntryReturns422AndReplays` / `...BudgetExceeded...` assert the committed rows, the amounts (0/0 for no-price; computed-estimate/0 for budget), no enqueue, and that a replay re-returns the sentinel. Replay keys the error off the job's committed `error_code`. |
| 9 | `jobs.Service.CreateAndEnqueue` restructured onto one transaction with pre-flight folded in. | 86 | The keyless fast-path and the idempotent tx-path are now one tx (the reservation FK needs the job to exist; the audit rows must commit atomically). All four Phase 3 idempotency integration tests still pass unchanged. Cap at 86 because this is the most-rewritten existing file — the control flow (insert → reserve → link → mark-failed? → idem → commit → enqueue) is linear but dense. |
| 10 | Handler: forwards pricing context (mock route), maps the two sentinels to 422, returns `apigen.GenerationJobAccepted` with `estimated_cost_usd` / `currency` / `cost_reservation_id`. | 92 | Promotes the 202 body to the codegen type (closes Phase 3 Entry 33). Provider/model/op/units are hardcoded to the seeded mock route because Phase 4 has no router yet — flagged in-code. Three new handler unit tests. |
| 11 | CI: apply 0002, assert the index + seed rows, assert a second active price is rejected. | 90 | The duplicate-active-price `INSERT` is expected to fail (`ON_ERROR_STOP`), and the `if psql ...; then exit 1` has no pipe so it reads psql's exit code (verified: exit 1). Table count stays 17 (0002 adds no tables). |
| 12 | Integration-test cleanup updated for the `generation_jobs` ↔ `cost_reservations` circular FK. | 93 | Null the job→reservation link, delete reservations, then jobs, then budgets. Verified by the full suite running green repeatedly against the same DB. |
| 13 | Deferred-with-documentation: period reset (Correction 5) and reservation commit/release (cost-control §9–10). | 85 | Both are explicit non-goals for Phase 4 and documented in frustration_log (Entries 46, 48). Cap at 85 because the deferred commit/release means an enforcing budget accrues stale `reserved_amount` until that lands — a real follow-up, not a hidden bug. |

## Aggregate

- **Mean across actions**: ~91 — **Very High**
- **Floor (lowest single action)**: 85 — the two documented deferrals, by design.
- **Verification**: `go build`, `go vet`, `golangci-lint` (v2.5.0, 0 issues),
  `go test ./...`, and `go test -tags=integration ./...` all run green
  against a live PG16 with migrations 0001 + 0002 applied. The deterministic
  budget-concurrency test (N=8 → exactly 1 success) passes.
- **Risks carried into Phase 5**:
  - Reservation lifecycle terminal steps (commit on success / release on
    failure) are unimplemented; budgets are not yet safely reusable.
  - Period-reset automation needs schema support (`period_start` or a
    rollover worker).
  - Provider routing is still stubbed — the handler hardcodes the mock
    route's `(provider, model, operation)` for pricing; a real router
    replaces those constants.
