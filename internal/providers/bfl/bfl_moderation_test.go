package bfl

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// TestGenerateModeratedIsContentPolicyRejected pins the moderation
// classification: BFL's "Request Moderated" / "Content Moderated" terminal
// statuses surface as providers.ErrContentPolicyRejected (distinct from a
// generic provider failure) so the worker can refuse to fallback-walk past a
// content-policy decision and callers see provider_content_rejected.
func TestGenerateModeratedIsContentPolicyRejected(t *testing.T) {
	for _, status := range []string{"Request Moderated", "Content Moderated"} {
		doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
			func(r *http.Request) (*http.Response, error) {
				return jsonResp(200, `{"id":"req-m","polling_url":"https://bfl.test/poll?id=req-m"}`), nil
			},
			func(r *http.Request) (*http.Response, error) {
				return jsonResp(200, `{"id":"req-m","status":"`+status+`"}`), nil
			},
		}}
		_, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "x"})
		if !errors.Is(err, providers.ErrContentPolicyRejected) {
			t.Fatalf("%s: expected ErrContentPolicyRejected, got %v", status, err)
		}
		if !errors.Is(err, ErrProvider) {
			t.Fatalf("%s: expected ErrProvider wrap preserved, got %v", status, err)
		}
	}
}

// TestGenerateErrorStatusIsNotContentPolicy pins the inverse: a generic
// terminal error status must NOT classify as a content-policy rejection
// (misclassifying infra failures would wrongly disable fallback).
func TestGenerateErrorStatusIsNotContentPolicy(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"id":"req-e","polling_url":"https://bfl.test/poll?id=req-e"}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"id":"req-e","status":"Error"}`), nil
		},
	}}
	_, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "x"})
	if errors.Is(err, providers.ErrContentPolicyRejected) {
		t.Fatalf("generic Error status must not classify as content policy, got %v", err)
	}
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("expected ErrProvider, got %v", err)
	}
}
