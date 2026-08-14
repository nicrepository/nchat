package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
)

const (
	serviceName = "media-service"
	defaultPort = 8087

	defaultJWTIssuer   = "nchat-auth"
	defaultJWTAudience = "nchat-api"

	defaultLiveKitTokenTTLSeconds = 300
	minLiveKitTokenTTLSeconds     = 60
	maxLiveKitTokenTTLSeconds     = 600
)

type Config struct {
	ServiceName              string
	Env                      string
	Port                     int
	ReadHeaderTimeoutSeconds int
	ReadTimeoutSeconds       int
	WriteTimeoutSeconds      int
	DatabaseURL              string
	DBConnectTimeoutSeconds  int
	AuthJWTHMACSecret        string
	AuthJWTIssuer            string
	AuthJWTAudience          string
	LiveKitEnabled           bool
	LiveKitAPIURL            string
	LiveKitAPIKey            string
	LiveKitAPISecret         string
	LiveKitTokenTTLSeconds   int
	liveKitEnabledInvalid    bool
}

func Load() Config {
	liveKitEnabled, liveKitEnabledInvalid := configuredBool("LIVEKIT_ENABLED", false)
	return Config{
		ServiceName:              serviceName,
		Env:                      platformconfig.GetString("APP_ENV", "development"),
		Port:                     platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds: platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		ReadTimeoutSeconds:       positiveInt("READ_TIMEOUT_SECONDS", 10),
		WriteTimeoutSeconds:      positiveInt("WRITE_TIMEOUT_SECONDS", 10),
		DatabaseURL:              strings.TrimSpace(platformconfig.GetString("DATABASE_URL", "")),
		DBConnectTimeoutSeconds:  positiveInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		AuthJWTHMACSecret:        platformconfig.GetString("AUTH_JWT_HMAC_SECRET", ""),
		AuthJWTIssuer:            strings.TrimSpace(platformconfig.GetString("AUTH_JWT_ISSUER", defaultJWTIssuer)),
		AuthJWTAudience:          strings.TrimSpace(platformconfig.GetString("AUTH_JWT_AUDIENCE", defaultJWTAudience)),
		LiveKitEnabled:           liveKitEnabled,
		LiveKitAPIURL:            strings.TrimSpace(platformconfig.GetString("LIVEKIT_API_URL", "")),
		LiveKitAPIKey:            strings.TrimSpace(platformconfig.GetString("LIVEKIT_API_KEY", "")),
		LiveKitAPISecret:         platformconfig.GetString("LIVEKIT_API_SECRET", ""),
		LiveKitTokenTTLSeconds:   configuredInt("LIVEKIT_TOKEN_TTL_SECONDS", defaultLiveKitTokenTTLSeconds),
		liveKitEnabledInvalid:    liveKitEnabledInvalid,
	}
}

func (c Config) Validate() error {
	if c.liveKitEnabledInvalid {
		return errors.New("LIVEKIT_ENABLED must be a valid boolean")
	}
	if c.LiveKitEnabled {
		if !validLiveKitAPIURL(c.LiveKitAPIURL) {
			return errors.New("LiveKit API URL must be a valid HTTP, HTTPS, or WSS URL")
		}
		if c.LiveKitAPIKey == "" {
			return errors.New("LiveKit API key is required")
		}
		if c.LiveKitAPISecret == "" {
			return errors.New("LiveKit API secret is required")
		}
		if c.LiveKitTokenTTLSeconds < minLiveKitTokenTTLSeconds || c.LiveKitTokenTTLSeconds > maxLiveKitTokenTTLSeconds {
			return fmt.Errorf("LiveKit token TTL must be between %d and %d seconds", minLiveKitTokenTTLSeconds, maxLiveKitTokenTTLSeconds)
		}
		if c.DatabaseURL == "" {
			return errors.New("database URL is required when LiveKit is enabled")
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
	}
	return nil
}

func configuredBool(key string, fallback bool) (bool, bool) {
	raw, configured := os.LookupEnv(key)
	if !configured {
		return fallback, false
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, true
	}
	return value, false
}

func validLiveKitAPIURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "wss") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "" || parsed.Path == "/"
}

func positiveInt(key string, fallback int) int {
	value := platformconfig.GetInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}

func configuredInt(key string, fallback int) int {
	raw, configured := os.LookupEnv(key)
	if !configured {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}
