package handlers

import (
	"net/http"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

type StylesHandler struct {
	Repo  styles.Repository
	NewID func() string
}

func NewStylesHandler(repo styles.Repository) *StylesHandler {
	return &StylesHandler{Repo: repo, NewID: ids.NewStyleProfileID}
}

type listStylesResponse struct {
	Styles []apigen.StyleProfile `json:"styles"`
}

func (h *StylesHandler) List(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	rows, err := h.Repo.ListActiveByTenant(r.Context(), principal.TenantID)
	if err != nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not list style profiles")
		return
	}

	out := make([]apigen.StyleProfile, 0, len(rows))
	for _, row := range rows {
		out = append(out, toStyleProfileAPI(row))
	}
	writeJSON(w, http.StatusOK, listStylesResponse{Styles: out})
}

func (h *StylesHandler) Create(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	var req apigen.CreateStyleProfileRequest
	if !readJSONBody(w, r, &req) {
		return
	}

	if req.Name == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "name is required")
		return
	}
	if req.PositivePrompt == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "positive_prompt is required")
		return
	}
	if req.StyleMode == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "style_mode is required")
		return
	}
	if !validStyleMode(req.StyleMode) {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "style_mode must be one of open_prompt, preset_style, creator_style, provider_native")
		return
	}

	tier := string(apigen.QualityTierStandard)
	if req.DefaultQualityTier != nil && *req.DefaultQualityTier != "" {
		if !validQualityTier(*req.DefaultQualityTier) {
			httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "default_quality_tier must be one of draft, standard, high")
			return
		}
		tier = string(*req.DefaultQualityTier)
	}

	created, err := h.Repo.Create(r.Context(), styles.CreateParams{
		ID:                 h.NewID(),
		TenantID:           principal.TenantID,
		Name:               req.Name,
		StyleMode:          string(req.StyleMode),
		PositivePrompt:     req.PositivePrompt,
		NegativePrompt:     req.NegativePrompt,
		DefaultQualityTier: tier,
	})
	if err != nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not create style profile")
		return
	}

	writeJSON(w, http.StatusCreated, toStyleProfileAPI(created))
}

func validStyleMode(m apigen.StyleMode) bool {
	switch m {
	case apigen.OpenPrompt, apigen.PresetStyle, apigen.CreatorStyle, apigen.ProviderNative:
		return true
	}
	return false
}

func validQualityTier(q apigen.QualityTier) bool {
	switch q {
	case apigen.QualityTierDraft, apigen.QualityTierStandard, apigen.QualityTierHigh:
		return true
	}
	return false
}

func toStyleProfileAPI(s styles.StyleProfile) apigen.StyleProfile {
	tier := apigen.QualityTier(s.DefaultQualityTier)
	status := apigen.StyleProfileStatus(s.Status)
	out := apigen.StyleProfile{
		Id:                 s.ID,
		Name:               s.Name,
		StyleMode:          apigen.StyleMode(s.StyleMode),
		PositivePrompt:     s.PositivePrompt,
		NegativePrompt:     s.NegativePrompt,
		DefaultQualityTier: &tier,
		Status:             &status,
	}
	return out
}
