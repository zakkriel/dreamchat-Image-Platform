package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
)

// defaultDeliveryTTL bounds presigned read URLs when no TTL is configured, so
// a misconfiguration can never mint a never-expiring URL.
const defaultDeliveryTTL = 15 * time.Minute

// RetrievalService is the retrieval decision layer the search handler depends
// on (implemented by *assets.Retriever). Keeping it an interface lets the
// handler be tested without a database.
type RetrievalService interface {
	Retrieve(ctx context.Context, q assets.RetrievalQuery) (assets.RetrievalResult, error)
}

// AssetURLSigner mints time-limited presigned read URLs for an object key.
// *storage.s3Storage (storage.Storage) satisfies it; a stub satisfies it in
// tests. Keeping it narrow means the read handler can only ask for a URL — it
// never gains the ability to write objects.
type AssetURLSigner interface {
	Presign(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// JobAssetsLookup is the narrow jobs dependency the job-assets read needs:
// the tenant-scoped job row and (for pack jobs) the ordered pack items.
// jobs.Repository satisfies it.
type JobAssetsLookup interface {
	GetByIDForTenant(ctx context.Context, id, tenantID string) (jobs.Job, error)
	ListAssetPackItems(ctx context.Context, packID string) ([]jobs.AssetPackItem, error)
}

type AssetsHandler struct {
	Repo      assets.Repository
	Retriever RetrievalService

	// Signer + URLTTL drive the Phase 6B presigned per-tier delivery URLs.
	// When Signer is nil the read responses omit the *_download_url fields
	// (additive default — the pre-6B behavior).
	Signer AssetURLSigner
	URLTTL time.Duration

	// Jobs backs GET /v1/jobs/{job_id}/assets. When nil that endpoint cannot
	// resolve a job and returns 500 (it is always wired in production).
	Jobs JobAssetsLookup
}

func NewAssetsHandler(repo assets.Repository, retriever RetrievalService) *AssetsHandler {
	return &AssetsHandler{Repo: repo, Retriever: retriever}
}

// WithDelivery wires the presigned read side: a URL signer and the TTL minted
// URLs carry. Returns the handler for fluent construction (mirrors
// PacksHandler.WithRetriever).
func (h *AssetsHandler) WithDelivery(signer AssetURLSigner, ttl time.Duration) *AssetsHandler {
	h.Signer = signer
	h.URLTTL = ttl
	return h
}

// WithJobs wires the jobs lookup the job-assets read depends on.
func (h *AssetsHandler) WithJobs(lookup JobAssetsLookup) *AssetsHandler {
	h.Jobs = lookup
	return h
}

func (h *AssetsHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	assetID := chi.URLParam(r, "asset_id")
	if assetID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "asset_id is required")
		return
	}

	row, err := h.Repo.GetByIDForTenant(r.Context(), assetID, principal.TenantID)
	if err != nil {
		if errors.Is(err, assets.ErrNotFound) {
			httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "asset not found")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not load asset")
		return
	}
	writeJSON(w, http.StatusOK, h.toAssetAPI(r.Context(), row))
}

// JobAssets wires GET /v1/jobs/{job_id}/assets (Phase 6B). It returns the
// job's delivered assets in deterministic delivery order — pack jobs by
// asset_pack_items.sort_order, artifact jobs by final_asset_ids order — each
// enriched with presigned per-tier download URLs. Tenant comes from the auth
// principal, never the path/body. Delivery is NOT restricted to status=ready:
// archived assets the tenant owns remain displayable.
func (h *AssetsHandler) JobAssets(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}
	if h.Jobs == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "job assets read not configured")
		return
	}

	jobID := chi.URLParam(r, "job_id")
	if jobID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "job_id is required")
		return
	}

	// Tenant-scoped job lookup gates everything: a presigned URL is only ever
	// minted after the caller is shown to own the job.
	job, err := h.Jobs.GetByIDForTenant(r.Context(), jobID, principal.TenantID)
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "job not found")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not load job")
		return
	}

	assetIDs, err := h.deliveryOrder(r.Context(), job)
	if err != nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not resolve job assets")
		return
	}

	out := make([]apigen.VisualAsset, 0, len(assetIDs))
	for _, id := range assetIDs {
		asset, err := h.Repo.GetByIDForTenant(r.Context(), id, principal.TenantID)
		if err != nil {
			// A referenced asset that is missing or belongs to another tenant is
			// skipped rather than failing the whole delivery read.
			if errors.Is(err, assets.ErrNotFound) {
				continue
			}
			httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not load job asset")
			return
		}
		out = append(out, h.toAssetAPI(r.Context(), asset))
	}
	writeJSON(w, http.StatusOK, apigen.JobAssetsResponse{Assets: out})
}

