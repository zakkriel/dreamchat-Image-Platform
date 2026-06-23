# Chunk 2 — Request Contract (governance gate + cost-routing) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /v1/generations` — the full combined request contract, validated + persisted, governance-verified, and cost-routed (intent → cheapest/premium capability-valid model) — as the first behavior chunk.

**Architecture:** A new strict async endpoint runs the chokepoint `verify governance → resolve cheapest capability-valid route → reserve cost → enqueue`. Governance lives in a new pure `internal/governance` package (real presence/freshness/allowlist checks now, signature crypto stubbed). Cost-routing extends the existing `internal/providers/routing` resolver with intent-driven, price-aware ranking over a capability floor. All Chunk-1 columns become written via the explicit-column `InsertGenerationJob`. Deferred behaviors that would mis-cost or mis-count are rejected `501`.

**Tech Stack:** Go 1.25, chi router, oapi-codegen (types only), sqlc v1.27.0, pgx v5, asynq, PostgreSQL 15, GitHub Actions.

## Global Constraints

Copied from the spec (`docs/superpowers/specs/2026-06-23-chunk2-request-contract-design.md`). Every task implicitly includes these.

- **New endpoint only:** `POST /v1/generations`, scope `images:write`, async (D-8). Existing generate endpoints are **untouched** in behavior.
- **Chokepoint order (non-negotiable):** verify governance → resolve route → reserve cost → enqueue. Governance (enforce) and capability failures reject **before** cost reservation and any provider call.
- **501 deferred-behavior rejection:** `render.transform_only == true` OR `grid.enabled == true` → `501`, before governance/routing/reservation, no job created. Off/absent → validated + persisted. A present `render.transform` with `transform_only=false`, a set `derive_from`/`anchor_asset_id`, and `lazy` are stored-not-acted (full untransformed single-image generation still happens).
- **Cost-routing floor:** `identity_capable` when `subject.identity_id != ""`, else `scene_capable`. `intent=draft` → cheapest priced route at/above floor; `intent=commit` → premium (highest `quality_tier`, tie toward identity-capable). Never below floor → 422. `provider_id` pin must still pass the floor. No silent identity downgrade (anchor creation: identity_id set, anchor+derive null → identity-capable under both intents).
- **Reservation basis:** price the existing route + standard single-image basis (`operation_type`/`units` via the unchanged cost-context path), **NOT** `max_megapixels`. MP is validated, clamped to a platform ceiling, persisted — never priced, never enforced at worker pixels this chunk.
- **Governance:** `GOVERNANCE_ENFORCEMENT = log_only | enforce` (default `log_only`). Real checks now: required fields + `schema_version` present (D-4), `issued_at` freshness (`GOVERNANCE_MAX_AGE`, default `24h`), `authorized_by` ∈ `GOVERNANCE_AUTHORIZED_ISSUERS`. Signature crypto = explicit no-op pass `// TODO(core-signing)`. Startup WARN if `enforce` while verifier is the stub. `content_class` opaque (stored/logged, never parsed). The gate NEVER receives prompt/description text. Audit `media.eligibility_verified` / `media.eligibility_blocked`.
- **Idempotency:** body `idempotency_key` canonical → existing `(token,key,endpoint,request_hash)` machinery. Header present & ≠ body → 422. Header-only (no body key) → 422.
- **sqlc explicit-column rule (standing from Chunk 1):** any query selecting/returning the new columns keeps the explicit full-column list — no `SELECT *`, no row-adapter boilerplate. `make generate` → `git diff --exit-code` clean.
- **OpenAPI additive only:** add schemas/path; keep `docs/api/openapi.yaml` and `api/openapi.yaml` byte-identical; CI validator + `diff -q` must pass.
- **RLS enforcement:** assert cross-tenant blocking (not just policy presence) under `image_platform_api` for the governance columns on `generation_jobs`.
- **Rules:** D-3/E-1 (verify & store, never own policy / read prompt), D-4 (JSONB + schema_version), D-8 (async only), D-9 (doc edits cite proving code). TDD failing-test-first; one chunk = one PR; gate red → stop.
- **Tests:** integration tests build-tagged `integration`, require `POSTGRES_DSN` (+ `POSTGRES_API_DSN` for RLS). **No `t.Parallel()`** in `internal/migrate` or the `internal/jobs` RLS tests. Local Postgres: container `chunk1-testpg` on **host port 55432** (reuse). `psql` is not on the local PATH — local tests use pgx; psql lines are CI-only.

## Local Prerequisites

```bash
export POSTGRES_DSN="postgres://image_platform:image_platform@localhost:55432/image_platform?sslmode=disable"
export POSTGRES_API_DSN="postgres://image_platform_api:image_platform_api@localhost:55432/image_platform?sslmode=disable"
# container chunk1-testpg should already be up; if not:
# docker run -d --name chunk1-testpg -e POSTGRES_USER=image_platform -e POSTGRES_PASSWORD=image_platform -e POSTGRES_DB=image_platform -p 55432:5432 postgres:15-alpine
```
`sqlc` v1.27.0 and `go tool oapi-codegen` must be available for `make generate`. Push the branch only after Task 11 (CI assertions and code must be mutually consistent).

> **Note on POSTGRES_API_DSN locally:** the `image_platform_api` role is created by migration 0009 when migrations run against a DB. The RLS Go tests `t.Skip` when `POSTGRES_API_DSN` is unset; to run them locally, ensure migrations have been applied to `chunk1-testpg` (the migrate integration tests do this on throwaway DBs; for the jobs RLS tests the harness applies migrations to its test DB). If the api role/password differs locally, the tests skip rather than fail — CI is the authoritative gate for RLS.

---

### Task 1: Config — governance settings

**Files:**
- Modify: `internal/config/config.go` (add `GovernanceMode` type + 3 fields + load + validate)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.GovernanceMode` (`GovernanceLogOnly` | `GovernanceEnforce`), and `Config.GovernanceEnforcement GovernanceMode`, `Config.GovernanceMaxAge time.Duration`, `Config.GovernanceAuthorizedIssuers []string`. Consumed by Tasks 8, 9 (wiring) and the governance gate's caller.

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go` (follow the existing table/env-set pattern in that file):

```go
func TestGovernanceConfigDefaults(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "x")
	t.Setenv("REDIS_ADDR", "x")
	t.Setenv("S3_BUCKET", "x")
	t.Setenv("S3_REGION", "x")
	t.Setenv("S3_ENDPOINT", "x")
	t.Setenv("S3_ACCESS_KEY_ID", "x")
	t.Setenv("S3_SECRET_ACCESS_KEY", "x")
	t.Setenv("API_TOKEN_PEPPER", "x")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.GovernanceEnforcement != config.GovernanceLogOnly {
		t.Fatalf("default enforcement = %q, want log_only", cfg.GovernanceEnforcement)
	}
	if cfg.GovernanceMaxAge != 24*time.Hour {
		t.Fatalf("default max age = %v, want 24h", cfg.GovernanceMaxAge)
	}
	if len(cfg.GovernanceAuthorizedIssuers) != 0 {
		t.Fatalf("default issuers = %v, want empty", cfg.GovernanceAuthorizedIssuers)
	}
}

func TestGovernanceConfigParsed(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "x")
	t.Setenv("REDIS_ADDR", "x")
	t.Setenv("S3_BUCKET", "x")
	t.Setenv("S3_REGION", "x")
	t.Setenv("S3_ENDPOINT", "x")
	t.Setenv("S3_ACCESS_KEY_ID", "x")
	t.Setenv("S3_SECRET_ACCESS_KEY", "x")
	t.Setenv("API_TOKEN_PEPPER", "x")
	t.Setenv("GOVERNANCE_ENFORCEMENT", "enforce")
	t.Setenv("GOVERNANCE_MAX_AGE", "1h")
	t.Setenv("GOVERNANCE_AUTHORIZED_ISSUERS", "core-signer-1, core-signer-2")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.GovernanceEnforcement != config.GovernanceEnforce {
		t.Fatalf("enforcement = %q, want enforce", cfg.GovernanceEnforcement)
	}
	if cfg.GovernanceMaxAge != time.Hour {
		t.Fatalf("max age = %v, want 1h", cfg.GovernanceMaxAge)
	}
	if len(cfg.GovernanceAuthorizedIssuers) != 2 || cfg.GovernanceAuthorizedIssuers[0] != "core-signer-1" || cfg.GovernanceAuthorizedIssuers[1] != "core-signer-2" {
		t.Fatalf("issuers = %v, want [core-signer-1 core-signer-2] (trimmed)", cfg.GovernanceAuthorizedIssuers)
	}
}

func TestGovernanceEnforcementInvalid(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "x")
	t.Setenv("REDIS_ADDR", "x")
	t.Setenv("S3_BUCKET", "x")
	t.Setenv("S3_REGION", "x")
	t.Setenv("S3_ENDPOINT", "x")
	t.Setenv("S3_ACCESS_KEY_ID", "x")
	t.Setenv("S3_SECRET_ACCESS_KEY", "x")
	t.Setenv("API_TOKEN_PEPPER", "x")
	t.Setenv("GOVERNANCE_ENFORCEMENT", "bogus")

	if _, err := config.Load(); err == nil {
		t.Fatal("expected error for invalid GOVERNANCE_ENFORCEMENT")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/config/ -run TestGovernance -v`
