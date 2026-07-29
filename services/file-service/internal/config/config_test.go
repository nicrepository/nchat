package config

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// testMasterKey generates a key at run time; no key literal is committed.
func testMasterKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func testJWTSecret(t *testing.T) string {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	return base64.RawStdEncoding.EncodeToString(secret)
}

// enableUploads sets the full happy-path environment, so each test only has to
// override the one variable it exercises.
func enableUploads(t *testing.T) {
	t.Helper()
	t.Setenv("FILE_UPLOADS_ENABLED", "true")
	t.Setenv("DATABASE_URL", "postgres://nchat@localhost:5432/nchat")
	t.Setenv("AUTH_JWT_HMAC_SECRET", testJWTSecret(t))
	t.Setenv("FILE_ENCRYPTION_MASTER_KEY", testMasterKey(t))
	t.Setenv("SEAWEEDFS_FILER_URL", "http://seaweedfs-filer:8888")
}

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ServiceName != "file-service" || cfg.Env != "development" || cfg.Port != 8083 {
		t.Fatalf("unexpected identity defaults: %+v", cfg)
	}
	if cfg.ReadHeaderTimeoutSeconds != 5 {
		t.Fatalf("expected 5s read-header timeout, got %d", cfg.ReadHeaderTimeoutSeconds)
	}
	if cfg.ReadTimeoutSeconds != defaultReadTimeoutSeconds || cfg.WriteTimeoutSeconds != defaultWriteTimeoutSeconds {
		t.Fatalf("unexpected body timeouts: %+v", cfg)
	}
	if cfg.UploadsEnabled {
		t.Fatal("uploads must stay disabled by default")
	}
	if !cfg.MalwareScanRequired {
		t.Fatal("malware scan must be required by default")
	}
	if cfg.AuthJWTIssuer != defaultJWTIssuer || cfg.AuthJWTAudience != defaultJWTAudience {
		t.Fatalf("unexpected jwt defaults: %+v", cfg)
	}
	if cfg.SeaweedFSTimeoutSeconds != defaultSeaweedFSTimeoutSeconds {
		t.Fatalf("unexpected storage timeout: %d", cfg.SeaweedFSTimeoutSeconds)
	}
}

func TestLoadDefaultsMaxUploadToFiftyMiB(t *testing.T) {
	cfg := Load()
	if cfg.MaxUploadBytes != domain.DefaultMaxUploadBytes {
		t.Fatalf("expected the RF-32 default of %d, got %d",
			domain.DefaultMaxUploadBytes, cfg.MaxUploadBytes)
	}
	if cfg.MaxUploadBytes != 52428800 {
		t.Fatalf("expected 50 MiB in bytes, got %d", cfg.MaxUploadBytes)
	}
}

func TestLoadUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("PORT", "18083")
	t.Setenv("FILE_MAX_UPLOAD_BYTES", "10485760")
	t.Setenv("FILE_MALWARE_SCAN_REQUIRED", "false")
	t.Setenv("SEAWEEDFS_FILER_URL", "  http://filer:8888  ")

	cfg := Load()
	if cfg.Env != "test" || cfg.Port != 18083 {
		t.Fatalf("expected env/port overrides, got %+v", cfg)
	}
	if cfg.MaxUploadBytes != 10485760 {
		t.Fatalf("expected the configured cap, got %d", cfg.MaxUploadBytes)
	}
	if cfg.MalwareScanRequired {
		t.Fatal("expected the scan requirement to be overridable")
	}
	if cfg.SeaweedFSFilerURL != "http://filer:8888" {
		t.Fatalf("expected a trimmed filer URL, got %q", cfg.SeaweedFSFilerURL)
	}
}

