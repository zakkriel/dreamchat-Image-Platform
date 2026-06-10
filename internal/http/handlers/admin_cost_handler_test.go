package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/admincost"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
)

// stubAdminCostService records calls and returns canned values.
type stubAdminCostService struct {
	createPriceCalls  int
	updatePriceCalls  int
	createBudgetCalls int
	updateBudgetCalls int
	lastReservationF  admincost.ReservationFilter
	lastCreateBudget  admincost.CreateBudgetInput
	lastUpdatePrice   admincost.UpdatePriceInput
}

func (s *stubAdminCostService) CreatePrice(_ context.Context, _ admincost.Actor, _ admincost.CreatePriceInput) (admincost.PriceEntry, error) {
	s.createPriceCalls++
	return admincost.PriceEntry{ID: "price_new", IsActive: true}, nil
}
func (s *stubAdminCostService) ListPrices(context.Context) ([]admincost.PriceEntry, error) {
	return []admincost.PriceEntry{{ID: "price_1"}}, nil
}
func (s *stubAdminCostService) GetPrice(_ context.Context, id string) (admincost.PriceEntry, error) {
	return admincost.PriceEntry{ID: id}, nil
}
func (s *stubAdminCostService) UpdatePrice(_ context.Context, _ admincost.Actor, id string, in admincost.UpdatePriceInput) (admincost.PriceEntry, error) {
	s.updatePriceCalls++
	s.lastUpdatePrice = in
	return admincost.PriceEntry{ID: id}, nil
}
func (s *stubAdminCostService) CreateBudget(_ context.Context, _ admincost.Actor, in admincost.CreateBudgetInput) (admincost.Budget, error) {
	s.createBudgetCalls++
	s.lastCreateBudget = in
	return admincost.Budget{ID: "bud_new", TenantID: in.TenantID}, nil
}
func (s *stubAdminCostService) ListBudgets(context.Context) ([]admincost.Budget, error) {
	return []admincost.Budget{{ID: "bud_1"}}, nil
}
func (s *stubAdminCostService) UpdateBudget(_ context.Context, _ admincost.Actor, id string, _ admincost.UpdateBudgetInput) (admincost.Budget, error) {
	s.updateBudgetCalls++
	return admincost.Budget{ID: id}, nil
}
func (s *stubAdminCostService) ListReservations(_ context.Context, f admincost.ReservationFilter) ([]admincost.ReservationRow, error) {
	s.lastReservationF = f
	return []admincost.ReservationRow{{ID: "resv_1"}}, nil
}

func newAdminRouter(svc AdminCostService) chi.Router {
	h := NewAdminCostHandler(svc)
	r := chi.NewRouter()
	r.Route("/v1/admin", func(a chi.Router) {
		a.Use(auth.RequireScopes("admin:costs"))
		a.Post("/price-book", h.CreatePrice)
		a.Get("/price-book", h.ListPrices)
		a.Get("/price-book/{price_id}", h.GetPrice)
		a.Put("/price-book/{price_id}", h.UpdatePrice)
		a.Post("/cost-budgets", h.CreateBudget)
		a.Get("/cost-budgets", h.ListBudgets)
		a.Put("/cost-budgets/{budget_id}", h.UpdateBudget)
		a.Get("/cost-reservations", h.ListReservations)
	})
	return r
}

