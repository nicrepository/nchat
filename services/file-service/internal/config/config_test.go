package config

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
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
	t.Setenv("FILE_ENCRYPTION_MASTER_KEY_ID", "kek-test-active")
	t.Setenv("SEAWEEDFS_FILER_URL", "http://seaweedfs-filer:8888")
}

func TestLoadDefaults(t *testing.T) {
	unsetAppEnv(t)
	cfg := Load()
	// APP_ENV has no default on purpose: an absent value must stay absent so it
	// cannot be mistaken for a declared development environment.
	if cfg.ServiceName != "file-service" || cfg.Env != "" || cfg.Port != 8083 {
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

// FILE_MAX_UPLOAD_BYTES is a deployment ceiling, not the RF-32 limit (issue
// #458): the limit is administrative and read per request from the destination
// workspace. Defaulting the ceiling to the domain maximum is what keeps it from
// binding out of the box, so the administrative value is what applies.
func TestLoadDefaultsMaxUploadToTheDomainCeiling(t *testing.T) {
	cfg := Load()
	if cfg.MaxUploadBytes != domain.MaxMaxUploadBytes {
		t.Fatalf("expected the domain ceiling of %d, got %d",
			domain.MaxMaxUploadBytes, cfg.MaxUploadBytes)
	}
	if cfg.MaxUploadBytes < domain.DefaultMaxUploadBytes {
		t.Fatal("the ceiling must never narrow the RF-32 default on its own")
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
		// The ceiling shares the administrative policy's value space, so it is
		// held to the same whole-MiB rule rather than to a looser one of its own.
		{name: "not a whole MiB", value: "1572864"},
		{name: "one byte above a whole MiB", value: strconv.FormatInt(domain.MinMaxUploadBytes+1, 10)},
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
	for _, value := range []int64{
		domain.MinMaxUploadBytes, domain.DefaultMaxUploadBytes, domain.MaxMaxUploadBytes,
	} {
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
		{name: "31 byte master key", key: "FILE_ENCRYPTION_MASTER_KEY", value: base64.StdEncoding.EncodeToString(make([]byte, 31)), message: "FILE_ENCRYPTION_MASTER_KEY"},
		{name: "33 byte master key", key: "FILE_ENCRYPTION_MASTER_KEY", value: base64.StdEncoding.EncodeToString(make([]byte, 33)), message: "FILE_ENCRYPTION_MASTER_KEY"},
		{name: "missing key id", key: "FILE_ENCRYPTION_MASTER_KEY_ID", value: "", message: "FILE_ENCRYPTION_MASTER_KEY_ID"},
		{name: "invalid key id", key: "FILE_ENCRYPTION_MASTER_KEY_ID", value: "Not A Key Id", message: "FILE_ENCRYPTION_MASTER_KEY_ID"},
		{name: "malformed previous keys", key: "FILE_ENCRYPTION_PREVIOUS_KEYS", value: "no-separator-here", message: "FILE_ENCRYPTION_PREVIOUS_KEYS"},
		{name: "previous key of the wrong length", key: "FILE_ENCRYPTION_PREVIOUS_KEYS", value: "kek-old:" + base64.StdEncoding.EncodeToString(make([]byte, 31)), message: "FILE_ENCRYPTION_PREVIOUS_KEYS"},
		{name: "previous key shadowing the active id", key: "FILE_ENCRYPTION_PREVIOUS_KEYS", value: "kek-test-active:" + base64.StdEncoding.EncodeToString(make([]byte, 32)), message: "FILE_ENCRYPTION_PREVIOUS_KEYS"},
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
	t.Setenv("FILE_ENCRYPTION_PREVIOUS_KEYS", "kek-old:"+secret)

	err := Load().Validate()
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("the configured key must never appear in an error message")
	}
}

// A rotation in progress — one active key plus the previous one kept only for
// reading — must start.
func TestValidateAcceptsAKeyRingUnderRotation(t *testing.T) {
	enableUploads(t)
	t.Setenv("FILE_ENCRYPTION_PREVIOUS_KEYS", "kek-2026-01:"+testMasterKey(t))
	cfg := Load()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EncryptionMasterKeyID != "kek-test-active" {
		t.Fatalf("unexpected active key id %q", cfg.EncryptionMasterKeyID)
	}
}

// Uploads disabled means no key is required at all: the service stays
// health-only exactly as before, instead of refusing to start.
func TestValidateDoesNotRequireKeysWhileUploadsAreDisabled(t *testing.T) {
	t.Setenv("FILE_UPLOADS_ENABLED", "false")
	t.Setenv("FILE_ENCRYPTION_MASTER_KEY", "")
	t.Setenv("FILE_ENCRYPTION_MASTER_KEY_ID", "")
	if err := Load().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
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

// unsetAppEnv removes APP_ENV for one test and restores whatever the machine
// had afterwards. t.Setenv is called first purely to register that restoration,
// so the suite never depends on the ambient environment.
func unsetAppEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "placeholder-restored-by-cleanup")
	if err := os.Unsetenv("APP_ENV"); err != nil {
		t.Fatalf("unset APP_ENV: %v", err)
	}
}

// SECURITY.md requires the scan gate to hold wherever it applies, so the escape
// hatch must be refused unless the deployment explicitly declares itself a
// development environment.
//
// The rule is a positive match against an allowlist, not the absence of a
// production marker: an APP_ENV that is missing, empty, whitespace, misspelled
// or simply unknown must be refused exactly like APP_ENV=production.
func TestValidateRefusesToDisableTheScanGateWithoutAnExplicitDevelopmentEnv(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		unset bool
	}{
		{name: "absent", unset: true},
		{name: "empty", env: ""},
		{name: "whitespace", env: "   "},
		{name: "unknown", env: "unknown"},
		{name: "production", env: "production"},
		{name: "prod", env: "prod"},
		{name: "staging", env: "staging"},
		{name: "stage", env: "stage"},
		{name: "STAGING", env: "STAGING"},
		{name: "nchat-staging", env: "nchat-staging"},
		{name: "homolog", env: "homolog"},
		{name: "typo developmnt", env: "developmnt"},
		{name: "near miss dev-environment", env: "dev-environment"},
		{name: "prefix of a dev value", env: "development-like"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enableUploads(t)
			if tt.unset {
				unsetAppEnv(t)
			} else {
				t.Setenv("APP_ENV", tt.env)
			}
			t.Setenv("FILE_MALWARE_SCAN_REQUIRED", "false")

			assertValidationError(t, Load(), "FILE_MALWARE_SCAN_REQUIRED")
			// The message names the allowlist, so the failure is actionable
			// without reading the source.
			assertValidationError(t, Load(), "APP_ENV")
			assertValidationError(t, Load(), "nchat-dev")
		})
	}
}