func TestLoadFallsBackOnNonPositiveTimeouts(t *testing.T) {
	t.Setenv("READ_HEADER_TIMEOUT_SECONDS", "0")
	t.Setenv("READ_TIMEOUT_SECONDS", "0")
	t.Setenv("WRITE_TIMEOUT_SECONDS", "-5")
	t.Setenv("DB_CONNECT_TIMEOUT_SECONDS", "0")
	t.Setenv("SEAWEEDFS_TIMEOUT_SECONDS", "-1")

	cfg := Load()
	if cfg.ReadHeaderTimeoutSeconds != 5 ||
		cfg.ReadTimeoutSeconds != defaultReadTimeoutSeconds ||
		cfg.WriteTimeoutSeconds != defaultWriteTimeoutSeconds ||
		cfg.DBConnectTimeoutSeconds != 5 ||
		cfg.SeaweedFSTimeoutSeconds != defaultSeaweedFSTimeoutSeconds {
		t.Fatalf("expected fallbacks for non-positive timeouts, got %+v", cfg)
	}
}

func TestValidatePassesWhileUploadsAreDisabled(t *testing.T) {
	// No database, no key, no storage: a health-only deployment stays valid.
	if err := Load().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAcceptsAFullyConfiguredService(t *testing.T) {
	enableUploads(t)
	if err := Load().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsInvalidBooleans(t *testing.T) {
	t.Run("uploads enabled", func(t *testing.T) {
		t.Setenv("FILE_UPLOADS_ENABLED", "maybe")
		assertValidationError(t, Load(), "FILE_UPLOADS_ENABLED")
	})
	t.Run("scan required", func(t *testing.T) {
		t.Setenv("FILE_MALWARE_SCAN_REQUIRED", "sometimes")
		assertValidationError(t, Load(), "FILE_MALWARE_SCAN_REQUIRED")
	})
}

func TestValidateRejectsUnusableUploadCaps(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "not a number", value: "fifty"},
		{name: "zero", value: "0"},
		{name: "negative", value: "-1"},
		{name: "below the floor", value: strconv.FormatInt(domain.MinMaxUploadBytes-1, 10)},
		{name: "above the ceiling", value: strconv.FormatInt(domain.MaxMaxUploadBytes+1, 10)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enableUploads(t)
			t.Setenv("FILE_MAX_UPLOAD_BYTES", tt.value)
			assertValidationError(t, Load(), "FILE_MAX_UPLOAD_BYTES")
		})
	}
}

func TestValidateAcceptsTheBoundsThemselves(t *testing.T) {
	for _, value := range []int64{domain.MinMaxUploadBytes, domain.MaxMaxUploadBytes} {
		enableUploads(t)
		t.Setenv("FILE_MAX_UPLOAD_BYTES", strconv.FormatInt(value, 10))
		if err := Load().Validate(); err != nil {
			t.Fatalf("expected %d to be accepted, got %v", value, err)
		}
	}
}

