package auth

import "context"

type Principal struct {
	TokenID     string
	TenantID    string
	Scopes      []string
	Environment string
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