// deliveryOrder returns the job's delivered asset ids in deterministic
// delivery order. For a pack job it follows asset_pack_items.sort_order; for
// an artifact job it follows generation_jobs.final_asset_ids.
func (h *AssetsHandler) deliveryOrder(ctx context.Context, job jobs.Job) ([]string, error) {
	if job.AssetPackID != nil && *job.AssetPackID != "" {
		items, err := h.Jobs.ListAssetPackItems(ctx, *job.AssetPackID)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.VisualAssetID)
		}
		return ids, nil
	}
	return job.FinalAssetIds, nil
}

// Search wires POST /v1/assets/search (Phase 6A1). It runs the retrieval
// decision layer (exact → compatible → preview → generated_required) and
// shapes an AssetSearchResponse. The tenant always comes from the auth
// principal, never the request body; read scope is enforced by the router.
func (h *AssetsHandler) Search(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	var req apigen.AssetSearchRequest
	if !readJSONBody(w, r, &req) {
		return
	}

	q, ok := h.buildRetrievalQuery(w, r, principal.TenantID, req)
	if !ok {
		return
	}

	result, err := h.Retriever.Retrieve(r.Context(), q)
	if err != nil {
		var badPolicy assets.ErrInvalidFallbackPolicy
		if errors.As(err, &badPolicy) {
			httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "invalid fallback_policy")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not search assets")
		return
	}

	writeJSON(w, http.StatusOK, h.toAssetSearchResponse(r.Context(), result))
}

// buildRetrievalQuery validates the request and assembles a RetrievalQuery.
// It returns ok=false (after writing a 400) when a required field is missing
// or owner_type is not a retrievable entity. Tenant is supplied by the caller
// from the auth principal.
func (h *AssetsHandler) buildRetrievalQuery(w http.ResponseWriter, r *http.Request, tenantID string, req apigen.AssetSearchRequest) (assets.RetrievalQuery, bool) {
	worldID := strVal(req.WorldId)
	visualIdentityID := strVal(req.VisualIdentityId)
	variantKey := strVal(req.VariantKey)
	styleProfileID := strVal(req.StyleProfileId)

	entityType, entityOK := retrievalEntityType(req.OwnerType)
	switch {
	case worldID == "":
		return badRetrievalRequest(w, r, "world_id is required")
	case visualIdentityID == "":
		return badRetrievalRequest(w, r, "visual_identity_id is required")
	case !entityOK:
		return badRetrievalRequest(w, r, "owner_type must be character or place")
	case variantKey == "":
		return badRetrievalRequest(w, r, "variant_key is required")
	case styleProfileID == "":
		return badRetrievalRequest(w, r, "style_profile_id is required")
	case req.StateVersion == nil:
		return badRetrievalRequest(w, r, "state_version is required")
	}

	q := assets.RetrievalQuery{
		TenantID:         tenantID,
		WorldID:          worldID,
		VisualIdentityID: visualIdentityID,
		EntityType:       entityType,
		VariantKey:       variantKey,
		StyleProfileID:   styleProfileID,
		StateVersion:     int32(*req.StateVersion),
		FallbackPolicy:   string(deref(req.FallbackPolicy)),
	}
	if req.StyleProfileVersion != nil {
		v := int32(*req.StyleProfileVersion)
		q.StyleProfileVersion = &v
	}
	if req.QualityTier != nil {
		q.QualityTier = string(*req.QualityTier)
	}
	return q, true
}

func badRetrievalRequest(w http.ResponseWriter, r *http.Request, msg string) (assets.RetrievalQuery, bool) {
	httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, msg)
	return assets.RetrievalQuery{}, false
}

