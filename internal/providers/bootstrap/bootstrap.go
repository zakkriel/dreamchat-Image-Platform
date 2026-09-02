// Package bootstrap wires the concrete provider adapters configured in a process
// into a providers.Registry, and derives the capability index used for PRD 03
// §8 reconciliation and fail-closed routing. It lives in its own package so both
// cmd/api and cmd/worker share ONE source of provider registration — the API
// resolves routes (and must know real provider capabilities) and the worker
// invokes providers, and they must agree on exactly which providers exist and
// what they can do.
//
// It imports the adapter packages (mock, bfl); the providers package does not,
// so there is no import cycle.
package bootstrap

import (
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/bfl"
	"github.com/zakkriel/drchat-image-platform/internal/providers/fal"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
)

// Registry registers exactly the providers configured in this process: mock is
// always available (synthetic/test provider); bfl is registered only when a
// BFL_API_KEY is set; fal (reference-conditioned, identity/pack-capable) is
// registered only when a FAL_KEY is set. This is the single source of truth that AvailableProviders below reads
// so the route resolver, the boot reconciler, and the worker all see the same
// provider set.
func Registry(cfg *config.Config) *providers.Registry {
	reg := providers.NewRegistry()
	reg.Register(mock.ProviderID, mock.New())
	if cfg.BFLAPIKey != "" {
		reg.Register(bfl.ProviderID, bfl.New(cfg.BFLAPIKey,
			bfl.WithSafetyTolerance(cfg.BFLSafetyTolerance)))
	}
	if cfg.FalKey != "" {
		reg.Register(fal.ProviderID, fal.New(cfg.FalKey))
		// Same key, second endpoint. FLUX.1 Kontext [dev] is registered under
		// its own provider id because capability reconciliation and pricing are
		// per adapter, and it is the cheaper identity path (migration 0020).
		reg.Register(fal.ProviderIDKontextDev, fal.NewKontextDev(cfg.FalKey,
			fal.WithSafetyChecker(cfg.FalSafetyChecker)))
		// Same key, third endpoint: the PROMPT-ONLY scene route. Kontext cannot take
		// scene work (it fails closed without a reference), so before this the only
		// real scene_capable route was bfl - a single provider whose account running
		// dry took every background in the product with it. Registered under its own
		// id because capability reconciliation is per adapter (image:ADR-016).
		reg.Register(fal.ProviderIDFluxPro11, fal.NewFluxPro11(cfg.FalKey,
			fal.WithSafetyChecker(cfg.FalSafetyChecker)))
	}
	return reg
}

// CapabilityIndex returns the advertised capabilities of every provider
// configured in this process, keyed by provider id — the authoritative input to
// routing.Reconcile and the resolver's provider-satisfies-route check. Building
// the adapters does no I/O, so this is safe to call at boot in either process.
func CapabilityIndex(cfg *config.Config) map[string]providers.ProviderCapabilities {
	return Registry(cfg).Capabilities()
}

// AvailableProviders is the set of provider ids the resolver may select, derived
// from THE REGISTRY rather than restated by hand.
//
// WHY IT LIVES HERE AND NOT ON Config. The resolver drops any route whose provider
// is absent from this set (routing.go stage 2), while the boot reconciler judges
// routes from CapabilityIndex - which is the registry. A hand-written second list
// can disagree with the registry, and when it does a route reconciles "valid" in
// the startup log and is then silently unselectable. That is the worst failure
// shape available: the log says the route is fine.
//
// Measured 2026-09-02: config.AvailableProviders() listed mock, bfl and fal only.
// fal_dev (migration 0020) and fal_t2i (0021) were therefore filtered out of every
// resolution - 0020's routes had never once been selectable, and the second scene
// route added specifically so bfl would stop being a single point of failure took
// no requests at all: 66 attempts, all bfl, all 402, while the boot log listed
// both routes as valid.
//
// One credential can buy several endpoints (FAL_KEY buys three), and each is its
// own provider id because capability and price are per adapter (image:ADR-016,
// image:ADR-017). Deriving from the registry makes that automatic: register an
// adapter and it is selectable, with nothing else to remember.
func AvailableProviders(cfg *config.Config) map[string]bool {
	return Registry(cfg).Available()
}
