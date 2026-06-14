package webhooks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/zakkriel/drchat-image-platform/internal/ids"
)

// secretBytes is the entropy of a generated signing secret (32 bytes -> 64 hex
// chars). HMAC-SHA256 keys of this length are well above the digest size.
const secretBytes = 32

// secretPrefix marks a value as a DreamChat webhook signing secret (mirrors the
// "whsec_" convention common to webhook providers). The secret is opaque to the
// receiver — it is only ever used as the HMAC key.
const secretPrefix = "whsec_"

// ConfigService implements the webhook endpoint config surface backing
// PUT/GET /v1/admin/webhook-endpoint. It owns signing-secret generation
// (crypto/rand) so the secret is created server-side and only set on create.
type ConfigService struct {
	Repo Repository
}

// NewConfigService wires the config service over a Repository.
func NewConfigService(repo Repository) *ConfigService {
	return &ConfigService{Repo: repo}
}

// UpsertEndpoint creates the tenant's active endpoint with a freshly generated
// signing secret, or — when one already exists — updates its URL and preserves
// the existing secret. MVP: exactly one active endpoint per tenant.
func (s *ConfigService) UpsertEndpoint(ctx context.Context, tenantID, rawURL string) (Endpoint, error) {
	existing, err := s.Repo.GetActiveEndpointByTenant(ctx, tenantID)
	switch {
	case err == nil:
		// Update URL, preserve the secret (and return it so callers can verify).
		return s.Repo.UpdateEndpointURL(ctx, existing.ID, rawURL)
	case errors.Is(err, ErrNotFound):
		secret, gerr := generateSecret()
		if gerr != nil {
			return Endpoint{}, gerr
		}
		return s.Repo.InsertEndpoint(ctx, InsertEndpointParams{
			ID:       ids.NewWebhookEndpointID(),
			TenantID: tenantID,
			URL:      rawURL,
			Secret:   secret,
		})
	default:
		return Endpoint{}, err
	}
}

// GetEndpoint returns the tenant's active endpoint (ErrNotFound when none). The
// handler drops the secret before responding.
func (s *ConfigService) GetEndpoint(ctx context.Context, tenantID string) (Endpoint, error) {
	return s.Repo.GetActiveEndpointByTenant(ctx, tenantID)
}

// generateSecret mints a random signing secret using crypto/rand, mirroring how
// the platform mints opaque ids (internal/ids) but with higher entropy suited
// to an HMAC key.
func generateSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return secretPrefix + hex.EncodeToString(b), nil
}
