package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

const (
	serviceName = "file-service"
	defaultPort = 8083

	defaultJWTIssuer   = "nchat-auth"
	defaultJWTAudience = "nchat-api"

	// Upload and download stream up to MaxUploadBytes, so the body timeouts are
	// generous. Slow-request protection comes from ReadHeaderTimeoutSeconds and
	// from the byte cap applied to the body, not from these.
	defaultReadTimeoutSeconds  = 300
	defaultWriteTimeoutSeconds = 300

	defaultSeaweedFSTimeoutSeconds = 120
)

type Config struct {
	ServiceName              string
	Env                      string
	Port                     int
	ReadHeaderTimeoutSeconds int
	ReadTimeoutSeconds       int
	WriteTimeoutSeconds      int

	// UploadsEnabled gates every attachment route and every dependency they
	// need. While false the service is health-only, exactly as before RF-30.
	UploadsEnabled bool

	DatabaseURL             string
	DBConnectTimeoutSeconds int

	AuthJWTHMACSecret string
	AuthJWTIssuer     string
	AuthJWTAudience   string

	// MaxUploadBytes is the RF-32 configurable cap, defaulting to 50 MiB.
	MaxUploadBytes int64

	// EncryptionMasterKey is the standard-base64 32-byte KEK used to wrap each
	// object's data key. There is no default and no hardcoded fallback.
	EncryptionMasterKey string

	// MalwareScanRequired keeps a fresh upload in pending_scan, which is not
	// downloadable, until the asynchronous antimalware worker clears it. It
	// defaults to true and must stay true wherever SECURITY.md applies. Setting
	// it to false is a local-development affordance for an environment that has
	// no scanner: uploads finalise as clean and are immediately downloadable.
	MalwareScanRequired bool

	SeaweedFSFilerURL       string
	SeaweedFSTimeoutSeconds int

	uploadsEnabledInvalid      bool
	malwareScanRequiredInvalid bool
	maxUploadBytesInvalid      bool
}

func Load() Config {
	uploadsEnabled, uploadsEnabledInvalid := configuredBool("FILE_UPLOADS_ENABLED", false)
	scanRequired, scanRequiredInvalid := configuredBool("FILE_MALWARE_SCAN_REQUIRED", true)
	maxUploadBytes, maxUploadBytesInvalid := configuredInt64("FILE_MAX_UPLOAD_BYTES", domain.DefaultMaxUploadBytes)

	return Config{
		ServiceName:                serviceName,
		Env:                        platformconfig.GetString("APP_ENV", "development"),
		Port:                       platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds:   positiveInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		ReadTimeoutSeconds:         positiveInt("READ_TIMEOUT_SECONDS", defaultReadTimeoutSeconds),
		WriteTimeoutSeconds:        positiveInt("WRITE_TIMEOUT_SECONDS", defaultWriteTimeoutSeconds),
		UploadsEnabled:             uploadsEnabled,
		DatabaseURL:                strings.TrimSpace(platformconfig.GetString("DATABASE_URL", "")),
		DBConnectTimeoutSeconds:    positiveInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		AuthJWTHMACSecret:          platformconfig.GetString("AUTH_JWT_HMAC_SECRET", ""),
		AuthJWTIssuer:              strings.TrimSpace(platformconfig.GetString("AUTH_JWT_ISSUER", defaultJWTIssuer)),
		AuthJWTAudience:            strings.TrimSpace(platformconfig.GetString("AUTH_JWT_AUDIENCE", defaultJWTAudience)),
		MaxUploadBytes:             maxUploadBytes,
		EncryptionMasterKey:        platformconfig.GetString("FILE_ENCRYPTION_MASTER_KEY", ""),
		MalwareScanRequired:        scanRequired,
		SeaweedFSFilerURL:          strings.TrimSpace(platformconfig.GetString("SEAWEEDFS_FILER_URL", "")),
		SeaweedFSTimeoutSeconds:    positiveInt("SEAWEEDFS_TIMEOUT_SECONDS", defaultSeaweedFSTimeoutSeconds),
		uploadsEnabledInvalid:      uploadsEnabledInvalid,
		malwareScanRequiredInvalid: scanRequiredInvalid,
		maxUploadBytesInvalid:      maxUploadBytesInvalid,
	}
}