Expected: FAIL (undefined `config.GovernanceLogOnly`, etc.).

- [ ] **Step 3: Implement**

In `internal/config/config.go`, after the `Provider` block (around line 18) add:

```go
type GovernanceMode string

const (
	GovernanceLogOnly GovernanceMode = "log_only"
	GovernanceEnforce GovernanceMode = "enforce"
)
```

Add to the `Config` struct (after `AllowSyntheticProviders`):

```go
	// GovernanceEnforcement gates the media-eligibility verification at the
	// /v1/generations chokepoint. log_only (default): record what WOULD be
	// rejected via an audit event, then proceed. enforce: reject. Default is
	// log_only everywhere because core cannot sign envelopes yet (Chunk 2).
	GovernanceEnforcement GovernanceMode
	// GovernanceMaxAge is the freshness window for governance issued_at.
	GovernanceMaxAge time.Duration
	// GovernanceAuthorizedIssuers is the allowlist of recognized authorized_by
	// values (comma-separated env). Empty means none recognized.
	GovernanceAuthorizedIssuers []string
```

In `Load()` (in the struct literal, after `AllowSyntheticProviders`):

```go
		GovernanceEnforcement:       GovernanceMode(getEnv("GOVERNANCE_ENFORCEMENT", string(GovernanceLogOnly))),
		GovernanceMaxAge:            getEnvDuration("GOVERNANCE_MAX_AGE", 24*time.Hour),
		GovernanceAuthorizedIssuers: getEnvCSV("GOVERNANCE_AUTHORIZED_ISSUERS"),
```

In `validate()` (after the `ImageProvider` switch, before the `missing` checks):

```go
	switch c.GovernanceEnforcement {
	case GovernanceLogOnly, GovernanceEnforce:
	default:
		return fmt.Errorf("invalid GOVERNANCE_ENFORCEMENT %q (expected log_only|enforce)", c.GovernanceEnforcement)
	}
```

Add this helper near `getEnvBool`:

```go
// getEnvCSV parses a comma-separated env var into a trimmed, non-empty slice.
// Unset or empty yields a nil slice.
func getEnvCSV(key string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/config/ -run TestGovernance -v && go vet ./internal/config/`
Expected: PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): GOVERNANCE_ENFORCEMENT/MAX_AGE/AUTHORIZED_ISSUERS settings"
```

---

### Task 2: `internal/audit` emitter

**Files:**
- Create: `internal/audit/audit.go`
- Test: `internal/audit/audit_integration_test.go`

**Interfaces:**
- Consumes: `dbgen.Queries.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{ID, TenantID *string, EventType, ActorTokenID *string, ResourceType *string, ResourceID *string, Metadata []byte})` (`internal/db/dbgen/admin_cost.sql.go:109`); `ids.NewAuditEventID()`.
- Produces: `audit.Emit(ctx, q *dbgen.Queries, ev audit.Event) error` where `Event{EventType, TenantID, ActorTokenID, ResourceType, ResourceID string, Metadata map[string]any}`. Consumed by the governance gate caller (Task 8). Empty optional strings map to SQL NULL.

- [ ] **Step 1: Write the failing test**

Create `internal/audit/audit_integration_test.go`:

```go
//go:build integration

package audit_test

import (
	"context"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/audit"
	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
	"github.com/zakkriel/drchat-image-platform/internal/testdb"
)

func TestEmitWritesAuditEvent(t *testing.T) {
	db, _ := testdb.New(t)
	if _, err := db.Exec(`SELECT 1`); err != nil { // ensure usable
		t.Fatalf("ping: %v", err)
	}
	// testdb.New gives a *sql.DB; audit needs a pgx pool/queries. Use the pgx
	// pool helper from the jobs harness pattern instead — see note below.
	t.Skip("replace with pgxpool-based harness per Step 3 note")
}
```

> **Note:** `internal/audit` operates on `*dbgen.Queries` built from a `pgx` connection/tx, not `database/sql`. The repo's pgx test pool helper is `openTestPool(t)` in `internal/jobs/integration_test.go` (package-private). Rather than depend on that, this task's integration test should construct a pgxpool from `POSTGRES_DSN` directly (mirror `openTestPool`: `pgxpool.New(ctx, os.Getenv("POSTGRES_DSN"))`, `t.Skip` if unset), apply migrations via `migrate.Up` on a throwaway DB **or** run against the shared DB inside a transaction it rolls back. Use the transaction-rollback approach to avoid polluting shared state:

Replace the test body with:

```go
//go:build integration

package audit_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/audit"
	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