// The absent case is the regression this closes: before, an unset APP_ENV was
// defaulted to "development" and silently granted the exception.
func TestAbsentAppEnvIsNeverTreatedAsDevelopment(t *testing.T) {
	unsetAppEnv(t)
	if cfg := Load(); cfg.Env != "" {
		t.Fatalf("an absent APP_ENV must stay empty, got %q", cfg.Env)
	}
	if isExplicitDevelopmentEnvironment("") {
		t.Fatal("an empty APP_ENV must not count as a development environment")
	}
}

// The allowlist is the single decision point, so it is asserted directly as
// well as through Validate.
func TestExplicitDevelopmentEnvironmentAllowlist(t *testing.T) {
	for _, allowed := range []string{
		"development", "dev", "local", "test", "nchat-dev",
		"Development", "DEV", " local ", "NCHAT-DEV",
	} {
		if !isExplicitDevelopmentEnvironment(allowed) {
			t.Fatalf("%q must be recognised as an explicit development environment", allowed)
		}
	}
	for _, refused := range []string{
		"", "   ", "\t", "unknown", "staging", "stage", "production", "prod",
		"homolog", "developmnt", "dev-server", "localhost", "testing", "nchat-prod",
	} {
		if isExplicitDevelopmentEnvironment(refused) {
			t.Fatalf("%q must not be recognised as a development environment", refused)
		}
	}
}

// The scan gate staying on is the safe combination, so an absent APP_ENV must
// not block a service that never asked for the exception.
func TestAbsentAppEnvIsFineWhileTheScanGateHolds(t *testing.T) {
	enableUploads(t)
	unsetAppEnv(t)
	t.Setenv("FILE_MALWARE_SCAN_REQUIRED", "true")
	if err := Load().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// With uploads disabled the service is health-only and validates nothing about
// scanning or keys, whatever APP_ENV says.
func TestHealthOnlyModeIsUnaffectedByAnAbsentAppEnv(t *testing.T) {
	t.Setenv("FILE_UPLOADS_ENABLED", "false")
	t.Setenv("FILE_MALWARE_SCAN_REQUIRED", "false")
	unsetAppEnv(t)

	cfg := Load()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("health-only mode must still start: %v", err)
	}
	if cfg.UploadsEnabled {
		t.Fatal("uploads must stay disabled")
	}
}

