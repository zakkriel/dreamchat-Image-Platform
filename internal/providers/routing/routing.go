// Package routing resolves a generation request to a concrete provider route
// (Phase 7A). Resolution is deterministic and data-driven: it reads
// provider_routes joined to provider_models, filters on what the schema can
// express (active route, active model, operation, quality tier, requested
// capability) and on provider availability (only providers configured in this
// process), then applies an explicit, tested tie-break.
//
// Resolution happens ONCE, at job-creation time in the handler — never in the
// worker. The resolved provider/model/route is persisted on the job so the
// worker consumes exactly what was priced.
//
// Pricing is intentionally NOT part of resolution. A resolved model with no
// active price fails later at cost-reservation time as no_price_entry (422);
// route selection itself fails as no_route / unsupported_capability /
// provider_unavailable_for_route (also 422). Keeping the two concerns separate
// is what lets each failure carry the right reason.
package routing

import (
	"context"
	"errors"
	"sort"
)

// Resolution failures. All three map to HTTP 422 at the handler boundary; the
// distinct sentinels let the handler emit the right error code.
var (
	// ErrNoRoute: no active route to an available provider satisfies the
	// operation + quality tier.
	ErrNoRoute = errors.New("routing: no route satisfies the request")
	// ErrUnsupportedCapability: routes exist for the operation/tier but none
	// satisfy the requested capability (e.g. a true_preview requirement).
	ErrUnsupportedCapability = errors.New("routing: no route satisfies the requested capability")
	// ErrProviderUnavailableForRoute: the only routes matching the operation are
	// to providers that are not configured/available in this process.
	ErrProviderUnavailableForRoute = errors.New("routing: route matched but its provider is unavailable")
)

// ResolveRequest is the routing input derived from a generation request.
type ResolveRequest struct {
	TenantID      string
	OperationType string
	QualityTier   string
	LatencyTier   string
	// RequiredCapability, when non-empty, restricts selection to routes whose
	// provider_routes.required_capability matches exactly (e.g. scene_capable,
	// pack_capable). This is the general route capability — distinct from the
	// preview capability below.
	RequiredCapability string
	// RequiredPreviewCapability, when non-empty and not "no_preview", restricts
	// selection to routes whose preview_capability matches. Optional /
	// future-facing: Phase 7A callers leave it empty (true_preview is 7B).
	RequiredPreviewCapability string
	// ProviderPreference, when non-empty, ranks routes for that provider ahead of
	// others during tie-break (e.g. from IMAGE_PROVIDER). It is a preference, not
	// a hard filter: an unavailable preferred provider is simply skipped.
	ProviderPreference string
}

// ResolvedRoute is the single route chosen for a request.
type ResolvedRoute struct {
	ProviderID        string
	ProviderRouteID   string
	ProviderModelID   string
	OperationType     string
	PreviewCapability string
}

// Route is one candidate row (provider_routes joined to its model's status).
type Route struct {
	RouteID            string
	ProviderID         string
	ModelID            string
	OperationType      string
	RequiredCapability string
	PreviewCapability  string
	QualityTier        string
	LatencyTier        string
	Priority           int32
	Enabled            bool
	ModelActive        bool
}

// RouteSource lists candidate routes for an operation. The DB-backed
// implementation (DBRouteSource) returns the joined provider_routes /
// provider_models rows; unit tests supply an in-memory list so the resolver can
// be exercised without a database.
type RouteSource interface {
	ListRoutes(ctx context.Context, operationType string) ([]Route, error)
}

// Resolver selects a route deterministically from a RouteSource and a set of
// available providers.
type Resolver struct {
	source    RouteSource
	available map[string]bool
}

// NewResolver builds a resolver over the given route source and availability
// set. available is the set of provider ids configured in this process (mock
// always; bfl only with a key) — a route to a provider not in this set is never
// selected.
func NewResolver(source RouteSource, available map[string]bool) *Resolver {
	if available == nil {
		available = map[string]bool{}
	}
	return &Resolver{source: source, available: available}
}

