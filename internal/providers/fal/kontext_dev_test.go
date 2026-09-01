package fal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// capturingDoer records the submit body and then drives the adapter through a
// minimal successful queue lifecycle: submit -> status COMPLETED -> result ->
// image download.
type capturingDoer struct {
	submitBody map[string]any
	submitPath string
}

func (d *capturingDoer) Do(req *http.Request) (*http.Response, error) {
	body := func(s string, ct string) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(s)),
			Header:     http.Header{"Content-Type": []string{ct}},
		}
	}
	switch {
	case req.Method == http.MethodPost:
		d.submitPath = req.URL.Path
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &d.submitBody)
		return body(`{"request_id":"r1","status_url":"http://x/status","response_url":"http://x/result","cancel_url":"http://x/cancel"}`, "application/json"), nil
	case req.URL.Path == "/status":
		return body(`{"status":"COMPLETED"}`, "application/json"), nil
	case req.URL.Path == "/result":
		return body(`{"images":[{"url":"http://x/img.png","width":512,"height":512,"content_type":"image/png"}],"seed":7}`, "application/json"), nil
	default:
		// The image download: one pixel of PNG is enough, the adapter only
		// forwards bytes.
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\n")),
			Header:     http.Header{"Content-Type": []string{"image/png"}},
		}, nil
	}
}

func devRequest() providers.ProviderGenerateRequest {
	return providers.ProviderGenerateRequest{
		JobID:         "job_1",
		Operation:     providers.OperationTextToImage,
		Prompt:        "a captain, three-quarter view",
		ReferenceURLs: []string{"https://s3/anchor-one.png", "https://s3/anchor-two.png"},
		AspectRatio:   "1:1",
	}
}

// The [dev] endpoint's contract differs from [pro] in three ways that matter,
// and sending [pro]'s shape to it would be an invented contract.
func TestKontextDevSubmitShape(t *testing.T) {
	d := &capturingDoer{}
	p := NewKontextDev("key", WithHTTPClient(d), WithBaseURL("http://x"))
	if _, err := p.Generate(context.Background(), devRequest()); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !strings.Contains(d.submitPath, modelPathKontextDev) {
		t.Fatalf("expected the dev endpoint path %q, got %q", modelPathKontextDev, d.submitPath)
	}
	// 1. ONE reference, as image_url — not [pro]'s image_urls array.
	if got, ok := d.submitBody["image_url"].(string); !ok || got != "https://s3/anchor-one.png" {
		t.Fatalf("expected the first anchor as image_url, got %v", d.submitBody["image_url"])
	}
	if _, present := d.submitBody["image_urls"]; present {
		t.Fatalf("image_urls belongs to the [pro] endpoint and must not be sent to [dev]: %v", d.submitBody)
	}
	// 2. Output follows the reference, which is what makes a small reference a
	//    cheap render.
	if got := d.submitBody["resolution_mode"]; got != resolutionModeMatchInput {
		t.Fatalf("expected resolution_mode=%q, got %v", resolutionModeMatchInput, got)
	}
	// 3. aspect_ratio does not exist on this endpoint.
	if _, present := d.submitBody["aspect_ratio"]; present {
		t.Fatalf("[dev] has no aspect_ratio; resolution_mode owns geometry: %v", d.submitBody)
	}
	if got := d.submitBody["output_format"]; got != "png" {
		t.Fatalf("expected png at the source (matting reads these bytes), got %v", got)
	}
}

// The safety checker must be SENT, and default to off. The platform is on fal
// specifically because permissiveness is configurable here; the endpoint
// defaults it ON, so an unset field silently adopts the vendor's policy as the
// product boundary.
func TestKontextDevSendsSafetyCheckerAndDefaultsOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
		want bool
	}{
		{"default is off", nil, false},
		{"explicitly on", []Option{WithSafetyChecker(true)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &capturingDoer{}
			opts := append([]Option{WithHTTPClient(d), WithBaseURL("http://x")}, tc.opts...)
			p := NewKontextDev("key", opts...)
			if _, err := p.Generate(context.Background(), devRequest()); err != nil {
				t.Fatalf("Generate: %v", err)
			}
			got, present := d.submitBody["enable_safety_checker"]
			if !present {
				t.Fatalf("enable_safety_checker must be sent explicitly, body was %v", d.submitBody)
			}
			if got != tc.want {
				t.Fatalf("expected enable_safety_checker=%v, got %v", tc.want, got)
			}
		})
	}
}

// Capability reconciliation compares ADAPTER-REPORTED identity against the
// seeded route and fails closed (image:ADR-016). A variant reporting the [pro]
// identity would either disable its own route at boot or pass reconciliation
// while calling a different model.
func TestKontextDevReportsItsOwnIdentity(t *testing.T) {
	dev := NewKontextDev("key").Capabilities()
	if dev.ProviderID != ProviderIDKontextDev || dev.ModelName != ModelNameKontextDev {
		t.Fatalf("dev must report %s/%s, got %s/%s",
			ProviderIDKontextDev, ModelNameKontextDev, dev.ProviderID, dev.ModelName)
	}
	if !dev.RequiresReferenceImage {
		t.Fatal("dev is reference-conditioned; it must require a reference or the worker will not gather anchors")
	}
	if dev.Synthetic {
		t.Fatal("dev is a real provider; marking it synthetic would exclude it under ALLOW_SYNTHETIC_PROVIDERS=false")
	}
	pro := New("key").Capabilities()
	if pro.ProviderID != ProviderID || pro.ModelName != ModelName {
		t.Fatalf("the [pro] adapter must be unchanged, got %s/%s", pro.ProviderID, pro.ModelName)
	}
}

// The [pro] shape must not drift while the variant is added: it still sends the
// array, and never the dev-only fields.
func TestKontextProShapeUnchanged(t *testing.T) {
	d := &capturingDoer{}
	p := New("key", WithHTTPClient(d), WithBaseURL("http://x"))
	if _, err := p.Generate(context.Background(), devRequest()); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	urls, ok := d.submitBody["image_urls"].([]any)
	if !ok || len(urls) != 2 {
		t.Fatalf("[pro] must send both anchors as image_urls, got %v", d.submitBody["image_urls"])
	}
	for _, devOnly := range []string{"image_url", "resolution_mode", "enable_safety_checker"} {
		if _, present := d.submitBody[devOnly]; present {
			t.Fatalf("%q is a [dev] field and must not appear in a [pro] request: %v", devOnly, d.submitBody)
		}
	}
	if got := d.submitBody["aspect_ratio"]; got != "1:1" {
		t.Fatalf("[pro] still takes aspect_ratio, got %v", got)
	}
}
