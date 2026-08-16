package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
)

type fakeIdentityAnchorWriter struct {
	calls []struct {
		identityID string
		tenantID   string
		assetIDs   []string
	}
	err error
}

func (w *fakeIdentityAnchorWriter) SetAnchorAssets(_ context.Context, identityID, tenantID string, anchorAssetIDs []string) (identities.VisualIdentity, error) {
	w.calls = append(w.calls, struct {
		identityID string
		tenantID   string
		assetIDs   []string
	}{
		identityID: identityID,
		tenantID:   tenantID,
		assetIDs:   append([]string(nil), anchorAssetIDs...),
	})
	if w.err != nil {
		return identities.VisualIdentity{}, w.err
	}
	return identities.VisualIdentity{ID: identityID, TenantID: tenantID, AnchorAssetIds: append([]string(nil), anchorAssetIDs...)}, nil
}

func seedBootstrapAnchorJob(repo *fakeJobsRepo, jobID string) {
	worldID := "w1"
	tokenID := "tok_test"
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID:                 jobID,
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload: map[string]any{
			"world_id":               "w1",
			"style_profile_id":       "sty_ok",
			"description":            "Captain Mira",
			"quality_tier":           "standard",
			"anchor_for_identity_id": "vi_anchor",
		},
	})
}

func TestWorkerBootstrapAnchorAttachWritesFinalAssetToIdentity(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	jobsRepo.assets = assetsRepo
	seedBootstrapAnchorJob(jobsRepo, "job_anchor_attach")
	writer := &fakeIdentityAnchorWriter{}

	w := &Worker{
		Jobs:            jobsRepo,
		Assets:          assetsRepo,
		Storage:         &fakeStorage{},
		Providers:       testRegistry(mock.New()),
		IdentityAnchors: writer,
	}

	if err := w.Process(context.Background(), "job_anchor_attach", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if got := len(writer.calls); got != 1 {
		t.Fatalf("expected one anchor attach call, got %d", got)
	}
	call := writer.calls[0]
	if call.identityID != "vi_anchor" {
		t.Fatalf("identityID=%q, want vi_anchor", call.identityID)
	}
	if call.tenantID != "tenant_a" {
		t.Fatalf("tenantID=%q, want tenant_a", call.tenantID)
	}
	if len(call.assetIDs) != 1 {
		t.Fatalf("expected one attached asset id, got %v", call.assetIDs)
	}
	job := jobsRepo.jobs["job_anchor_attach"]
	if len(job.FinalAssetIds) != 1 || call.assetIDs[0] != job.FinalAssetIds[0] {
		t.Fatalf("attach used %v, final assets=%v", call.assetIDs, job.FinalAssetIds)
	}
}

func TestWorkerBootstrapAnchorAttachFailureKeepsJobCompleted(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	jobsRepo.assets = assetsRepo
	seedBootstrapAnchorJob(jobsRepo, "job_anchor_attach_fail")
	writer := &fakeIdentityAnchorWriter{err: errors.New("attach failed")}

	w := &Worker{
		Jobs:            jobsRepo,
		Assets:          assetsRepo,
		Storage:         &fakeStorage{},
		Providers:       testRegistry(mock.New()),
		IdentityAnchors: writer,
	}

	if err := w.Process(context.Background(), "job_anchor_attach_fail", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if got := len(writer.calls); got != 1 {
		t.Fatalf("expected one attach attempt, got %d", got)
	}
	if status := jobsRepo.jobs["job_anchor_attach_fail"].Status; status != "completed" {
		t.Fatalf("job status=%q, want completed", status)
	}
}
