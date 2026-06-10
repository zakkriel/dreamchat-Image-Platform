package http

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/http/handlers"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

// Deps bundles the long-lived dependencies the router needs to wire
// middlewares and handlers. The router does not own these objects; the
// caller manages their lifecycle.
type Deps struct {
	Logger         *slog.Logger
	Config         *config.Config
	AuthRepo       auth.Repository
	StylesRepo     styles.Repository
	IdentitiesRepo identities.Repository
	AssetsRepo     assets.Repository
	JobsRepo       jobs.Repository
	JobsService    jobs.Creator
	AdminCost      handlers.AdminCostService
}

type HealthResponse struct {
	Status string `json:"status"`
}

const (
	scopeAdminRead  = "admin:read"
	scopeAdminCosts = "admin:costs"
)

func NewRouter(deps Deps) *chi.Mux {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(AccessLog(deps.Logger))

	r.Get("/health", healthHandler)

	mountDocs(r, deps)
	mountV1(r, deps)

	return r
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
}

// mountDocs wires GET /openapi.json and GET /docs per ADR-015.
//
// Gating:
//
//   - dev/test environments serve docs publicly.
//   - live with OPENAPI_DOCS_ENABLED=false hides both endpoints behind 404.
//   - live with OPENAPI_DOCS_ENABLED=true requires bearer auth with the
//     admin:read scope; failures return 404 (not 401) to avoid leaking the
//     existence of the docs surface.
func mountDocs(r chi.Router, deps Deps) {
	enabled := deps.Config.OpenAPIDocsEnabled
	env := deps.Config.Environment

	if env == config.EnvLive && !enabled {
		return
	}

	if env == config.EnvLive {
		r.Get("/openapi.json", gatedDocs(deps, openAPIJSONHandler))
		r.Get("/docs", gatedDocs(deps, docsHandler))
		return
	}

	r.Get("/openapi.json", openAPIJSONHandler)
	r.Get("/docs", docsHandler)
}

// gatedDocs verifies the bearer token inline and serves 404 on any auth
// failure or missing admin:read scope, so the docs surface is invisible to
// callers without the right credentials.
func gatedDocs(deps Deps, next http.HandlerFunc) http.HandlerFunc {
	pepper := deps.Config.APITokenPepper
	env := string(deps.Config.Environment)
	return func(w http.ResponseWriter, r *http.Request) {
		principal, rej := auth.Verify(r.Context(), deps.AuthRepo, r.Header.Get("Authorization"), pepper, env)
		if rej != nil || !principal.HasScope(scopeAdminRead) {
			notFound(w, r)
			return
		}
		next(w, r)
	}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "not found")
}

// mountV1 creates the authenticated /v1 group and wires Phase 2 endpoints.
// Missing or invalid auth returns 401; valid auth on an unimplemented path
// falls through to 404.
//
// The catch-all handler is registered explicitly so the auth middleware on
// the subrouter runs before chi's route lookup decides the path is unknown;
// chi only runs subrouter middleware once a matching route is selected.
func mountV1(r chi.Router, deps Deps) {
	if deps.AuthRepo == nil {
		return
	}
	r.Route("/v1", func(v1 chi.Router) {
		v1.Use(auth.Middleware(deps.AuthRepo, deps.Config.APITokenPepper, string(deps.Config.Environment)))
		mountStyles(v1, deps)
		mountIdentities(v1, deps)
		mountAssets(v1, deps)
		mountArtifacts(v1, deps)
		mountPacks(v1, deps)
		mountJobs(v1, deps)
		mountAdminCost(v1, deps)
		v1.Handle("/*", http.HandlerFunc(notFound))
	})
}

func mountStyles(v1 chi.Router, deps Deps) {
	if deps.StylesRepo == nil {
		return
	}
	h := handlers.NewStylesHandler(deps.StylesRepo)
	v1.With(auth.RequireScopes("styles:read")).Get("/styles", h.List)
	v1.With(auth.RequireScopes("styles:write")).Post("/styles", h.Create)
}

