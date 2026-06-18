// Package bfl implements the providers.ImageProvider interface for Black Forest
// Labs (BFL), the first real image provider (Phase 7A).
//
// BFL API contract (assumptions, documented per the public BFL docs at
// https://docs.bfl.ai). These are the only behaviours this adapter relies on;
// no undocumented behaviour is invented:
//
//   - Submit (async): POST {baseURL}/{modelPath} with header `x-key: <API_KEY>`
//     and a JSON body { "prompt", "width", "height", ... }. The model path
//     selects the FLUX variant (default "v1/flux-pro-1.1"). A success response
//     is JSON { "id": "<request-id>", "polling_url": "<url>" }.
//   - Poll: GET the polling_url (falling back to {baseURL}/v1/get_result?id=<id>)
//     with header `x-key`. Response JSON:
//     { "id", "status": "Pending"|"Ready"|"Error"|"Request Moderated"|
//     "Content Moderated"|"Task not found", "result": { "sample": "<image-url>" } }.
//     "Ready" means result.sample is a short-lived signed URL to the image.
//   - Download: GET result.sample (no auth header required; it is pre-signed).
//
// The adapter submits and polls internally until the image is ready, the
// context is cancelled, the provider errors, or a bounded timeout elapses. It
// does NOT introduce a new async job lifecycle — Generate still returns final
// image bytes or an error (Phase 7A worker model). The HTTP client is injectable
// so unit tests exercise the full submit/poll/download flow against a stub with
// no real network access.
package bfl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

const (
	ProviderID = "bfl"
	// ModelName is the default FLUX variant this adapter targets; it matches the
	// model_name seeded in migrations/0006_bfl_provider_seed.sql.
	ModelName = "flux-pro-1.1"

	defaultBaseURL      = "https://api.bfl.ai"
	defaultModelPath    = "v1/flux-pro-1.1"
	defaultPollInterval = 1 * time.Second
	defaultTimeout      = 60 * time.Second

	statusReady            = "Ready"
	statusPending          = "Pending"
	statusError            = "Error"
	statusModerated        = "Request Moderated"
	statusContentModerated = "Content Moderated"
	statusNotFound         = "Task not found"
)

// ErrProvider is the base for provider-side failures so callers can
// errors.Is(err, bfl.ErrProvider) without matching on strings.
var ErrProvider = errors.New("bfl")

// Doer is the minimal HTTP surface the adapter needs; *http.Client satisfies
// it, and tests supply a stub.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Provider is the BFL adapter.
type Provider struct {
	apiKey       string
	baseURL      string
	modelPath    string
	httpClient   Doer
	pollInterval time.Duration
	timeout      time.Duration
}

// Option configures a Provider (used to inject the HTTP client, base URL, and
// timing in tests).
type Option func(*Provider)

// WithHTTPClient injects the HTTP client (default &http.Client{}).
func WithHTTPClient(d Doer) Option { return func(p *Provider) { p.httpClient = d } }

// WithBaseURL overrides the API base URL (default https://api.bfl.ai).
func WithBaseURL(base string) Option { return func(p *Provider) { p.baseURL = base } }

// WithModelPath overrides the model path under the base URL.
func WithModelPath(path string) Option { return func(p *Provider) { p.modelPath = path } }

// WithPollInterval overrides the poll cadence (default 1s).
func WithPollInterval(d time.Duration) Option { return func(p *Provider) { p.pollInterval = d } }

// WithTimeout overrides the bounded submit+poll deadline (default 60s).
func WithTimeout(d time.Duration) Option { return func(p *Provider) { p.timeout = d } }