func TestValidateAllowsDisablingTheScanGateInDevelopment(t *testing.T) {
	// The values the overlays and the local stack actually use, plus the
	// case- and whitespace-insensitive forms of them.
	for _, env := range []string{
		"development", "dev", "local", "test", "nchat-dev", "Development", "DEV", " local ",
	} {
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

// --- antimalware scanner (RF-22) ----------------------------------------

// An absent scanner address is a supported deployment, and — this is the point
// — it must not relax the gate. Without a daemon the worker does not run, so
// uploads stay in pending_scan; what must never happen is an absent address
// being read as "no scan needed".
func TestAnAbsentScannerAddressDoesNotRelaxTheScanGate(t *testing.T) {
	enableUploads(t)
	unsetAppEnv(t)
	t.Setenv("FILE_MALWARE_SCANNER_ADDRESS", "")

	cfg := Load()
	if cfg.MalwareScannerAddress != "" {
		t.Fatalf("scanner address = %q, want empty", cfg.MalwareScannerAddress)
	}
	if !cfg.MalwareScanRequired {
		t.Fatal("an absent scanner address turned the scan gate off")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsAMalformedScannerAddress(t *testing.T) {
	for name, address := range map[string]string{
		"no port":        "clamav",
		"a URL":          "tcp://clamav:3310",
		"non-numeric":    "clamav:threeThreeOne",
		"port too large": "clamav:70000",
		"port zero":      "clamav:0",
		"no host":        ":3310",
	} {
		t.Run(name, func(t *testing.T) {
			enableUploads(t)
			unsetAppEnv(t)
			t.Setenv("FILE_MALWARE_SCANNER_ADDRESS", address)

			if err := Load().Validate(); err == nil {
				t.Fatalf("Validate accepted %q", address)
			}
		})
	}
}

func TestValidateAcceptsAHostPortScannerAddress(t *testing.T) {
	for _, address := range []string{"clamav:3310", "127.0.0.1:3310", "[::1]:3310"} {
		enableUploads(t)
		unsetAppEnv(t)
		t.Setenv("FILE_MALWARE_SCANNER_ADDRESS", address)

		cfg := Load()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate(%q): %v", address, err)
		}
		if cfg.MalwareScannerAddress != address {
			t.Fatalf("scanner address = %q, want %q", cfg.MalwareScannerAddress, address)
		}
	}
}

// The scan budget has to cover streaming the largest attachment the deployment
// accepts, so it defaults high and a non-positive override falls back rather
// than making every large file unscannable.
func TestScanTimeoutDefaultsAndIgnoresNonPositiveOverrides(t *testing.T) {
	t.Setenv("FILE_MALWARE_SCAN_TIMEOUT_SECONDS", "")
	if got := Load().MalwareScanTimeoutSeconds; got != defaultMalwareScanTimeoutSeconds {
		t.Fatalf("timeout = %d, want %d", got, defaultMalwareScanTimeoutSeconds)
	}
	t.Setenv("FILE_MALWARE_SCAN_TIMEOUT_SECONDS", "0")
	if got := Load().MalwareScanTimeoutSeconds; got != defaultMalwareScanTimeoutSeconds {
		t.Fatalf("timeout = %d for a zero override, want the default", got)
	}
	t.Setenv("FILE_MALWARE_SCAN_TIMEOUT_SECONDS", "45")
	if got := Load().MalwareScanTimeoutSeconds; got != 45 {
		t.Fatalf("timeout = %d, want 45", got)
	}
}

// The bus identifier ends up in an envelope the consumer validates, so a value
// it would reject is dropped here rather than producing events nothing delivers.
func TestWSInstanceIDIsSanitizedRatherThanTrusted(t *testing.T) {
	for name, value := range map[string]string{
		"a glob":     "file-*",
		"a newline":  "file\nsvc",
		"a space":    "file svc",
		"too long":   strings.Repeat("a", 65),
		"whitespace": "   ",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("WS_INSTANCE_ID", value)
			if got := Load().WSInstanceID; got != "" {
				t.Fatalf("WSInstanceID = %q, want it rejected", got)
			}
		})
	}
	t.Setenv("WS_INSTANCE_ID", "file-service-0")
	if got := Load().WSInstanceID; got != "file-service-0" {
		t.Fatalf("WSInstanceID = %q, want it kept", got)
	}
}

// The budget must actually apply. A value above the old hardcoded 300s is the
// case that used to be silently unreachable, so it is the one that matters.
func TestValidateAcceptsAScanTimeoutAboveTheOldFixedBudget(t *testing.T) {
	enableUploads(t)
	unsetAppEnv(t)
	t.Setenv("FILE_MALWARE_SCAN_TIMEOUT_SECONDS", "900")

	cfg := Load()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.MalwareScanTimeoutSeconds != 900 {
		t.Fatalf("timeout = %d, want 900 — the configuration must not be capped",
			cfg.MalwareScanTimeoutSeconds)
	}
}

// The ceiling exists to stop an overflow, not to restore the old cap: it has to
// be well above the default, and a value past it is a start-up error rather
// than a silently clamped one.
func TestValidateRejectsAScanTimeoutThatWouldOverflow(t *testing.T) {
	if maxMalwareScanTimeoutSeconds <= defaultMalwareScanTimeoutSeconds {
		t.Fatal("the ceiling must leave room above the default, or it is the old cap by another name")
	}
	enableUploads(t)
	unsetAppEnv(t)
	t.Setenv("FILE_MALWARE_SCAN_TIMEOUT_SECONDS", strconv.Itoa(maxMalwareScanTimeoutSeconds+1))

	if err := Load().Validate(); err == nil {
		t.Fatal("Validate accepted a timeout past the ceiling")
	}

	t.Setenv("FILE_MALWARE_SCAN_TIMEOUT_SECONDS", strconv.Itoa(maxMalwareScanTimeoutSeconds))
	if err := Load().Validate(); err != nil {
		t.Fatalf("Validate refused the ceiling itself: %v", err)
	}
}
