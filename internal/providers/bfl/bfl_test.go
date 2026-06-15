package bfl

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

// stubDoer routes requests by URL substring so a single client can model the
// submit → poll → download flow with no real network.
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
		WithBaseURL("https://bfl.test"),
		WithPollInterval(time.Millisecond),
		WithTimeout(2*time.Second),
	)
}

func TestCapabilitiesFloor(t *testing.T) {
	caps := New("k").Capabilities()
	if caps.ProviderID != ProviderID {
		t.Fatalf("provider id = %q", caps.ProviderID)
	}
	if caps.PreviewCapability != providers.PreviewCapabilityNone {
		t.Fatalf("expected no_preview, got %q", caps.PreviewCapability)
	}
	if caps.SupportsHighRes {
		t.Fatalf("must not claim high-res")
	}
	if caps.Synthetic {
		t.Fatalf("bfl is a real provider, not synthetic")
	}
	if caps.RequiresReferenceImage {
		t.Fatalf("bfl flux-pro-1.1 is prompt-only and must not require reference images")
	}
	forbidden := map[providers.Capability]bool{
		providers.CapabilityIdentityCapable:   true,
		providers.CapabilityPackCapable:       true,
		providers.CapabilityProductionCapable: true,
	}
	has := map[providers.Capability]bool{}
	for _, c := range caps.Capabilities {
		if forbidden[c] {
			t.Fatalf("must not advertise %q without benchmark evidence", c)
		}
		has[c] = true
	}
	// BFL remains scene_capable only: it must positively advertise scene_capable
	// (so it keeps serving scene/artifact generation) and nothing on the identity
	// axis.
	if !has[providers.CapabilitySceneCapable] {
		t.Fatalf("bfl must advertise scene_capable")
	}
}

func TestGenerateSubmitShapeAndSuccess(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		// submit
		func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodPost {
				t.Errorf("submit method = %s", r.Method)
			}
			if r.Header.Get("x-key") != "test-key" {
				t.Errorf("missing x-key header: %v", r.Header)
			}
			if !strings.HasSuffix(r.URL.String(), "/v1/flux-pro-1.1") {
				t.Errorf("submit url = %s", r.URL.String())
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"prompt"`) {
				t.Errorf("submit body missing prompt: %s", body)
			}
			return jsonResp(200, `{"id":"req-1","polling_url":"https://bfl.test/poll?id=req-1"}`), nil
		},
		// poll: pending then ready (handler index clamps; use a counter via separate handlers)
		func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("x-key") != "test-key" {
				t.Errorf("poll missing x-key")
			}
			return jsonResp(200, `{"id":"req-1","status":"Pending"}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"id":"req-1","status":"Ready","result":{"sample":"https://cdn.bfl.test/img.jpg"}}`), nil
		},
		// download
		func(r *http.Request) (*http.Response, error) {
			if !strings.Contains(r.URL.String(), "cdn.bfl.test") {
				t.Errorf("download url = %s", r.URL.String())
			}
			return bytesResp(200, "image/jpeg", []byte("JPEGDATA")), nil
		},
	}}

	res, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{
		Prompt: "a castle", Width: 1024, Height: 768, Seed: "42",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.Status != providers.JobStatusCompleted {
		t.Fatalf("status = %q", res.Status)
	}
	if len(res.Images) != 1 || string(res.Images[0].Bytes) != "JPEGDATA" {
		t.Fatalf("unexpected images: %+v", res.Images)
	}
	if res.Images[0].ContentType != "image/jpeg" {
		t.Fatalf("content type = %q", res.Images[0].ContentType)
	}
	if res.ProviderJobID != "req-1" {
		t.Fatalf("provider job id = %q", res.ProviderJobID)
	}
}

func TestGenerateProviderErrorStatus(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"id":"req-2","polling_url":"https://bfl.test/poll?id=req-2"}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"id":"req-2","status":"Error"}`), nil
		},
	}}
	_, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "x"})
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestGenerateSubmitHTTPError(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(500, `{"error":"boom"}`), nil
		},
	}}
	_, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "x"})
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("expected provider error for 500, got %v", err)
	}
}

func TestGenerateMalformedSubmitResponse(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `not json`), nil
		},
	}}
	_, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "x"})
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("expected provider error for malformed response, got %v", err)
	}
}

func TestGenerateContextCancellationDuringPoll(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"id":"req-3","polling_url":"https://bfl.test/poll?id=req-3"}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			// always pending → never ready; cancellation must break the loop
			return jsonResp(200, `{"id":"req-3","status":"Pending"}`), nil
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := testProvider(doer).Generate(ctx, providers.ProviderGenerateRequest{Prompt: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestGenerateTimeoutIsBounded(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"id":"req-4","polling_url":"https://bfl.test/poll?id=req-4"}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"id":"req-4","status":"Pending"}`), nil
		},
	}}
	p := New("test-key",
		WithHTTPClient(doer),
		WithBaseURL("https://bfl.test"),
		WithPollInterval(time.Millisecond),
		WithTimeout(30*time.Millisecond),
	)
	start := time.Now()
	_, err := p.Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "x"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout not bounded: %v", elapsed)
	}
}

func TestGenerateMissingAPIKey(t *testing.T) {
	p := New("", WithHTTPClient(&stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) { return jsonResp(200, `{}`), nil },
	}}))
	_, err := p.Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "x"})
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("expected provider error for missing key, got %v", err)
	}
}

func TestUpscaleAndPollStatusNotImplemented(t *testing.T) {
	p := New("k")
	if _, err := p.Upscale(context.Background(), providers.ProviderUpscaleRequest{}); !errors.Is(err, providers.ErrNotImplemented) {
		t.Fatalf("Upscale: %v", err)
	}
	if _, err := p.PollStatus(context.Background(), "x"); !errors.Is(err, providers.ErrNotImplemented) {
		t.Fatalf("PollStatus: %v", err)
	}
}
