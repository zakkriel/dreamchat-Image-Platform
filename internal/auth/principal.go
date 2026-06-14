package auth

import "context"

// Platform default per-token limits (Phase 7C-2). A token whose override
// column is NULL uses these. Documented in docs/api/rate-limits.md.
const (
	DefaultRequestsPerMinute = 60
	DefaultRequestsPerHour   = 1000
	DefaultMaxConcurrentJobs = 5
)

// Limits is the effective set of per-token throttling limits resolved at auth
// time: a token's override columns when set, otherwise the platform defaults.
// Carrying the resolved values on the Principal lets the rate-limit middleware
// and the jobs service enforce caps without an extra DB round-trip.
type Limits struct {
	RequestsPerMinute int
	RequestsPerHour   int
	MaxConcurrentJobs int
}

// DefaultLimits returns the platform-default limits (no overrides applied).
func DefaultLimits() Limits {
	return Limits{
		RequestsPerMinute: DefaultRequestsPerMinute,
		RequestsPerHour:   DefaultRequestsPerHour,
		MaxConcurrentJobs: DefaultMaxConcurrentJobs,
	}
}

type Principal struct {
	TokenID     string
	TenantID    string
	Scopes      []string
	Environment string

	// Limits are the effective per-token throttling limits (override-or-default),
	// resolved during Verify. Always populated for an authenticated principal.
	Limits Limits
}

func (p *Principal) HasScope(scope string) bool {
	if p == nil {
		return false
	}
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

type ctxKey int

const principalKey ctxKey = iota

func ContextWithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

func PrincipalFromContext(ctx context.Context) *Principal {
	if v, ok := ctx.Value(principalKey).(*Principal); ok {
		return v
	}
	return nil
}
