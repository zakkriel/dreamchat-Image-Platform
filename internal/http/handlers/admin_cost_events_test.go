package handlers

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdminListCostEventsPassesFilters(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	rec := sendAdmin(t, r, http.MethodGet,
		"/v1/admin/cost-events?tenant_id=tenant_x&token_id=tok_1&job_id=job_1&status=completed"+
			"&provider_id=fal&model_id=pm_1&world_id=w1&limit=5",
		[]string{"admin:costs"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	f := svc.lastCostEventF
	if f.TenantID == nil || *f.TenantID != "tenant_x" {
		t.Fatalf("expected tenant_id filter, got %+v", f.TenantID)
	}
	if f.JobID == nil || *f.JobID != "job_1" {
		t.Fatalf("expected job_id filter, got %+v", f.JobID)
	}
	if f.Status == nil || *f.Status != "completed" {
		t.Fatalf("expected status filter, got %+v", f.Status)
	}
	if f.ProviderID == nil || *f.ProviderID != "fal" {
		t.Fatalf("expected provider_id filter, got %+v", f.ProviderID)
	}
	// The runbook groups spend by token, world, provider and model; every one
	// of those filters must reach the query or a cost investigation silently
	// reads the wrong rows.
	if f.TokenID == nil || *f.TokenID != "tok_1" {
		t.Fatalf("expected token_id filter, got %+v", f.TokenID)
	}
	if f.ModelID == nil || *f.ModelID != "pm_1" {
		t.Fatalf("expected model_id filter, got %+v", f.ModelID)
	}
	if f.WorldID == nil || *f.WorldID != "w1" {
		t.Fatalf("expected world_id filter, got %+v", f.WorldID)
	}
	if f.Limit != 5 {
		t.Fatalf("expected limit=5, got %d", f.Limit)
	}
	assertListKey(t, rec, "cost_events")
}

// A malformed timestamp must be rejected, not silently dropped — a cost
// investigation that quietly ignores its time window returns the wrong rows.
func TestAdminListCostEventsRejectsBadTimestamp(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	rec := sendAdmin(t, r, http.MethodGet, "/v1/admin/cost-events?created_after=yesterday", []string{"admin:costs"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-RFC3339 created_after, got %d", rec.Code)
	}
}

func TestAdminListCostEventsRequiresScope(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	rec := sendAdmin(t, r, http.MethodGet, "/v1/admin/cost-events", []string{"jobs:read"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without admin:costs, got %d", rec.Code)
	}
}

// The metrics endpoint must serve the Prometheus exposition content type: a
// scraper keys off it, and JSON here would be silently unscrapeable.
func TestAdminMetricsServesPrometheusExposition(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	rec := sendAdmin(t, r, http.MethodGet, "/v1/admin/metrics", []string{"admin:costs"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("expected the Prometheus exposition content type, got %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"# TYPE asset_cache_hit_count counter", "# TYPE actual_cost_usd gauge", "provider_call_count"} {
		if !strings.Contains(body, want) {
			t.Fatalf("exposition missing %q, got:\n%s", want, body)
		}
	}
}

func TestAdminMetricsRequiresScope(t *testing.T) {
	svc := &stubAdminCostService{}
	r := newAdminRouter(svc)
	rec := sendAdmin(t, r, http.MethodGet, "/v1/admin/metrics", []string{"jobs:read"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without admin:costs, got %d", rec.Code)
	}
}
