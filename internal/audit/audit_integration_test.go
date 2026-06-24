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
	// ActorTokenID is left empty (→ SQL NULL) because actor_token_id has a FK
	// to api_tokens(id); passing an arbitrary string would violate the constraint.
	err = audit.Emit(ctx, q, audit.Event{
		EventType:    "media.eligibility_verified",
		TenantID:     "tenant_audit_test",
		ActorTokenID: "",
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