func TestEmitWritesAuditEvent(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) // never commit — keep shared DB clean

	q := dbgen.New(tx)
	err = audit.Emit(ctx, q, audit.Event{
		EventType:    "media.eligibility_verified",
		TenantID:     "tenant_audit_test",
		ActorTokenID: "tok_audit_test",
		ResourceType: "generation",
		ResourceID:   "job_audit_test",
		Metadata:     map[string]any{"reason": "ok", "classification_id": "c1"},
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}

	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE event_type='media.eligibility_verified' AND tenant_id='tenant_audit_test'`,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test -tags=integration ./internal/audit/ -run TestEmit -v`
Expected: FAIL (no package `audit` / `audit.Emit` undefined).

- [ ] **Step 3: Implement**

Create `internal/audit/audit.go`:

```go
// Package audit emits rows to the audit_events table. It is a thin, shared
// wrapper over the sqlc InsertAuditEvent query so any service can record a
// security-relevant event without duplicating the marshal+insert. Event types
// follow the dotted <domain>.<resource>.<action> convention.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
)

// Event is one audit row. Optional string fields ("" → SQL NULL): TenantID,
// ActorTokenID, ResourceType, ResourceID. Metadata is marshalled to JSONB.
type Event struct {
	EventType    string
	TenantID     string
	ActorTokenID string
	ResourceType string
	ResourceID   string
	Metadata     map[string]any
}

// Emit inserts the audit event using the supplied queries handle (built on a
// pool or tx). Audit rows are tenant-scoped (RLS), so q must run under the
// correct tenant context (or the system/bypass role).
func Emit(ctx context.Context, q *dbgen.Queries, ev Event) error {
	meta := ev.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("audit: marshal metadata: %w", err)
	}
	return q.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
		ID:           ids.NewAuditEventID(),
		EventType:    ev.EventType,
		TenantID:     strPtr(ev.TenantID),
		ActorTokenID: strPtr(ev.ActorTokenID),
		ResourceType: strPtr(ev.ResourceType),
		ResourceID:   strPtr(ev.ResourceID),
		Metadata:     raw,
	})
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
```

> Verify `ids.NewAuditEventID()` exists (`grep -rn "func NewAuditEventID" internal/ids/`). If the constructor has a different name, use the actual one.

- [ ] **Step 4: Run, verify pass**

Run: `go test -tags=integration ./internal/audit/ -run TestEmit -v && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 5: Commit**

```bash
git add internal/audit/
git commit -m "feat(audit): shared audit_events Emit helper"
```

---

### Task 3: `internal/governance` package (verification logic + stubbed signature)

**Files:**
- Create: `internal/governance/governance.go`
- Create: `internal/governance/signature.go`
- Test: `internal/governance/governance_test.go`

**Interfaces:**
- Produces (consumed by Tasks 7/8/9):
  - `governance.Envelope{SchemaVersion, ClassificationID, Visibility, ContentClass, AuthorizedBy, Signature string; IssuedAt time.Time}`
  - `governance.SubjectMeta{IdentityID, AnchorAssetID, DeriveFrom string}` — **NO prompt/description field by design.**
  - `governance.Verifier` interface: `Verify(ctx, Envelope, SubjectMeta) Result`
  - `governance.Result{OK bool; Reason string}` (Reason set when !OK; one of the `Reason*` consts)
  - `governance.NewVerifier(sig SignatureVerifier, maxAge time.Duration, issuers []string) *RealVerifier`
  - `governance.SignatureVerifier` interface: `VerifySignature(ctx, Envelope) (bool, error)`; `governance.StubSignatureVerifier{}` (crypto no-op pass); `governance.IsStub(SignatureVerifier) bool`
  - `governance.Decision` helper: `Decide(mode config.GovernanceMode, res Result) (proceed bool, eventType string)` → `("media.eligibility_verified"|"media.eligibility_blocked")`, `proceed=false` only when `mode==enforce && !res.OK`.

> Import note: to avoid an import cycle, `governance` may import `internal/config` (config imports nothing from governance). If you prefer zero deps, model the mode as a local `governance.Mode` string with the same values and convert at the call site. Either is fine; pick one and be consistent.

- [ ] **Step 1: Write the failing tests**

Create `internal/governance/governance_test.go`:

```go
package governance_test

import (
	"context"
	"testing"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/governance"
)

func freshEnvelope() governance.Envelope {
	return governance.Envelope{
		SchemaVersion:    "1",
		ClassificationID: "class-1",
		Visibility:       "private",
		ContentClass:     "anything-opaque",
		AuthorizedBy:     "core-signer-1",
		IssuedAt:         time.Now().Add(-1 * time.Minute),
		Signature:        "sig-bytes",
	}
}

func newV() governance.Verifier {
	return governance.NewVerifier(governance.StubSignatureVerifier{}, 24*time.Hour, []string{"core-signer-1"})
}

func TestVerifyOK(t *testing.T) {
	res := newV().Verify(context.Background(), freshEnvelope(), governance.SubjectMeta{IdentityID: "id1"})
	if !res.OK {
		t.Fatalf("want OK, got reason %q", res.Reason)
	}
}

func TestVerifyMissingField(t *testing.T) {
	env := freshEnvelope()
	env.ClassificationID = ""
	res := newV().Verify(context.Background(), env, governance.SubjectMeta{IdentityID: "id1"})
	if res.OK || res.Reason != governance.ReasonMissingField {
		t.Fatalf("want missing_field block, got OK=%v reason=%q", res.OK, res.Reason)
	}
}

func TestVerifyMissingSchemaVersion(t *testing.T) {
	env := freshEnvelope()
	env.SchemaVersion = ""
	res := newV().Verify(context.Background(), env, governance.SubjectMeta{IdentityID: "id1"})
	if res.OK || res.Reason != governance.ReasonMissingSchemaVersion {
		t.Fatalf("want missing_schema_version, got OK=%v reason=%q", res.OK, res.Reason)
	}
}

func TestVerifyStale(t *testing.T) {
	env := freshEnvelope()
	env.IssuedAt = time.Now().Add(-48 * time.Hour)
	res := newV().Verify(context.Background(), env, governance.SubjectMeta{IdentityID: "id1"})
	if res.OK || res.Reason != governance.ReasonStale {
		t.Fatalf("want stale, got OK=%v reason=%q", res.OK, res.Reason)
	}
}

func TestVerifyFutureIssuedAt(t *testing.T) {
	env := freshEnvelope()
	env.IssuedAt = time.Now().Add(1 * time.Hour)
	res := newV().Verify(context.Background(), env, governance.SubjectMeta{IdentityID: "id1"})
	if res.OK || res.Reason != governance.ReasonStale {
		t.Fatalf("want stale (future), got OK=%v reason=%q", res.OK, res.Reason)
	}
}

func TestVerifyUnknownIssuer(t *testing.T) {
	env := freshEnvelope()
	env.AuthorizedBy = "stranger"
	res := newV().Verify(context.Background(), env, governance.SubjectMeta{IdentityID: "id1"})
	if res.OK || res.Reason != governance.ReasonUnknownIssuer {
		t.Fatalf("want unknown_issuer, got OK=%v reason=%q", res.OK, res.Reason)
	}
}

// content_class is opaque: the verdict must not depend on its value (D-3/E-1).
func TestContentClassOpaque(t *testing.T) {
	v := newV()
	a := freshEnvelope()
	a.ContentClass = "benign"
	b := freshEnvelope()
	b.ContentClass = "../../etc/passwd; DROP TABLE; nsfw?maybe"
	if got := v.Verify(context.Background(), a, governance.SubjectMeta{IdentityID: "id1"}); !got.OK {
		t.Fatalf("a should be OK")
	}
	if got := v.Verify(context.Background(), b, governance.SubjectMeta{IdentityID: "id1"}); !got.OK {
		t.Fatalf("verdict changed with content_class value — not opaque")
	}
}

func TestDecide(t *testing.T) {
	blocked := governance.Result{OK: false, Reason: governance.ReasonStale}
	ok := governance.Result{OK: true}
	// log_only: always proceed; event reflects verdict.
	if proceed, ev := governance.Decide(governance.ModeLogOnly, blocked); !proceed || ev != governance.EventBlocked {
		t.Fatalf("log_only blocked: proceed=%v ev=%q", proceed, ev)
	}
	if proceed, ev := governance.Decide(governance.ModeLogOnly, ok); !proceed || ev != governance.EventVerified {
		t.Fatalf("log_only ok: proceed=%v ev=%q", proceed, ev)
	}
	// enforce: block stops; ok proceeds.
	if proceed, ev := governance.Decide(governance.ModeEnforce, blocked); proceed || ev != governance.EventBlocked {
		t.Fatalf("enforce blocked: proceed=%v ev=%q", proceed, ev)
	}
	if proceed, _ := governance.Decide(governance.ModeEnforce, ok); !proceed {
		t.Fatalf("enforce ok should proceed")
	}
}

func TestIsStub(t *testing.T) {
	if !governance.IsStub(governance.StubSignatureVerifier{}) {
		t.Fatal("StubSignatureVerifier must report IsStub true")
	}
}
```

> **Prompt-never-read is structural:** `SubjectMeta` (and `Verify`'s signature) have no prompt field, so the gate *cannot* read prompt text. The end-to-end "hostile prompt doesn't change the verdict" assertion lives in the handler integration test (Task 8). Add a short comment in `governance.go` stating this invariant.

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/governance/ -v`
Expected: FAIL (package/types undefined).

- [ ] **Step 3: Implement**

Create `internal/governance/governance.go`:

```go
// Package governance verifies the media-eligibility envelope at the generation
// chokepoint (D-3/E-1): it VERIFIES and stores, it never interprets policy and
// never reads prompt/description text. SubjectMeta carries only identity refs —
// there is intentionally NO prompt field, so the gate cannot inspect the prompt.
// content_class is opaque: stored/logged, never parsed for meaning.
package governance

import (
	"context"
	"time"
)

// Mode mirrors config.GovernanceMode without importing it (avoids a cycle).
type Mode string

const (
	ModeLogOnly Mode = "log_only"
	ModeEnforce Mode = "enforce"
)

const (
	EventVerified = "media.eligibility_verified"
	EventBlocked  = "media.eligibility_blocked"
)

// Block reasons (also used as audit metadata).
const (
	ReasonMissingField         = "missing_field"
	ReasonMissingSchemaVersion = "missing_schema_version"
	ReasonStale                = "stale"
	ReasonUnknownIssuer        = "unknown_issuer"
	ReasonBadSignature         = "bad_signature"
)

// Envelope is the governance object to verify. Persisted JSONB carries
// SchemaVersion (D-4).
type Envelope struct {
	SchemaVersion    string
	ClassificationID string
	Visibility       string
	ContentClass     string // OPAQUE — never parsed
	AuthorizedBy     string
	IssuedAt         time.Time
	Signature        string
}

// SubjectMeta is the only non-envelope context the gate sees. No prompt field.
type SubjectMeta struct {
	IdentityID    string
	AnchorAssetID string
	DeriveFrom    string
}

type Result struct {
	OK     bool
	Reason string
}

type Verifier interface {
	Verify(ctx context.Context, env Envelope, subj SubjectMeta) Result
}

// clockSkew tolerates small future issued_at values.
const clockSkew = 2 * time.Minute

type RealVerifier struct {
	sig     SignatureVerifier
	maxAge  time.Duration
	issuers map[string]struct{}
}

func NewVerifier(sig SignatureVerifier, maxAge time.Duration, issuers []string) *RealVerifier {
	set := make(map[string]struct{}, len(issuers))
	for _, i := range issuers {
		set[i] = struct{}{}
	}
	return &RealVerifier{sig: sig, maxAge: maxAge, issuers: set}
}

func (v *RealVerifier) Verify(ctx context.Context, env Envelope, _ SubjectMeta) Result {
	// (a) required field presence + schema_version (D-4).
	if env.SchemaVersion == "" {
		return Result{Reason: ReasonMissingSchemaVersion}
	}
	if env.ClassificationID == "" || env.Visibility == "" || env.ContentClass == "" ||
		env.AuthorizedBy == "" || env.Signature == "" || env.IssuedAt.IsZero() {
		return Result{Reason: ReasonMissingField}
	}
	// (b) freshness: not older than maxAge, not in the future beyond skew.
	now := time.Now()
	if env.IssuedAt.Before(now.Add(-v.maxAge)) || env.IssuedAt.After(now.Add(clockSkew)) {
		return Result{Reason: ReasonStale}
	}
	// (c) authorized_by allowlist.
	if _, ok := v.issuers[env.AuthorizedBy]; !ok {
		return Result{Reason: ReasonUnknownIssuer}
	}
	// (d) signature — crypto is stubbed (see signature.go).
	ok, err := v.sig.VerifySignature(ctx, env)
	if err != nil || !ok {
		return Result{Reason: ReasonBadSignature}
	}
	return Result{OK: true}
}

// Decide maps (mode, result) to (proceed, auditEventType). proceed is false only
// when enforcing AND the result is a block. The audit event always reflects the
// verdict (verified vs blocked), in BOTH modes.
func Decide(mode Mode, res Result) (proceed bool, eventType string) {
	if res.OK {
		return true, EventVerified
	}
	if mode == ModeEnforce {
		return false, EventBlocked
	}
	return true, EventBlocked
}
```

Create `internal/governance/signature.go`:

```go
package governance

import "context"

// SignatureVerifier checks the envelope signature. The canonicalization +
// crypto is a cross-system contract with core that is NOT YET DESIGNED.
type SignatureVerifier interface {
	VerifySignature(ctx context.Context, env Envelope) (bool, error)
}

// StubSignatureVerifier is an explicit no-op that PASSES every signature.
//
// TODO(core-signing): replace with real canonicalization + signature
// verification once core ships envelope signing and the on-the-wire format is
// pinned. Do NOT invent the format here — it is a cross-system contract.
type StubSignatureVerifier struct{}

func (StubSignatureVerifier) VerifySignature(ctx context.Context, env Envelope) (bool, error) {
	return true, nil
}

// IsStub reports whether the active verifier is the no-op stub. Wiring uses this
// to emit a startup WARN when enforce mode runs against the stub (Task 9).
func IsStub(s SignatureVerifier) bool {
	_, ok := s.(StubSignatureVerifier)
	return ok
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/governance/ -v && go vet ./internal/governance/`
Expected: all PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/governance/
git commit -m "feat(governance): envelope verifier (real checks) + stubbed signature seam [D-3/E-1]"
```

---

### Task 4: OpenAPI — combined contract schemas + `/v1/generations` path

**Files:**
- Modify: `docs/api/openapi.yaml` (canonical: add enum `Intent`, schemas, path)
- Modify: `api/openapi.yaml` (byte-mirror)
- Modify: `internal/http/apigen/apigen.gen.go` (regenerated — do not hand-edit)

**Interfaces:**
- Produces: `apigen.GenerationRequest`, `apigen.GovernanceEnvelope`, `apigen.GenerationSubject`, `apigen.RenderOptions`, `apigen.GridOptions`, `apigen.Intent` (consumed by Tasks 7/8).

- [ ] **Step 1: Add schemas + path to `docs/api/openapi.yaml`**

In the canonical enum block (near `QualityTier`, ~line 1444) add:

```yaml
    Intent:
      type: string
      enum: [draft, commit]
      description: Cost/quality selector. draft = cheapest capability-valid model; commit = premium.
```

In `components/schemas` (with the other request schemas, ~line 1622) add:

```yaml
    GovernanceEnvelope:
      type: object
      required: [schema_version, classification_id, visibility, content_class, authorized_by, issued_at, signature]
      properties:
        schema_version: { type: string }
        classification_id: { type: string }
        visibility: { type: string }
        content_class:
          type: string
          description: Opaque to this service — stored and logged, never parsed for meaning.
        authorized_by: { type: string }
        issued_at: { type: string, format: date-time }
        signature: { type: string }
    GenerationSubject:
      type: object
      required: [identity_id]
      properties:
        identity_id: { type: string }
        anchor_asset_id: { type: string, nullable: true }
        derive_from: { type: string, nullable: true }
    RenderOptions:
      type: object
      required: [intent]
      properties:
        intent: { $ref: '#/components/schemas/Intent' }
        transform_only:
          type: boolean
          default: false
          description: When true, returns 501 this chunk (transform execution is deferred).
        transform:
          type: object
          nullable: true
          additionalProperties: true
          description: Validated (requires schema_version) and stored; execution deferred.
        max_megapixels:
          type: number
          description: Validated and clamped to a platform ceiling, persisted. Not priced or pixel-enforced this chunk.
        provider_id: { type: string, nullable: true }
    GridOptions:
      type: object
      required: [enabled]
      properties:
        enabled:
          type: boolean
          default: false
          description: When true, returns 501 this chunk (grid slicing is deferred).
        contract_id: { type: string, nullable: true }
        cells:
          type: array
          items: { type: object, additionalProperties: true }
    GenerationRequest:
      type: object
      required: [governance, subject, render, idempotency_key]
      properties:
        governance: { $ref: '#/components/schemas/GovernanceEnvelope' }
        subject: { $ref: '#/components/schemas/GenerationSubject' }
        render: { $ref: '#/components/schemas/RenderOptions' }
        grid: { $ref: '#/components/schemas/GridOptions' }
        lazy: { type: boolean, default: false }
        idempotency_key: { type: string }
```

In `paths` (after the last `/v1/...` generate path) add:

```yaml
  /v1/generations:
    post:
      tags: [Jobs]
      summary: Create a generation from the combined governance + cost contract
      security:
        - BearerAuth: [images:write]
      parameters:
        - $ref: '#/components/parameters/IdempotencyKey'
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/GenerationRequest'
      responses:
        '202':
          description: Generation accepted
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/GenerationJobAccepted'
        '429':
          $ref: '#/components/responses/TooManyRequests'
        default:
          $ref: '#/components/responses/ErrorResponse'
```

- [ ] **Step 2: Mirror + regenerate**

```bash
cp docs/api/openapi.yaml api/openapi.yaml
make generate
```

- [ ] **Step 3: Verify**

```bash
diff -q docs/api/openapi.yaml api/openapi.yaml && echo "mirror ok"
python3 -c "import yaml,sys; import openapi_spec_validator as v" 2>/dev/null || true   # validator is CI-only; skip locally if absent
go build ./...
grep -n "type GenerationRequest struct" internal/http/apigen/apigen.gen.go
git status --short
```
Expected: mirror ok; `GenerationRequest`, `GovernanceEnvelope`, `GenerationSubject`, `RenderOptions`, `GridOptions`, `Intent` present in apigen; build clean; only the two yaml files + `apigen.gen.go` changed.

- [ ] **Step 4: Commit**

```bash
git add docs/api/openapi.yaml api/openapi.yaml internal/http/apigen/apigen.gen.go
git commit -m "feat(openapi): GenerationRequest combined contract + POST /v1/generations (additive)"
```

---

### Task 5: Persistence — write the Chunk-1 columns via `InsertGenerationJob`

**Files:**
- Modify: `internal/db/queries/generation_jobs.sql` (extend `InsertGenerationJob` column list — explicit, no `*`)
- Modify: `internal/db/dbgen/*` (regenerated)
- Modify: `internal/jobs/service.go` (`CreateAndEnqueueParams` + the `insertJob` mapping)
- Modify existing callers that build `InsertGenerationJobParams` to pass the new params as nil/zero
- Test: `internal/jobs/cost_integration_test.go` or a new `internal/jobs/generations_persist_integration_test.go`

**Interfaces:**
- Produces: `jobs.CreateAndEnqueueParams` gains nullable governance/render/subject fields:
  `GovernanceEnvelope []byte` (JSONB), `ClassificationID, Visibility, ContentClass, AuthorizedBy *string`, `GovernanceVerifiedAt *time.Time`, `Intent *string`, `TransformOnly *bool`, `Transform []byte`, `MaxMegapixels *float64` (or `pgtype.Numeric`), `Lazy *bool`, `AnchorAssetID, DeriveFrom *string`. Consumed by Task 7/8.

- [ ] **Step 1: Write the failing test**

Add (new file) `internal/jobs/generations_persist_integration_test.go`:

```go
//go:build integration

package jobs_test

import (
	"context"
	"testing"
	// plus the package's existing test helpers / imports
)

// TestInsertPersistsGovernanceColumns proves the InsertGenerationJob writer
// persists the Chunk-1 governance columns (not NULL) when supplied.
func TestInsertPersistsGovernanceColumns(t *testing.T) {
	// Use the package's existing harness to create a Service + pool (see
	// integration_test.go openTestPool / newTestService). Build
	// CreateAndEnqueueParams with governance fields set, call CreateAndEnqueue
	// (or the lower-level insert path the harness exposes), then query
	// generation_jobs and assert classification_id / intent / governance_envelope
	// are persisted for the created job id.
	t.Skip("fill in using the jobs package test harness; assert classification_id, intent, governance_envelope persisted non-null")
}
```

> This test must use the `internal/jobs` package's existing integration harness (look at `internal/jobs/cost_integration_test.go` and `integration_test.go` for `openTestPool`, the `Service` constructor, and a minimal `CreateAndEnqueueParams`). Concretely: construct params with `ClassificationID=strptr("c1")`, `Intent=strptr("draft")`, `GovernanceEnvelope=[]byte(\`{"schema_version":"1"}\`)`, call the create path, then `SELECT classification_id, intent, governance_envelope FROM generation_jobs WHERE id=$1` and assert non-null/expected. Replace the `t.Skip`.

- [ ] **Step 2: Run, verify fail**

Run: `go test -tags=integration ./internal/jobs/ -run TestInsertPersistsGovernanceColumns -v`
Expected: FAIL (params fields undefined / columns NULL).

- [ ] **Step 3: Extend the query (explicit-column rule)**

In `internal/db/queries/generation_jobs.sql`, the `InsertGenerationJob` query: add the 13 columns to BOTH the column list and the `VALUES` list (append after `cache_result`), keeping it explicit:

```sql
-- name: InsertGenerationJob :one
INSERT INTO generation_jobs (
    id, tenant_id, world_id, job_type, status,
    requested_by_token_id, input_payload, fallback_policy, cache_result,
    governance_envelope, classification_id, visibility, content_class,
    authorized_by, governance_verified_at,
    intent, transform_only, transform, max_megapixels, lazy,
    anchor_asset_id, derive_from
) VALUES (
    $1, $2, $3, $4, 'queued',
    $5, $6, $7, $8,
    $9, $10, $11, $12,
    $13, $14,
    $15, $16, $17, $18, $19,
    $20, $21
)
RETURNING <keep the existing explicit RETURNING column list unchanged>;
```

(The `RETURNING` list already includes every column from the Chunk-1 work — leave it as-is.)

- [ ] **Step 4: Regenerate + thread params**

```bash
make generate
```
This regenerates `InsertGenerationJobParams` with the new fields (nullable pointers / `[]byte` / `pgtype.Numeric`). Then:
- In `internal/jobs/service.go`: add the matching fields to `CreateAndEnqueueParams`, and in `insertJob` (the function that builds `dbgen.InsertGenerationJobParams`, ~`service.go:1049`) map them through.
- Find every other caller of `dbgen.InsertGenerationJobParams` (`grep -rn "InsertGenerationJobParams{" internal/`) and add the new fields as their zero value (nil pointers, nil `[]byte`) so existing endpoints persist NULL (no behavior change).

- [ ] **Step 5: Run + zero-drift**

```bash
go test -tags=integration ./internal/jobs/ -run TestInsertPersistsGovernanceColumns -v
go build ./...
make generate && git diff --exit-code   # zero codegen drift
sqlc vet
```
Expected: PASS; build clean; zero drift; sqlc vet clean. Confirm `internal/jobs/repository.go`/`internal/adminjobs/adminjobs.go` did NOT need row-adapter changes (the RETURNING list is unchanged, so query return types stay `GenerationJob`).

- [ ] **Step 6: Commit**

```bash
git add internal/db/ internal/jobs/service.go internal/http/handlers/ internal/jobs/generations_persist_integration_test.go
git commit -m "feat(jobs): persist Chunk-1 governance/render/subject columns via InsertGenerationJob (explicit-list)"
```

---

### Task 6: Cost-routing — intent-driven, price-aware ranking over a capability floor

**Files:**
- Modify: `internal/providers/routing/routing.go` (`ResolveRequest` + ranking)
- Modify: `internal/providers/routing/dbsource.go` (+ the routes query) to surface active unit price + quality_tier per candidate
- Test: `internal/providers/routing/*_test.go` (table tests with in-memory route source)

**Interfaces:**
- Produces: `routing.ResolveRequest.Intent string` (`"draft"|"commit"|""`). When `Intent != ""`, the resolver:
  - applies `RequiredCapability` as the hard floor (caller sets it from the request: `identity_capable` if identity_id present else `scene_capable`),
  - does NOT require an exact `QualityTier` match,
  - ranks survivors: `draft` → cheapest active unit price asc; `commit` → highest `quality_tier` rank (high>standard>draft) then identity-capable preference,
  - returns `ErrUnsupportedCapability` / `ErrNoRoute` (existing 422 mapping) when nothing meets the floor.
  When `Intent == ""` behavior is unchanged (legacy endpoints).

> **Capability floor exactness:** today `RequiredCapability` is an EXACT match filter. For the floor to be a true "at/above" floor, the resolver must accept candidates whose route capability *satisfies* the required capability via the existing hierarchy (`capability.CapabilitySatisfies`, `internal/providers/capability.go:54`) when `Intent != ""`. Keep the legacy exact-match path for `Intent == ""`. This is the one semantic change to the filter; cover it with a test (a `pack_capable`/`identity_capable` route satisfies an `identity_capable` floor; a `scene_capable` route does NOT).

- [ ] **Step 1: Write the failing tests**

Add to the routing test file (use the existing in-memory route source test pattern — inspect `routing_test.go` for the fixture builder; routes need `Price` + `QualityTier` + `Capability`):

```go
// draft picks the cheapest capability-valid route at/above the floor.
func TestResolveDraftPicksCheapest(t *testing.T) { /* two scene_capable routes priced 0.02 and 0.01; Intent=draft, RequiredCapability=scene_capable → the 0.01 route */ }