func sendAdmin(t *testing.T, h http.Handler, method, path string, scopes []string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if raw, ok := body.(json.RawMessage); ok {
			buf.Write(raw)
		} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf).WithContext(authedContext(tenantA, scopes...))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAdminPriceCreateRequiresScope(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	body := map[string]any{"provider_id": "mock", "model_id": "pm_mock_v1", "operation_type": "text_to_image", "unit_type": "image", "price_per_unit": "0.0100"}

	// Without admin:costs → 403.
	rec := sendAdmin(t, r, http.MethodPost, "/v1/admin/price-book", []string{"images:write"}, body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without admin:costs, got %d", rec.Code)
	}
	if svc.createPriceCalls != 0 {
		t.Fatalf("service must not be called without scope")
	}

	// With admin:costs → 201.
	rec = sendAdmin(t, r, http.MethodPost, "/v1/admin/price-book", []string{"admin:costs"}, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 with admin:costs, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.createPriceCalls != 1 {
		t.Fatalf("expected service called once, got %d", svc.createPriceCalls)
	}
}

func TestAdminPriceUpdateRejectsImmutableField(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	rec := sendAdmin(t, r, http.MethodPut, "/v1/admin/price-book/price_1", []string{"admin:costs"},
		map[string]any{"price_per_unit": "0.0200"})
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
	if svc.updatePriceCalls != 0 {
		t.Fatalf("service must not be called when an immutable field is present")
	}
}

func TestAdminPriceUpdateAllowsMutableFields(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	rec := sendAdmin(t, r, http.MethodPut, "/v1/admin/price-book/price_1", []string{"admin:costs"},
		map[string]any{"is_active": false, "notes": "retired"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.updatePriceCalls != 1 {
		t.Fatalf("expected update called once, got %d", svc.updatePriceCalls)
	}
	if svc.lastUpdatePrice.IsActive == nil || *svc.lastUpdatePrice.IsActive {
		t.Fatalf("expected is_active=false passed to service")
	}
	if svc.lastUpdatePrice.Notes == nil || *svc.lastUpdatePrice.Notes != "retired" {
		t.Fatalf("expected notes passed to service")
	}
}

func TestAdminBudgetCreateRejectsReservedAndSpent(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	for _, field := range []string{"reserved_amount", "spent_amount"} {
		body := map[string]any{
			"tenant_id": tenantA, "scope_type": "tenant", "scope_id": tenantA,
			"period": "daily", "limit_amount": "5.0000", field: "1.0000",
		}
		rec := sendAdmin(t, r, http.MethodPost, "/v1/admin/cost-budgets", []string{"admin:costs"}, body)
		assertError(t, rec, http.StatusBadRequest, "invalid_request")
	}
	if svc.createBudgetCalls != 0 {
		t.Fatalf("service must not be called when reserved/spent present")
	}
}

func TestAdminBudgetCreateHappyPath(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	body := map[string]any{
		"tenant_id": tenantA, "scope_type": "tenant", "scope_id": tenantA,
		"period": "daily", "limit_amount": "5.0000",
	}
	rec := sendAdmin(t, r, http.MethodPost, "/v1/admin/cost-budgets", []string{"admin:costs"}, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.lastCreateBudget.LimitAmount != "5.0000" || svc.lastCreateBudget.Period != "daily" {
		t.Fatalf("unexpected create input: %+v", svc.lastCreateBudget)
	}
}

func TestAdminBudgetUpdateAllowsLimitAndStatus(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	rec := sendAdmin(t, r, http.MethodPut, "/v1/admin/cost-budgets/bud_1", []string{"admin:costs"},
		map[string]any{"limit_amount": "9.0000", "status": "paused"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.updateBudgetCalls != 1 {
		t.Fatalf("expected update called once, got %d", svc.updateBudgetCalls)
	}
}

func TestAdminBudgetUpdateRejectsReservedAndSpent(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	for _, field := range []string{"reserved_amount", "spent_amount", "tenant_id", "scope_type", "scope_id", "period"} {
		rec := sendAdmin(t, r, http.MethodPut, "/v1/admin/cost-budgets/bud_1", []string{"admin:costs"},
			map[string]any{field: "x"})
		assertError(t, rec, http.StatusBadRequest, "invalid_request")
	}
	if svc.updateBudgetCalls != 0 {
		t.Fatalf("service must not be called when a forbidden field is present")
	}
}

func TestAdminListReservationsPassesFilters(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	rec := sendAdmin(t, r, http.MethodGet, "/v1/admin/cost-reservations?tenant_id=tenant_x&status=committed&limit=5", []string{"admin:costs"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.lastReservationF.TenantID == nil || *svc.lastReservationF.TenantID != "tenant_x" {
		t.Fatalf("expected tenant_id filter passed, got %+v", svc.lastReservationF.TenantID)
	}
	if svc.lastReservationF.Status == nil || *svc.lastReservationF.Status != "committed" {
		t.Fatalf("expected status filter passed, got %+v", svc.lastReservationF.Status)
	}
	if svc.lastReservationF.Limit != 5 {
		t.Fatalf("expected limit=5, got %d", svc.lastReservationF.Limit)
	}
}

func TestAdminListReservationsRequiresScope(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	rec := sendAdmin(t, r, http.MethodGet, "/v1/admin/cost-reservations", []string{"jobs:read"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without admin:costs, got %d", rec.Code)
	}
}

// assertListKey checks the response has exactly the wanted top-level key (an
// array) and none of the forbidden legacy keys — the OpenAPI list contract.
func assertListKey(t *testing.T, rec *httptest.ResponseRecorder, want string, forbidden ...string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := decode[map[string]any](t, rec)
	if _, ok := body[want].([]any); !ok {
		t.Fatalf("expected top-level array key %q, got body=%s", want, rec.Body.String())
	}
	if len(body) != 1 {
		t.Fatalf("expected exactly one top-level key %q, got %v", want, body)
	}
	for _, f := range forbidden {
		if _, present := body[f]; present {
			t.Fatalf("forbidden legacy key %q present in response: %s", f, rec.Body.String())
		}
	}
}

func TestAdminListPricesResponseKey(t *testing.T) {
	r := newAdminRouter(&stubAdminCostService{})
	rec := sendAdmin(t, r, http.MethodGet, "/v1/admin/price-book", []string{"admin:costs"}, nil)
	assertListKey(t, rec, "entries", "price_book")
}

func TestAdminListBudgetsResponseKey(t *testing.T) {
	r := newAdminRouter(&stubAdminCostService{})
	rec := sendAdmin(t, r, http.MethodGet, "/v1/admin/cost-budgets", []string{"admin:costs"}, nil)
	assertListKey(t, rec, "budgets", "cost_budgets")
}

func TestAdminListReservationsResponseKey(t *testing.T) {
	r := newAdminRouter(&stubAdminCostService{})
	rec := sendAdmin(t, r, http.MethodGet, "/v1/admin/cost-reservations", []string{"admin:costs"}, nil)
	assertListKey(t, rec, "reservations", "cost_reservations")
}