// Validate fails closed: while uploads are enabled every dependency the feature
// needs must be present and well formed. It is split into the three questions
// it actually asks — limits, scan policy, dependencies — so each stays readable
// on its own.
func (c Config) Validate() error {
	if c.uploadsEnabledInvalid {
		return errors.New("FILE_UPLOADS_ENABLED must be a valid boolean")
	}
	if c.malwareScanRequiredInvalid {
		return errors.New("FILE_MALWARE_SCAN_REQUIRED must be a valid boolean")
	}
	if !c.UploadsEnabled {
		return nil
	}
	if err := c.validateUploadLimits(); err != nil {
		return err
	}
	if err := c.validateScanPolicy(); err != nil {
		return err
	}
	return c.validateUploadDependencies()
}

// validateUploadLimits checks the RF-32 cap. An explicitly configured value out
// of bounds is an error rather than something silently corrected.
func (c Config) validateUploadLimits() error {
	if c.maxUploadBytesInvalid {
		return errors.New("FILE_MAX_UPLOAD_BYTES must be a valid integer")
	}
	if c.MaxUploadBytes < domain.MinMaxUploadBytes || c.MaxUploadBytes > domain.MaxMaxUploadBytes {
		return fmt.Errorf(
			"FILE_MAX_UPLOAD_BYTES must be between %d and %d bytes",
			domain.MinMaxUploadBytes, domain.MaxMaxUploadBytes,
		)
	}
	return nil
}

// validateUploadDependencies requires every external dependency the attachment
// routes need to be present and well formed before the service starts.
func (c Config) validateUploadDependencies() error {
	if c.DatabaseURL == "" {
		return errors.New("database URL is required when uploads are enabled")
	}
	if len([]byte(c.AuthJWTHMACSecret)) < 32 {
		return errors.New("JWT HMAC secret must be at least 32 bytes")
	}
	if c.AuthJWTIssuer == "" {
		return errors.New("JWT issuer is required")
	}
	if c.AuthJWTAudience == "" {
		return errors.New("JWT audience is required")
	}
	if err := crypto.ValidateMasterKey(c.EncryptionMasterKey); err != nil {
		// The key value itself never reaches the message.
		return fmt.Errorf("FILE_ENCRYPTION_MASTER_KEY is invalid: %w", err)
	}
	if !validFilerURL(c.SeaweedFSFilerURL) {
		return errors.New("SEAWEEDFS_FILER_URL must be a valid HTTP or HTTPS URL without credentials")
	}
	return nil
}

// validateScanPolicy refuses to disable the malware-scan gate outside a
// development environment.
//
// SECURITY.md requires downloads to stay blocked until the scan approves them,
// so FILE_MALWARE_SCAN_REQUIRED=false is a local-development affordance for a
// cluster that has no scanner, never a deployment option. APP_ENV is a reliable
// discriminator here: every Kubernetes overlay sets it explicitly
// (infra/k8s/overlays/*/configmap-patch.yaml uses development, staging and
// nchat-dev), so the check reads a value the deployment owns rather than
// guessing from a hostname.
//
// It fails closed: an APP_ENV this function does not recognise is treated as a
// deployed environment and the escape hatch is refused.
func (c Config) validateScanPolicy() error {
	if c.MalwareScanRequired || isDevelopmentEnvironment(c.Env) {
		return nil
	}
	return fmt.Errorf(
		"FILE_MALWARE_SCAN_REQUIRED may only be false in a development environment, got APP_ENV=%q",
		c.Env,
	)
}

func isDevelopmentEnvironment(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "development", "dev", "local", "test", "nchat-dev":
		return true
	default:
		return false
	}
}

// validFilerURL restricts the storage endpoint to a plain http/https origin
// supplied by configuration. Credentials, query strings and fragments are
// refused so the endpoint cannot smuggle anything into a storage request.
func validFilerURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "" || parsed.Path == "/"
}

func configuredBool(key string, fallback bool) (value, invalid bool) {
	raw, configured := os.LookupEnv(key)
	if !configured {
		return fallback, false
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, true
	}
	return parsed, false
}

func configuredInt64(key string, fallback int64) (value int64, invalid bool) {
	raw, configured := os.LookupEnv(key)
	if !configured {
		return fallback, false
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, true
	}
	return parsed, false
}

func positiveInt(key string, fallback int) int {
	value := platformconfig.GetInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}
