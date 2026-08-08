# Cost Optimization Wave 3 - Measurement + cost accounting truth

> **Status:** implemented (this commit).
> **Date:** 2026-08-08
> Program: `docs/superpowers/plans/2026-08-07-cost-optimization-waves.md`
> (Wave 1: `2026-08-07-cost-optimization-wave1-design.md`; Wave 2:
> `2026-08-07-wave2-governance-interfaces-design.md`). Migrations head
> `18 -> 19`. No OpenAPI change.

## 1. Problem

After Wave 2 the platform could generate images under governance and hold
budget, but it could not answer the questions every later cost lever depends
on: what did a generation actually cost, how often did reuse work, and how
much of the spend came from failure paths. Five gaps:

1. **No measurement surface.** Cache-hit rate, $/usable image, policy-reject
   rate, estimated-vs-actual variance, and fallback frequency were not counted
   anywhere, so no cost lever could be judged after shipping.
2. **Under-reserved multi-call jobs.** A pack reserved one operation for a job
   that makes one provider call per missing cell, and a true-preview job
   reserved one call for a job that makes a preview *and* a final. Those jobs
   could spend past their hold.
3. **Cost events were not attributable.** `generation_cost_events` had no link
   to the reservation that priced the call, so a retried job's historical
   attempts summed into the new reservation's actual - a retry could double
   charge. Provider-reported spend was also discarded: the reservation always
   recorded the estimate as the actual.
4. **`max_megapixels` was silently clamped**, and it was enforced against
   provider-reported dimensions rather than the bytes actually persisted.
5. **Pack generation ignored its fallback chain.** Same-price fallback routes
   were resolved, priced, and persisted on the job, but `ProcessPack` only ever
   called the primary - the single-image path walked them, packs did not.

## 2. What shipped

### 2.1 Telemetry (`internal/telemetry/metrics.go`)

A process-local counter surface, deliberately dependency-free: cache hits and
misses, usable images, provider calls, policy rejects, fallback attempts and
successes, and estimated/actual micro-USD totals. Money is accumulated in
micro-USD integers so repeated float addition cannot drift. `MetricsSnapshot`
is an immutable read exposing `CacheHitRate`, `ActualEstimateVarianceMicrosUSD`,
and `ActualCostPerUsableImageMicrosUSD`.

Recorded at the real decision points: cache hits in the artifact, pack, and
generations handlers; provider calls, fallback attempts/successes, and policy
rejects in `generateWithFallback`; usable images on asset persistence; cost on
terminal finalization and reconciliation deltas (`internal/cost/cost.go`).

Invalid or unparseable cost strings are ignored rather than turning telemetry
into a source of request failures (`RecordCost`, `micros`).

### 2.2 Reservation covers the planned calls, not the retry cap

`worstCaseBillableUnits` (`internal/jobs/service.go`) prices the provider calls
one run of the job plans to make: one operation per missing pack cell (or the
requested image count), doubled when a true preview delivers a separate preview
and final render. It saturates instead of wrapping the signed SQLC quantity on
a hostile pack size.

Retries and same-price fallback routes are **deliberately excluded**. They are
failure paths, not planned work; pre-charging every hold for the retry cap and
the route-chain length inflates an ordinary single-image hold 3-9x, which
denies requests that sit comfortably inside their budget. That is a silent
capacity cut, and it is not a safety measure - the real spend of a failure path
is recorded per attempt as a reservation-scoped cost event and reconciled
against the hold at terminal finalization (§2.3).

The create transaction stays `READ COMMITTED`. The concurrent-job cap counts
live jobs and the budget hold updates a shared row; under `REPEATABLE READ`
both read a snapshot taken before the competing writers committed, so the cap
silently overcounts capacity and concurrent holds abort with a serialization
failure instead of queueing.

### 2.3 Reservation-scoped cost events + provider actuals

Migration `0019_cost_event_reservation.sql` adds
`generation_cost_events.cost_reservation_id` (FK to `cost_reservations`) plus a
partial index. Every provider attempt's cost event carries the reservation that
priced the call, so a retry under the same `generation_job_id` can never sum a
previous reservation's attempts into its own actual.

Commit and release now derive `actual_amount` by summing the reservation's
*provider-reported* cost events rather than assuming the estimate. Events whose
actual was inferred from the estimate are excluded from that sum. Commit still
falls back to the estimate when no provider reported anything; release does
**not** - a plain failure leaves `actual_amount` NULL rather than charging for a
bill that never arrived.