// retrievalEntityType maps the request OwnerType to the retrieval entity type.
// Only character and place are retrievable in 6A1; artifact retrieval is out
// of scope, and a missing owner_type is rejected.
func retrievalEntityType(ot *apigen.OwnerType) (string, bool) {
	if ot == nil {
		return "", false
	}
	switch *ot {
	case apigen.OwnerType(assets.EntityCharacter):
		return assets.EntityCharacter, true
	case apigen.OwnerType(assets.EntityPlace):
		return assets.EntityPlace, true
	default:
		return "", false
	}
}

func (h *AssetsHandler) toAssetSearchResponse(ctx context.Context, result assets.RetrievalResult) apigen.AssetSearchResponse {
	matchType := apigen.MatchType(result.MatchType)
	score := float32(result.CompatibilityScore)
	genRecommended := result.GenerationRecommended
	out := apigen.AssetSearchResponse{
		Assets:                []apigen.VisualAsset{},
		MatchType:             &matchType,
		CompatibilityScore:    &score,
		GenerationRecommended: &genRecommended,
	}
	if result.FallbackReason != "" {
		reason := result.FallbackReason
		out.FallbackReason = &reason
	}
	if result.Asset != nil {
		out.Assets = []apigen.VisualAsset{h.toAssetAPI(ctx, *result.Asset)}
	}
	return out
}

// toAssetAPI shapes the API view of an asset and, when a presign signer is
// wired, adds the Phase 6B per-tier download URLs + their shared expiry. The
// object keys are DERIVED from the asset id and variant via
// storage.ObjectKey — never from a client-supplied path — so the read surface
// can't be coerced into signing an arbitrary object. Presigning is a local
// (no-network) signing operation, so it does not slow the read.
func (h *AssetsHandler) toAssetAPI(ctx context.Context, a assets.VisualAsset) apigen.VisualAsset {
	out := toVisualAssetAPI(a)
	if h.Signer == nil {
		return out
	}
	ttl := h.URLTTL
	if ttl <= 0 {
		ttl = defaultDeliveryTTL
	}
	if u, err := h.Signer.Presign(ctx, storage.ObjectKey(a.ID, storage.VariantThumb, "png"), ttl); err == nil {
		out.ThumbnailDownloadUrl = &u
	}
	if u, err := h.Signer.Presign(ctx, storage.ObjectKey(a.ID, storage.VariantLow, "png"), ttl); err == nil {
		out.PreviewDownloadUrl = &u
	}
	if u, err := h.Signer.Presign(ctx, storage.ObjectKey(a.ID, storage.VariantHigh, "png"), ttl); err == nil {
		out.FinalDownloadUrl = &u
	}
	expires := time.Now().Add(ttl).UTC()
	out.UrlExpiresAt = &expires
	return out
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func toVisualAssetAPI(a assets.VisualAsset) apigen.VisualAsset {
	world := a.WorldID
	version := int(a.Version)
	stateVersion := int(a.StateVersion)
	out := apigen.VisualAsset{
		Id:               a.ID,
		AssetType:        apigen.AssetType(a.AssetType),
		VariantKey:       a.VariantKey,
		VariantFamily:    a.VariantFamily,
		Version:          version,
		StateVersion:     &stateVersion,
		Status:           apigen.AssetStatus(a.Status),
		VisualIdentityId: a.VisualIdentityID,
		WorldId:          &world,
		LowResUrl:        a.LowResUrl,
		HighResUrl:       a.HighResUrl,
		ThumbnailUrl:     a.ThumbnailUrl,
		ProviderId:       a.ProviderID,
		ModelId:          a.ModelID,
		PromptHash:       a.PromptHash,
		Seed:             a.Seed,
		FallbackAllowed:  &a.FallbackAllowed,
		IsIdentityAnchor: &a.IsIdentityAnchor,
	}
	if len(a.CompatibilityTags) > 0 {
		tags := a.CompatibilityTags
		out.CompatibilityTags = &tags
	}
	if a.FallbackRank != nil {
		fr := int(*a.FallbackRank)
		out.FallbackRank = &fr
	}
	if len(a.Metadata) > 0 {
		meta := a.Metadata
		out.Metadata = &meta
	}
	return out
}
