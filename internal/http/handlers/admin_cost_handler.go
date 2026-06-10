package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/admincost"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

// AdminCostService is the handler-facing slice of the admin cost surface.
// Tests stub this.
type AdminCostService interface {
	CreatePrice(ctx context.Context, actor admincost.Actor, in admincost.CreatePriceInput) (admincost.PriceEntry, error)
	ListPrices(ctx context.Context) ([]admincost.PriceEntry, error)
	GetPrice(ctx context.Context, id string) (admincost.PriceEntry, error)
	UpdatePrice(ctx context.Context, actor admincost.Actor, id string, in admincost.UpdatePriceInput) (admincost.PriceEntry, error)
	CreateBudget(ctx context.Context, actor admincost.Actor, in admincost.CreateBudgetInput) (admincost.Budget, error)
	ListBudgets(ctx context.Context) ([]admincost.Budget, error)
	UpdateBudget(ctx context.Context, actor admincost.Actor, id string, in admincost.UpdateBudgetInput) (admincost.Budget, error)
	ListReservations(ctx context.Context, f admincost.ReservationFilter) ([]admincost.ReservationRow, error)
}

// AdminCostHandler serves the price-book, cost-budget, and cost-reservation
// admin endpoints. Authorization (admin:costs) is enforced by route
// middleware; the handler validates payloads and shapes responses.
type AdminCostHandler struct {
	Service AdminCostService
}

func NewAdminCostHandler(svc AdminCostService) *AdminCostHandler {
	return &AdminCostHandler{Service: svc}
}

// Immutable fields rejected with 400 when present in a mutation body.
var (
	priceUpdateForbidden  = []string{"provider_id", "model_id", "operation_type", "unit_type", "price_per_unit", "currency"}
	budgetCreateForbidden = []string{"reserved_amount", "spent_amount"}
	budgetUpdateForbidden = []string{"reserved_amount", "spent_amount", "tenant_id", "scope_type", "scope_id", "period"}
)

// ---------------------------------------------------------------------------
// Price book
// ---------------------------------------------------------------------------

func (h *AdminCostHandler) CreatePrice(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	body, ok := readBodyMap(w, r)
	if !ok {
		return
	}
	in := admincost.CreatePriceInput{
		ProviderID:    rawStr(body["provider_id"]),
		ModelID:       rawStr(body["model_id"]),
		OperationType: rawStr(body["operation_type"]),
		UnitType:      rawStr(body["unit_type"]),
		Currency:      rawStr(body["currency"]),
	}
	if dec, ok := rawDecimal(body["price_per_unit"]); ok {
		in.PricePerUnit = dec
	}
	if s, ok := rawStrPtr(body["source"]); ok {
		in.Source = s
	}
	if n, ok := rawStrPtr(body["notes"]); ok {
		in.Notes = n
	}
	entry, err := h.Service.CreatePrice(r.Context(), actor, in)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (h *AdminCostHandler) ListPrices(w http.ResponseWriter, r *http.Request) {
	entries, err := h.Service.ListPrices(r.Context())
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (h *AdminCostHandler) GetPrice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "price_id")
	if id == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "price_id is required")
		return
	}
	entry, err := h.Service.GetPrice(r.Context(), id)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (h *AdminCostHandler) UpdatePrice(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "price_id")
	if id == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "price_id is required")
		return
	}
	body, ok := readBodyMap(w, r)
	if !ok {
		return
	}
	if rejectForbidden(w, r, body, priceUpdateForbidden) {
		return
	}
	var in admincost.UpdatePriceInput
	if raw, found := body["effective_to"]; found {
		in.SetEffectiveTo = true
		if t, ok := rawTimePtr(raw); ok {
			in.EffectiveTo = t
		} else if !isJSONNull(raw) {
			httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "effective_to must be an RFC3339 timestamp or null")
			return
		}
	}
	if b, ok := rawBoolPtr(body["is_active"]); ok {
		in.IsActive = b
	}
	if n, ok := rawStrPtr(body["notes"]); ok {
		in.Notes = n
	}
	entry, err := h.Service.UpdatePrice(r.Context(), actor, id, in)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// ---------------------------------------------------------------------------
// Cost budgets
// ---------------------------------------------------------------------------

