//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

func strptr(s string) *string       { return &s }
func boolptr(b bool) *bool          { return &b }
func timeptr(t time.Time) *time.Time { return &t }

func TestInsertPersistsGovernanceColumns(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_gov_test", "tenant", itTenant, "active", "1.0000")

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	p := baseParams()
	p.ClassificationID = strptr("c1")
	p.Intent = strptr("draft")
	p.GovernanceEnvelope = []byte(`{"schema_version":"1"}`)

	res, err := svc.CreateAndEnqueue(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateAndEnqueue: %v", err)
	}
	if res.JobID == "" {
		t.Fatalf("expected job_id, got empty")
	}

	var classID, intent *string
	var govEnv []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT classification_id, intent, governance_envelope FROM generation_jobs WHERE id = $1`,
		res.JobID,
	).Scan(&classID, &intent, &govEnv); err != nil {
		t.Fatalf("query job: %v", err)
	}
	if classID == nil || *classID != "c1" {
		t.Fatalf("expected classification_id=c1, got %v", classID)
	}
	if intent == nil || *intent != "draft" {
		t.Fatalf("expected intent=draft, got %v", intent)
	}
	// Postgres JSONB normalizes whitespace, so compare via round-trip decode.
	var govParsed map[string]any
	if err := json.Unmarshal(govEnv, &govParsed); err != nil {
		t.Fatalf("unmarshal governance_envelope: %v", err)
	}
	if v, ok := govParsed["schema_version"]; !ok || v != "1" {
		t.Fatalf("expected governance_envelope.schema_version=1, got %v", govParsed)
	}
}
