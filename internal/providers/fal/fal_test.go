package fal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// stubDoer replays a fixed sequence of handlers, one per request, modelling the
// fal submit → poll → result → download flow with no real network.
type stubDoer struct {
	mu       sync.Mutex
	handlers []func(*http.Request) (*http.Response, error)
	requests []*http.Request
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	idx := len(s.requests) - 1
	if idx >= len(s.handlers) {
		idx = len(s.handlers) - 1
	}
	return s.handlers[idx](req)
}

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func bytesResp(status int, contentType string, body []byte) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body))), Header: h}
}

func testProvider(doer Doer) *Provider {
	return New("test-key",
		WithHTTPClient(doer),
		WithBaseURL("https://fal.test"),
		WithPollInterval(time.Millisecond),
		WithTimeout(2*time.Second),
	)
}

// TestCapabilitiesReferenceConditioned proves fal advertises a REAL,
// reference-conditioned identity/pack provider — and is honestly NOT
// production_capable until benchmarked.
func TestCapabilitiesReferenceConditioned(t *testing.T) {
	caps := New("k").Capabilities()
	if caps.ProviderID != ProviderID {
		t.Fatalf("provider id = %q", caps.ProviderID)
	}
	if caps.Synthetic {
		t.Fatalf("fal must be a real (non-synthetic) provider")
	}
	if !caps.RequiresReferenceImage {
		t.Fatalf("fal must require reference images")
	}
	has := map[providers.Capability]bool{}
	for _, c := range caps.Capabilities {
		has[c] = true
	}
	if !has[providers.CapabilityIdentityCapable] || !has[providers.CapabilityPackCapable] {
		t.Fatalf("expected identity_capable + pack_capable, got %v", caps.Capabilities)
	}
	if has[providers.CapabilityProductionCapable] {
		t.Fatalf("must not claim production_capable without benchmark evidence")
	}
}

// TestGenerateFailsClosedWithoutReference proves the adapter never renders a
// prompt-only image when no reference URL is supplied.
func TestGenerateFailsClosedWithoutReference(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) {
			t.Fatalf("provider must not be called without a reference URL")
			return nil, nil
		},
	}}
	_, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{
		Prompt: "Captain Mira, three-quarter portrait",
	})
	if !errors.Is(err, providers.ErrReferenceRequired) {
		t.Fatalf("expected ErrReferenceRequired, got %v", err)
	}
}

// TestGenerateSubmitShapeAndSuccess drives the full submit → poll → result →
// download flow and asserts the reference URLs are sent as image_urls.
func TestGenerateSubmitShapeAndSuccess(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		// submit
		func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodPost {
				t.Errorf("submit method = %s", r.Method)
			}
			if r.Header.Get("Authorization") != "Key test-key" {
				t.Errorf("missing/incorrect auth header: %q", r.Header.Get("Authorization"))
			}
			if !strings.HasSuffix(r.URL.String(), "/"+modelPath) {
				t.Errorf("submit url = %s", r.URL.String())
			}
			body, _ := io.ReadAll(r.Body)
			s := string(body)
			if !strings.Contains(s, `"image_urls"`) {
				t.Errorf("submit body missing image_urls: %s", s)
			}
			if !strings.Contains(s, "https://ref.test/anchor-1.png") {
				t.Errorf("submit body missing reference url: %s", s)
			}
			if !strings.Contains(s, `"prompt"`) {
				t.Errorf("submit body missing prompt: %s", s)
			}
			return jsonResp(200, `{"request_id":"req_1","status_url":"https://fal.test/status/req_1","response_url":"https://fal.test/result/req_1"}`), nil
		},
		// poll: still in progress
		func(r *http.Request) (*http.Response, error) {
			if !strings.Contains(r.URL.String(), "/status/req_1") {
				t.Errorf("poll url = %s", r.URL.String())
			}
			return jsonResp(200, `{"status":"IN_PROGRESS"}`), nil
		},
		// poll: completed
		func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"status":"COMPLETED"}`), nil
		},
		// result
		func(r *http.Request) (*http.Response, error) {
			if !strings.Contains(r.URL.String(), "/result/req_1") {
				t.Errorf("result url = %s", r.URL.String())
			}
			return jsonResp(200, `{"images":[{"url":"https://fal.media/out.png","width":1024,"height":1024,"content_type":"image/png"}],"seed":42}`), nil
		},
		// download
		func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://fal.media/out.png" {
				t.Errorf("download url = %s", r.URL.String())
			}
			return bytesResp(200, "image/png", []byte("PNGDATA")), nil
		},
	}}

	res, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{
		Prompt:        "Captain Mira, three-quarter portrait",
		Seed:          "7",
		AspectRatio:   "1:1",
		ReferenceURLs: []string{"https://ref.test/anchor-1.png"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.Status != providers.JobStatusCompleted {
		t.Fatalf("status = %q", res.Status)
	}
	if len(res.Images) != 1 || string(res.Images[0].Bytes) != "PNGDATA" {
		t.Fatalf("unexpected images: %+v", res.Images)
	}
	if res.Images[0].ContentType != "image/png" {
		t.Fatalf("content type = %q", res.Images[0].ContentType)
	}
	if res.Seed != "42" {
		t.Fatalf("seed = %q, want 42 (from result)", res.Seed)
	}
	if res.ProviderJobID != "req_1" {
		t.Fatalf("provider job id = %q", res.ProviderJobID)
	}
}

func TestGenerateMissingAPIKey(t *testing.T) {
	p := New("", WithHTTPClient(&stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) { return jsonResp(200, "{}"), nil },
	}}))
	_, err := p.Generate(context.Background(), providers.ProviderGenerateRequest{
		ReferenceURLs: []string{"https://ref.test/a.png"},
	})
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("expected ErrProvider for missing key, got %v", err)
	}
}

func TestGenerateSubmitErrorStatus(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) { return jsonResp(500, `{"detail":"boom"}`), nil },
	}}
	_, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{
		Prompt:        "x",
		ReferenceURLs: []string{"https://ref.test/a.png"},
	})
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("expected ErrProvider, got %v", err)
	}
}
