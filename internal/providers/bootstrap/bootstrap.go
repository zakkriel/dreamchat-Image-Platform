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
// registered only when a FAL_KEY is set. This mirrors config.AvailableProviders
// so the route resolver, the boot reconciler, and the worker all see the same
// provider set.
func Registry(cfg *config.Config) *providers.Registry {
	reg := providers.NewRegistry()
	reg.Register(mock.ProviderID, mock.New())
	if cfg.BFLAPIKey != "" {
		reg.Register(bfl.ProviderID, bfl.New(cfg.BFLAPIKey))
	}
	if cfg.FalKey != "" {
		reg.Register(fal.ProviderID, fal.New(cfg.FalKey))
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
