package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
)

type AssetsHandler struct {
	Repo assets.Repository
}

func NewAssetsHandler(repo assets.Repository) *AssetsHandler {
	return &AssetsHandler{Repo: repo}
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
