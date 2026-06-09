package handlers

import (
	"net/http"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
)

func TestIdempotencySameKeySameBodyReturnsSameJob(t *testing.T) {
	creator := newStubCreator()
	router := newArtifactsRouter(creator, seededStyles(), config.ProviderMock)
	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "A bronze key",
	}
	headers := map[string]string{idempotency.HeaderKey: "phase3-acceptance-1"}

	first := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, headers)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first call: expected 202, got %d body=%s", first.Code, first.Body.String())
	}
	firstBody := decode[map[string]any](t, first)
	jobID := firstBody["job_id"].(string)

	second := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, headers)
	if second.Code != http.StatusAccepted {
		t.Fatalf("second call: expected 202, got %d body=%s", second.Code, second.Body.String())
	}
	secondBody := decode[map[string]any](t, second)
	if secondBody["job_id"] != jobID {
		t.Fatalf("expected same job_id %q on replay, got %v", jobID, secondBody["job_id"])
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("expected replay body to match original\nfirst=%s\nsecond=%s", first.Body.String(), second.Body.String())
	}
	if len(creator.calls) != 2 {
		t.Fatalf("expected two service calls (one fresh + one replay), got %d", len(creator.calls))
	}
}

func TestIdempotencySameKeyDifferentBodyReturns409(t *testing.T) {
	creator := newStubCreator()
	router := newArtifactsRouter(creator, seededStyles(), config.ProviderMock)
	headers := map[string]string{idempotency.HeaderKey: "phase3-conflict"}

	first := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"},
		map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "A bronze key"}, headers)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first call: expected 202, got %d", first.Code)
	}

	second := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"},
		map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "A silver key"}, headers)
	assertError(t, second, http.StatusConflict, "idempotency_conflict")
}

func TestIdempotencyNoHeaderProducesTwoJobs(t *testing.T) {
	creator := newStubCreator()
	router := newArtifactsRouter(creator, seededStyles(), config.ProviderMock)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "A bronze key"}

	first := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	second := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("expected both 202, got %d / %d", first.Code, second.Code)
	}
	firstID := decode[map[string]any](t, first)["job_id"]
	secondID := decode[map[string]any](t, second)["job_id"]
	if firstID == secondID {
		t.Fatalf("expected different job ids when no idempotency header, got same %v", firstID)
	}
}

func TestIdempotencyReplayResponseShape(t *testing.T) {
	creator := newStubCreator()
	router := newArtifactsRouter(creator, seededStyles(), config.ProviderMock)
	headers := map[string]string{idempotency.HeaderKey: "phase3-shape"}
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "A bronze key"}

	_ = sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, headers)
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, headers)

	replay := decode[map[string]any](t, rec)
	if _, ok := replay["job_id"].(string); !ok {
		t.Fatalf("replay missing job_id: %v", replay)
	}
	if replay["status"] != "queued" {
		t.Fatalf("replay status not queued: %v", replay["status"])
	}
}

func TestIdempotencyDifferentEndpointSameKeyReturns409(t *testing.T) {
	creator := newStubCreator()
	router := newArtifactsRouter(creator, seededStyles(), config.ProviderMock)
	headers := map[string]string{idempotency.HeaderKey: "phase3-endpoint-collision"}
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "A bronze key"}

	first := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, headers)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first call: expected 202, got %d body=%s", first.Code, first.Body.String())
	}

	// Same key, same body, different endpoint path → 409.
	second := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_2/generate", tenantA, []string{"images:write"}, body, headers)
	assertError(t, second, http.StatusConflict, "idempotency_conflict")
}
