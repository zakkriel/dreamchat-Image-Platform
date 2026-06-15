package routing

import (
	"context"
	"log/slog"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// Boot-time provider capability reconciliation (PRD 03 §8).
//
// At startup the platform reconciles every configured provider route against the
// capabilities the registered provider adapters actually advertise. A route that
// CLAIMS a capability its provider cannot back (config drift) is flagged invalid
// and logged loudly; the route resolver enforces the same check at resolution
// time as defense-in-depth, so an invalid route can never be selected even if it
// is not removed from config. The repo's existing pattern is to fail closed at
// resolution rather than refuse to boot (an unconfigured provider is simply not
// registered, and resolution then fails the request clearly) — reconciliation
// follows that pattern: it disables invalid routes by exclusion + loud structured
// logs, it does not panic the process.

// RouteDecision is the reconciliation outcome for one configured route.
type RouteDecision struct {
	RouteID            string
	ProviderID         string
	ModelID            string
	RequiredCapability string
	// ProviderCapabilities are the capabilities the registered adapter advertises
	// (empty when the provider is not registered in this process).
	ProviderCapabilities []providers.Capability
	// Valid is true when the provider satisfies the route's claimed capability
	// under the §8.3 hierarchy.
	Valid bool
	// Reason explains an invalid decision (empty when valid).
	Reason string
}

// ReconcileReport is the full boot-time reconciliation result.
type ReconcileReport struct {
	Decisions []RouteDecision
	// Readiness reports whether a real (non-synthetic) identity-capable provider
	// is configured (§8 readiness observability).
	Readiness providers.IdentityReadiness
}

// InvalidCount returns how many routes failed reconciliation.
func (r ReconcileReport) InvalidCount() int {
	n := 0
	for _, d := range r.Decisions {
		if !d.Valid {
			n++
		}
	}
	return n
}

// reconcileReasonUnregistered / reconcileReasonCapabilityMismatch are the stable
// reason codes attached to invalid decisions for structured logs.
const (
	reconcileReasonUnregistered       = "provider_not_registered"
	reconcileReasonCapabilityMismatch = "provider_capability_mismatch"
)

// Reconcile checks every route against the provider capability index and returns
// the per-route decisions plus the identity-readiness summary. It is pure (no
// logging, no DB) so it is fully unit-testable; LogReconciliation renders it.
func Reconcile(routes []Route, index map[string]providers.ProviderCapabilities) ReconcileReport {
	report := ReconcileReport{
		Decisions: make([]RouteDecision, 0, len(routes)),
		Readiness: providers.AssessIdentityReadiness(index),
	}
	for _, rt := range routes {
		caps := index[rt.ProviderID]
		d := RouteDecision{
			RouteID:              rt.RouteID,
			ProviderID:           rt.ProviderID,
			ModelID:              rt.ModelID,
			RequiredCapability:   rt.RequiredCapability,
			ProviderCapabilities: caps.Capabilities,
		}
		switch {
		case len(caps.Capabilities) == 0:
			// The provider is not registered in this process. A route to an
			// unregistered provider is already unselectable (availability filter),
			// so this is informational rather than a config error per se — but it is
			// surfaced so an operator notices a route pointing at a provider that was
			// never wired.
			d.Valid = false
			d.Reason = reconcileReasonUnregistered
		case rt.RequiredCapability == "" ||
			providers.CapabilitiesSatisfy(caps.Capabilities, providers.Capability(rt.RequiredCapability)):
			d.Valid = true
		default:
			d.Valid = false
			d.Reason = reconcileReasonCapabilityMismatch
		}
		report.Decisions = append(report.Decisions, d)
	}
	return report
}

// GatherRoutes lists every route across the given operation types from a
// RouteSource, so boot-time reconciliation can inspect the whole route table
// without a dedicated "list all" query. Operations are the platform's known
// OperationType values; routes for an unknown operation are not reconciled (and
// would also never resolve). Errors from a single operation are returned
// immediately so a boot reconciliation surfaces a broken route source.
func GatherRoutes(ctx context.Context, source RouteSource, operations []string) ([]Route, error) {
	var all []Route
	for _, op := range operations {
		routes, err := source.ListRoutes(ctx, op)
		if err != nil {
			return nil, err
		}
		all = append(all, routes...)
	}
	return all, nil
}

// LogReconciliation emits the boot-time reconciliation report as structured logs
// (PRD 03 §8 observability): one line per route carrying route id, provider id,
// model id, required capability, provider capabilities, and the decision; plus a
// single readiness summary line distinguishing a real identity-capable provider
// from synthetic/test-only providers. Invalid routes and a missing real identity
// provider are logged at WARN so they are loud.
func LogReconciliation(logger *slog.Logger, report ReconcileReport) {
	if logger == nil {
		return
	}
	for _, d := range report.Decisions {
		attrs := []any{
			"route_id", d.RouteID,
			"provider_id", d.ProviderID,
			"model_id", d.ModelID,
			"required_capability", d.RequiredCapability,
			"provider_capabilities", capabilityStrings(d.ProviderCapabilities),
			"decision", decisionString(d.Valid),
		}
		if d.Valid {
			logger.Info("provider route reconciliation", attrs...)
		} else {
			logger.Warn("provider route reconciliation: route disabled (fail-closed)", append(attrs, "reason", d.Reason)...)
		}
	}

	rd := report.Readiness
	summary := []any{
		"real_identity_capable_provider", rd.RealIdentityCapable,
		"real_identity_providers", rd.RealProviders,
		"synthetic_identity_capable_provider", rd.SyntheticIdentityCapable,
		"synthetic_identity_providers", rd.SyntheticProviders,
		"invalid_routes", report.InvalidCount(),
	}
	if rd.RealIdentityCapable {
		logger.Info("provider capability readiness", summary...)
	} else {
		logger.Warn("provider capability readiness: no identity-capable provider configured", summary...)
	}
}

func capabilityStrings(caps []providers.Capability) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, string(c))
	}
	return out
}

func decisionString(valid bool) string {
	if valid {
		return "valid"
	}
	return "invalid"
}
