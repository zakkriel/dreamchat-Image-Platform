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
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
)

// assetReader is the narrow read the anchor-attach flow needs to validate each
// candidate reference asset. *assets.Repository satisfies it; tests fake it.
type assetReader interface {
	GetByIDForTenant(ctx context.Context, id, tenantID string) (assets.VisualAsset, error)
}

type IdentitiesHandler struct {
	Repo  identities.Repository
	NewID func() string
	// Assets validates anchor candidate assets when attaching anchors. Optional:
	// when nil the attach-anchors endpoint is not mounted (router gates on it).
	Assets assetReader
}

func NewIdentitiesHandler(repo identities.Repository) *IdentitiesHandler {
	return &IdentitiesHandler{Repo: repo, NewID: ids.NewVisualIdentityID}
}

// WithAssets wires the asset reader used to validate anchor candidates.
func (h *IdentitiesHandler) WithAssets(a assetReader) *IdentitiesHandler {
	h.Assets = a
	return h
}

func (h *IdentitiesHandler) UpsertCharacter(w http.ResponseWriter, r *http.Request) {
	h.upsert(w, r, "character_id", apigen.OwnerTypeCharacter)
}

func (h *IdentitiesHandler) GetCharacter(w http.ResponseWriter, r *http.Request) {
	h.get(w, r, "character_id", apigen.OwnerTypeCharacter)
}

func (h *IdentitiesHandler) UpsertPlace(w http.ResponseWriter, r *http.Request) {
	h.upsert(w, r, "place_id", apigen.OwnerTypePlace)
}

func (h *IdentitiesHandler) GetPlace(w http.ResponseWriter, r *http.Request) {
	h.get(w, r, "place_id", apigen.OwnerTypePlace)
}

func (h *IdentitiesHandler) upsert(w http.ResponseWriter, r *http.Request, pathParam string, expectedOwner apigen.OwnerType) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	ownerID := chi.URLParam(r, pathParam)
	if ownerID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "owner id path parameter is required")
		return
	}

	var req apigen.CreateVisualIdentityRequest
	if !readJSONBody(w, r, &req) {
		return
	}

	if req.OwnerType != expectedOwner {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "owner_type must match the route")
		return
	}
	if req.OwnerId != ownerID {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "owner_id must match the path parameter")
		return
	}
	if req.WorldId == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "world_id is required")
		return
	}
	if req.DisplayName == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "display_name is required")
		return
	}
	if req.StyleProfileId == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "style_profile_id is required")
		return
	}
	if req.CanonicalVisualTraits == nil {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "canonical_visual_traits is required")
		return
	}

	row, err := h.Repo.Upsert(r.Context(), identities.UpsertParams{
		NewID:                 h.NewID(),
		TenantID:              principal.TenantID,
		WorldID:               req.WorldId,
		OwnerType:             string(req.OwnerType),
		OwnerID:               req.OwnerId,
		DisplayName:           req.DisplayName,
		CanonicalVisualTraits: req.CanonicalVisualTraits,
		StyleProfileID:        req.StyleProfileId,
		ConsistencyKey:        req.ConsistencyKey,
	})
	if err != nil {
		if errors.Is(err, identities.ErrInvalidStyle) {
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidStyleProfile, "style_profile_id is invalid for this tenant")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not upsert visual identity")
		return
	}

	writeJSON(w, http.StatusOK, toVisualIdentityAPI(row))
}

func (h *IdentitiesHandler) get(w http.ResponseWriter, r *http.Request, pathParam string, expectedOwner apigen.OwnerType) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	ownerID := chi.URLParam(r, pathParam)
	if ownerID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "owner id path parameter is required")
		return
	}

	worldID := r.URL.Query().Get("world_id")
	if worldID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "world_id query parameter is required")
		return
	}

	row, err := h.Repo.GetByOwner(r.Context(), principal.TenantID, worldID, string(expectedOwner), ownerID)
	if err != nil {
		if errors.Is(err, identities.ErrNotFound) {
			httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "visual identity not found")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not load visual identity")
		return
	}
	writeJSON(w, http.StatusOK, toVisualIdentityAPI(row))
}

// AttachCharacterAnchors sets a character visual identity's anchor_asset_ids.
func (h *IdentitiesHandler) AttachCharacterAnchors(w http.ResponseWriter, r *http.Request) {
	h.attachAnchors(w, r, "character_id", apigen.OwnerTypeCharacter)
}

// AttachPlaceAnchors sets a place visual identity's anchor_asset_ids. Place packs
// also request pack_capable and so may resolve the reference-conditioned fal
// route; a symmetric anchor flow keeps place packs from failing closed once fal
// is enabled (ADR-017).
func (h *IdentitiesHandler) AttachPlaceAnchors(w http.ResponseWriter, r *http.Request) {
	h.attachAnchors(w, r, "place_id", apigen.OwnerTypePlace)
}

