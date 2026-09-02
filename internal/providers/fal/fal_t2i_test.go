package fal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

func testFluxPro11(doer Doer) *Provider {
	return NewFluxPro11("test-key",
		WithHTTPClient(doer),
		WithBaseURL("https://fal.test"),
		WithPollInterval(time.Millisecond),
		WithTimeout(2*time.Second),
	)
}

// A BACKGROUND HAS NO REFERENCE IMAGE, WHICH IS WHY THIS ENDPOINT EXISTS.
//
// Until now `scene_capable` had exactly one real route: bfl. fal's only adapter is
// FLUX.1 Kontext, which is reference-conditioned and fails closed without a
// reference - correctly, so a recurring character never drifts. A place or a world
// cover has nothing to condition on, so it could never go to fal, and BFL was a
// single point of failure for every background in the product. When BFL's account
// ran dry on 2026-09-01, 875 scene jobs failed in 24h with nothing to fall back to.
//
// fal-ai/flux-pro/v1.1 is prompt-only on the same queue API this adapter already
// speaks (verified against fal's published schema: prompt, image_size, num_images,
// enable_safety_checker, safety_tolerance, seed).
func TestFluxPro11RendersFromAPromptAlone(t *testing.T) {
	var body map[string]any
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.String(), "/"+modelPathFluxPro11) {
				t.Errorf("submit url = %s, want the v1.1 path", r.URL.String())
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			return jsonResp(200, `{"request_id":"req_t1","status_url":"https://fal.test/status/req_t1","response_url":"https://fal.test/result/req_t1"}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"status":"COMPLETED"}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"images":[{"url":"https://fal.test/img.png","content_type":"image/png","width":1024,"height":768}],"seed":7}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("PNGDATA")), Header: http.Header{}}, nil
		},
	}}

	// No ReferenceURLs at all: the whole point.
	res, err := testFluxPro11(doer).Generate(context.Background(), providers.ProviderGenerateRequest{
		Prompt:      "the rearmost district of Ossa, sloping to the water",
		AspectRatio: "4:3",
	})
	if err != nil {
		t.Fatalf("a prompt-only scene must render without a reference: %v", err)
	}
	if len(res.Images) != 1 || len(res.Images[0].Bytes) == 0 {
		t.Fatalf("expected one downloaded image, got %+v", res.Images)
	}
	if _, sent := body["image_urls"]; sent {
		t.Error("prompt-only must not send image_urls; that is the kontext contract, not this one")
	}
	if _, sent := body["image_url"]; sent {
		t.Error("prompt-only must not send image_url")
	}
	// fal v1.1 takes image_size presets, NOT aspect_ratio. Sending aspect_ratio
	// would be an invented contract, the mistake the kontext-dev branch calls out.
	if got := body["image_size"]; got != "landscape_4_3" {
		t.Errorf("4:3 must map to fal's landscape_4_3 preset, got %v", got)
	}
	if _, sent := body["aspect_ratio"]; sent {
		t.Error("v1.1 has no aspect_ratio; image_size owns geometry")
	}
}

// The permissiveness must be SENT, not inherited. fal documents
// enable_safety_checker default TRUE and safety_tolerance default 2 of 5 for this
// endpoint - so an omitted dial silently imports the vendor's policy as the product
// boundary, which is the exact wording config.go uses about the kontext-dev switch.
// The dial is the criterion this platform picked fal on; it must hold on every fal
// endpoint, not only the one it was first noticed on.
func TestFluxPro11SendsThePermissivenessExplicitly(t *testing.T) {
	var body map[string]any
	doer := &stubDoer{handlers: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			return jsonResp(200, `{"request_id":"req_t2","status_url":"https://fal.test/status/req_t2","response_url":"https://fal.test/result/req_t2"}`), nil
		},
		func(r *http.Request) (*http.Response, error) { return jsonResp(200, `{"status":"COMPLETED"}`), nil },
		func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"images":[{"url":"https://fal.test/i.png","content_type":"image/png"}],"seed":1}`), nil
		},
		func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("PNG")), Header: http.Header{}}, nil
		},
	}}

	if _, err := testFluxPro11(doer).Generate(context.Background(), providers.ProviderGenerateRequest{Prompt: "a harbour"}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	checker, present := body["enable_safety_checker"]
	if !present {
		t.Fatal("enable_safety_checker must be sent; fal defaults it TRUE")
	}
	if checker != false {
		t.Errorf("safety checker must default OFF, matching the stated no-censorship posture; got %v", checker)
	}
	tol, present := body["safety_tolerance"]
	if !present {
		t.Fatal("safety_tolerance must be sent; fal defaults it to 2 of 5")
	}
	// 5 rather than 6: fal documents 1..5 for this endpoint (1 strictest, 5 most
	// permissive) while BFL documents 1..6. Pinned as a number so a quiet revert to
	// the vendor's 2 fails here rather than shipping.
	if n, ok := tol.(float64); !ok || n != 5 {
		t.Errorf("safety_tolerance must be 5, the most permissive fal documents for v1.1; got %v", tol)
	}
}

// Capability reconciliation (image:ADR-016) compares ADAPTER-REPORTED capabilities
// against the seeded route and fails closed, so the adapter must claim
// scene_capable and must NOT claim the reference-conditioned capabilities it cannot
// honour. Claiming identity_capable here would let identity work resolve to a
// prompt-only endpoint and render a different character every call.
func TestFluxPro11CapabilitiesAreSceneOnly(t *testing.T) {
	caps := testFluxPro11(&stubDoer{}).Capabilities()
	if caps.ProviderID != ProviderIDFluxPro11 || caps.ModelName != ModelNameFluxPro11 {
		t.Fatalf("must report its own id/model, got %s/%s", caps.ProviderID, caps.ModelName)
	}
	if caps.RequiresReferenceImage {
		t.Error("a prompt-only endpoint must not require a reference image")
	}
	has := func(c providers.Capability) bool {
		for _, got := range caps.Capabilities {
			if got == c {
				return true
			}
		}
		return false
	}
	if !has(providers.CapabilitySceneCapable) {
		t.Error("must advertise scene_capable: that is the gap it fills")
	}
	if has(providers.CapabilityIdentityCapable) || has(providers.CapabilityPackCapable) {
		t.Error("must NOT advertise identity/pack: prompt-only cannot hold a recurring character")
	}
}