Release also implements `cost-control.md` §3 step 10: when a provider billed a
real call before the job failed (preview succeeded, final failed), that partial
actual moves from reserved to spent, is stamped on the job, and is folded into
the identity ledger, while the reservation still ends `released` and the
unbilled remainder is dropped from `reserved_amount`.

`ReconcileForReservation` applies late-arriving provider actuals (a discarded
success, a cancellation race) as a signed delta exactly once against each budget
and the identity ledger.

Both commit and release bind to the job's *current* reservation
(`cr.id = (SELECT j.cost_reservation_id ...)`), so a stale worker task cannot
finalize a hold created by a later admin retry. They deliberately do **not**
gate on `generation_jobs.status`: a status gate turns a legitimate release into
a silent no-op whenever a caller releases before stamping the terminal status,
and a leaked hold permanently consumes budget - a worse failure than the one it
would prevent. Exactly-once is already guaranteed by the `status = 'reserved'`
transition plus the reservation-identity binding.

### 2.4 `max_megapixels` is enforced, never clamped

`maxMegapixelsForWorker` rejects a malformed, non-positive, or over-ceiling
value with an error instead of quietly reducing it, and
`providerImageDimensions` decodes the bytes that will actually be persisted
rather than trusting provider-reported width/height, which is provenance and
not a safety boundary. Exceeding the budget is terminal
(`max_megapixels_exceeded`) - an explicit failure, never a degraded render.

### 2.5 Pack fallback parity

`ProcessPack` calls `generateWithFallback` per cell with the persisted primary
plus same-price alternates, so packs walk the same chain the single-image path
does, and each failed route is recorded as its own provider attempt. Reference
assets are gathered once for the whole pack when *any* route in the chain
requires them and threaded through every route, so a fallback cannot silently
render a different character.

A content-policy rejection is terminal for the pack: the walk stops, pack
completeness is recorded, and the job fails `provider_content_rejected`. The
rejection is never retried on another route or worked around by trying the next
cell.

## 3. Verification

Real PostgreSQL 15, schema at migration 19:

- `go test -tags=integration ./internal/jobs ./internal/adminjobs
  ./internal/migrate ./internal/assets ./internal/identities
  ./internal/http/handlers` - all packages ok, with `POSTGRES_DSN` set so the
  DB-backed tests execute rather than skip.
- `TestMigration0019CostEventReservation` proves the reservation link applies
  and that cost events are attributable across a retry reusing the same
  `generation_job_id`; `TestMigration0017NewTableRLS` still passes.
- Reservation sizing: `TestWorstCaseBillableUnits{CoversPreviewAndFinal,
  ExcludesRetriesAndFallbacks,PricesOnlyMissingPackCells,SaturatesInt32}`
  (`internal/jobs/service_wave3_test.go`).
- Partial-charge release and late reconciliation:
  `TestLifecyclePartialChargeOnReleaseCommitsActualAndReleasesRemainder`,
  `TestLifecycleReleaseWithNoChargeStaysFullyUnbilled`,
  `TestLateReportedDiscardedCostReconcilesTerminalReservation`
  (`internal/jobs/lifecycle_integration_test.go`).
- Concurrency and budget semantics unchanged from the Wave 2 baseline:
  `TestConcurrentCapParallelCreatesCannotExceed`,
  `TestPreflightConcurrentTightBudgetExactlyOneSucceeds`,
  `TestBudgetReset*`, `TestIdempotencyConcurrentRequestsCreateExactlyOneJob`.
- Provider cost normalization and megapixel enforcement:
  `internal/jobs/worker_wave3_test.go`; row mapping:
  `internal/jobs/repository_row_mapping_test.go`; telemetry:
  `internal/telemetry/metrics_test.go`.
- `go build ./...`, `go vet ./...`, `gofmt -l .` (empty), `sqlc generate`
  (no diff), and the full unit suite pass.

## 4. Rules

D-3/E-1 (governance still authorizes only; no cost path reads prompt or
content), D-4 (migration 0019 is additive; JSONB contracts untouched), D-8
(async lifecycle unchanged), D-9 (this doc cites the proving code and tests).

Non-negotiables held: no prompt inspection, `provider_content_rejected` surfaces
verbatim and is never fallback-walked, and budget pressure still produces an
explicit `422 budget_exceeded` rather than a silent quality downgrade.

## 5. Explicitly NOT in this wave

Wave 4 amortization (sprite sheets, anchor-derive defaults, lazy finalization)
remains specification-only behind its release gates. Telemetry is a process-local
counter surface, not an exported Prometheus/OTel collector - export is deployment
work, not a platform contract.
