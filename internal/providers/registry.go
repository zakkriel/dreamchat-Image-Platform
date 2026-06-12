package providers

// Registry maps a provider_id to its concrete ImageProvider adapter (Phase
// 7A). It replaces the pre-7A "one provider from config" model: the worker no
// longer holds a single provider, it looks the adapter up by the resolved
// provider_id persisted on the job. A provider is "available" iff it is
// registered, and a provider is only registered when it is configured (mock
// always; bfl only when BFL_API_KEY is set), so an unconfigured provider can
// never be selected or invoked.
type Registry struct {
	providers map[string]ImageProvider
}

// NewRegistry returns an empty registry. Callers register each configured
// provider with Register.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]ImageProvider)}
}

// Register adds (or replaces) the adapter for a provider id.
func (r *Registry) Register(providerID string, p ImageProvider) {
	r.providers[providerID] = p
}

// Get returns the adapter for a provider id and whether it is registered. The
// worker fails the job clearly (provider unavailable) when ok is false, rather
// than silently falling back to another provider.
func (r *Registry) Get(providerID string) (ImageProvider, bool) {
	p, ok := r.providers[providerID]
	return p, ok
}

// Available returns the set of registered (configured) provider ids. The route
// resolver consults the same set so it never selects a route to a provider this
// process cannot actually invoke.
func (r *Registry) Available() map[string]bool {
	out := make(map[string]bool, len(r.providers))
	for id := range r.providers {
		out[id] = true
	}
	return out
}
