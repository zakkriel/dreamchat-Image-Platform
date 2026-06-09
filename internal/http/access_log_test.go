package http

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLogIdentityAuthenticatedRequest(t *testing.T) {
	var buf bytes.Buffer
	deps := newTestDeps(t, newStubRepo(), "dev", true)
	deps.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	r := NewRouter(deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/styles", nil)
	req.Header.Set("Authorization", "Bearer "+testPrefix+"_"+testSecret)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	authedLine := findLogLine(t, buf.Bytes(), "/v1/styles")
	if got := authedLine["tenant_id"]; got != testTenantID {
		t.Fatalf("expected tenant_id=%q, got %v", testTenantID, got)
	}
	if got := authedLine["token_id"]; got != testTokenID {
		t.Fatalf("expected token_id=%q, got %v", testTokenID, got)
	}
}

func TestAccessLogIdentityUnauthenticatedRequest(t *testing.T) {
	var buf bytes.Buffer
	deps := newTestDeps(t, newStubRepo(), "dev", true)
	deps.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	r := NewRouter(deps)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	line := findLogLine(t, buf.Bytes(), "/health")
	if line["tenant_id"] != "" {
		t.Fatalf("expected empty tenant_id, got %v", line["tenant_id"])
	}
	if line["token_id"] != "" {
		t.Fatalf("expected empty token_id, got %v", line["token_id"])
	}
}

func TestRawBearerNeverInLogs(t *testing.T) {
	var buf bytes.Buffer
	repo := newStubRepo()
	deps := newTestDeps(t, repo, "dev", true)
	deps.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	r := NewRouter(deps)

	raw := testPrefix + "_" + testSecret
	req := httptest.NewRequest(http.MethodGet, "/v1/styles", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if bytes.Contains(buf.Bytes(), []byte(testSecret)) {
		t.Fatalf("logs contained raw secret portion")
	}
	if bytes.Contains(buf.Bytes(), []byte(raw)) {
		t.Fatalf("logs contained raw bearer token")
	}
}

func findLogLine(t *testing.T, raw []byte, pathContains string) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal(line, &parsed); err != nil {
			continue
		}
		if msg, _ := parsed["msg"].(string); msg != "http_request" {
			continue
		}
		if p, _ := parsed["path"].(string); strings.Contains(p, pathContains) {
			return parsed
		}
	}
	t.Fatalf("no http_request log line found for path containing %q in: %s", pathContains, raw)
	return nil
}