func mountIdentities(v1 chi.Router, deps Deps) {
	if deps.IdentitiesRepo == nil {
		return
	}
	h := handlers.NewIdentitiesHandler(deps.IdentitiesRepo)
	v1.With(auth.RequireScopes("images:write")).Post("/characters/{character_id}/visual-identity", h.UpsertCharacter)
	v1.With(auth.RequireScopes("images:read")).Get("/characters/{character_id}/visual-identity", h.GetCharacter)
	v1.With(auth.RequireScopes("images:write")).Post("/places/{place_id}/visual-identity", h.UpsertPlace)
	v1.With(auth.RequireScopes("images:read")).Get("/places/{place_id}/visual-identity", h.GetPlace)
}

func mountAssets(v1 chi.Router, deps Deps) {
	if deps.AssetsRepo == nil {
		return
	}
	retriever := assets.NewRetriever(deps.AssetsRepo)
	h := handlers.NewAssetsHandler(deps.AssetsRepo, retriever)
	v1.With(auth.RequireScopes("images:read")).Post("/assets/search", h.Search)
	v1.With(auth.RequireScopes("images:read")).Get("/assets/{asset_id}", h.Get)
}

func mountArtifacts(v1 chi.Router, deps Deps) {
	if deps.JobsService == nil || deps.StylesRepo == nil {
		return
	}
	// deps.AssetsRepo is the Phase 6A2 exact-reuse lookup; nil-safe (the
	// handler skips reuse when it is nil).
	var reuse handlers.ArtifactReuseLookup
	if deps.AssetsRepo != nil {
		reuse = deps.AssetsRepo
	}
	h := handlers.NewArtifactsHandler(deps.JobsService, deps.StylesRepo, deps.Config.ImageProvider, reuse)
	v1.With(auth.RequireScopes("images:write")).Post("/artifacts/{artifact_id}/generate", h.Generate)
}

// mountPacks wires the Phase 5A generate-pack endpoints. They mirror the
// artifact generate path but create an asset_packs row and fan out per
// variant in the worker (ADR-008).
func mountPacks(v1 chi.Router, deps Deps) {
	if deps.JobsService == nil || deps.StylesRepo == nil || deps.IdentitiesRepo == nil {
		return
	}
	h := handlers.NewPacksHandler(deps.JobsService, deps.StylesRepo, deps.IdentitiesRepo, deps.Config.ImageProvider)
	v1.With(auth.RequireScopes("images:write")).Post("/characters/{character_id}/generate-pack", h.GenerateCharacterPack)
	v1.With(auth.RequireScopes("images:write")).Post("/places/{place_id}/generate-pack", h.GeneratePlacePack)
}

func mountJobs(v1 chi.Router, deps Deps) {
	if deps.JobsRepo == nil {
		return
	}
	h := handlers.NewJobsHandler(deps.JobsRepo)
	v1.With(auth.RequireScopes("jobs:read")).Get("/jobs/{job_id}", h.Get)
}

// mountAdminCost wires the Phase 4B admin cost surface. Every route requires
// the admin:costs scope (docs/architecture/admin-control-surface.md).
func mountAdminCost(v1 chi.Router, deps Deps) {
	if deps.AdminCost == nil {
		return
	}
	h := handlers.NewAdminCostHandler(deps.AdminCost)
	v1.Route("/admin", func(a chi.Router) {
		a.Use(auth.RequireScopes(scopeAdminCosts))

		a.Post("/price-book", h.CreatePrice)
		a.Get("/price-book", h.ListPrices)
		a.Get("/price-book/{price_id}", h.GetPrice)
		a.Put("/price-book/{price_id}", h.UpdatePrice)

		a.Post("/cost-budgets", h.CreateBudget)
		a.Get("/cost-budgets", h.ListBudgets)
		a.Put("/cost-budgets/{budget_id}", h.UpdateBudget)

		a.Get("/cost-reservations", h.ListReservations)
	})
}
