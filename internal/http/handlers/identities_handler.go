package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
)

type IdentitiesHandler struct {
	Repo  identities.Repository
	NewID func() string
}

func NewIdentitiesHandler(repo identities.Repository) *IdentitiesHandler {
	return &IdentitiesHandler{Repo: repo, NewID: ids.NewVisualIdentityID}
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
