package config

import platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"

const (
	serviceName = "auth-service"
	defaultPort = 8081

	defaultJWTIssuer              = "nchat-auth"
	defaultJWTAudience            = "nchat-api"
	defaultAccessTokenTTLSeconds  = 900
	defaultRefreshTokenTTLSeconds = 2592000
)

type Config struct {
	ServiceName                string
	Env                        string
	Port                       int
	ReadHeaderTimeoutSeconds   int
	DatabaseURL                string
	DBConnectTimeoutSeconds    int
	AdminBootstrapToken        string
	AuthJWTHMACSecret          string
	AuthJWTIssuer              string
	AuthJWTAudience            string
	AuthAccessTokenTTLSeconds  int
	AuthRefreshTokenTTLSeconds int
}

func Load() Config {
	return Config{
		ServiceName:                serviceName,
		Env:                        platformconfig.GetString("APP_ENV", "development"),
		Port:                       platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds:   platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		DatabaseURL:                platformconfig.GetString("DATABASE_URL", ""),
		DBConnectTimeoutSeconds:    platformconfig.GetInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		AdminBootstrapToken:        platformconfig.GetString("ADMIN_BOOTSTRAP_TOKEN", ""),
		AuthJWTHMACSecret:          platformconfig.GetString("AUTH_JWT_HMAC_SECRET", ""),
		AuthJWTIssuer:              platformconfig.GetString("AUTH_JWT_ISSUER", defaultJWTIssuer),
		AuthJWTAudience:            platformconfig.GetString("AUTH_JWT_AUDIENCE", defaultJWTAudience),
		AuthAccessTokenTTLSeconds:  positiveInt("AUTH_ACCESS_TOKEN_TTL_SECONDS", defaultAccessTokenTTLSeconds),
		AuthRefreshTokenTTLSeconds: positiveInt("AUTH_REFRESH_TOKEN_TTL_SECONDS", defaultRefreshTokenTTLSeconds),
	}
}

func positiveInt(key string, fallback int) int {
	value := platformconfig.GetInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}
