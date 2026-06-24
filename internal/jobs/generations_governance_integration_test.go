//go:build integration

package jobs_test

// Integration tests for the governance gate on POST /v1/generations (Task 8).
//
// These tests run against a real Postgres database (POSTGRES_DSN env var).
// They use a recording governance.Verifier (spy) and a discarding AuditSink
// so the audit FK path is exercised without requiring a pool-scoped emitter.
//
// Test plan:
//   1. TestGenerationsGovernanceEnforceBlockLeavesNoReservation: an enforce-mode
//      block short-circuits before route resolution / cost reservation — the
//      cost_reservations table is unchanged after a blocked request.
//   2. TestGenerationsGovernancePromptNeverReachesGate: a spy verifier records
//      the exact (Envelope, SubjectMeta) it received; assert SubjectMeta carries
//      only the identity ID refs and no prompt/description/display-name text.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/audit"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/governance"
	"github.com/zakkriel/drchat-image-platform/internal/http/handlers"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

// ---------------------------------------------------------------------------
// Governance integration-test constants
// ---------------------------------------------------------------------------

const (
	itGovIdentityID = "vi_gov_it_test"
	itGovCharID     = "char_gov_it_test"
)

// ---------------------------------------------------------------------------
// Governance-test doubles
// ---------------------------------------------------------------------------

// spyVerifier records its call arguments and returns a preset result.
type spyVerifier struct {
	mu     sync.Mutex
	result governance.Result
	calls  []spyVerifyCall
}

type spyVerifyCall struct {
	env  governance.Envelope
	subj governance.SubjectMeta
}

func (s *spyVerifier) Verify(_ context.Context, env governance.Envelope, subj governance.SubjectMeta) governance.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spyVerifyCall{env: env, subj: subj})
	return s.result
}

// blockVerifier always blocks.
type blockVerifier struct{}

func (blockVerifier) Verify(_ context.Context, _ governance.Envelope, _ governance.SubjectMeta) governance.Result {
	return governance.Result{OK: false, Reason: "integration_test_block"}
}

// discardAuditSink implements handlers.AuditSink and discards all events.
// This avoids the need for a real tenant-scoped DB connection in these tests;
// audit correctness is already asserted by the unit tests with fakeAuditSink.
type discardAuditSink struct{}

func (discardAuditSink) Emit(_ context.Context, _ string, _ audit.Event) error { return nil }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sendGenerationsRequest submits POST /v1/generations to r with the
// integration test's tenant/token principal.
func sendGenerationsRequest(t *testing.T, r http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/generations", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.ContextWithPrincipal(
		telemetry.ContextWithRequestLog(
			telemetry.ContextWithRequestID(req.Context(), "req_gov_it"),
			&telemetry.RequestLog{},
		),
		&auth.Principal{TokenID: itTokenID, TenantID: itTenant, Scopes: []string{"images:write"}},
	))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// govBody returns the minimal valid GenerationRequest body with governance envelope.
func govBody(identityID, idempKey string, extra ...map[string]any) map[string]any {
	b := map[string]any{
		"governance": map[string]any{
			"schema_version":    "1.0",
			"classification_id": "cls_gov_it",
			"visibility":        "private",
			"content_class":     "safe",
			"authorized_by":     "auth_gov_it",
			"issued_at":         "2026-06-18T00:00:00Z",
			"signature":         "sig_gov_it",
		},
		"subject": map[string]any{
			"identity_id": identityID,
		},
		"render": map[string]any{
			"intent": "draft",
		},
		"idempotency_key": idempKey,
	}
	for _, m := range extra {
		for k, v := range m {
			b[k] = v
		}
	}
	return b
}

// ---------------------------------------------------------------------------
// Test 1: enforce-block leaves no cost_reservations row
// ---------------------------------------------------------------------------

// TestGenerationsGovernanceEnforceBlockLeavesNoReservation verifies that an
// enforce-mode governance block short-circuits before route resolution and cost
// reservation: cost_reservations is unchanged, no generation_jobs row is
// created, and the handler returns 403 governance_blocked.
func TestGenerationsGovernanceEnforceBlockLeavesNoReservation(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	// Seed an identity so the handler can find it in GetByIDForTenant.
	// (The governance gate runs AFTER the identity fetch but BEFORE reservation.)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO visual_identities
		 (id, tenant_id, world_id, owner_type, owner_id, display_name, canonical_visual_traits, style_profile_id, current_version, status)
		 VALUES ($1, $2, 'w1', 'character', $3, 'Alice', '{}', $4, 1, 'active')`,
		itGovIdentityID, itTenant, itGovCharID, itStyleID,
	); err != nil {
		t.Fatalf("seed visual_identity: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO visual_identity_versions (visual_identity_id, version, reason) VALUES ($1, 1, 'initial')`,
		itGovIdentityID,
	); err != nil {
		t.Fatalf("seed visual_identity_version: %v", err)
	}

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	idRepo := identities.NewRepository(pool)
	resolver := itResolver(pool)

	h := handlers.NewGenerationsHandler(svc, resolver, idRepo)
	h.Verifier = blockVerifier{}
	h.Mode = governance.ModeEnforce
	h.Audit = discardAuditSink{}

	r := chi.NewRouter()
	r.Post("/v1/generations", h.Create)

	reservationsBefore := countScalar(t, pool,
		`SELECT count(*) FROM cost_reservations WHERE tenant_id = $1`, itTenant)
	jobsBefore := countScalar(t, pool,
		`SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant)

	rec := sendGenerationsRequest(t, r, govBody(itGovIdentityID, "gov-it-block-001"))

	// Handler must return 403 governance_blocked.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on enforce block, got %d body=%s", rec.Code, rec.Body.String())
	}
	var errBody map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
	if errBody["code"] != "governance_blocked" {
		t.Fatalf("expected code=governance_blocked, got %v", errBody["code"])
	}

	// No cost_reservation row created (governance before reserve).
	if after := countScalar(t, pool,
		`SELECT count(*) FROM cost_reservations WHERE tenant_id = $1`, itTenant); after != reservationsBefore {
		t.Fatalf("enforce-block must create no cost_reservations: before=%d after=%d",
			reservationsBefore, after)
	}

	// No generation_jobs row created (governance before job insert).
	if after := countScalar(t, pool,
		`SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant); after != jobsBefore {
		t.Fatalf("enforce-block must create no generation_jobs: before=%d after=%d",
			jobsBefore, after)
	}

	// Enqueuer was never called.
	if enqueued := enq.snapshot(); len(enqueued) != 0 {
		t.Fatalf("enforce-block must not enqueue, got %d enqueued", len(enqueued))
	}
}