// commit picks the premium (highest quality_tier) route at/above the floor.
func TestResolveCommitPicksPremium(t *testing.T) { /* scene_capable 'standard' cheap vs 'high' pricier; Intent=commit → the 'high' route */ }

// identity floor: a scene-only route can NEVER serve an identity floor, even for draft.
func TestResolveIdentityFloorNoDowngrade(t *testing.T) {
	// routes: cheap scene_capable, pricier identity_capable.
	// Intent=draft, RequiredCapability=identity_capable → identity_capable route (NOT the cheaper scene one).
	// Intent=commit, RequiredCapability=identity_capable → identity_capable route.
}

// anchor creation (identity floor) resolves identity-capable under BOTH intents.
func TestResolveAnchorCreationStaysIdentity(t *testing.T) { /* RequiredCapability=identity_capable; assert both draft and commit resolve an identity-axis route */ }

// provider_id pin must still pass the floor.
func TestResolvePinMustPassFloor(t *testing.T) {
	// pin a scene-only provider under an identity floor → ErrUnsupportedCapability/ErrNoRoute (not the scene route).
}

// nothing at/above floor → routing error (422 mapping).
func TestResolveNoCapableRoute(t *testing.T) { /* only scene routes, identity floor → ErrUnsupportedCapability */ }
```

> Fill in each test body using the existing fixture builder. If the current fixture `Route` struct lacks `Price`/`QualityTier`, extend it in Step 3 and set them in the fixtures.

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/providers/routing/ -run 'TestResolveDraft|TestResolveCommit|TestResolveIdentity|TestResolveAnchor|TestResolvePin|TestResolveNoCapable' -v`
Expected: FAIL (`Intent` field undefined / ranking not implemented).

