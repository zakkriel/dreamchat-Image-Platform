package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/webhooks"
)

// WebhooksConfigService is the handler-facing slice of the Phase 7C-4 webhook
// endpoint config surface. Tenant is supplied by the handler (from the
// principal); the service never trusts a tenant in the request body. Tests stub
// this. The concrete implementation lives in internal/webhooks.
type WebhooksConfigService interface {
	// UpsertEndpoint creates the tenant's active endpoint (generating a signing
	// secret) or updates its URL (preserving the existing secret). The returned
	// Endpoint carries the secret so the PUT response can show it.
	UpsertEndpoint(ctx context.Context, tenantID, rawURL string) (webhooks.Endpoint, error)
	// GetEndpoint returns the tenant's active endpoint, or webhooks.ErrNotFound.
	GetEndpoint(ctx context.Context, tenantID string) (webhooks.Endpoint, error)
}

// WebhooksHandler serves PUT/GET /v1/admin/webhook-endpoint. Authorization is
// enforced by route middleware; tenant comes from the authenticated principal,
// never from the path or body. MVP: exactly one active endpoint per tenant.
type WebhooksHandler struct {
	Service WebhooksConfigService
}

func NewWebhooksHandler(svc WebhooksConfigService) *WebhooksHandler {
	return &WebhooksHandler{Service: svc}
}

// webhookEndpointRequest is the PUT body. tenant_id is rejected by readJSONBody.
type webhookEndpointRequest struct {
	URL string `json:"url"`
}

// webhookEndpointResponse is the GET response (and the shared shape): no secret.
type webhookEndpointResponse struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	IsActive bool   `json:"is_active"`
}

// webhookEndpointCreateResponse is the PUT response: includes the signing
// secret so the caller can verify X-DreamChat-Signature. On update the existing
// secret is preserved and returned.
type webhookEndpointCreateResponse struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	IsActive bool   `json:"is_active"`
	Secret   string `json:"secret"`
}

// Put upserts the tenant's active webhook endpoint and returns 200 with the
// signing secret.
func (h *WebhooksHandler) Put(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}
	var req webhookEndpointRequest
	if !readJSONBody(w, r, &req) {
		return
	}
	if !validWebhookURL(req.URL) {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "url must be a valid absolute http(s) URL")
		return
	}
	endpoint, err := h.Service.UpsertEndpoint(r.Context(), principal.TenantID, req.URL)
	if err != nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not save webhook endpoint")
		return
	}
	writeJSON(w, http.StatusOK, webhookEndpointCreateResponse{
		ID:       endpoint.ID,
		URL:      endpoint.URL,
		IsActive: endpoint.IsActive,
		Secret:   endpoint.Secret,
	})
}

// Get returns the tenant's active webhook endpoint WITHOUT the secret, or 404.
func (h *WebhooksHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}
	endpoint, err := h.Service.GetEndpoint(r.Context(), principal.TenantID)
	if err != nil {
		if errors.Is(err, webhooks.ErrNotFound) {
			httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "no webhook endpoint configured")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not read webhook endpoint")
		return
	}
	writeJSON(w, http.StatusOK, webhookEndpointResponse{
		ID:       endpoint.ID,
		URL:      endpoint.URL,
		IsActive: endpoint.IsActive,
	})
}

// validWebhookURL reports whether s is a syntactically valid absolute http(s)
// URL with a host. It does not perform a network check.
func validWebhookURL(s string) bool {
	if s == "" {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}
