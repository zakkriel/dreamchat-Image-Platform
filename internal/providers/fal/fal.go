// Package fal implements the providers.ImageProvider interface for fal.ai, the
// first REAL reference-conditioned provider (recurring-character consistency).
//
// Model: fal-ai/flux-pro/kontext/multi (FLUX.1 Kontext [pro], multi-reference).
// Unlike BFL flux-pro-1.1 (scene_capable, prompt-only), FLUX.1 Kontext is
// reference-conditioned: it takes one or more reference image URLs plus a text
// prompt and renders the SAME subject in the prompted variation — the documented
// use case being character consistency and identity-preserving edits. This
// adapter therefore advertises identity_capable + pack_capable and REQUIRES at
// least one reference URL (it fails closed otherwise; it never falls back to a
// prompt-only render that would produce a different character).
//
// fal.ai queue API contract (https://docs.fal.ai/model-endpoints/queue/ and the
// model page https://fal.ai/models/fal-ai/flux-pro/kontext/max/multi/api). These
// are the only behaviours this adapter relies on; no undocumented behaviour is
// invented:
//
//   - Submit (async): POST {queueBaseURL}/{modelID} with header
//     `Authorization: Key <FAL_KEY>` and a JSON body
//     { "prompt", "image_urls": [...], "seed"?, "guidance_scale"?,
//     "aspect_ratio"?, "num_images": 1 }. A success response is JSON
//     { "request_id", "status_url", "response_url" }.
//   - Poll: GET status_url with the same auth header. Response JSON
//     { "status": "IN_QUEUE"|"IN_PROGRESS"|"COMPLETED", ... }. "COMPLETED" means
//     the result is available at response_url.
//   - Result: GET response_url with the same auth header. Response JSON
//     { "images": [ { "url", "width", "height", "content_type"? } ], "seed"? }.
//   - Download: GET images[0].url (no auth header required; it is a public
//     fal media URL).
//
// Pricing: fal bills FLUX.1 Kontext [pro] at $0.04 per output image (per-image
// unit, representable by the existing provider_model_prices schema). The [max]
// tier is $0.08; this adapter targets [pro] for the first slice.
//
// As with the BFL adapter, the adapter submits and polls internally until the
// image is ready, the context is cancelled, the provider errors, or a bounded
// timeout elapses — Generate returns final image bytes or an error (it does NOT
// introduce a new async job lifecycle). The HTTP client is injectable so unit
// tests exercise the full submit/poll/result/download flow against a stub with
// no real network access.
package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

const (
	ProviderID = "fal"
	// ModelName is the fal model id this adapter targets; it matches the
	// model_name seeded in migrations/0011_fal_provider_seed.sql.
	ModelName = "flux-pro-kontext-multi"
	// modelPath is the fal queue endpoint path for the model.
	modelPath = "fal-ai/flux-pro/kontext/multi"

	// ProviderIDKontextDev, ModelNameKontextDev and modelPathKontextDev are the
	// FLUX.1 Kontext [dev] endpoint: same model family, open weights, billed per
	// compute-second instead of per image. Seeded in migration 0020.
	ProviderIDKontextDev = "fal_dev"
	ModelNameKontextDev  = "flux-kontext-dev"
	modelPathKontextDev  = "fal-ai/flux-kontext/dev"

	// resolutionModeMatchInput makes the dev endpoint render at the reference
	// image's resolution. That is what turns a cheap small reference into a
	// cheap small render: the compute-second meter scales with pixels
	// (measured 8.45s at 1024 against 2.64s at 512).
	resolutionModeMatchInput = "match_input"

	defaultBaseURL      = "https://queue.fal.run"
	defaultPollInterval = 1 * time.Second
	defaultTimeout      = 120 * time.Second
	// cancelTimeout bounds the best-effort cancel call made after a local
	// timeout/cancellation. It uses a fresh context (the request context is
	// already done), so it must carry its own short deadline.
	cancelTimeout = 5 * time.Second

	statusInQueue    = "IN_QUEUE"
	statusInProgress = "IN_PROGRESS"
	statusCompleted  = "COMPLETED"
)

// ErrProvider is the base for provider-side failures so callers can
// errors.Is(err, fal.ErrProvider) without matching on strings.
var ErrProvider = errors.New("fal")

