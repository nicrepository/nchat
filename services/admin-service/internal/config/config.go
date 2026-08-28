package config

import (
	"errors"
	"strings"
	"time"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
)

const (
	serviceName = "admin-service"
	defaultPort = 8085

	defaultJWTIssuer   = "nchat-auth"
	defaultJWTAudience = "nchat-api"

	// The administrative session is deliberately shorter-lived than the chat
	// session (auth.auth_policy_settings.session_idle_timeout_minutes, 60 by
	// default). A console that can suspend accounts should not sit unattended
	// on a locked-away tab for an hour.
	defaultSessionIdleMinutes     = 15
	defaultSessionAbsoluteMinutes = 480

	minJWTSecretBytes = 32
)

// Environment is the deployment label the console displays. It comes from the
// service configuration, never from the request: a hostname, a query string or
// a stored preference must not be able to make the console claim it is
// somewhere else.
type Environment string

const (
	EnvironmentDevelopment Environment = "DEVELOPMENT"
	EnvironmentStaging     Environment = "STAGING"
	EnvironmentProduction  Environment = "PRODUCTION"
)

type Config struct {
	ServiceName              string
	Env                      string
	Port                     int
	ReadHeaderTimeoutSeconds int
	DatabaseURL              string
	DBConnectTimeoutSeconds  int

	AuthJWTHMACSecret string
	AuthJWTIssuer     string
	AuthJWTAudience   string

	SessionIdleTTL     time.Duration
	SessionAbsoluteTTL time.Duration

	// AllowedOrigins is the explicit CORS allowlist for the administrative
	// browser origin. Empty means same-origin only: no CORS headers are
	// emitted at all, which is the correct answer for the deployed topology
	// where the console and the Admin API share a host.
	AllowedOrigins []string

	TrustedProxyCIDRs string

	SessionRateLimitPerMinute int
	SessionRateLimitBurst     int
}

func Load() Config {
	return Config{
		ServiceName:               serviceName,
		Env:                       platformconfig.GetString("APP_ENV", "development"),
		Port:                      platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds:  platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		DatabaseURL:               strings.TrimSpace(platformconfig.GetString("DATABASE_URL", "")),
		DBConnectTimeoutSeconds:   positiveInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		AuthJWTHMACSecret:         platformconfig.GetString("AUTH_JWT_HMAC_SECRET", ""),
		AuthJWTIssuer:             strings.TrimSpace(platformconfig.GetString("AUTH_JWT_ISSUER", defaultJWTIssuer)),
		AuthJWTAudience:           strings.TrimSpace(platformconfig.GetString("AUTH_JWT_AUDIENCE", defaultJWTAudience)),
		SessionIdleTTL:            minutes("ADMIN_SESSION_IDLE_MINUTES", defaultSessionIdleMinutes),
		SessionAbsoluteTTL:        minutes("ADMIN_SESSION_ABSOLUTE_MINUTES", defaultSessionAbsoluteMinutes),
		AllowedOrigins:            splitOrigins(platformconfig.GetString("ADMIN_ALLOWED_ORIGINS", "")),
		TrustedProxyCIDRs:         platformconfig.GetString("AUTH_TRUSTED_PROXY_CIDRS", ""),
		SessionRateLimitPerMinute: positiveInt("ADMIN_SESSION_RATE_LIMIT_PER_MINUTE", 30),
		SessionRateLimitBurst:     positiveInt("ADMIN_SESSION_RATE_LIMIT_BURST", 10),
	}
}

// Environment maps the deployment's APP_ENV onto the label the console shows.
//
// Unrecognized values resolve to PRODUCTION on purpose. The banner exists so
// an operator knows how much damage a click can do; guessing "development" for
// an environment nobody described would understate exactly that.
func (c Config) Environment() Environment {
	switch strings.ToLower(strings.TrimSpace(c.Env)) {
	case "development", "dev", "local", "nchat-dev", "test":
		return EnvironmentDevelopment
	case "staging", "stage", "homolog":
		return EnvironmentStaging
	default:
		return EnvironmentProduction
	}
}

// AdminAPIEnabled reports whether the privileged surface has everything it
// needs. When it is false the Admin API answers 503 rather than serving an
// unguarded route: a partially configured pod must not be reachable, and it
// must not look healthy enough to be trusted either.
func (c Config) AdminAPIEnabled() bool {
	return c.ValidateAdminAPI() == nil
}

func (c Config) ValidateAdminAPI() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required for the admin API")
	}
	if len([]byte(c.AuthJWTHMACSecret)) < minJWTSecretBytes {
		return errors.New("AUTH_JWT_HMAC_SECRET must be at least 32 bytes")
	}
	if c.AuthJWTIssuer == "" || c.AuthJWTAudience == "" {
		return errors.New("AUTH_JWT_ISSUER and AUTH_JWT_AUDIENCE are required")
	}
	if c.SessionIdleTTL <= 0 || c.SessionAbsoluteTTL <= 0 {
		return errors.New("admin session TTLs must be positive")
	}
	if c.SessionIdleTTL > c.SessionAbsoluteTTL {
		return errors.New("admin session idle TTL cannot exceed the absolute TTL")
	}
	return nil
}

func splitOrigins(raw string) []string {
	origins := make([]string, 0)
	for _, candidate := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(candidate)
		if trimmed != "" && trimmed != "*" {
			origins = append(origins, trimmed)
		}
	}
	return origins
}

func minutes(key string, fallback int) time.Duration {
	return time.Duration(positiveInt(key, fallback)) * time.Minute
}

func positiveInt(key string, fallback int) int {
	value := platformconfig.GetInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}
