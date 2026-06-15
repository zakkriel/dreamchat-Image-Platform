package config

import (
	"os"
	"strings"
	"testing"
)

var envVars = []string{
	"APP_PORT", "ENVIRONMENT", "LOG_LEVEL", "WORKER_CONCURRENCY",
	"POSTGRES_DSN", "REDIS_ADDR", "REDIS_PASSWORD",
	"S3_BUCKET", "S3_REGION", "S3_ENDPOINT",
	"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_USE_PATH_STYLE",
	"IMAGE_PROVIDER", "BFL_API_KEY",
	"API_TOKEN_PEPPER", "OPENAPI_DOCS_ENABLED",
}

func clearEnv(t *testing.T) {
	t.Helper()
	saved := make(map[string]string)
	for _, v := range envVars {
		if val, ok := os.LookupEnv(v); ok {
			saved[v] = val
			_ = os.Unsetenv(v)
		}
	}
	t.Cleanup(func() {
		for _, v := range envVars {
			_ = os.Unsetenv(v)
		}
		for k, v := range saved {
			_ = os.Setenv(k, v)
		}
	})
}

func TestLoadFailsWithMissingRequiredVars(t *testing.T) {
	clearEnv(t)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing required env vars")
	}
	if !strings.Contains(err.Error(), "POSTGRES_DSN") {
		t.Fatalf("expected POSTGRES_DSN in error, got %v", err)
	}
}

func TestLoadSucceedsWithMockProvider(t *testing.T) {
	clearEnv(t)
	t.Setenv("POSTGRES_DSN", "postgres://localhost/test")
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("S3_BUCKET", "test")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "x")
	t.Setenv("S3_SECRET_ACCESS_KEY", "y")
	t.Setenv("API_TOKEN_PEPPER", "pepper")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ImageProvider != ProviderMock {
		t.Fatalf("expected mock provider by default, got %s", cfg.ImageProvider)
	}
	if cfg.AppPort != 8080 {
		t.Fatalf("expected default port 8080, got %d", cfg.AppPort)
	}
}

func TestOpenAPIDocsEnabledDefaultsByEnvironment(t *testing.T) {
	requiredEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("POSTGRES_DSN", "postgres://localhost/test")
		t.Setenv("REDIS_ADDR", "localhost:6379")
		t.Setenv("S3_BUCKET", "test")
		t.Setenv("S3_REGION", "us-east-1")
		t.Setenv("S3_ENDPOINT", "http://localhost:9000")
		t.Setenv("S3_ACCESS_KEY_ID", "x")
		t.Setenv("S3_SECRET_ACCESS_KEY", "y")
		t.Setenv("API_TOKEN_PEPPER", "pepper")
	}

	cases := []struct {
		name     string
		env      string
		override string
		setFlag  bool
		want     bool
	}{
		{name: "dev unset defaults on", env: "dev", want: true},
		{name: "test unset defaults on", env: "test", want: true},
		{name: "live unset defaults off", env: "live", want: false},
		{name: "live override on", env: "live", setFlag: true, override: "true", want: true},
		{name: "live override off", env: "live", setFlag: true, override: "false", want: false},
		{name: "dev override off still respected", env: "dev", setFlag: true, override: "false", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			requiredEnv(t)
			t.Setenv("ENVIRONMENT", tc.env)
			if tc.setFlag {
				t.Setenv("OPENAPI_DOCS_ENABLED", tc.override)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.OpenAPIDocsEnabled != tc.want {
				t.Fatalf("OpenAPIDocsEnabled = %v, want %v", cfg.OpenAPIDocsEnabled, tc.want)
			}
		})
	}
}

func TestAllowSyntheticProvidersDefaultsByEnvironment(t *testing.T) {
	requiredEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("POSTGRES_DSN", "postgres://localhost/test")
		t.Setenv("REDIS_ADDR", "localhost:6379")
		t.Setenv("S3_BUCKET", "test")
		t.Setenv("S3_REGION", "us-east-1")
		t.Setenv("S3_ENDPOINT", "http://localhost:9000")
		t.Setenv("S3_ACCESS_KEY_ID", "x")
		t.Setenv("S3_SECRET_ACCESS_KEY", "y")
		t.Setenv("API_TOKEN_PEPPER", "pepper")
	}

	cases := []struct {
		name     string
		env      string
		override string
		setFlag  bool
		want     bool
	}{
		{name: "dev unset defaults on", env: "dev", want: true},
		{name: "test unset defaults on", env: "test", want: true},
		{name: "live unset defaults off (safe for public/Railway)", env: "live", want: false},
		{name: "live override on", env: "live", setFlag: true, override: "true", want: true},
		{name: "dev override off respected", env: "dev", setFlag: true, override: "false", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			requiredEnv(t)
			t.Setenv("ENVIRONMENT", tc.env)
			if tc.setFlag {
				t.Setenv("ALLOW_SYNTHETIC_PROVIDERS", tc.override)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.AllowSyntheticProviders != tc.want {
				t.Fatalf("AllowSyntheticProviders = %v, want %v", cfg.AllowSyntheticProviders, tc.want)
			}
		})
	}
}

func TestBFLProviderRequiresAPIKey(t *testing.T) {
	clearEnv(t)
	t.Setenv("POSTGRES_DSN", "postgres://localhost/test")
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("S3_BUCKET", "test")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "x")
	t.Setenv("S3_SECRET_ACCESS_KEY", "y")
	t.Setenv("API_TOKEN_PEPPER", "pepper")
	t.Setenv("IMAGE_PROVIDER", "bfl")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BFL provider selected but BFL_API_KEY missing")
	}
	if !strings.Contains(err.Error(), "BFL_API_KEY") {
		t.Fatalf("expected BFL_API_KEY in error, got %v", err)
	}
}