// Doer is the minimal HTTP surface the adapter needs; *http.Client satisfies
// it, and tests supply a stub.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Provider is the fal.ai adapter.
type Provider struct {
	apiKey       string
	baseURL      string
	modelPath    string
	httpClient   Doer
	pollInterval time.Duration
	timeout      time.Duration
	logger       *slog.Logger

	// kontextDev selects the [dev] endpoint's request shape: a SINGLE image_url
	// instead of the [pro] image_urls array, plus resolution_mode and the
	// safety-checker switch.
	kontextDev bool
	// safetyChecker is the [dev] endpoint's enable_safety_checker. Default
	// false — see WithSafetyChecker.
	safetyChecker bool
}

// Option configures a Provider (used to inject the HTTP client, base URL, and
// timing in tests).
type Option func(*Provider)

// WithHTTPClient injects the HTTP client (default &http.Client{}).
func WithHTTPClient(d Doer) Option { return func(p *Provider) { p.httpClient = d } }

// WithBaseURL overrides the queue base URL (default https://queue.fal.run).
func WithBaseURL(base string) Option { return func(p *Provider) { p.baseURL = base } }

// WithModelPath overrides the model path under the base URL.
func WithModelPath(path string) Option { return func(p *Provider) { p.modelPath = path } }

// WithPollInterval overrides the poll cadence (default 1s).
func WithPollInterval(d time.Duration) Option { return func(p *Provider) { p.pollInterval = d } }

// WithTimeout overrides the bounded submit+poll deadline (default 120s).
func WithTimeout(d time.Duration) Option { return func(p *Provider) { p.timeout = d } }

// WithLogger injects the structured logger used for cancel/orphan observability
// (default slog.Default()).
func WithLogger(l *slog.Logger) Option { return func(p *Provider) { p.logger = l } }

