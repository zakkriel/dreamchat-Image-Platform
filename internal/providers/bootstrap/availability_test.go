package bootstrap

import (
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/config"
)

// AVAILABILITY MUST COME FROM THE REGISTRY, NOT A SECOND HAND-WRITTEN LIST.
//
// The resolver drops any route whose provider is missing from its availability map
// (routing.go stage 2), and the boot reconciler judges routes from the capability
// index, which IS Registry(cfg).Capabilities(). When the two disagree, a route
// reconciles "valid" in the startup log and is then silently unselectable - the
// worst combination, because the log says the route is fine.
//
// Measured 2026-09-02: exactly that. config.AvailableProviders() listed mock, bfl
// and fal only, so `fal_dev` (migration 0020) and `fal_t2i` (0021) were filtered
// out of every resolution. 0020's routes had never once been selectable, and the
// second scene route added specifically to stop bfl being a single point of failure
// did not take a single request: 66 attempts, all bfl, all 402.
//
// This test compares the two sets, so adding an adapter can never again leave a
// route reconciling valid while being unreachable.
func TestAvailabilityCoversEveryRegisteredProvider(t *testing.T) {
	cfg := &config.Config{BFLAPIKey: "bfl-key", FalKey: "fal-key"}

	reg := Registry(cfg)
	registered := reg.Available()
	caps := CapabilityIndex(cfg)
	avail := AvailableProviders(cfg)

	if len(registered) == 0 {
		t.Fatal("registry registered nothing with both keys set")
	}
	for id := range registered {
		if !avail[id] {
			t.Errorf("provider %q is registered but not available to the resolver — its routes reconcile valid and are then silently unselectable", id)
		}
	}
	for id := range caps {
		if !avail[id] {
			t.Errorf("provider %q is in the capability index (so the boot reconciler judges its routes) but not available to the resolver", id)
		}
	}
	for id := range avail {
		if _, ok := reg.Get(id); !ok {
			t.Errorf("provider %q is advertised as available but nothing is registered for it — resolution would pick a route with no adapter", id)
		}
	}
}

// The fal key buys THREE endpoints, and each is a separate provider id because
// capability reconciliation and pricing are per adapter (image:ADR-016/017). All
// three must become available together: they share one credential.
func TestFalKeyMakesEveryFalEndpointAvailable(t *testing.T) {
	none := AvailableProviders(&config.Config{})
	for _, id := range []string{"fal", "fal_dev", "fal_t2i"} {
		if none[id] {
			t.Errorf("%q must not be available without a fal key", id)
		}
	}

	with := AvailableProviders(&config.Config{FalKey: "k"})
	for _, id := range []string{"fal", "fal_dev", "fal_t2i"} {
		if !with[id] {
			t.Errorf("%q must be available once FAL_KEY is set; it is the same credential", id)
		}
	}
	if with["bfl"] {
		t.Error("a fal key must not make bfl available")
	}
}
