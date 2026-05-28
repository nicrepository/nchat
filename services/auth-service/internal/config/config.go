package config

import platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"

const (
	serviceName = "auth-service"
	defaultPort = 8081

	defaultJWTIssuer                       = "nchat-auth"
	defaultJWTAudience                     = "nchat-api"
	defaultAccessTokenTTLSeconds           = 900
	defaultRefreshTokenTTLSeconds          = 2592000
	defaultTokenEndpointRateLimitPerMinute = 60
	defaultTokenEndpointRateLimitBurst     = 10
)

type Config struct {
	ServiceName                         string
	Env                                 string
	Port                                int
	ReadHeaderTimeoutSeconds            int
	DatabaseURL                         string
	DBConnectTimeoutSeconds             int
	AdminBootstrapToken                 string
	AuthJWTHMACSecret                   string
	AuthJWTIssuer                       string
	AuthJWTAudience                     string
	AuthAccessTokenTTLSeconds           int
	AuthRefreshTokenTTLSeconds          int
	AuthTokenEndpointRateLimitPerMinute int
	AuthTokenEndpointRateLimitBurst     int
	// AuthTrustedProxyCIDRs is a comma-separated list of CIDRs (e.g. "10.0.0.0/8,172.16.0.0/12")
	// whose X-Forwarded-For header is trusted for client-IP extraction by the rate limiter.
	// Leave empty (default) to always use RemoteAddr — safe for direct or single-instance deployments.
	// In production behind Traefik, set this to the Traefik ingress CIDR.
	AuthTrustedProxyCIDRs string
}

func Load() Config {
	return Config{
		ServiceName:                         serviceName,
		Env:                                 platformconfig.GetString("APP_ENV", "development"),
		Port:                                platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds:            platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		DatabaseURL:                         platformconfig.GetString("DATABASE_URL", ""),
		DBConnectTimeoutSeconds:             platformconfig.GetInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		AdminBootstrapToken:                 platformconfig.GetString("ADMIN_BOOTSTRAP_TOKEN", ""),
		AuthJWTHMACSecret:                   platformconfig.GetString("AUTH_JWT_HMAC_SECRET", ""),
		AuthJWTIssuer:                       platformconfig.GetString("AUTH_JWT_ISSUER", defaultJWTIssuer),
		AuthJWTAudience:                     platformconfig.GetString("AUTH_JWT_AUDIENCE", defaultJWTAudience),
		AuthAccessTokenTTLSeconds:           positiveInt("AUTH_ACCESS_TOKEN_TTL_SECONDS", defaultAccessTokenTTLSeconds),
		AuthRefreshTokenTTLSeconds:          positiveInt("AUTH_REFRESH_TOKEN_TTL_SECONDS", defaultRefreshTokenTTLSeconds),
		AuthTokenEndpointRateLimitPerMinute: positiveInt("AUTH_TOKEN_ENDPOINT_RATE_LIMIT_PER_MINUTE", defaultTokenEndpointRateLimitPerMinute),
		AuthTokenEndpointRateLimitBurst:     positiveInt("AUTH_TOKEN_ENDPOINT_RATE_LIMIT_BURST", defaultTokenEndpointRateLimitBurst),
		AuthTrustedProxyCIDRs:               platformconfig.GetString("AUTH_TRUSTED_PROXY_CIDRS", ""),
	}
}

func positiveInt(key string, fallback int) int {
	value := platformconfig.GetInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}