// Resolve returns the chosen route or one of the sentinel errors.
//
// Filter / tie-break precedence (explicit and tested):
//
//	active route + active model + operation match   (hard filter; else no_route)
//	provider availability                            (hard filter; else provider_unavailable_for_route)
//	quality tier match (when requested)              (hard filter; else no_route)
//	required_capability match (when requested)       (hard filter; else unsupported_capability)
//	requested preview capability (when requested)    (hard filter; else unsupported_capability)
//	-- among survivors, ranked by: --
//	latency tier match (when requested)              (matching first)
//	configured provider preference (when given)      (preferred first)
//	provider_route.priority ASC                      (lower = preferred)
//	provider_id ASC, model_id ASC, route_id ASC      (final deterministic order)
func (r *Resolver) Resolve(ctx context.Context, req ResolveRequest) (ResolvedRoute, error) {
	candidates, err := r.candidates(ctx, req)
	if err != nil {
		return ResolvedRoute{}, err
	}

	// Stage 6: deterministic tie-break.
	best := candidates[0]
	for _, rt := range candidates[1:] {
		if ranksBefore(rt, best, req) {
			best = rt
		}
	}

	return resolvedRouteFrom(best), nil
}

// ResolveChain returns the ordered fallback chain for a request (Phase 7C-4):
// every candidate that survives the same Stage 1–5 hard filters as Resolve,
// sorted by the existing total order (ranksBefore), best (the primary) first. It
// shares the exact filtering and sentinel errors with Resolve via the private
// candidates helper, so a request that resolves a route here also resolves under
// Resolve and ResolveChain(...)[0] equals Resolve(...). It returns the same
// sentinel error (no_route / unsupported_capability /
// provider_unavailable_for_route) when no candidate survives.
//
// The handler resolves this chain once, at job-creation time, then filters it to
// the same-price-class subset and persists it on the job so the worker can walk
// the alternates on a primary-provider failure — without ever re-resolving or
// re-reserving cost.
func (r *Resolver) ResolveChain(ctx context.Context, req ResolveRequest) ([]ResolvedRoute, error) {
	candidates, err := r.candidates(ctx, req)
	if err != nil {
		return nil, err
	}

	// Stable sort by the existing total order so ties never depend on the source
	// row ordering — the same deterministic order Resolve's max-scan produces,
	// extended to the full surviving set. SliceStable keeps ranksBefore as the
	// sole authority (it is already a total order, so stability is belt-and-
	// suspenders for equal-rank rows, which cannot occur given route_id is unique).
	ordered := make([]Route, len(candidates))
	copy(ordered, candidates)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ranksBefore(ordered[i], ordered[j], req)
	})

	chain := make([]ResolvedRoute, 0, len(ordered))
	for _, rt := range ordered {
		chain = append(chain, resolvedRouteFrom(rt))
	}
	return chain, nil
}