// New builds a fal.ai adapter. apiKey is the FAL_KEY; opts inject the HTTP
// client / endpoints / timing for tests.
func New(apiKey string, opts ...Option) *Provider {
	p := &Provider{
		apiKey:       apiKey,
		baseURL:      defaultBaseURL,
		modelPath:    modelPath,
		httpClient:   &http.Client{},
		pollInterval: defaultPollInterval,
		timeout:      defaultTimeout,
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.logger == nil {
		p.logger = slog.Default()
	}
	return p
}

// NewKontextDev builds the FLUX.1 Kontext [dev] adapter: the same
// reference-conditioned model family as [pro], on the open-weight endpoint.
//
// Why it exists: measured on this platform's own renders, dev holds a recurring
// character as well as pro on the drawn styles at roughly a quarter of the
// price, and its output size follows the reference image, so a small reference
// buys a small (cheap) render that the imaging package enlarges for delivery.
//
// It is registered under a DISTINCT provider id rather than overloading "fal".
// Capability and price are per (provider, model) in the route table, and the
// two endpoints differ in both — dev is billed per compute-second, pro at a
// flat per-image rate — so one id per endpoint keeps the route table honest
// (image:ADR-016, and the same reasoning image:ADR-017 used to keep fal off the
// bfl id).
func NewKontextDev(apiKey string, opts ...Option) *Provider {
	p := New(apiKey, opts...)
	p.kontextDev = true
	if p.modelPath == modelPath {
		p.modelPath = modelPathKontextDev
	}
	return p
}

// WithSafetyChecker enables or disables the dev endpoint's safety checker.
//
// This is the whole reason the platform is on fal. The provider research
// (docs/research/2026-08-08-alpha-channel-generation.md §1.5) records the
// selection criterion verbatim: a permissiveness dial plus prompt-rewriting off
// by default is "why we are on fal, and it is worth protecting", and OpenAI was
// rejected outright for having moderation with no off switch. The [pro]
// endpoint exposes a 1-6 dial; [dev] exposes a boolean, which is a cleaner fit
// for the same goal. Default OFF, matching the stated no-censorship posture.
func WithSafetyChecker(enabled bool) Option {
	return func(p *Provider) { p.safetyChecker = enabled }
}

// Capabilities advertises a reference-conditioned, identity/pack-capable real
// provider. It is intentionally NOT production_capable: that tier is claimed only
// after an acceptance/quality benchmark pass demonstrates recurring-character
// consistency (PRD 03 §8). RequiresReferenceImage is true so the worker gathers
// the identity's anchor assets into ReferenceURLs and fails closed when none
// exist. The capability set matches the model row seeded in migration 0011.
func (p *Provider) Capabilities() providers.ProviderCapabilities {
	id, model := ProviderID, ModelName
	if p.kontextDev {
		// Report the endpoint this adapter actually calls. Capability
		// reconciliation compares these against the seeded route (image:ADR-016,
		// fail closed), so a variant that reported the [pro] identity would
		// disable its own route at boot — or worse, pass reconciliation while
		// calling a different model.
		id, model = ProviderIDKontextDev, ModelNameKontextDev
	}
	return providers.ProviderCapabilities{
		ProviderID: id,
		ModelName:  model,
		Capabilities: []providers.Capability{
			providers.CapabilitySceneCapable,
			providers.CapabilityIdentityCapable,
			providers.CapabilityPackCapable,
		},
		PreviewCapability:      providers.PreviewCapabilityNone,
		SupportsHighRes:        false,
		MaxBatchSize:           1,
		SupportedAspects:       []string{"1:1", "16:9", "9:16", "4:3", "3:4"},
		Synthetic:              false,
		RequiresReferenceImage: true,
	}
}

// submitResponse is the fal async-submit body.
type submitResponse struct {
	RequestID   string `json:"request_id"`
	StatusURL   string `json:"status_url"`
	ResponseURL string `json:"response_url"`
	CancelURL   string `json:"cancel_url"`
}

// statusResponse is the fal queue status body.
type statusResponse struct {
	Status string `json:"status"`
}

// resultResponse is the fal result body fetched from response_url.
type resultResponse struct {
	Images []struct {
		URL         string `json:"url"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		ContentType string `json:"content_type"`
	} `json:"images"`
	Seed json.Number `json:"seed"`
}

// Generate submits a reference-conditioned request and polls until the image is
// ready, returning the final image bytes. It fails closed when no reference URL
// is supplied (ErrReferenceRequired) so a recurring-character render never
// silently degrades to a prompt-only image of a different character. It respects
// context cancellation and a bounded timeout.
func (p *Provider) Generate(ctx context.Context, req providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	if p.apiKey == "" {
		return providers.ProviderGenerateResult{}, fmt.Errorf("%w: missing API key", ErrProvider)
	}
	if len(req.ReferenceURLs) == 0 {
		// Fail closed: FLUX.1 Kontext is reference-conditioned. With no reference we
		// would produce an arbitrary subject, not the recurring character.
		return providers.ProviderGenerateResult{}, providers.ErrReferenceRequired
	}
	if err := ctx.Err(); err != nil {
		return providers.ProviderGenerateResult{}, err
	}

	// Bound the whole submit + poll flow so a stuck provider can never hang a
	// worker slot indefinitely.
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	submitted, err := p.submit(ctx, req)
	if err != nil {
		return providers.ProviderGenerateResult{}, err
	}

	result, err := p.awaitResult(ctx, submitted)
	if err != nil {
		// If the failure was a LOCAL timeout/cancellation (not a provider error),
		// the fal request is still queued/running and would keep billing — best-
		// effort cancel it so we never pay for an orphaned job.
		if ctx.Err() != nil {
			p.cancel(submitted)
		}
		return providers.ProviderGenerateResult{ProviderJobID: submitted.RequestID, ProviderRequestID: submitted.RequestID}, err
	}
	if len(result.Images) == 0 || result.Images[0].URL == "" {
		return providers.ProviderGenerateResult{ProviderJobID: submitted.RequestID, ProviderRequestID: submitted.RequestID}, fmt.Errorf("%w: completed result missing image url", ErrProvider)
	}

	img := result.Images[0]
	imgBytes, contentType, err := p.download(ctx, img.URL)
	if err != nil {
		return providers.ProviderGenerateResult{ProviderJobID: submitted.RequestID, ProviderRequestID: submitted.RequestID}, err
	}
	if img.ContentType != "" {
		contentType = img.ContentType
	}

	seed := req.Seed
	if s := result.Seed.String(); s != "" {
		seed = s
	}

	return providers.ProviderGenerateResult{
		ProviderJobID:     submitted.RequestID,
		ProviderRequestID: submitted.RequestID,
		Status:            providers.JobStatusCompleted,
		Images: []providers.ProviderImage{{
			URL:         img.URL,
			Bytes:       imgBytes,
			ContentType: contentType,
			Width:       img.Width,
			Height:      img.Height,
		}},
		Seed:     seed,
		Metadata: map[string]any{"provider": ProviderID, "model": ModelName},
	}, nil
}

// awaitResult polls until the request is COMPLETED, then fetches the result body.
// Split out of Generate so the timeout/cancellation cancel path wraps the whole
// post-submit phase.
func (p *Provider) awaitResult(ctx context.Context, submitted submitResponse) (resultResponse, error) {
	if err := p.poll(ctx, submitted); err != nil {
		return resultResponse{}, err
	}
	return p.fetchResult(ctx, submitted)
}

// cancel best-effort cancels an orphaned fal request after a local
// timeout/cancellation. It uses a FRESH bounded context (the request context is
// already done) and never returns an error — it only logs the outcome so an
// orphaned, still-billing request is observable. fal exposes cancellation as
// PUT {cancel_url} (https://docs.fal.ai/model-endpoints/queue/).
func (p *Provider) cancel(submitted submitResponse) {
	if submitted.CancelURL == "" {
		p.logger.Warn("fal: cannot cancel request (no cancel_url)", "request_id", submitted.RequestID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, submitted.CancelURL, nil)
	if err != nil {
		p.logger.Warn("fal: build cancel request failed", "request_id", submitted.RequestID, "error", err)
		return
	}
	p.setAuth(httpReq)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		p.logger.Warn("fal: cancel request failed", "request_id", submitted.RequestID, "error", err)
		return
	}
	defer drainClose(resp.Body)
	p.logger.Info("fal: cancelled orphaned request after local timeout/cancellation",
		"request_id", submitted.RequestID, "cancel_status", resp.StatusCode)
}

func (p *Provider) submit(ctx context.Context, req providers.ProviderGenerateRequest) (submitResponse, error) {
	body := map[string]any{
		"prompt":     req.Prompt,
		"num_images": 1,
		// fal defaults this endpoint's output_format to "jpeg". The bytes go
		// straight into matting and tier encoding, so the default made each
		// render a lossy round trip for nothing. It also actively hurt
		// transparent packs: the background remover has to resolve alpha on
		// hair, and JPEG ringing lands on exactly those high-frequency edges.
		// Ask for PNG at the source.
		"output_format": "png",
	}
	if p.kontextDev {
		// [dev] takes ONE reference (image_url), not [pro]'s image_urls array.
		// An identity with several anchors therefore conditions on its FIRST
		// anchor only — a real difference between the two endpoints, and the
		// reason the [pro] route stays seeded as the multi-anchor path.
		if len(req.ReferenceURLs) > 0 {
			body["image_url"] = req.ReferenceURLs[0]
		}
		// Output follows the reference resolution: a 512 reference renders 512,
		// which is what makes the compute-second meter cheap.
		body["resolution_mode"] = resolutionModeMatchInput
		// Explicit rather than defaulted: the endpoint defaults this to true,
		// and the platform's whole reason for being on fal is that the
		// permissiveness is ours to set.
		body["enable_safety_checker"] = p.safetyChecker
	} else {
		body["image_urls"] = req.ReferenceURLs
	}
	if req.AspectRatio != "" && !p.kontextDev {
		// [dev] has no aspect_ratio; resolution_mode owns output geometry and
		// sending both would be an invented contract.
		body["aspect_ratio"] = req.AspectRatio
	}
	if req.Seed != "" {
		if seed, err := strconv.Atoi(req.Seed); err == nil {
			body["seed"] = seed
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return submitResponse{}, fmt.Errorf("%w: marshal submit body: %v", ErrProvider, err)
	}

	endpoint := p.baseURL + "/" + p.modelPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return submitResponse{}, fmt.Errorf("%w: build submit request: %v", ErrProvider, err)
	}
	p.setAuth(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return submitResponse{}, fmt.Errorf("%w: submit request failed: %v", ErrProvider, err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return submitResponse{}, fmt.Errorf("%w: submit returned status %d%s", ErrProvider, resp.StatusCode, errorDetail(resp.Body))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return submitResponse{}, fmt.Errorf("%w: read submit response: %v", ErrProvider, err)
	}
	var sr submitResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return submitResponse{}, fmt.Errorf("%w: malformed submit response: %v", ErrProvider, err)
	}
	if sr.RequestID == "" || sr.StatusURL == "" || sr.ResponseURL == "" {
		return submitResponse{}, fmt.Errorf("%w: submit response missing request_id/status_url/response_url", ErrProvider)
	}
	return sr, nil
}

// poll polls the request's status URL until the result is COMPLETED. It stops on
// a terminal error status, context cancellation, or the bounded timeout.
func (p *Provider) poll(ctx context.Context, submitted submitResponse) error {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		status, err := p.pollOnce(ctx, submitted.StatusURL)
		if err != nil {
			return err
		}
		switch status.Status {
		case statusCompleted:
			return nil
		case statusInQueue, statusInProgress, "":
			// keep polling
		default:
			// Unknown but non-terminal: keep polling until timeout.
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *Provider) pollOnce(ctx context.Context, statusURL string) (statusResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return statusResponse{}, fmt.Errorf("%w: build poll request: %v", ErrProvider, err)
	}
	p.setAuth(httpReq)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return statusResponse{}, fmt.Errorf("%w: poll request failed: %v", ErrProvider, err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusResponse{}, fmt.Errorf("%w: poll returned status %d%s", ErrProvider, resp.StatusCode, errorDetail(resp.Body))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return statusResponse{}, fmt.Errorf("%w: read poll response: %v", ErrProvider, err)
	}
	var sr statusResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return statusResponse{}, fmt.Errorf("%w: malformed poll response: %v", ErrProvider, err)
	}
	return sr, nil
}

// fetchResult fetches the completed result body from the request's response URL.
func (p *Provider) fetchResult(ctx context.Context, submitted submitResponse) (resultResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, submitted.ResponseURL, nil)
	if err != nil {
		return resultResponse{}, fmt.Errorf("%w: build result request: %v", ErrProvider, err)
	}
	p.setAuth(httpReq)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return resultResponse{}, fmt.Errorf("%w: result request failed: %v", ErrProvider, err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resultResponse{}, fmt.Errorf("%w: result returned status %d%s", ErrProvider, resp.StatusCode, errorDetail(resp.Body))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resultResponse{}, fmt.Errorf("%w: read result response: %v", ErrProvider, err)
	}
	var rr resultResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return resultResponse{}, fmt.Errorf("%w: malformed result response: %v", ErrProvider, err)
	}
	return rr, nil
}

// download fetches the result image URL and returns its bytes + content type.
func (p *Provider) download(ctx context.Context, imageURL string) ([]byte, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: build download request: %v", ErrProvider, err)
	}
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("%w: download request failed: %v", ErrProvider, err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("%w: download returned status %d%s", ErrProvider, resp.StatusCode, errorDetail(resp.Body))
	}
	imgBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return nil, "", fmt.Errorf("%w: read image bytes: %v", ErrProvider, err)
	}
	if len(imgBytes) == 0 {
		return nil, "", fmt.Errorf("%w: provider returned empty image", ErrProvider)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return imgBytes, contentType, nil
}

// setAuth stamps the fal `Authorization: Key <FAL_KEY>` header.
func (p *Provider) setAuth(req *http.Request) {
	req.Header.Set("Authorization", "Key "+p.apiKey)
}

// PollStatus is not used by the worker model (Generate submits and polls
// internally); it is part of the interface and reports not-implemented.
func (p *Provider) PollStatus(_ context.Context, _ string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotImplemented
}

// Upscale is not implemented for fal in this slice.
func (p *Provider) Upscale(_ context.Context, _ providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}

const (
	maxResponseBytes = 1 << 20  // 1 MiB cap on JSON responses
	maxImageBytes    = 64 << 20 // 64 MiB cap on a downloaded image
)

// maxErrorDetailBytes bounds how much of a non-2xx body is folded into the
// error message. fal's errors are small JSON documents; the cap keeps a
// pathological body out of the logs and the job's error_message column.
const maxErrorDetailBytes = 512

// signedQuery matches a URL query string inside an error body. fal echoes the
// offending input back — for a reference-image failure that is our PRESIGNED
// URL, whose query carries X-Amz-Credential and X-Amz-Signature. That suffix
// then lands in worker logs and in the job's persisted error_message, so it is
// stripped before the body is folded into an error.
var signedQuery = regexp.MustCompile(`\?[^"\s]+`)

// errorDetail renders a non-2xx response body as a short, single-line suffix
// for the returned error. Without it every fal failure collapses to a bare
// status code — fal reports the ACTIONABLE cause in the body (e.g.
// file_download_error when a reference image URL is not reachable from fal's
// servers), and losing it turns a two-second diagnosis into a manual probe.
// Query strings are redacted (see signedQuery); the object path survives, which
// is what identifies the failing reference. Best-effort: an unreadable or empty
// body yields no suffix.
func errorDetail(body io.Reader) string {
	if body == nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorDetailBytes))
	if err != nil || len(raw) == 0 {
		return ""
	}
	detail := strings.Join(strings.Fields(string(raw)), " ")
	detail = signedQuery.ReplaceAllString(detail, "?<redacted>")
	if detail == "" {
		return ""
	}
	return ": " + detail
}

// drainClose drains and closes a response body so the underlying connection can
// be reused, ignoring errors (best-effort cleanup).
func drainClose(rc io.ReadCloser) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, maxImageBytes))
	_ = rc.Close()
}