func (h *AdminCostHandler) CreateBudget(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	body, ok := readBodyMap(w, r)
	if !ok {
		return
	}
	if rejectForbidden(w, r, body, budgetCreateForbidden) {
		return
	}
	in := admincost.CreateBudgetInput{
		TenantID:  rawStr(body["tenant_id"]),
		ScopeType: rawStr(body["scope_type"]),
		ScopeID:   rawStr(body["scope_id"]),
		Period:    rawStr(body["period"]),
		Currency:  rawStr(body["currency"]),
		Status:    rawStr(body["status"]),
	}
	if dec, ok := rawDecimal(body["limit_amount"]); ok {
		in.LimitAmount = dec
	}
	budget, err := h.Service.CreateBudget(r.Context(), actor, in)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, budget)
}

func (h *AdminCostHandler) ListBudgets(w http.ResponseWriter, r *http.Request) {
	budgets, err := h.Service.ListBudgets(r.Context())
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"budgets": budgets})
}

func (h *AdminCostHandler) UpdateBudget(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "budget_id")
	if id == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "budget_id is required")
		return
	}
	body, ok := readBodyMap(w, r)
	if !ok {
		return
	}
	if rejectForbidden(w, r, body, budgetUpdateForbidden) {
		return
	}
	var in admincost.UpdateBudgetInput
	if dec, ok := rawDecimal(body["limit_amount"]); ok {
		in.LimitAmount = &dec
	}
	if s, ok := rawStrPtr(body["status"]); ok {
		in.Status = s
	}
	budget, err := h.Service.UpdateBudget(r.Context(), actor, id, in)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, budget)
}

// ---------------------------------------------------------------------------
// Cost reservations (read-only)
// ---------------------------------------------------------------------------

func (h *AdminCostHandler) ListReservations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f admincost.ReservationFilter
	if v := q.Get("tenant_id"); v != "" {
		f.TenantID = &v
	}
	if v := q.Get("status"); v != "" {
		f.Status = &v
	}
	if v := q.Get("created_after"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "created_after must be an RFC3339 timestamp")
			return
		}
		f.CreatedAfter = &t
	}
	if v := q.Get("created_before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "created_before must be an RFC3339 timestamp")
			return
		}
		f.CreatedBefore = &t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "limit must be a non-negative integer")
			return
		}
		f.Limit = n
	}
	rows, err := h.Service.ListReservations(r.Context(), f)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reservations": rows})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (h *AdminCostHandler) actor(w http.ResponseWriter, r *http.Request) (admincost.Actor, bool) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return admincost.Actor{}, false
	}
	return admincost.Actor{
		TokenID:   principal.TokenID,
		TenantID:  principal.TenantID,
		RequestID: telemetry.RequestIDFromContext(r.Context()),
	}, true
}

func (h *AdminCostHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, admincost.ErrInvalid):
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, err.Error())
	case errors.Is(err, admincost.ErrNotFound):
		httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "resource not found")
	default:
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "admin cost operation failed")
	}
}

// readBodyMap reads a JSON object body into a raw-message map so the handler
// can both detect forbidden fields and extract typed values.
func readBodyMap(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err != nil {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "could not read request body")
		return nil, false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "request body required")
		return nil, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "request body must be a JSON object")
		return nil, false
	}
	return m, true
}

func rejectForbidden(w http.ResponseWriter, r *http.Request, body map[string]json.RawMessage, forbidden []string) bool {
	for _, k := range forbidden {
		if _, found := body[k]; found {
			httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, k+" is not a mutable field")
			return true
		}
	}
	return false
}

func rawStr(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func rawStrPtr(raw json.RawMessage) (*string, bool) {
	if raw == nil || isJSONNull(raw) {
		return nil, false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil, false
	}
	return &s, true
}

func rawBoolPtr(raw json.RawMessage) (*bool, bool) {
	if raw == nil || isJSONNull(raw) {
		return nil, false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return nil, false
	}
	return &b, true
}

func rawTimePtr(raw json.RawMessage) (*time.Time, bool) {
	if raw == nil || isJSONNull(raw) {
		return nil, false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, false
	}
	return &t, true
}

// rawDecimal preserves the exact decimal text whether the value arrives as a
// JSON number (literal bytes) or a JSON string ("0.0100").
func rawDecimal(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", false
	}
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	if s[0] == '"' {
		var str string
		if json.Unmarshal(raw, &str) != nil {
			return "", false
		}
		return str, true
	}
	return s, true
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}
