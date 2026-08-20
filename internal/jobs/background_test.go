package jobs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// bgStubDoer answers the birefnet call with a canned response and records the request.
type bgStubDoer struct {
	status       int
	responseBody string
	lastBody     string
	lastAuth     string
	lastURL      string
}

func (d *bgStubDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		d.lastBody = string(raw)
	}
	d.lastAuth = req.Header.Get("Authorization")
	d.lastURL = req.URL.String()
	status := d.status
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(d.responseBody)),
		Header:     make(http.Header),
	}, nil
}

// The remover speaks fal's documented shape: the image travels as a data URI, the Portrait model is
// pinned, and a sync data-URI answer comes back decoded. This is the whole transparent-sprite
// mechanism, so its wire shape is pinned rather than assumed.
func TestFalBackgroundRemover_RoundTripsADataURI(t *testing.T) {
	cleaned := tinyPNGBytes()
	doer := &bgStubDoer{responseBody: `{"image":{"url":"data:image/png;base64,` +
		base64.StdEncoding.EncodeToString(cleaned) + `","content_type":"image/png"}}`}
	r := &FalBackgroundRemover{BaseURL: "https://fal.run", APIKey: "key-test", Doer: doer}

	out, err := r.Remove(context.Background(), providers.ProviderImage{Bytes: []byte{0x1, 0x2}, ContentType: "image/png"})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if string(out.Bytes) != string(cleaned) || out.ContentType != "image/png" {
		t.Fatalf("output = %d bytes %q, want the decoded data-URI PNG", len(out.Bytes), out.ContentType)
	}
	if doer.lastAuth != "Key key-test" {
		t.Fatalf("Authorization = %q, want the fal key header", doer.lastAuth)
	}
	if !strings.Contains(doer.lastURL, "/fal-ai/birefnet") {
		t.Fatalf("URL = %q, want the birefnet endpoint", doer.lastURL)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(doer.lastBody), &sent); err != nil {
		t.Fatalf("request body does not parse: %v", err)
	}
	if sent["model"] != "Portrait" {
		t.Fatalf("model = %v, want the portrait-tuned segmentation model", sent["model"])
	}
	if sent["output_format"] != "png" {
		t.Fatalf("output_format = %v, want png — alpha needs a format that can carry it", sent["output_format"])
	}
	if u, _ := sent["image_url"].(string); !strings.HasPrefix(u, "data:image/png;base64,") {
		t.Fatalf("image_url = %.40v, want a data URI — nothing is uploaded anywhere first", sent["image_url"])
	}
}

// A provider-side failure surfaces as an error, never as the original opaque image passed through.
func TestFalBackgroundRemover_FailureIsAnErrorNotAPassthrough(t *testing.T) {
	doer := &bgStubDoer{status: 500, responseBody: `{"detail":"model exploded"}`}
	r := &FalBackgroundRemover{BaseURL: "https://fal.run", APIKey: "k", Doer: doer}
	if _, err := r.Remove(context.Background(), providers.ProviderImage{Bytes: []byte{0x1}}); err == nil {
		t.Fatal("a 500 from the remover was swallowed")
	}
}

// countingRemover stands in for the fal call in pack tests: it returns a distinct, decodable PNG so
// the tier encoder downstream proves the REMOVED bytes (not the provider's) are what got stored.
type countingRemover struct{ calls int }

func (c *countingRemover) Remove(_ context.Context, _ providers.ProviderImage) (providers.ProviderImage, error) {
	c.calls++
	return providers.ProviderImage{Bytes: tinyPNGBytes(), ContentType: "image/png"}, nil
}

