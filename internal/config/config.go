package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Provider string

const (
	ProviderMock Provider = "mock"
	ProviderBFL  Provider = "bfl"
)

type Environment string

const (
	EnvDev  Environment = "dev"
	EnvTest Environment = "test"
	EnvLive Environment = "live"
)

type Config struct {
	AppPort           int
	Environment       Environment
	LogLevel          string
	WorkerConcurrency int

	PostgresDSN string
	// PostgresSystemDSN is the DSN for the system / BYPASSRLS role
	// (image_platform_system), used for pre-tenant auth lookups, the worker, the
	// system cost lifecycle, and explicit admin cross-tenant operations
	// (Phase 7C-3). When empty it falls back to PostgresDSN — acceptable for
	// local/dev/CI, where the configured role already bypasses RLS (superuser),
	// but production must set both DSNs to distinct roles. See SystemDSN().
	PostgresSystemDSN string
	RedisAddr         string
	RedisPassword     string

	S3Bucket          string
	S3Region          string
	S3Endpoint        string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3UsePathStyle    bool
	// S3PresignTTL bounds how long a minted presigned read URL stays valid
	// (Phase 6B delivery). Default 15m.
	S3PresignTTL time.Duration

	ImageProvider Provider
	BFLAPIKey     string

	APITokenPepper     string
	OpenAPIDocsEnabled bool

	// AllowSyntheticProviders gates whether synthetic providers (mock) may satisfy
	// identity-axis routes (identity/pack/production) during route resolution
	// (PRD 03 §8). Default: on in dev/test, OFF in live — so a public/production
	// deployment with only a scene-capable real provider fails character/pack
	// requests closed (HTTP 422) instead of resolving synthetic placeholder grids.
	// An explicit ALLOW_SYNTHETIC_PROVIDERS env var overrides the default.
	AllowSyntheticProviders bool
}

func Load() (*Config, error) {
	env := Environment(getEnv("ENVIRONMENT", "dev"))
	cfg := &Config{
		AppPort:           getEnvInt("APP_PORT", 8080),
		Environment:       env,
		LogLevel:          getEnv("LOG_LEVEL", "info"),
		WorkerConcurrency: getEnvInt("WORKER_CONCURRENCY", 4),

		PostgresDSN:       getEnv("POSTGRES_DSN", ""),
		PostgresSystemDSN: getEnv("POSTGRES_SYSTEM_DSN", ""),
		RedisAddr:         getEnv("REDIS_ADDR", ""),
		RedisPassword:     getEnv("REDIS_PASSWORD", ""),

		S3Bucket:          getEnv("S3_BUCKET", ""),
		S3Region:          getEnv("S3_REGION", ""),
		S3Endpoint:        getEnv("S3_ENDPOINT", ""),
		S3AccessKeyID:     getEnv("S3_ACCESS_KEY_ID", ""),
		S3SecretAccessKey: getEnv("S3_SECRET_ACCESS_KEY", ""),
		S3UsePathStyle:    getEnvBool("S3_USE_PATH_STYLE", false),
		S3PresignTTL:      getEnvDuration("S3_PRESIGN_TTL", 15*time.Minute),

		ImageProvider: Provider(getEnv("IMAGE_PROVIDER", string(ProviderMock))),
		BFLAPIKey:     getEnv("BFL_API_KEY", ""),

		APITokenPepper:     getEnv("API_TOKEN_PEPPER", ""),
		OpenAPIDocsEnabled: getEnvBool("OPENAPI_DOCS_ENABLED", defaultDocsEnabled(env)),

		AllowSyntheticProviders: getEnvBool("ALLOW_SYNTHETIC_PROVIDERS", defaultAllowSyntheticProviders(env)),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// defaultDocsEnabled returns the default for OPENAPI_DOCS_ENABLED when the
// env var is unset: docs are on by default in dev/test and off by default
// in live (ADR-015). An explicit env var value overrides this.
func defaultDocsEnabled(env Environment) bool {
	return env != EnvLive
}

// defaultAllowSyntheticProviders returns the default for ALLOW_SYNTHETIC_PROVIDERS
// when the env var is unset: synthetic providers (mock) may satisfy identity/pack
// routes in dev/test but NOT in live (PRD 03 §8). This keeps local/CI mock-pack
// flows working while ensuring a public/production deployment fails character/pack
// requests closed unless a real identity-capable provider is configured. An
// explicit env var value overrides this.
func defaultAllowSyntheticProviders(env Environment) bool {
	return env != EnvLive
}

// AvailableProviders returns the set of provider ids configured/usable in this
// process (Phase 7A). mock is always available; bfl is available only when a
// BFL_API_KEY is set. The route resolver consults this so it never selects a
// route to a provider this process cannot invoke, and the worker registry
// registers exactly these providers.
func (c *Config) AvailableProviders() map[string]bool {
	available := map[string]bool{string(ProviderMock): true}
	if c.BFLAPIKey != "" {
		available[string(ProviderBFL)] = true
	}
	return available
}

// SystemDSN resolves the DSN for the system / BYPASSRLS role. It returns
// PostgresSystemDSN when set, otherwise falls back to PostgresDSN. The fallback
// keeps local/dev/CI working when only POSTGRES_DSN is configured (the role
// there bypasses RLS already); production deployments must set
// POSTGRES_SYSTEM_DSN to a dedicated BYPASSRLS role distinct from the
// RLS-enforced API role.
func (c *Config) SystemDSN() string {
	if c.PostgresSystemDSN != "" {
		return c.PostgresSystemDSN
	}
	return c.PostgresDSN
}

func (c *Config) validate() error {
	var missing []string

	switch c.Environment {
	case EnvDev, EnvTest, EnvLive:
	default:
		return fmt.Errorf("invalid ENVIRONMENT %q (expected dev|test|live)", c.Environment)
	}

	switch c.ImageProvider {
	case ProviderMock:
	case ProviderBFL:
		if c.BFLAPIKey == "" {
			missing = append(missing, "BFL_API_KEY")
		}
	default:
		return fmt.Errorf("invalid IMAGE_PROVIDER %q (expected mock|bfl)", c.ImageProvider)
	}

	if c.PostgresDSN == "" {
		missing = append(missing, "POSTGRES_DSN")
	}
	if c.RedisAddr == "" {
		missing = append(missing, "REDIS_ADDR")
	}
	if c.S3Bucket == "" {
		missing = append(missing, "S3_BUCKET")
	}
	if c.S3Region == "" {
		missing = append(missing, "S3_REGION")
	}
	if c.S3Endpoint == "" {
		missing = append(missing, "S3_ENDPOINT")
	}
	if c.S3AccessKeyID == "" {
		missing = append(missing, "S3_ACCESS_KEY_ID")
	}
	if c.S3SecretAccessKey == "" {
		missing = append(missing, "S3_SECRET_ACCESS_KEY")
	}
	if c.APITokenPepper == "" {
		missing = append(missing, "API_TOKEN_PEPPER")
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return nil
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// getEnvDuration parses a Go duration string (e.g. "15m", "1h"). A bare
// integer is treated as seconds for convenience. Falls back to def when unset
// or unparseable.
func getEnvDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

var ErrMissingEnv = errors.New("missing required environment variable")
