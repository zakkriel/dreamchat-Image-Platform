package bfl

import (
	"context"
	"encoding/json"
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

// A 402 from submit means the ACCOUNT IS UNPAID. Measured 2026-09-01: 875 jobs
// in 24h all failed `bfl: submit returned status 402`, each one re-submitted by
// the world backend's 2-minute reconciler sweep. Those doomed submits drained the
// platform's 1000-requests/hour token budget to zero, and because the READ path
// (assetURL) shares that budget, every already-rendered picture in the product
// became unfetchable - a total art blackout caused entirely by retrying a refusal
// that cannot change. A 402 is therefore classified terminal at the adapter, the
// same way a moderation refusal already is.
func TestGenerateUnpaidAccountIsTerminal(t *testing.T) {
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(402, `{"detail":"insufficient credit"}`), nil
		},
	}}
	_, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "a harbour at dusk"})
	if err == nil {
		t.Fatal("a 402 submit must fail")
	}
	if !errors.Is(err, providers.ErrProviderUnpaid) {
		t.Fatalf("a 402 must carry ErrProviderUnpaid so the worker stops retrying it; got %v", err)
	}
}

// A 429 or 5xx is the opposite case and must stay retryable: the account is fine
// and the next attempt can genuinely succeed. Pinning both directions keeps a
// future "classify more statuses" edit from sweeping transient errors into the
// terminal bucket, which would strand recoverable renders forever.
func TestGenerateRateLimitStaysRetryable(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
			func(r *http.Request) (*http.Response, error) { return jsonResp(code, `{}`), nil },
		}}
		_, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "x"})
		if err == nil {
			t.Fatalf("status %d must fail", code)
		}
		if errors.Is(err, providers.ErrProviderUnpaid) {
			t.Fatalf("status %d is transient and must NOT be terminal: %v", code, err)
		}
	}
}

// THE PERMISSIVENESS DIAL MUST BE SENT, NOT INHERITED.
//
// The provider research (docs/research/2026-08-08-alpha-channel-generation.md §1.5)
// records the criterion this platform picked its provider on: "a permissiveness dial
// and prompt-rewriting off by default - that is why we are on fal, and it is worth
// protecting". config.go says the same thing about the fal safety checker in one line:
// "leaving it unset would silently import the vendor's policy as the product boundary."
//
// That protection was applied to fal and never to BFL. BFL's safety_tolerance defaults
// to 2 of 6 - near the strictest - and the adapter sent only {prompt,width,height}, so
// every background this product has ever rendered was moderated at the vendor's default
// while the world's own latitude block said otherwise. Measured 2026-09-01: 20 slots
// terminal on "Content Moderated".
//
// The value is a product decision, so the request must carry it explicitly. Sending
// nothing is the one outcome this test exists to forbid.
func TestSubmitCarriesAnExplicitSafetyTolerance(t *testing.T) {
	var body map[string]any
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			return jsonResp(200, `{"id":"req-st","polling_url":"https://bfl.test/poll?id=req-st"}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"id":"req-st","status":"Ready","result":{"sample":"https://bfl.test/img.png"}}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("PNGDATA")), Header: http.Header{}}, nil
		},
	}}

	if _, err := testProvider(doer).Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "a harbour at dusk"}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	got, ok := body["safety_tolerance"]
	if !ok {
		t.Fatal("submit body must carry safety_tolerance; omitting it imports the vendor's default policy as the product boundary")
	}
	// The NUMBER is pinned deliberately, not compared against the constant: asserting
	// `got == defaultSafetyTolerance` is a tautology that passes when someone quietly
	// moves the constant back to the vendor's stricter default. 6 is the most permissive
	// BFL offers, and it is a product decision - changing it must break this test and be
	// argued for, not slip through as a one-character edit.
	// JSON numbers decode as float64.
	if n, isNum := got.(float64); !isNum || n != 6 {
		t.Fatalf("safety_tolerance must be 6, the most permissive BFL offers; got %v", got)
	}
	if defaultSafetyTolerance != 6 {
		t.Fatalf("the default itself must be 6, got %d", defaultSafetyTolerance)
	}
}

// The dial is a deployment decision, so it must be overridable without editing code -
// the same shape as FAL_SAFETY_CHECKER. A hardcoded constant would make the product
// boundary a code change in a vendor adapter.
func TestSafetyToleranceIsConfigurable(t *testing.T) {
	var body map[string]any
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			return jsonResp(200, `{"id":"req-st2","polling_url":"https://bfl.test/poll?id=req-st2"}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"id":"req-st2","status":"Ready","result":{"sample":"https://bfl.test/img.png"}}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("PNGDATA")), Header: http.Header{}}, nil
		},
	}}

	p := New("test-key",
		WithHTTPClient(doer),
		WithBaseURL("https://bfl.test"),
		WithPollInterval(time.Millisecond),
		WithTimeout(2*time.Second),
		WithSafetyTolerance(3),
	)
	if _, err := p.Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "x"}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if n, _ := body["safety_tolerance"].(float64); n != 3 {
		t.Fatalf("configured tolerance must reach the wire, got %v", body["safety_tolerance"])
	}
}
