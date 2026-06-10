package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
)

// RetrievalService is the retrieval decision layer the search handler depends
// on (implemented by *assets.Retriever). Keeping it an interface lets the
// handler be tested without a database.
type RetrievalService interface {
	Retrieve(ctx context.Context, q assets.RetrievalQuery) (assets.RetrievalResult, error)
}

type AssetsHandler struct {
	Repo      assets.Repository
	Retriever RetrievalService
}

func NewAssetsHandler(repo assets.Repository, retriever RetrievalService) *AssetsHandler {
	return &AssetsHandler{Repo: repo, Retriever: retriever}
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
	writeJSON(w, http.StatusOK, toVisualAssetAPI(row))
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

	writeJSON(w, http.StatusOK, toAssetSearchResponse(result))
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

func toAssetSearchResponse(result assets.RetrievalResult) apigen.AssetSearchResponse {
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
		out.Assets = []apigen.VisualAsset{toVisualAssetAPI(*result.Asset)}
	}
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
