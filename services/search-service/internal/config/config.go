package config

import (
	"errors"
	"strings"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
)

type Config struct {
	ServiceName              string
	Env                      string
	Port                     int
	ReadHeaderTimeoutSeconds int
	DatabaseURL              string
	DBConnectTimeoutSeconds  int
	AuthJWTHMACSecret        string
	AuthJWTIssuer            string
	AuthJWTAudience          string
}

func Load() Config {
	return Config{ServiceName: "search-service", Env: platformconfig.GetString("APP_ENV", "development"), Port: platformconfig.GetInt("PORT", 8086), ReadHeaderTimeoutSeconds: platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5), DatabaseURL: strings.TrimSpace(platformconfig.GetString("DATABASE_URL", "")), DBConnectTimeoutSeconds: platformconfig.GetInt("DB_CONNECT_TIMEOUT_SECONDS", 5), AuthJWTHMACSecret: platformconfig.GetString("AUTH_JWT_HMAC_SECRET", ""), AuthJWTIssuer: strings.TrimSpace(platformconfig.GetString("AUTH_JWT_ISSUER", "nchat-auth")), AuthJWTAudience: strings.TrimSpace(platformconfig.GetString("AUTH_JWT_AUDIENCE", "nchat-api"))}
}
func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("port must be valid")
	}
	if c.ReadHeaderTimeoutSeconds < 1 || c.DBConnectTimeoutSeconds < 1 {
		return errors.New("timeouts must be positive")
	}
	if c.DatabaseURL == "" {
		return errors.New("database URL is required")
	}
	if len([]byte(c.AuthJWTHMACSecret)) < 32 {
		return errors.New("JWT HMAC secret must be at least 32 bytes")
	}
	if c.AuthJWTIssuer == "" || c.AuthJWTAudience == "" {
		return errors.New("JWT issuer and audience are required")
	}
	return nil
}