// attachAnchors sets a visual identity's anchor_asset_ids — the reference images
// a reference-conditioned provider uses to hold the recurring character/place
// (ADR-017). It validates each candidate asset (tenant ownership, ready status, a
// high-res object, and binding to this identity or unassigned) before persisting
// the set, so pack generation can use the anchors with no manual SQL. The set is
// replaced wholesale. Shared by the character and place endpoints so their
// validation can never drift.
func (h *IdentitiesHandler) attachAnchors(w http.ResponseWriter, r *http.Request, pathParam string, ownerType apigen.OwnerType) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}
	if h.Assets == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "asset validation not configured")
		return
	}

	ownerID := chi.URLParam(r, pathParam)
	if ownerID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, pathParam+" is required")
		return
	}

	var req apigen.AttachAnchorAssetsRequest
	if !readJSONBody(w, r, &req) {
		return
	}
	if req.WorldId == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "world_id is required")
		return
	}
	if len(req.AnchorAssetIds) == 0 {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "anchor_asset_ids must not be empty")
		return
	}

	// The anchors hang off an existing identity; this endpoint never creates one.
	identity, err := h.Repo.GetByOwner(r.Context(), principal.TenantID, req.WorldId, string(ownerType), ownerID)
	if err != nil {
		if errors.Is(err, identities.ErrNotFound) {
			httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "visual identity not found")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not load visual identity")
		return
	}

	// Validate + de-duplicate each candidate anchor (order preserved).
	seen := make(map[string]struct{}, len(req.AnchorAssetIds))
	anchorIDs := make([]string, 0, len(req.AnchorAssetIds))
	for _, assetID := range req.AnchorAssetIds {
		if assetID == "" {
			httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "anchor_asset_ids must not contain empty strings")
			return
		}
		if _, dup := seen[assetID]; dup {
			continue
		}
		seen[assetID] = struct{}{}

		asset, err := h.Assets.GetByIDForTenant(r.Context(), assetID, principal.TenantID)
		if err != nil {
			if errors.Is(err, assets.ErrNotFound) {
				httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidAnchorAsset, "anchor asset "+assetID+" not found for tenant")
				return
			}
			httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not validate anchor asset")
			return
		}
		if asset.Status != "ready" {
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidAnchorAsset, "anchor asset "+assetID+" is not ready")
			return
		}
		if asset.HighResUrl == nil || *asset.HighResUrl == "" {
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidAnchorAsset, "anchor asset "+assetID+" has no high-res object")
			return
		}
		if asset.WorldID != "" && asset.WorldID != identity.WorldID {
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidAnchorAsset, "anchor asset "+assetID+" belongs to a different world")
			return
		}
		// Acceptable binding policy: the asset is unassigned, or already bound to
		// this identity. An asset bound to a DIFFERENT identity is rejected.
		if asset.VisualIdentityID != nil && *asset.VisualIdentityID != "" && *asset.VisualIdentityID != identity.ID {
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidAnchorAsset, "anchor asset "+assetID+" is bound to a different visual identity")
			return
		}
		anchorIDs = append(anchorIDs, assetID)
	}

	updated, err := h.Repo.SetAnchorAssets(r.Context(), identity.ID, principal.TenantID, anchorIDs)
	if err != nil {
		if errors.Is(err, identities.ErrNotFound) {
			httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "visual identity not found")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not attach anchor assets")
		return
	}
	writeJSON(w, http.StatusOK, toVisualIdentityAPI(updated))
}

func toVisualIdentityAPI(vi identities.VisualIdentity) apigen.VisualIdentity {
	traits := vi.CanonicalVisualTraits
	var traitsPtr *map[string]any
	if traits != nil {
		t := map[string]any(traits)
		traitsPtr = &t
	}
	var anchorPtr *[]string
	if len(vi.AnchorAssetIds) > 0 {
		a := vi.AnchorAssetIds
		anchorPtr = &a
	}
	return apigen.VisualIdentity{
		Id:                    vi.ID,
		WorldId:               vi.WorldID,
		OwnerType:             apigen.OwnerType(vi.OwnerType),
		OwnerId:               vi.OwnerID,
		DisplayName:           vi.DisplayName,
		CanonicalVisualTraits: traitsPtr,
		StyleProfileId:        vi.StyleProfileID,
		ConsistencyKey:        vi.ConsistencyKey,
		AnchorAssetIds:        anchorPtr,
		CurrentVersion:        int(vi.CurrentVersion),
		Status:                apigen.VisualIdentityStatus(vi.Status),
	}
}
