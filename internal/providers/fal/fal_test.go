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

// routingDoer dispatches by method+URL substring (not call sequence) so a test
// can model an indefinitely-running request plus a cancel call.
type routingDoer struct {
	mu        sync.Mutex
	cancelHit int
	cancelReq []*http.Request
}

func (d *routingDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	url := req.URL.String()
	switch {
	case req.Method == http.MethodPost && strings.HasSuffix(url, "/"+modelPath):
		return jsonResp(200, `{"request_id":"req_t","status_url":"https://fal.test/status/req_t","response_url":"https://fal.test/result/req_t","cancel_url":"https://fal.test/cancel/req_t"}`), nil
	case strings.Contains(url, "/status/"):
		// Never completes → forces the bounded context to time out.
		return jsonResp(200, `{"status":"IN_PROGRESS"}`), nil
	case strings.Contains(url, "/cancel/"):
		d.cancelHit++
		d.cancelReq = append(d.cancelReq, req)
		return jsonResp(200, `{"status":"CANCELLED"}`), nil
	default:
		return jsonResp(200, `{}`), nil
	}
}

func (d *routingDoer) cancels() (int, []*http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancelHit, append([]*http.Request(nil), d.cancelReq...)
}

// TestCancelCalledAfterPostSubmitTimeout proves that when the request times out
// AFTER a successful submit, the adapter best-effort cancels the orphaned fal
// request (PUT cancel_url) rather than leaving it queued and billing.
func TestCancelCalledAfterPostSubmitTimeout(t *testing.T) {
	doer := &routingDoer{}
	p := New("test-key",
		WithHTTPClient(doer),
		WithBaseURL("https://fal.test"),
		WithPollInterval(time.Millisecond),
		WithTimeout(10*time.Millisecond), // expires while status stays IN_PROGRESS
	)

	_, err := p.Generate(context.Background(), providers.ProviderGenerateRequest{
		Prompt:        "Captain Mira",
		ReferenceURLs: []string{"https://ref.test/anchor-1.png"},
	})
	if err == nil {
		t.Fatal("expected a timeout error")
	}

	hits, reqs := doer.cancels()
	if hits != 1 {
		t.Fatalf("expected exactly 1 cancel call, got %d", hits)
	}
	if reqs[0].Method != http.MethodPut {
		t.Fatalf("cancel method = %s, want PUT", reqs[0].Method)
	}
	if reqs[0].Header.Get("Authorization") != "Key test-key" {
		t.Fatalf("cancel missing auth header: %q", reqs[0].Header.Get("Authorization"))
	}
	if reqs[0].URL.String() != "https://fal.test/cancel/req_t" {
		t.Fatalf("cancel url = %s", reqs[0].URL.String())
	}
}

// TestProviderErrorDoesNotCancel proves a genuine provider error (not a local
// timeout) does NOT trigger a cancel — there is nothing orphaned to cancel.
func TestProviderErrorDoesNotCancel(t *testing.T) {
	doer := &routingDoer2{}
	p := New("test-key",
		WithHTTPClient(doer),
		WithBaseURL("https://fal.test"),
		WithPollInterval(time.Millisecond),
		WithTimeout(2*time.Second),
	)
	_, err := p.Generate(context.Background(), providers.ProviderGenerateRequest{
		Prompt:        "x",
		ReferenceURLs: []string{"https://ref.test/a.png"},
	})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if doer.cancelHit != 0 {
		t.Fatalf("provider error must not trigger cancel; got %d", doer.cancelHit)
	}
}

// routingDoer2 returns a terminal provider error status on poll (not a timeout).
type routingDoer2 struct {
	mu        sync.Mutex
	cancelHit int
}

func (d *routingDoer2) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	url := req.URL.String()
	switch {
	case req.Method == http.MethodPost && strings.HasSuffix(url, "/"+modelPath):
		return jsonResp(200, `{"request_id":"req_e","status_url":"https://fal.test/status/req_e","response_url":"https://fal.test/result/req_e","cancel_url":"https://fal.test/cancel/req_e"}`), nil
	case strings.Contains(url, "/status/"):
		return jsonResp(500, `{"detail":"boom"}`), nil // provider error, not a timeout
	case strings.Contains(url, "/cancel/"):
		d.cancelHit++
		return jsonResp(200, `{}`), nil
	default:
		return jsonResp(200, `{}`), nil
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
