package config

import platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"

const (
	serviceName = "chat-service"
	defaultPort = 8082
)

type Config struct {
	ServiceName              string
	Env                      string
	Port                     int
	ReadHeaderTimeoutSeconds int
	DatabaseURL              string
	DBConnectTimeoutSeconds  int

	// ValkeyURL is the connection string for the Valkey instance.
	// Example: "valkey://localhost:6379". Empty disables Valkey features.
	ValkeyURL string
	// ValkeyWSBroadcastEnabled enables distributed WebSocket broadcast via
	// Valkey Pub/Sub. When false, broadcast is in-process only (NopBus).
	// Defaults to false; safe for local development and test environments.
	ValkeyWSBroadcastEnabled bool
	// WSInstanceID is a stable identifier for this chat-service instance.
	// Used for echo-suppression in distributed broadcast.
	// Generated automatically at startup if not set via WS_INSTANCE_ID.
	WSInstanceID string
}

func Load() Config {
	return Config{
		ServiceName:              serviceName,
		Env:                      platformconfig.GetString("APP_ENV", "development"),
		Port:                     platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds: platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		DatabaseURL:              platformconfig.GetString("DATABASE_URL", ""),
		DBConnectTimeoutSeconds:  platformconfig.GetInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		ValkeyURL:                platformconfig.GetString("VALKEY_URL", ""),
		ValkeyWSBroadcastEnabled: platformconfig.GetBool("VALKEY_WS_BROADCAST_ENABLED", false),
		WSInstanceID:             platformconfig.GetString("WS_INSTANCE_ID", ""),
	}
}
