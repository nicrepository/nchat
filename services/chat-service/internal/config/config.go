package config

import (
	"regexp"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
)

const (
	serviceName = "chat-service"
	defaultPort = 8082

	// wsInstanceIDMaxLen matches sourceInstanceIDMaxLen in the ws package.
	// Kept in sync manually; both must allow the same set of valid identifiers.
	wsInstanceIDMaxLen = 64
)

// wsInstanceIDRe mirrors sourceInstanceIDRe in the ws package.
// Restricts WS_INSTANCE_ID to characters that are safe for structured logging
// and Valkey echo-suppression. Raw UUIDs, hostnames, and pod names all match.
var wsInstanceIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

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
	// Generated automatically at startup if not set via WS_INSTANCE_ID or if
	// the configured value fails validation (non-empty, ≤64 chars, [A-Za-z0-9._-]).
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
		WSInstanceID:             sanitizeWSInstanceID(platformconfig.GetString("WS_INSTANCE_ID", "")),
	}
}

// sanitizeWSInstanceID returns id if it passes validation, or empty string to
// signal that the caller should generate a safe instance ID. The policy mirrors
// the remote source_instance_id checks in the ws package:
//   - non-empty
//   - at most wsInstanceIDMaxLen characters
//   - only [A-Za-z0-9._-] characters (safe for logging and echo-suppression)
func sanitizeWSInstanceID(id string) string {
	if id == "" || len(id) > wsInstanceIDMaxLen || !wsInstanceIDRe.MatchString(id) {
		return ""
	}
	return id
}
