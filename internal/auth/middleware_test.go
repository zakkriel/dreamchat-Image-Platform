package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testPepper      = "test-pepper"
	testPrefix      = "dci_dev_abc123"
	testSecret      = "supersecret"
	testTokenID     = "tok_test"
	testTenantID    = "tenant_test"
	testEnvironment = "dev"
)

type stubRepo struct {
	token  Token
	getErr error
}

func newStubRepo() *stubRepo {
	hash := sha256.Sum256([]byte(testSecret + testPepper))
	return &stubRepo{
		token: Token{
			ID:          testTokenID,
			TenantID:    testTenantID,
			TokenHash:   hex.EncodeToString(hash[:]),
			Scopes:      []string{"images:read", "images:write"},
			Environment: testEnvironment,
			Status:      "active",
		},
	}
}

func (s *stubRepo) GetActiveAPITokenByPrefix(_ context.Context, prefix string) (Token, error) {
	if s.getErr != nil {
		return Token{}, s.getErr
	}
	if prefix != testPrefix {
		return Token{}, ErrTokenNotFound
	}
	return s.token, nil
}

func (s *stubRepo) TouchAPITokenLastUsed(_ context.Context, _ string) error { return nil }

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestVerifyHappyPath(t *testing.T) {
	repo := newStubRepo()
	principal, rej := Verify(context.Background(), repo, "Bearer "+testPrefix+"_"+testSecret, testPepper, testEnvironment)
	if rej != nil {
		t.Fatalf("expected success, got %+v", rej)
	}
	if principal.TokenID != testTokenID || principal.TenantID != testTenantID {
		t.Fatalf("unexpected principal: %+v", principal)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	repo := newStubRepo()
	_, rej := Verify(context.Background(), repo, "Bearer "+testPrefix+"_wrongsecret", testPepper, testEnvironment)
	if rej == nil || rej.Reason != ReasonHashMismatch {
		t.Fatalf("expected hash mismatch, got %+v", rej)
	}
}

func TestVerifyRejectsRevokedToken(t *testing.T) {
	repo := newStubRepo()
	repo.token.Status = "revoked"
	_, rej := Verify(context.Background(), repo, "Bearer "+testPrefix+"_"+testSecret, testPepper, testEnvironment)
	if rej == nil || rej.Reason != ReasonInactive {
		t.Fatalf("expected inactive token, got %+v", rej)
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	repo := newStubRepo()
	past := time.Now().Add(-time.Hour)
	repo.token.ExpiresAt = &past
	_, rej := Verify(context.Background(), repo, "Bearer "+testPrefix+"_"+testSecret, testPepper, testEnvironment)
	if rej == nil || rej.Reason != ReasonExpired {
		t.Fatalf("expected expired token, got %+v", rej)
	}
}

func TestVerifyRejectsEnvironmentMismatch(t *testing.T) {
	repo := newStubRepo()
	_, rej := Verify(context.Background(), repo, "Bearer "+testPrefix+"_"+testSecret, testPepper, "live")
	if rej == nil || rej.Reason != ReasonEnvironmentInvalid {
		t.Fatalf("expected environment mismatch, got %+v", rej)
	}
}

func TestVerifyRejectsMissingAuthorization(t *testing.T) {
	repo := newStubRepo()
	_, rej := Verify(context.Background(), repo, "", testPepper, testEnvironment)
	if rej == nil || rej.Reason != ReasonMissing {
		t.Fatalf("expected missing authorization, got %+v", rej)
	}
}

func TestVerifyRejectsMalformedAuthorization(t *testing.T) {
	repo := newStubRepo()
	cases := []string{
		"Basic abc",
		"Bearer",
		"Bearer ",
		"Bearer nounderscore",
		"Bearer dci_dev_abc_",
	}
	for _, header := range cases {
		_, rej := Verify(context.Background(), repo, header, testPepper, testEnvironment)
		if rej == nil || rej.Reason != ReasonMalformed {
			t.Fatalf("expected malformed for %q, got %+v", header, rej)
		}
	}
}

func TestVerifyAllowsFutureExpiry(t *testing.T) {
	repo := newStubRepo()
	future := time.Now().Add(time.Hour)
	repo.token.ExpiresAt = &future
	principal, rej := Verify(context.Background(), repo, "Bearer "+testPrefix+"_"+testSecret, testPepper, testEnvironment)
	if rej != nil || principal == nil {
		t.Fatalf("expected success with future expiry, got %+v", rej)
	}
}

func TestRequireScopesSupersetPasses(t *testing.T) {
	called := false
	handler := RequireScopes("images:read")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	principal := &Principal{Scopes: []string{"images:read", "images:write", "extra"}}
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ContextWithPrincipal(context.Background(), principal))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("expected handler called with 200, got code=%d called=%v", rec.Code, called)
	}
}

func TestRequireScopesRejectsSubset(t *testing.T) {
	handler := RequireScopes("images:read", "images:write")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("downstream handler should not run")
	}))

	principal := &Principal{Scopes: []string{"images:read"}}
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ContextWithPrincipal(context.Background(), principal))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("expected application/problem+json, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("expected code=forbidden body, got %s", rec.Body.String())
	}
}

func TestRequireScopesRejectsMissingPrincipal(t *testing.T) {
	handler := RequireScopes("images:read")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("downstream handler should not run")
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestVerifyBackendErrorBubbles(t *testing.T) {
	boom := errors.New("boom")
	repo := newStubRepo()
	repo.getErr = boom
	_, rej := Verify(context.Background(), repo, "Bearer "+testPrefix+"_"+testSecret, testPepper, testEnvironment)
	if rej == nil || rej.Reason != ReasonBackendError {
		t.Fatalf("expected backend error reason, got %+v", rej)
	}
	if !errors.Is(rej.Err, boom) {
		t.Fatalf("expected wrapped boom, got %v", rej.Err)
	}
}
