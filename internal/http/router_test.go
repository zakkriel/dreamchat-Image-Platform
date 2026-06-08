package http

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthEndpoint(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r := NewRouter(logger)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get(HeaderRequestID); got == "" {
		t.Fatalf("expected %s header", HeaderRequestID)
	}
	var body HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("expected status ok, got %s", body.Status)
	}
}

func TestRequestIDPassthrough(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r := NewRouter(logger)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set(HeaderRequestID, "fixed-id")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get(HeaderRequestID); got != "fixed-id" {
		t.Fatalf("expected request id passthrough, got %q", got)
	}
}
