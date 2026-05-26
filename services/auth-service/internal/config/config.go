package config

import platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"

const (
	serviceName = "auth-service"
	defaultPort = 8081
)

type Config struct {
	ServiceName              string
	Env                      string
	Port                     int
	ReadHeaderTimeoutSeconds int
	DatabaseURL              string
	DBConnectTimeoutSeconds  int
	AdminBootstrapToken      string
}

func Load() Config {
	return Config{
		ServiceName:              serviceName,
		Env:                      platformconfig.GetString("APP_ENV", "development"),
		Port:                     platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds: platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		DatabaseURL:              platformconfig.GetString("DATABASE_URL", ""),
		DBConnectTimeoutSeconds:  platformconfig.GetInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		AdminBootstrapToken:      platformconfig.GetString("ADMIN_BOOTSTRAP_TOKEN", ""),
	}
}
