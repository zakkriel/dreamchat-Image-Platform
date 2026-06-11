package handlers

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

func newStylePreviewRouter(creator jobs.Creator, stylesRepo styles.Repository, provider config.Provider) chi.Router {
	h := NewStylePreviewHandler(creator, stylesRepo, provider)
	r := chi.NewRouter()
	r.Post("/v1/styles/{style_id}/preview", h.GeneratePreview)
	return r
}

func seededPreviewStyles() *stubStylesRepo {
	repo := newStubStylesRepo()
	repo.seed(styles.StyleProfile{
		ID: "sty_ok", TenantID: tenantA, Name: "watercolor",
		StyleMode: "open_prompt", PositivePrompt: "soft watercolor",
		DefaultQualityTier: "standard", Status: "active",
	})
	return repo
}

func TestStylePreviewReservesAndEnqueuesOneImage(t *testing.T) {
	creator := newStubCreator()
	router := newStylePreviewRouter(creator, seededPreviewStyles(), config.ProviderMock)

	body := map[string]any{"world_id": "w1"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/styles/sty_ok/preview", tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	if !jobIDRe.MatchString(resp["job_id"].(string)) {
		t.Fatalf("expected job_id, got %v", resp["job_id"])
	}
	if resp["status"] != "queued" {
		t.Fatalf("expected status=queued, got %v", resp["status"])
	}
	if len(creator.calls) != 1 {
		t.Fatalf("style preview must reserve+enqueue exactly one job, got %d", len(creator.calls))
	}
	call := creator.calls[0]
	if call.JobType != "artifact" {
		t.Fatalf("preview sample is an artifact job, got %q", call.JobType)
	}
	if call.WorldID != "w1" {
		t.Fatalf("expected world_id forwarded, got %q", call.WorldID)
	}
	if call.Units != 1 {
		t.Fatalf("preview must be a single image, got Units=%d", call.Units)
	}
	if call.InputPayload["style_profile_id"] != "sty_ok" {
		t.Fatalf("payload must carry the style id, got %v", call.InputPayload["style_profile_id"])
	}
	if call.InputPayload["preview_kind"] != "style_preview" {
		t.Fatalf("payload must mark the preview provenance, got %v", call.InputPayload["preview_kind"])
	}
	if ph, _ := call.InputPayload["prompt_hash"].(string); ph == "" {
		t.Fatalf("payload must carry a render hash so the worker persists it")
	}
}

func TestStylePreviewUnknownStyleReturns422(t *testing.T) {
	creator := newStubCreator()
	router := newStylePreviewRouter(creator, newStubStylesRepo(), config.ProviderMock)
	body := map[string]any{"world_id": "w1"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/styles/sty_ghost/preview", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_style_profile")
	if len(creator.calls) != 0 {
		t.Fatalf("unknown style must not enqueue, got %d calls", len(creator.calls))
	}
}

func TestStylePreviewMissingWorldIDReturns400(t *testing.T) {
	creator := newStubCreator()
	router := newStylePreviewRouter(creator, seededPreviewStyles(), config.ProviderMock)
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/styles/sty_ok/preview", tenantA, []string{"images:write"}, map[string]any{}, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
	if len(creator.calls) != 0 {
		t.Fatalf("missing world_id must not enqueue, got %d calls", len(creator.calls))
	}
}

func TestStylePreviewBFLProviderReturns503(t *testing.T) {
	creator := newStubCreator()
	router := newStylePreviewRouter(creator, seededPreviewStyles(), config.ProviderBFL)
	body := map[string]any{"world_id": "w1"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/styles/sty_ok/preview", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusServiceUnavailable, "provider_unavailable")
	if len(creator.calls) != 0 {
		t.Fatalf("provider gate must reject before any enqueue, got %d calls", len(creator.calls))
	}
}

func TestStylePreviewCrossTenantStyleReturns422(t *testing.T) {
	creator := newStubCreator()
	// Style belongs to tenantA; tenantB must not preview it.
	router := newStylePreviewRouter(creator, seededPreviewStyles(), config.ProviderMock)
	body := map[string]any{"world_id": "w1"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/styles/sty_ok/preview", tenantB, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_style_profile")
}
