// Command seed-token inserts ONE API token directly into the api_tokens table
// and prints the raw bearer value exactly once. It is staging / dev ops
// tooling for Railway smoke testing — NOT a user-management or token-admin
// product surface. It exists so a single token can be created without Docker
// Compose and without psql.
//
// Storage matches the auth middleware exactly (internal/auth): only the
// non-secret token_prefix and token_hash = sha256(secret || API_TOKEN_PEPPER)
// are persisted; the raw secret is never stored.
//
// Inputs (env):
//
//	POSTGRES_DSN            required  target database
//	API_TOKEN_PEPPER        required  pepper mixed into the stored hash; MUST
//	                                  match the API/worker pepper or auth fails
//	SEED_TENANT_ID          optional  default "tenant_dev"
//	SEED_TOKEN_NAME         optional  default "staging seed token"
//	SEED_TOKEN_SCOPES       optional  comma-separated; overrides the defaults
//	SEED_TOKEN_PREFIX_KIND  optional  "dev" (default) or "admin"; sets the
//	                                  dci_<kind>_ prefix label and, for "admin",
//	                                  the default admin scope set
//	SEED_TOKEN_ENVIRONMENT  optional  token environment (dev|test|live); defaults
//	                                  to ENVIRONMENT, else "dev". MUST match the
//	                                  API's ENVIRONMENT or auth rejects the token.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Default scopes for a normal staging token (matches scripts/seed_dev_token.sh).
var defaultNormalScopes = []string{
	"images:read",
	"images:write",
	"styles:read",
	"styles:write",
	"jobs:read",
}

// Default scopes for an admin staging token (matches scripts/seed_admin_token.sh).
var defaultAdminScopes = []string{
	"admin:costs",
	"admin:jobs",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed-token: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		return fmt.Errorf("POSTGRES_DSN is required")
	}
	pepper := os.Getenv("API_TOKEN_PEPPER")
	if pepper == "" {
		return fmt.Errorf("API_TOKEN_PEPPER is required")
	}

	tenantID := envOr("SEED_TENANT_ID", "tenant_dev")
	name := envOr("SEED_TOKEN_NAME", "staging seed token")
	kind := envOr("SEED_TOKEN_PREFIX_KIND", "dev")
	environment := envOr("SEED_TOKEN_ENVIRONMENT", envOr("ENVIRONMENT", "dev"))

	scopes := scopesFor(kind)
	if raw := os.Getenv("SEED_TOKEN_SCOPES"); raw != "" {
		scopes = splitScopes(raw)
	}
	if len(scopes) == 0 {
		return fmt.Errorf("no scopes resolved (set SEED_TOKEN_SCOPES)")
	}

	prefix := fmt.Sprintf("dci_%s_%s", kind, randAlnum(8))
	secret := randAlnum(32)
	rawToken := prefix + "_" + secret
	hash := sha256.Sum256([]byte(secret + pepper))
	tokenHash := hex.EncodeToString(hash[:])
	tokenID := "tok_" + randAlnum(16)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connecting to Postgres: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	const q = `
INSERT INTO api_tokens (id, tenant_id, token_prefix, token_hash, name, owner_type, scopes, environment, status)
VALUES ($1, $2, $3, $4, $5, 'tenant', $6, $7, 'active')`
	if _, err := conn.Exec(ctx, q, tokenID, tenantID, prefix, tokenHash, name, scopes, environment); err != nil {
		return fmt.Errorf("inserting token: %w", err)
	}

	// The raw value is printed exactly once — it is NEVER stored.
	fmt.Println("================================================================")
	fmt.Println("Staging API token created. Raw value printed ONCE — save it now.")
	fmt.Println("  Token ID    : " + tokenID)
	fmt.Println("  Tenant ID   : " + tenantID)
	fmt.Println("  Prefix      : " + prefix)
	fmt.Println("  Scopes      : " + strings.Join(scopes, ", "))
	fmt.Println("  Environment : " + environment)
	fmt.Println()
	fmt.Println("  Authorization: Bearer " + rawToken)
	fmt.Println("================================================================")
	return nil
}

func scopesFor(kind string) []string {
	if kind == "admin" {
		return defaultAdminScopes
	}
	return defaultNormalScopes
}

func splitScopes(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// randAlnum returns n cryptographically-random lowercase-alphanumeric
// characters (matching the dci_*/tok_* shape of the existing seed scripts).
func randAlnum(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("seed-token: crypto/rand failed: " + err.Error())
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