// New builds a BFL adapter. apiKey is the BFL_API_KEY; opts inject the HTTP
// client / endpoints / timing for tests.
func New(apiKey string, opts ...Option) *Provider {
	p := &Provider{
		apiKey:       apiKey,
		baseURL:      defaultBaseURL,
		modelPath:    defaultModelPath,
		httpClient:   &http.Client{},
		pollInterval: defaultPollInterval,
		timeout:      defaultTimeout,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Capabilities advertises the provider-capability floor (draft_only,
// no_preview, no high-res). This is intentionally conservative until a
// benchmark pass produces evidence for a higher tier; it matches the model row
// seeded in migration 0006.
func (p *Provider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{
		ProviderID: ProviderID,
		ModelName:  ModelName,
		Capabilities: []providers.Capability{
			providers.CapabilityDraftOnly,
			providers.CapabilitySceneCapable,
		},
		PreviewCapability: providers.PreviewCapabilityNone,
		SupportsHighRes:   false,
		MaxBatchSize:      1,
		SupportedAspects:  []string{"1:1", "16:9", "9:16", "4:3", "3:4"},
	}
}

// submitResponse is the BFL async-submit body.
type submitResponse struct {
	ID         string `json:"id"`
	PollingURL string `json:"polling_url"`
}

// resultResponse is the BFL get_result body.
type resultResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Result struct {
		Sample string `json:"sample"`
	} `json:"result"`
}

// Generate submits a text-to-image request and polls until the image is ready,
// returning the final image bytes. It respects context cancellation and a
// bounded timeout.
func (p *Provider) Generate(ctx context.Context, req providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	if p.apiKey == "" {
		return providers.ProviderGenerateResult{}, fmt.Errorf("%w: missing API key", ErrProvider)
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

	imageURL, err := p.poll(ctx, submitted)
	if err != nil {
		return providers.ProviderGenerateResult{}, err
	}

	imgBytes, contentType, err := p.download(ctx, imageURL)
	if err != nil {
		return providers.ProviderGenerateResult{}, err
	}

	return providers.ProviderGenerateResult{
		ProviderJobID: submitted.ID,
		Status:        providers.JobStatusCompleted,
		Images: []providers.ProviderImage{{
			URL:         imageURL,
			Bytes:       imgBytes,
			ContentType: contentType,
		}},
		Seed:     req.Seed,
		Metadata: map[string]any{"provider": ProviderID, "model": ModelName},
	}, nil
}

func (p *Provider) submit(ctx context.Context, req providers.ProviderGenerateRequest) (submitResponse, error) {
	body := map[string]any{"prompt": req.Prompt}
	if req.Width > 0 {
		body["width"] = req.Width
	}
	if req.Height > 0 {
		body["height"] = req.Height
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
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("x-key", p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return submitResponse{}, fmt.Errorf("%w: submit request failed: %v", ErrProvider, err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return submitResponse{}, fmt.Errorf("%w: submit returned status %d", ErrProvider, resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return submitResponse{}, fmt.Errorf("%w: read submit response: %v", ErrProvider, err)
	}
	var sr submitResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return submitResponse{}, fmt.Errorf("%w: malformed submit response: %v", ErrProvider, err)
	}
	if sr.ID == "" {
		return submitResponse{}, fmt.Errorf("%w: submit response missing id", ErrProvider)
	}
	return sr, nil
}

// poll polls the request's polling URL until the image is ready, returning the
// signed sample URL. It stops on a terminal provider status, context
// cancellation, or the bounded timeout.
func (p *Provider) poll(ctx context.Context, submitted submitResponse) (string, error) {
	pollURL := submitted.PollingURL
	if pollURL == "" {
		pollURL = p.baseURL + "/v1/get_result?id=" + url.QueryEscape(submitted.ID)
	}

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		result, err := p.pollOnce(ctx, pollURL)
		if err != nil {
			return "", err
		}
		switch result.Status {
		case statusReady:
			if result.Result.Sample == "" {
				return "", fmt.Errorf("%w: ready result missing sample url", ErrProvider)
			}
			return result.Result.Sample, nil
		case statusError, statusModerated, statusContentModerated, statusNotFound:
			return "", fmt.Errorf("%w: provider returned terminal status %q", ErrProvider, result.Status)
		case statusPending, "":
			// keep polling
		default:
			// Unknown but non-terminal: keep polling until timeout.
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *Provider) pollOnce(ctx context.Context, pollURL string) (resultResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return resultResponse{}, fmt.Errorf("%w: build poll request: %v", ErrProvider, err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("x-key", p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return resultResponse{}, fmt.Errorf("%w: poll request failed: %v", ErrProvider, err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resultResponse{}, fmt.Errorf("%w: poll returned status %d", ErrProvider, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resultResponse{}, fmt.Errorf("%w: read poll response: %v", ErrProvider, err)
	}
	var rr resultResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return resultResponse{}, fmt.Errorf("%w: malformed poll response: %v", ErrProvider, err)
	}
	return rr, nil
}

// download fetches the signed sample URL and returns its bytes + content type.
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
		return nil, "", fmt.Errorf("%w: download returned status %d", ErrProvider, resp.StatusCode)
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

// PollStatus is not used by the Phase 7A worker model (Generate submits and
// polls internally); it is part of the interface and reports not-implemented.
func (p *Provider) PollStatus(_ context.Context, _ string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotImplemented
}

// Upscale is not implemented for BFL in Phase 7A.
func (p *Provider) Upscale(_ context.Context, _ providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}

const (
	maxResponseBytes = 1 << 20  // 1 MiB cap on JSON responses
	maxImageBytes    = 64 << 20 // 64 MiB cap on a downloaded image
)

// drainClose drains and closes a response body so the underlying connection can
// be reused, ignoring errors (best-effort cleanup).
func drainClose(rc io.ReadCloser) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, maxImageBytes))
	_ = rc.Close()
}
