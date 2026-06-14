package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/webhooks"
)

// stubWebhooksConfigService is an in-memory WebhooksConfigService: it keeps one
// endpoint per tenant, mints a fixed secret on create, and preserves it on
// update — enough to exercise the handler's PUT-then-GET and 404 paths.
type stubWebhooksConfigService struct {
	byTenant map[string]webhooks.Endpoint
}

func newStubWebhooksConfigService() *stubWebhooksConfigService {
	return &stubWebhooksConfigService{byTenant: map[string]webhooks.Endpoint{}}
}

func (s *stubWebhooksConfigService) UpsertEndpoint(_ context.Context, tenantID, rawURL string) (webhooks.Endpoint, error) {
	e, ok := s.byTenant[tenantID]
	if ok {
		e.URL = rawURL // preserve existing secret
	} else {
		e = webhooks.Endpoint{ID: "whe_test", TenantID: tenantID, URL: rawURL, Secret: "whsec_test", IsActive: true}
	}
	s.byTenant[tenantID] = e
	return e, nil
}

func (s *stubWebhooksConfigService) GetEndpoint(_ context.Context, tenantID string) (webhooks.Endpoint, error) {
	e, ok := s.byTenant[tenantID]
	if !ok {
		return webhooks.Endpoint{}, webhooks.ErrNotFound
	}
	return e, nil
}

func newWebhooksRouter(svc WebhooksConfigService) chi.Router {
	h := NewWebhooksHandler(svc)
	r := chi.NewRouter()
	r.With(auth.RequireScopes("admin:jobs")).Put("/v1/admin/webhook-endpoint", h.Put)
	r.With(auth.RequireScopes("admin:jobs")).Get("/v1/admin/webhook-endpoint", h.Get)
	return r
}

func TestWebhookEndpointPutThenGet(t *testing.T) {
	svc := newStubWebhooksConfigService()
	r := newWebhooksRouter(svc)

	// PUT creates: 200 with the secret shown.
	putBody := strings.NewReader(`{"url":"https://example.test/hook"}`)
	putReq := httptest.NewRequest(http.MethodPut, "/v1/admin/webhook-endpoint", putBody).
		WithContext(authedContext(tenantA, "admin:jobs"))
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d body=%s", putRec.Code, putRec.Body.String())
	}
	var putResp struct {
		Id       string `json:"id"`
		Url      string `json:"url"`
		IsActive bool   `json:"is_active"`
		Secret   string `json:"secret"`
	}
	if err := json.Unmarshal(putRec.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("decode PUT body: %v", err)
	}
	if putResp.Url != "https://example.test/hook" || putResp.Secret == "" || !putResp.IsActive {
		t.Fatalf("unexpected PUT response: %+v", putResp)
	}

	// GET returns the config WITHOUT the secret.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/admin/webhook-endpoint", nil).
		WithContext(authedContext(tenantA, "admin:jobs"))
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET expected 200, got %d body=%s", getRec.Code, getRec.Body.String())
	}
	if strings.Contains(getRec.Body.String(), "secret") {
		t.Fatalf("GET response must not contain the secret: %s", getRec.Body.String())
	}
	var getResp struct {
		Url string `json:"url"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	if getResp.Url != "https://example.test/hook" {
		t.Fatalf("GET unexpected url: %q", getResp.Url)
	}
}

func TestWebhookEndpointGetNotFound(t *testing.T) {
	svc := newStubWebhooksConfigService()
	r := newWebhooksRouter(svc)

	getReq := httptest.NewRequest(http.MethodGet, "/v1/admin/webhook-endpoint", nil).
		WithContext(authedContext(tenantA, "admin:jobs"))
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 with no endpoint, got %d", getRec.Code)
	}
}

func TestWebhookEndpointPutRequiresScope(t *testing.T) {
	svc := newStubWebhooksConfigService()
	r := newWebhooksRouter(svc)

	putReq := httptest.NewRequest(http.MethodPut, "/v1/admin/webhook-endpoint", strings.NewReader(`{"url":"https://x.test"}`)).
		WithContext(authedContext(tenantA, "images:write"))
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without admin:jobs, got %d", putRec.Code)
	}
}

func TestWebhookEndpointPutRejectsBadURL(t *testing.T) {
	svc := newStubWebhooksConfigService()
	r := newWebhooksRouter(svc)

	putReq := httptest.NewRequest(http.MethodPut, "/v1/admin/webhook-endpoint", strings.NewReader(`{"url":"not-a-url"}`)).
		WithContext(authedContext(tenantA, "admin:jobs"))
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid url, got %d body=%s", putRec.Code, putRec.Body.String())
	}
}