func TestValidateRequiresEveryUploadDependency(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		message string
	}{
		{name: "database", key: "DATABASE_URL", value: "", message: "database URL"},
		{name: "short jwt secret", key: "AUTH_JWT_HMAC_SECRET", value: "too-short", message: "JWT HMAC secret"},
		{name: "empty jwt issuer", key: "AUTH_JWT_ISSUER", value: "  ", message: "JWT issuer"},
		{name: "empty jwt audience", key: "AUTH_JWT_AUDIENCE", value: "", message: "JWT audience"},
		{name: "missing master key", key: "FILE_ENCRYPTION_MASTER_KEY", value: "", message: "FILE_ENCRYPTION_MASTER_KEY"},
		{name: "non base64 master key", key: "FILE_ENCRYPTION_MASTER_KEY", value: "%%%", message: "FILE_ENCRYPTION_MASTER_KEY"},
		{name: "short master key", key: "FILE_ENCRYPTION_MASTER_KEY", value: base64.StdEncoding.EncodeToString(make([]byte, 16)), message: "FILE_ENCRYPTION_MASTER_KEY"},
		{name: "missing filer url", key: "SEAWEEDFS_FILER_URL", value: "", message: "SEAWEEDFS_FILER_URL"},
		//nolint:gosec // G101: inert fixture asserting that a credentialed endpoint is refused.
		{name: "filer url with credentials", key: "SEAWEEDFS_FILER_URL", value: "http://user:pass@filer:8888", message: "SEAWEEDFS_FILER_URL"},
		{name: "filer url with query", key: "SEAWEEDFS_FILER_URL", value: "http://filer:8888?a=b", message: "SEAWEEDFS_FILER_URL"},
		{name: "filer url with fragment", key: "SEAWEEDFS_FILER_URL", value: "http://filer:8888#f", message: "SEAWEEDFS_FILER_URL"},
		{name: "filer url with path", key: "SEAWEEDFS_FILER_URL", value: "http://filer:8888/buckets", message: "SEAWEEDFS_FILER_URL"},
		{name: "filer url with wrong scheme", key: "SEAWEEDFS_FILER_URL", value: "file:///etc/passwd", message: "SEAWEEDFS_FILER_URL"},
		{name: "filer url without host", key: "SEAWEEDFS_FILER_URL", value: "http://", message: "SEAWEEDFS_FILER_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enableUploads(t)
			t.Setenv(tt.key, tt.value)
			assertValidationError(t, Load(), tt.message)
		})
	}
}

func TestValidateNeverEchoesTheMasterKey(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString(make([]byte, 20))
	enableUploads(t)
	t.Setenv("FILE_ENCRYPTION_MASTER_KEY", secret)

	err := Load().Validate()
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("the configured key must never appear in an error message")
	}
}

func TestValidateAcceptsHTTPSFilerURL(t *testing.T) {
	enableUploads(t)
	t.Setenv("SEAWEEDFS_FILER_URL", "https://filer.internal/")
	if err := Load().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertValidationError(t *testing.T, cfg Config, contains string) {
	t.Helper()
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected a validation error mentioning %q", contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("expected the error to mention %q, got %v", contains, err)
	}
}

// SECURITY.md requires the scan gate to hold wherever it applies, so the escape
// hatch must be refused outside a development environment. APP_ENV is what the
// Kubernetes overlays set, so it is what the check reads.
func TestValidateRefusesToDisableTheScanGateOutsideDevelopment(t *testing.T) {
	deployed := []string{"staging", "production", "prod", "nchat-staging", "", "STAGING"}
	for _, env := range deployed {
		t.Run("refused in "+env, func(t *testing.T) {
			enableUploads(t)
			t.Setenv("APP_ENV", env)
			t.Setenv("FILE_MALWARE_SCAN_REQUIRED", "false")
			assertValidationError(t, Load(), "FILE_MALWARE_SCAN_REQUIRED")
		})
	}
}

func TestValidateAllowsDisablingTheScanGateInDevelopment(t *testing.T) {
	// The values the overlays and the local stack actually use.
	for _, env := range []string{"development", "dev", "local", "test", "nchat-dev", "Development"} {
		t.Run("allowed in "+env, func(t *testing.T) {
			enableUploads(t)
			t.Setenv("APP_ENV", env)
			t.Setenv("FILE_MALWARE_SCAN_REQUIRED", "false")
			if err := Load().Validate(); err != nil {
				t.Fatalf("expected %q to allow the dev-only escape hatch, got %v", env, err)
			}
		})
	}
}

// Keeping the gate on is valid in every environment, including the deployed ones.
func TestValidateAcceptsTheScanGateEverywhere(t *testing.T) {
	for _, env := range []string{"development", "staging", "production"} {
		enableUploads(t)
		t.Setenv("APP_ENV", env)
		t.Setenv("FILE_MALWARE_SCAN_REQUIRED", "true")
		if err := Load().Validate(); err != nil {
			t.Fatalf("expected %q to be valid, got %v", env, err)
		}
	}
}