// ---------------------------------------------------------------------------
// Test 2: prompt-never-read spy (structural integration check)
// ---------------------------------------------------------------------------

// TestGenerationsGovernancePromptNeverReachesGate asserts that the governance
// gate receives only the governance envelope and identity ID refs — never the
// fetched identity's DisplayName or any prompt/description text.
func TestGenerationsGovernancePromptNeverReachesGate(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	const displayName = "Alice the Character (DisplayName Must Not Reach Gate)"
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO visual_identities
		 (id, tenant_id, world_id, owner_type, owner_id, display_name, canonical_visual_traits, style_profile_id, current_version, status)
		 VALUES ($1, $2, 'w1', 'character', $3, $4, '{}', $5, 1, 'active')`,
		itGovIdentityID, itTenant, itGovCharID, displayName, itStyleID,
	); err != nil {
		t.Fatalf("seed visual_identity: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO visual_identity_versions (visual_identity_id, version, reason) VALUES ($1, 1, 'initial')`,
		itGovIdentityID,
	); err != nil {
		t.Fatalf("seed visual_identity_version: %v", err)
	}

	seedBudget(t, pool, "bud_gov_spy", "tenant", itTenant, "active", "1.0000")

	spy := &spyVerifier{result: governance.Result{OK: true}}
	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	idRepo := identities.NewRepository(pool)
	resolver := itResolver(pool)

	h := handlers.NewGenerationsHandler(svc, resolver, idRepo)
	h.Verifier = spy
	h.Mode = governance.ModeEnforce
	h.Audit = discardAuditSink{}

	r := chi.NewRouter()
	r.Post("/v1/generations", h.Create)

	const anchorID = "anchor_spy_abc"
	const deriveFrom = "derive_spy_xyz"
	body := govBody(itGovIdentityID, "gov-spy-it-002", map[string]any{
		"subject": map[string]any{
			"identity_id":     itGovIdentityID,
			"anchor_asset_id": anchorID,
			"derive_from":     deriveFrom,
		},
	})
	rec := sendGenerationsRequest(t, r, body)

	// The governance gate must NOT have blocked (spy returns OK=true, so no 403).
	// We do not assert a specific success status because downstream failures
	// (price-entry / enqueue) may vary by CI seed; the key assertion is the spy
	// call below, which proves the gate was reached with correct inputs.
	if rec.Code == http.StatusForbidden {
		t.Fatalf("spy: governance gate blocked unexpectedly: %s", rec.Body.String())
	}

	spy.mu.Lock()
	calls := spy.calls
	spy.mu.Unlock()

	if len(calls) == 0 {
		t.Fatal("spy: verifier was never called")
	}
	call := calls[0]

	// SubjectMeta must carry only ID fields.
	if call.subj.IdentityID != itGovIdentityID {
		t.Fatalf("spy: IdentityID=%q, want %q", call.subj.IdentityID, itGovIdentityID)
	}
	if call.subj.AnchorAssetID != anchorID {
		t.Fatalf("spy: AnchorAssetID=%q, want %q", call.subj.AnchorAssetID, anchorID)
	}
	if call.subj.DeriveFrom != deriveFrom {
		t.Fatalf("spy: DeriveFrom=%q, want %q", call.subj.DeriveFrom, deriveFrom)
	}

	// The identity's DisplayName must NOT appear in SubjectMeta.
	subjFields := []string{call.subj.IdentityID, call.subj.AnchorAssetID, call.subj.DeriveFrom}
	for _, f := range subjFields {
		if f == displayName {
			t.Fatalf("spy: identity DisplayName %q leaked into SubjectMeta", displayName)
		}
	}

	// The identity's DisplayName must NOT appear in Envelope fields.
	envTextFields := []string{
		call.env.SchemaVersion, call.env.ClassificationID, call.env.Visibility,
		call.env.ContentClass, call.env.AuthorizedBy, call.env.Signature,
	}
	for _, f := range envTextFields {
		if f == displayName {
			t.Fatalf("spy: identity DisplayName %q leaked into Envelope field", displayName)
		}
	}
}