// candidates applies the Stage 1–5 hard filters shared by Resolve and
// ResolveChain (active route + active model + operation, provider availability,
// quality tier, required_capability, required preview capability), returning the
// surviving candidate rows in source order or one of the sentinel errors. It is
// the single source of the filter precedence so the two entry points can never
// drift: ResolveChain[0] is guaranteed to equal Resolve's pick because both rank
// the very same survivor set with the very same ranksBefore order.
//
// Filter / tie-break precedence (explicit and tested):
//
//	active route + active model + operation match   (hard filter; else no_route)
//	provider availability                            (hard filter; else provider_unavailable_for_route)
//	quality tier match (when requested)              (hard filter; else no_route)
//	required_capability match (when requested)       (hard filter; else unsupported_capability)
//	requested preview capability (when requested)    (hard filter; else unsupported_capability)
func (r *Resolver) candidates(ctx context.Context, req ResolveRequest) ([]Route, error) {
	routes, err := r.source.ListRoutes(ctx, req.OperationType)
	if err != nil {
		return nil, err
	}

	// Stage 1: active route + active model + operation match.
	active := make([]Route, 0, len(routes))
	for _, rt := range routes {
		if rt.Enabled && rt.ModelActive && rt.OperationType == req.OperationType {
			active = append(active, rt)
		}
	}
	if len(active) == 0 {
		return nil, ErrNoRoute
	}

	// Stage 2: provider availability. A route whose provider is not configured
	// in this process is never selectable.
	avail := make([]Route, 0, len(active))
	for _, rt := range active {
		if r.available[rt.ProviderID] {
			avail = append(avail, rt)
		}
	}
	if len(avail) == 0 {
		return nil, ErrProviderUnavailableForRoute
	}

	// Stage 3: quality tier (hard filter when the request specifies one).
	candidates := avail
	if req.QualityTier != "" {
		filtered := make([]Route, 0, len(candidates))
		for _, rt := range candidates {
			if rt.QualityTier == req.QualityTier {
				filtered = append(filtered, rt)
			}
		}
		if len(filtered) == 0 {
			return nil, ErrNoRoute
		}
		candidates = filtered
	}

	// Stage 4: general required capability (hard filter when requested). Routes
	// exist for the operation/quality but none satisfy the requested capability →
	// unsupported_capability (NOT no_route).
	if req.RequiredCapability != "" {
		filtered := make([]Route, 0, len(candidates))
		for _, rt := range candidates {
			if rt.RequiredCapability == req.RequiredCapability {
				filtered = append(filtered, rt)
			}
		}
		if len(filtered) == 0 {
			return nil, ErrUnsupportedCapability
		}
		candidates = filtered
	}

	// Stage 5: requested preview capability (hard filter when requested). An
	// empty value or "no_preview" imposes no requirement.
	if req.RequiredPreviewCapability != "" && req.RequiredPreviewCapability != "no_preview" {
		filtered := make([]Route, 0, len(candidates))
		for _, rt := range candidates {
			if rt.PreviewCapability == req.RequiredPreviewCapability {
				filtered = append(filtered, rt)
			}
		}
		if len(filtered) == 0 {
			return nil, ErrUnsupportedCapability
		}
		candidates = filtered
	}

	return candidates, nil
}

// resolvedRouteFrom projects a candidate Route row into the ResolvedRoute the
// handler persists. Shared by Resolve and ResolveChain so the two produce
// byte-identical results for the same row.
func resolvedRouteFrom(rt Route) ResolvedRoute {
	return ResolvedRoute{
		ProviderID:        rt.ProviderID,
		ProviderRouteID:   rt.RouteID,
		ProviderModelID:   rt.ModelID,
		OperationType:     rt.OperationType,
		PreviewCapability: rt.PreviewCapability,
	}
}

// ranksBefore reports whether route a should be preferred over route b for the
// request, implementing the Stage-5 precedence. It is a total, deterministic
// order, so ties never depend on input ordering.
func ranksBefore(a, b Route, req ResolveRequest) bool {
	// 1. latency tier match (when requested).
	if req.LatencyTier != "" {
		aMatch := a.LatencyTier == req.LatencyTier
		bMatch := b.LatencyTier == req.LatencyTier
		if aMatch != bMatch {
			return aMatch
		}
	}
	// 2. configured provider preference.
	if req.ProviderPreference != "" {
		aPref := a.ProviderID == req.ProviderPreference
		bPref := b.ProviderID == req.ProviderPreference
		if aPref != bPref {
			return aPref
		}
	}
	// 3. provider_route.priority ASC (lower = preferred).
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	// 4. provider_id, model_id, route_id ASC.
	if a.ProviderID != b.ProviderID {
		return a.ProviderID < b.ProviderID
	}
	if a.ModelID != b.ModelID {
		return a.ModelID < b.ModelID
	}
	return a.RouteID < b.RouteID
}
