package providers

// Capability reconciliation (PRD 03 §8 — Provider Capability Floor).
//
// A provider route stored in the DB carries a required_capability the platform
// trusts at request-to-route matching time. But config is mutable: a route can
// CLAIM a capability (e.g. pack_capable) that the actual provider adapter behind
// it does NOT support. If that mismatch is not caught, the platform silently
// routes consistency-critical character/pack work to a provider that cannot hold
// identity, producing drifted recurring characters.
//
// The helpers here are the single source of truth for "does this provider
// satisfy this route's required capability", separate from request-to-route
// matching (which stays EXACT — see internal/providers/routing). They let the
// boot-time reconciler and the route resolver fail closed instead of trusting
// config.

// capabilityImplications maps a capability a provider HAS to the full set of
// capabilities it can therefore SATISFY, encoding the §8.3 hierarchy:
//
//	production_capable ⊇ pack_capable ⊇ identity_capable
//
// scene_capable and draft_only are PARALLEL axes (§8.3: "scene_capable is
// parallel to the identity axis"): each satisfies only itself. The identity axis
// does not imply the scene axis, and vice-versa. Any capability not present here
// satisfies only itself via the exact-match check in CapabilitySatisfies, so a
// future enum value fails closed until it is classified explicitly.
var capabilityImplications = map[Capability]map[Capability]bool{
	CapabilityProductionCapable: {
		CapabilityProductionCapable: true,
		CapabilityPackCapable:       true,
		CapabilityIdentityCapable:   true,
	},
	CapabilityPackCapable: {
		CapabilityPackCapable:     true,
		CapabilityIdentityCapable: true,
	},
	CapabilityIdentityCapable: {
		CapabilityIdentityCapable: true,
	},
	CapabilitySceneCapable: {
		CapabilitySceneCapable: true,
	},
	CapabilityDraftOnly: {
		CapabilityDraftOnly: true,
	},
}

// CapabilitySatisfies reports whether a provider advertising `have` can serve a
// route requiring `need`, applying the §8.3 hierarchy. An exact match always
// satisfies; otherwise `have` must imply `need`. Unknown/empty capabilities
// satisfy only themselves, so the function fails closed for anything it does not
// recognize.
func CapabilitySatisfies(have, need Capability) bool {
	if have == need {
		return true
	}
	return capabilityImplications[have][need]
}

// CapabilitiesSatisfy reports whether ANY of a provider's advertised
// capabilities satisfies the required capability. This is the provider-satisfies
// -route check used by the reconciler and the resolver — distinct from
// request-to-route matching, which compares the request's requested capability
// to route.required_capability EXACTLY.
func CapabilitiesSatisfy(have []Capability, need Capability) bool {
	for _, h := range have {
		if CapabilitySatisfies(h, need) {
			return true
		}
	}
	return false
}

// IdentityReadiness summarizes whether the configured providers can actually
// serve recurring-character / pack work (§8 readiness). It distinguishes REAL
// production providers from synthetic/test-only providers (mock): a synthetic
// provider may satisfy capability tests in dev/test, but it must NOT make
// production readiness report that an identity-capable provider is configured.
type IdentityReadiness struct {
	// RealIdentityCapable is true when at least one NON-synthetic provider
	// satisfies identity_capable (the production readiness signal).
	RealIdentityCapable bool
	// SyntheticIdentityCapable is true when a synthetic/test provider satisfies
	// identity_capable (acceptable for dev/test, never for production readiness).
	SyntheticIdentityCapable bool
	// RealProviders / SyntheticProviders list the provider ids in each class that
	// satisfy identity_capable, for structured startup logs.
	RealProviders      []string
	SyntheticProviders []string
}

// AssessIdentityReadiness inspects a provider capability index (provider_id →
// advertised capabilities) and reports whether a real identity-capable provider
// is configured. Synthetic providers are tracked separately so dev/test can run
// against mock while production readiness reports honestly.
func AssessIdentityReadiness(index map[string]ProviderCapabilities) IdentityReadiness {
	var r IdentityReadiness
	for id, caps := range index {
		if !CapabilitiesSatisfy(caps.Capabilities, CapabilityIdentityCapable) {
			continue
		}
		if caps.Synthetic {
			r.SyntheticIdentityCapable = true
			r.SyntheticProviders = append(r.SyntheticProviders, id)
			continue
		}
		r.RealIdentityCapable = true
		r.RealProviders = append(r.RealProviders, id)
	}
	return r
}