- [ ] **Step 3: Implement**

- Add `Intent string` to `ResolveRequest` (`routing.go`).
- Ensure each candidate `Route` carries its active unit price and `quality_tier`. Extend `DBRouteSource.ListRoutes` (`dbsource.go`) + the underlying `ListProviderRoutesForOperation` query to `LEFT JOIN provider_model_prices` (active row: `is_active = true`, latest `effective_from`) and select `price_per_unit` + `m.quality_tier` (or the route's quality_tier column). Add the fields to the routing `Route` struct. (Keep the explicit-column rule on the SQL.)
- In `candidates` (`routing.go:260`): when `req.Intent != ""`, replace the exact `RequiredCapability` filter with a hierarchy-satisfies floor check (`capability.CapabilitySatisfies(routeCap, req.RequiredCapability)`); keep exact-match when `Intent == ""`. After the hard filters, when `Intent != ""`, sort survivors by the intent comparator BEFORE the existing priority tie-break:
  - `draft`: ascending active unit price (nil/unpriced sorts last); ties → existing tie-break.
  - `commit`: descending quality_tier rank (`high`=3,`standard`=2,`draft`=1,else 0), then identity-axis-capable first (`capability.IsIdentityAxisCapability`), then existing tie-break.
- Leave `ResolveChain` semantics intact (fallbacks); the primary `Resolve` returns the top-ranked survivor.

- [ ] **Step 4: Run, verify pass**

```bash
go test ./internal/providers/routing/ -v
go build ./...
make generate && git diff --exit-code   # the dbsource query change regenerates sqlc; commit it
sqlc vet
```
Expected: PASS; zero drift after committing the regenerated query; sqlc vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/providers/routing/ internal/db/
git commit -m "feat(routing): intent-driven price-aware ranking over a capability floor"
```

---

### Task 7: `POST /v1/generations` endpoint scaffold (no governance yet)

**Files:**
- Create: `internal/http/handlers/generations_handler.go`
- Modify: `internal/http/router.go` (mount the route)
- Modify: `internal/httperr/errors.go` (add codes)
- Test: `internal/http/handlers/generations_handler_test.go` (unit, fake resolver/creator)

**Interfaces:**
- Consumes: `apigen.GenerationRequest` (T4); `RouteResolver`, `applyResolvedRoute`, `writeJobAccepted`, `writeRouteError`, `handleReplay` (`handlers/routing.go`); `jobs.Creator`/`CreateAndEnqueue` + `CreateAndEnqueueParams` (T5); `routing.ResolveRequest{Intent, RequiredCapability, ProviderID, OperationType=...}`.
- Produces: `handlers.NewGenerationsHandler(...)` + `(*GenerationsHandler).Create`. Task 8 inserts the governance gate into `Create`.

- [ ] **Step 1: Add httperr codes**

In `internal/httperr/errors.go` add:

```go
	// Chunk 2: deferred-behavior rejections (HTTP 501) — the request is
	// well-formed but actively invokes behavior not implemented yet, and routing
	// it through the single-image path would mis-cost (transform_only) or
	// mis-count (grid) the result.
	CodeTransformOnlyNotSupported = "transform_only_not_supported"
	CodeGridNotSupported          = "grid_not_supported"
	// CodeGovernanceBlocked surfaces (HTTP 403) when GOVERNANCE_ENFORCEMENT=enforce
	// and the media-eligibility envelope fails verification (Chunk 2).
	CodeGovernanceBlocked = "governance_blocked"
```

- [ ] **Step 2: Write the failing tests (unit, fakes)**

Create `internal/http/handlers/generations_handler_test.go` covering (use the existing handler-test fakes; inspect `artifacts_handler_test.go` for `fakeResolver`, `fakeCreator`, request helpers):

```go
// - valid minimal request (governance+subject+render+idempotency_key, transform_only=false, grid absent) → 202.
// - unknown field in body → 422 (DisallowUnknownFields).
// - render.transform_only=true → 501 transform_only_not_supported, no job created.
// - grid.enabled=true → 501 grid_not_supported, no job created.
// - render.intent missing/invalid → 422.
// - subject.identity_id missing → 422.
// - header Idempotency-Key present and != body idempotency_key → 422.
// - header-only (no body idempotency_key) → 422.
// - body idempotency_key present (no header) → proceeds (202).
// - identity_id present → resolver received RequiredCapability=identity_capable; absent-identity path n/a (identity required), but assert Intent threaded.
```

> Model these on `artifacts_handler_test.go`. The fake resolver records `lastReq routing.ResolveRequest`; assert `lastReq.Intent` and `lastReq.RequiredCapability`. The fake creator records the `CreateAndEnqueueParams`; assert the persisted-field mapping.

- [ ] **Step 3: Run, verify fail**

Run: `go test ./internal/http/handlers/ -run TestGenerations -v`
Expected: FAIL (handler undefined).

- [ ] **Step 4: Implement the handler**

Create `internal/http/handlers/generations_handler.go`. Structure (model on `artifacts_handler.go:66` + `handlers/routing.go` helpers):

1. `auth.PrincipalFromContext` → tenant, token.
2. `readRawJSONBody` (raw bytes for idempotency hash) → `rejectBodyTenantID`.
3. Decode with **DisallowUnknownFields**: `dec := newJSONDecoder(raw); dec.DisallowUnknownFields(); if err := dec.Decode(&req); ... 422`.
4. Validate: `req.Governance` all required fields non-empty + `schema_version`; `req.Subject.IdentityId` non-empty; `req.Render.Intent` ∈ {draft,commit}; if `req.Render.Transform != nil` require its `schema_version` (D-4); `req.Grid` shape. 422 via `httperr.Write(..., http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, msg)`.
5. **501 checks (before everything else after validation):** `if req.Render.TransformOnly != nil && *req.Render.TransformOnly { httperr.Write(w,r,http.StatusNotImplemented, httperr.CodeTransformOnlyNotSupported, "...") ; return }` and the same for `req.Grid != nil && req.Grid.Enabled`.
6. **Idempotency reconcile:** body key required (422 if empty); read header `Idempotency-Key`; if header != "" and header != body key → 422; (header-only impossible since body required). endpoint = `r.Method + " " + r.URL.Path`; requestHash = `HashRequestBody(raw)`.
7. `handleReplay(...)` pre-check → return if handled.
8. **Cost-routing:** build `routing.ResolveRequest{TenantID, OperationType: "text_to_image", Intent: string(req.Render.Intent), RequiredCapability: floor, ProviderID: deref(req.Render.ProviderId)}` where `floor = "identity_capable" if req.Subject.IdentityId != "" else "scene_capable"` (identity_id is required, so floor is effectively identity_capable; keep the conditional for clarity/futureproofing). `resolved, err := h.Resolver.Resolve(...)`; on err `writeRouteError`; `applyResolvedRoute(&params, payload, resolved)`.
9. **MP clamp:** `clamped := clampMegapixels(req.Render.MaxMegapixels, platformCeiling)`; store `clamped` + the raw contract objects (`governance`, `subject`, `render`, `grid`, `lazy`) into `payload`; set `params.MaxMegapixels = &clamped`.
10. Map governance/subject/render fields onto `CreateAndEnqueueParams` (T5 fields): `ClassificationID`, `Visibility`, `ContentClass`, `AuthorizedBy`, `GovernanceEnvelope` (marshal the governance object incl. schema_version), `Intent`, `TransformOnly`, `Transform` (marshal if present), `Lazy`, `AnchorAssetID`, `DeriveFrom`. (`GovernanceVerifiedAt` set in Task 8.)
11. `result, err := h.Service.CreateAndEnqueue(ctx, params)` → `writeJobServiceError` on err; else `writeJobAccepted(w, result)`.

Define `clampMegapixels(p *float32, ceiling float64) float64` (min of requested-and-ceiling; if nil → ceiling) and a `platformCeiling` const. Wire `NewGenerationsHandler(svc, resolver, ...)` and mount in `router.go` under `v1.With(auth.RequireScopes("images:write")).Post("/generations", h.Create)`.

> The reservation basis is unchanged: do NOT pass `max_megapixels` into the cost-context. Let the existing `withCostContextPayload` path derive `operation_type`/`units` from the resolved route + standard basis (Task 5/the service already does this for the artifact path; reuse it).

- [ ] **Step 5: Run, verify pass**

```bash
go test ./internal/http/handlers/ -run TestGenerations -v
go build ./... && go vet ./...
```
Expected: PASS, build + vet clean.

- [ ] **Step 6: Commit**

```bash
git add internal/http/handlers/generations_handler.go internal/http/router.go internal/httperr/errors.go internal/http/handlers/generations_handler_test.go
git commit -m "feat(http): POST /v1/generations endpoint scaffold (validate, 501 gates, idempotency, cost-routing)"
```

---

### Task 8: Integrate the governance gate into the chokepoint

**Files:**
- Modify: `internal/http/handlers/generations_handler.go` (insert gate before route resolution; emit audit; enforce/log_only; set `GovernanceVerifiedAt`)
- Modify: `internal/http/router.go` / `handlers` deps to carry the `governance.Verifier`, mode, and an audit sink
- Test: `internal/http/handlers/generations_handler_test.go` (gate unit tests) + `internal/jobs/generations_governance_integration_test.go` (end-to-end: governance before reserve; prompt-never-read)

**Interfaces:**
- Consumes: `governance.Verifier`, `governance.Decide`, `governance.Envelope`/`SubjectMeta`, `governance.Mode` (T3); `audit.Emit` (T2).
- Produces: the gate runs in `Create` **after idempotency pre-check, before route resolution**.

- [ ] **Step 1: Write the failing tests**

Unit (handlers test): with a fake verifier returning a block:
```go
// - log_only + block → 202 (proceeds) AND an audit emit recorded with EventBlocked.
// - enforce + block → 403 governance_blocked AND audit EventBlocked AND resolver/creator NOT called (no reservation).
// - verified → 202, audit EventVerified, params.GovernanceVerifiedAt set.
```
Integration (`internal/jobs/...` or a handler integration test): 
```go
// - chokepoint order: an enforce-block leaves NO cost_reservations row for that request (governance before reserve).
// - prompt-never-read (spy): a recording governance.Verifier captures the exact
//   (Envelope, SubjectMeta) it was called with; assert SubjectMeta contains ONLY
//   the identity refs (IdentityID/AnchorAssetID/DeriveFrom) and the Envelope
//   carries only the governance fields — NO prompt/description/identity-traits
//   text reaches the gate. (The combined contract has no prompt field, and
//   SubjectMeta/Verify have no prompt parameter, so this is structural; the spy
//   asserts the handler doesn't smuggle any prompt-bearing content in.)
```

> Use a fake `governance.Verifier` and a fake audit sink in unit tests. For the integration order test, run enforce mode and assert `SELECT count(*) FROM cost_reservations` for the job's tenant is unchanged. For prompt-never-read, the spy verifier records its arguments; the assertion is that no prompt/description text appears in what the gate received.

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/http/handlers/ -run TestGenerationsGovernance -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

- Add to the handler deps: `verifier governance.Verifier`, `mode governance.Mode`, and an audit-emitting closure/sink (since audit needs a `*dbgen.Queries` under tenant context, the simplest seam is a small `AuditSink` interface on the handler: `Emit(ctx, tenantID, ev audit.Event) error` implemented in wiring via `appdb.WithTenant` + `audit.Emit`). Define the interface in `handlers` and implement it in `router.go`/`cmd` wiring.
- In `Create`, after `handleReplay` and before building the `ResolveRequest`:

```go
env := governance.Envelope{
	SchemaVersion:    req.Governance.SchemaVersion,
	ClassificationID: req.Governance.ClassificationId,
	Visibility:       req.Governance.Visibility,
	ContentClass:     req.Governance.ContentClass,
	AuthorizedBy:     req.Governance.AuthorizedBy,
	IssuedAt:         req.Governance.IssuedAt,
	Signature:        req.Governance.Signature,
}
subj := governance.SubjectMeta{IdentityID: req.Subject.IdentityId, AnchorAssetID: deref(req.Subject.AnchorAssetId), DeriveFrom: deref(req.Subject.DeriveFrom)}
res := h.verifier.Verify(ctx, env, subj)
proceed, eventType := governance.Decide(h.mode, res)
_ = h.audit.Emit(ctx, tenantID, audit.Event{
	EventType: eventType, TenantID: tenantID, ActorTokenID: tokenID,
	ResourceType: "generation",
	Metadata: map[string]any{"reason": res.Reason, "classification_id": req.Governance.ClassificationId, "content_class": req.Governance.ContentClass, "mode": string(h.mode)},
})
if !proceed {
	httperr.Write(w, r, http.StatusForbidden, httperr.CodeGovernanceBlocked, "governance verification failed: "+res.Reason)
	return
}
if res.OK {
	now := time.Now()
	params.GovernanceVerifiedAt = &now
}
```

(`content_class` goes into audit metadata as an opaque value — stored/logged, never parsed.)

- [ ] **Step 4: Run, verify pass**

```bash
go test ./internal/http/handlers/ -run TestGenerations -v
go test -tags=integration ./internal/jobs/ -run TestGenerationsGovernance -v
go build ./... && go vet ./...
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/http/handlers/ internal/http/router.go internal/jobs/generations_governance_integration_test.go
git commit -m "feat(governance): wire eligibility gate before route+reserve on /v1/generations (audit, log_only/enforce)"
```

---

### Task 9: Startup WARN (enforce + stub) and full wiring

**Files:**
- Modify: `cmd/api/main.go` (+ `cmd/worker/main.go` if it constructs the verifier) — construct `governance.NewVerifier(StubSignatureVerifier{}, cfg.GovernanceMaxAge, cfg.GovernanceAuthorizedIssuers)`, pass mode `governance.Mode(cfg.GovernanceEnforcement)`, the audit sink, into the handler deps; log the WARN.
- Test: `cmd/api/main_test.go` or a small `internal/governance` helper test for the warn condition.

**Interfaces:**
- Consumes: `config` (T1), `governance` (T3). Produces: a startup WARN when `mode==enforce && governance.IsStub(sig)`.

- [ ] **Step 1: Write the failing test**

Add a tiny pure helper + test (so it's unit-testable without booting the server). In `internal/governance/governance.go` add:

```go
// EnforceWithStubWarning returns a non-empty warning when enforce mode is active
// against a stubbed signature verifier (signatures are NOT actually verified).
func EnforceWithStubWarning(mode Mode, sig SignatureVerifier) string {
	if mode == ModeEnforce && IsStub(sig) {
		return "GOVERNANCE_ENFORCEMENT=enforce but signature verification is STUBBED — signatures are not verified; do not trust enforce for signature integrity (TODO core-signing)"
	}
	return ""
}
```

Test in `internal/governance/governance_test.go`:

```go
func TestEnforceWithStubWarning(t *testing.T) {
	if governance.EnforceWithStubWarning(governance.ModeEnforce, governance.StubSignatureVerifier{}) == "" {
		t.Fatal("expected warning for enforce+stub")
	}
	if governance.EnforceWithStubWarning(governance.ModeLogOnly, governance.StubSignatureVerifier{}) != "" {
		t.Fatal("no warning expected in log_only")
	}
}
```

- [ ] **Step 2: Run, verify fail** → `go test ./internal/governance/ -run TestEnforceWithStub -v` (FAIL: undefined).

- [ ] **Step 3: Implement** the helper (above) and call it during wiring in `cmd/api/main.go` (and worker if it builds the verifier):

```go
sig := governance.StubSignatureVerifier{}
gmode := governance.Mode(cfg.GovernanceEnforcement)
if w := governance.EnforceWithStubWarning(gmode, sig); w != "" {
	logger.Warn(w)
}
verifier := governance.NewVerifier(sig, cfg.GovernanceMaxAge, cfg.GovernanceAuthorizedIssuers)
// pass verifier, gmode, audit sink into apphttp.Deps / NewGenerationsHandler
```

- [ ] **Step 4: Run, verify pass** → `go test ./internal/governance/ -v && go build ./...` (PASS).

- [ ] **Step 5: Commit**

```bash
git add internal/governance/ cmd/
git commit -m "feat(governance): enforce-with-stub startup WARN + verifier wiring"
```

---

### Task 10: RLS cross-tenant enforcement (Go + CI)

**Files:**
- Modify: `internal/jobs/rls_integration_test.go` (governance-column cross-tenant blocking; add Chunk-1 tables to `protected`)
- Modify: `.github/workflows/ci.yml` (`migrations` job: psql cross-tenant SELECT on a governance-bearing row under `image_platform_api`)

**Interfaces:**
- Consumes: the two-pool harness (`openTestPool`/`openAPITestPool`, `withGUC`, `seedRLSFixtures`, `protected`).

- [ ] **Step 1: Write the failing test**

In `internal/jobs/rls_integration_test.go`:
- Add `"sprite_sheet_contract", "sprite_sheet_slice", "identity_cost_ledger"` to the `protected` slice used by `TestRLSEnabledForcedAndPolicies`.
- Add a new test proving governance-column cross-tenant blocking:

```go
func TestRLSGovernanceColumnsCrossTenantBlocked(t *testing.T) {
	sys := openTestPool(t)
	api := openAPITestPool(t) // skips if POSTGRES_API_DSN unset
	// seed a generation_jobs row for rlsTenantB WITH governance columns populated, as the owner (sys).
	// then under api pool with GUC=rlsTenantA, assert SELECT count(*) of that row (and of its classification_id) == 0.
	// under GUC=rlsTenantB, assert == 1 and classification_id is the seeded value.
	// (Follows TestRLSTenantVisibilityAndDenyByDefault.)
}
```

> Reuse `seedRLSFixtures`/`rlsCleanup` (add the governance-bearing row + cleanup). Assert the row is RLS-hidden cross-tenant — not that the columns are absent.

- [ ] **Step 2: Run, verify fail** → `go test -tags=integration ./internal/jobs/ -run TestRLSGovernanceColumns -v` (FAIL until the seed/test is wired; if it passes trivially, ensure the seed row actually exists for tenant B).

- [ ] **Step 3: Implement** the seed + assertions; confirm pass.

- [ ] **Step 4: CI psql check**

In `.github/workflows/ci.yml`, in the `migrations` job after the existing "assert RLS enforced under non-superuser API role" step, add a step that: as owner inserts a `generation_jobs` row for `ci_tenant_b` with `classification_id`/`governance_envelope` set; as `image_platform_api` with `PGOPTIONS="-c app.current_tenant=ci_tenant_a"` asserts `SELECT count(*) ... WHERE id='ci_gov_b'` is `0`; cleans up. (Mirror the existing cross-tenant psql block at `ci.yml:367`.)

- [ ] **Step 5: Verify YAML + commit**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml')); print('yaml ok')"
git add internal/jobs/rls_integration_test.go .github/workflows/ci.yml
git commit -m "test(rls): assert cross-tenant blocking of governance columns under image_platform_api"
```

---

### Task 11: Docs (D-9) + PR follow-ups

**Files:**
- Create: `docs/adr/ADR-P002-governance-and-cost-routing.md` (short ADR for the verify-never-interpret gate + intent cost-routing decision, citing proving code)
- Modify: `docs/api/openapi.yaml` trailing change-log comment (optional, if the file maintains one)

**Interfaces:** none (docs only).

- [ ] **Step 1: Write ADR-P002** — record: the governance gate verifies & stores, never interprets (D-3/E-1); `log_only` default with stubbed signature seam + enforce-with-stub WARN; cost-routing intent floor (identity when identity_id present, no silent downgrade) + price-aware ranking; the reservation-prices-existing-basis decision; the 501 deferred-behavior rejections. Cite proving code: `internal/governance/`, `internal/http/handlers/generations_handler.go`, `internal/providers/routing/routing.go`, `internal/audit/`, the CI RLS step. Record the **follow-ups**: (1) legacy resource-scoped endpoints remain ungoverned — route them through the gate or retire them later (governance hole); (2) worker pixel-level MP enforcement; (3) `derive_from`/`transform`(non-only) still do full untransformed generation this chunk; (4) real signature crypto + flip default to `enforce`.

- [ ] **Step 2: Verify citations exist**

```bash
ls internal/governance/governance.go internal/http/handlers/generations_handler.go
grep -n "Intent" internal/providers/routing/routing.go
ls internal/audit/audit.go
```
Expected: all resolve.

- [ ] **Step 3: Commit**

```bash
git add docs/adr/ADR-P002-governance-and-cost-routing.md docs/api/openapi.yaml
git commit -m "docs(adr): ADR-P002 governance verification + cost-routing [D-9]"
```

---

## Final verification (before opening the PR)

- [ ] Full local gate (with `POSTGRES_DSN` + `POSTGRES_API_DSN` set):

```bash
go vet ./...
go build ./...
make generate && git diff --exit-code     # zero codegen drift (sqlc + oapi)
sqlc vet
go test ./...                              # unit suite (config, governance, handlers)
go test -tags=integration ./internal/audit/ ./internal/jobs/ ./internal/providers/routing/ -v
go vet -tags=integration ./...             # integration files compile everywhere
```
- [ ] Push `chunk2-request-contract`; open ONE PR to `main`. PR body cites **D-3/E-1, D-4, D-8, D-9**, notes the spec/plan, and records the four follow-ups (esp. the **ungoverned legacy endpoints** governance hole).
- [ ] Confirm CI green: `go` (drift), `openapi` (mirror + validator), `sqlc` vet, `migrations` (RLS enablement + the new cross-tenant governance-column blocking step), integration tests.

## Definition of done (from spec §11)

Full contract DTOs + additive OpenAPI (mirror + validator green); every field validated and persisted; `transform_only`/`grid.enabled` → 501; governance gate runs before cost reservation with `log_only` default, real presence/freshness/allowlist checks, stubbed-signature seam, and the enforce-with-stub startup WARN; prompt never read by the gate (structural + tested); `content_class` opaque (tested); cost-routing cheapest-by-intent with identity floor and no silent downgrade (anchor-creation tested), MP clamp computed + persisted, reservation priced on the existing basis (tested independent of `max_megapixels`); audit events emitted; RLS cross-tenant blocking asserted in CI + Go under `image_platform_api`; sqlc explicit-list honored, zero codegen drift; PR cites rule IDs + follow-ups.