// A transparent pack removes the background of every generated variant, and each caller-defined
// cell renders EXACTLY the subject prose the caller authored — the platform holds no vocabulary of
// its own. Aspect ratio rides every cell.
func TestProcessPackTransparentCallerDefinedCells(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	provider := &selectiveProvider{}
	variants := []string{"emotion_neutral", "emotion_happy", "emotion_angry", "emotion_sad"}
	seedPackJob(repo, "job_sprites", "pack_sprites", JobTypeCharacterPack, variants)
	job := repo.jobs["job_sprites"]
	job.InputPayload["output_background"] = "transparent"
	job.InputPayload["aspect_ratio"] = "3:4"
	job.InputPayload["variant_prompts"] = map[string]any{
		"emotion_neutral": "a weathered duelist, bust portrait, calm expression",
		"emotion_happy":   "a weathered duelist, bust portrait, warm open smile",
		"emotion_angry":   "a weathered duelist, bust portrait, furrowed brow",
		"emotion_sad":     "a weathered duelist, bust portrait, downcast eyes",
	}
	repo.jobs["job_sprites"] = job

	remover := &countingRemover{}
	w := newPackWorker(repo, assetsRepo, provider, nil)
	w.Background = remover
	if err := w.ProcessPack(context.Background(), "job_sprites"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	if len(repo.packAssets) != 4 {
		t.Fatalf("assets = %d, want the four caller-defined variants", len(repo.packAssets))
	}
	if remover.calls != 4 {
		t.Fatalf("remover ran %d time(s), want once per variant", remover.calls)
	}
	joined := strings.Join(provider.calls, "\n")
	for _, subject := range []string{"calm expression", "warm open smile", "furrowed brow", "downcast eyes"} {
		if !strings.Contains(joined, subject) {
			t.Fatalf("no prompt carried the caller-authored subject %q", subject)
		}
	}
	// The 5A shape (name — key) must be GONE for authored cells: the caller's prose is the whole
	// subject, not a suffix on an identifier.
	if strings.Contains(joined, "Captain Mira — emotion_neutral") {
		t.Fatalf("an authored cell still rendered the name-dash-key prompt shape:\n%s", joined)
	}
}

// A cell with NO caller prompt keeps the 5A prompt shape — the passthrough is additive, not a
// rewrite of existing pack behavior.
func TestProcessPackUnauthoredCellKeepsLegacyPrompt(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	provider := &selectiveProvider{}
	seedPackJob(repo, "job_legacy", "pack_legacy", JobTypeCharacterPack, []string{"neutral_front_portrait"})

	w := newPackWorker(repo, assetsRepo, provider, nil)
	if err := w.ProcessPack(context.Background(), "job_legacy"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	if len(provider.calls) != 1 || !strings.Contains(provider.calls[0], "Captain Mira — neutral_front_portrait") {
		t.Fatalf("prompt = %v, want the unchanged 5A name-dash-key shape", provider.calls)
	}
}

// Transparency promised with no remover configured fails CLOSED: no variant ships opaque under a
// transparent request, and the pack reports failure rather than quiet wrong output.
func TestProcessPackTransparentFailsClosedWithoutRemover(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	provider := &selectiveProvider{}
	seedPackJob(repo, "job_noremover", "pack_noremover", JobTypeCharacterPack,
		[]string{"emotion_neutral", "emotion_happy", "emotion_angry", "emotion_sad"})
	job := repo.jobs["job_noremover"]
	job.InputPayload["output_background"] = "transparent"
	repo.jobs["job_noremover"] = job

	w := newPackWorker(repo, assetsRepo, provider, nil)
	if err := w.ProcessPack(context.Background(), "job_noremover"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	if len(repo.packAssets) != 0 {
		t.Fatalf("assets = %d, want none — an opaque sprite under a transparent promise is the failure mode this guards", len(repo.packAssets))
	}
}

// An opaque (default) pack never touches the remover — today's behavior, byte for byte.
func TestProcessPackOpaqueSkipsTheRemover(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	provider := &selectiveProvider{}
	seedPackJob(repo, "job_opaque", "pack_opaque", JobTypeCharacterPack, []string{"neutral_front_portrait"})

	remover := &countingRemover{}
	w := newPackWorker(repo, assetsRepo, provider, nil)
	w.Background = remover
	if err := w.ProcessPack(context.Background(), "job_opaque"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	if remover.calls != 0 {
		t.Fatalf("remover ran %d time(s) on an opaque pack", remover.calls)
	}
	if len(repo.packAssets) != 1 {
		t.Fatalf("assets = %d, want the one variant", len(repo.packAssets))
	}
}
