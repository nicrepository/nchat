package config

import (
	"regexp"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

const (
	serviceName = "chat-service"
	defaultPort = 8082

	defaultJWTIssuer   = "nchat-auth"
	defaultJWTAudience = "nchat-api"

	// defaultMentionLabelCacheTTLSeconds matches service.defaultMentionLabelCacheTTL.
	defaultMentionLabelCacheTTLSeconds  = 45
	defaultReactionRateLimitMaxActions  = 60
	defaultReactionRateLimitWindowSecs  = 60
	defaultCallRingTimeoutSeconds       = 30
	defaultCallStartRateLimitMaxActions = 10
	defaultCallStartRateLimitWindowSecs = 60

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

	// AuthJWTHMACSecret is the shared HMAC secret used to validate access tokens
	// issued by auth-service. Must match AUTH_JWT_HMAC_SECRET in auth-service.
	// When empty or too short, the sidebar and all authenticated endpoints
	// return 503.
	AuthJWTHMACSecret string
	// AuthJWTIssuer is the expected JWT issuer claim. Must match auth-service.
	AuthJWTIssuer string
	// AuthJWTAudience is the expected JWT audience claim. Must match auth-service.
	AuthJWTAudience string

	// ValkeyURL is the connection string for the Valkey instance.
	// Example: "valkey://localhost:6379". Empty disables Valkey features and
	// makes a database-backed instance unready because reactions require the
	// shared Valkey anti-abuse limiter.
	// Must be provided via Sealed Secrets/vault in staging and production —
	// see docs/security/secrets-owners.md for the owner of this secret.
	ValkeyURL string
	// MentionLabelCacheTTLSeconds controls how long resolved mention labels
	// (display names) stay cached in Valkey. Defaults to 45s. Lower values
	// propagate display-name changes and account deactivation/deletion
	// ("right to be forgotten") faster, at the cost of more Valkey load.
	MentionLabelCacheTTLSeconds int
	// ReactionRateLimitMaxActions and ReactionRateLimitWindowSeconds control
	// the shared per-user reaction limiter in Valkey.
	ReactionRateLimitMaxActions     int
	ReactionRateLimitWindowSeconds  int
	CallRingTimeoutSeconds          int
	CallStartRateLimitMaxActions    int
	CallStartRateLimitWindowSeconds int
	// ValkeyWSBroadcastEnabled enables distributed WebSocket broadcast via
	// Valkey Pub/Sub. When false, broadcast is in-process only (NopBus).
	// Defaults to false; safe for local development and test environments.
	ValkeyWSBroadcastEnabled bool
	// WSInstanceID is a stable identifier for this chat-service instance.
	// Used for echo-suppression in distributed broadcast.
	// Generated automatically at startup if not set via WS_INSTANCE_ID or if
	// the configured value fails validation (non-empty, ≤64 chars, [A-Za-z0-9._-]).
	WSInstanceID string

	// WebSocket resource controls. Values are loaded from:
	// WS_MAX_CONNECTIONS_PER_USER, WS_INBOUND_MESSAGES_PER_MINUTE,
	// WS_INBOUND_BURST, and WS_MAX_INVALID_MESSAGES.
	// Missing, non-integer, zero, or negative values fall back to the safe
	// defaults from ws.DefaultHandlerConfig().
	WSMaxConnectionsPerUser    int
	WSInboundMessagesPerMinute int
	WSInboundBurst             int
	WSMaxInvalidMessages       int
}

func Load() Config {
	wsDefaults := ws.DefaultHandlerConfig()
	return Config{
		ServiceName:                 serviceName,
		Env:                         platformconfig.GetString("APP_ENV", "development"),
		Port:                        platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds:    platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		DatabaseURL:                 platformconfig.GetString("DATABASE_URL", ""),
		DBConnectTimeoutSeconds:     platformconfig.GetInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		AuthJWTHMACSecret:           platformconfig.GetString("AUTH_JWT_HMAC_SECRET", ""),
		AuthJWTIssuer:               platformconfig.GetString("AUTH_JWT_ISSUER", defaultJWTIssuer),
		AuthJWTAudience:             platformconfig.GetString("AUTH_JWT_AUDIENCE", defaultJWTAudience),
		ValkeyURL:                   platformconfig.GetString("VALKEY_URL", ""),
		MentionLabelCacheTTLSeconds: getPositiveInt("MENTION_LABEL_CACHE_TTL_SECONDS", defaultMentionLabelCacheTTLSeconds),
		ReactionRateLimitMaxActions: getPositiveInt("REACTION_RATE_LIMIT_MAX_ACTIONS", defaultReactionRateLimitMaxActions),
		ReactionRateLimitWindowSeconds: getPositiveInt(
			"REACTION_RATE_LIMIT_WINDOW_SECONDS", defaultReactionRateLimitWindowSecs,
		),
		CallRingTimeoutSeconds: getPositiveInt("CALL_RING_TIMEOUT_SECONDS", defaultCallRingTimeoutSeconds),
		CallStartRateLimitMaxActions: getPositiveInt(
			"CALL_START_RATE_LIMIT_MAX_ACTIONS", defaultCallStartRateLimitMaxActions,
		),
		CallStartRateLimitWindowSeconds: getPositiveInt("CALL_START_RATE_LIMIT_WINDOW_SECONDS", defaultCallStartRateLimitWindowSecs),
		ValkeyWSBroadcastEnabled:        platformconfig.GetBool("VALKEY_WS_BROADCAST_ENABLED", false),
		WSInstanceID:                    sanitizeWSInstanceID(platformconfig.GetString("WS_INSTANCE_ID", "")),
		WSMaxConnectionsPerUser:         getPositiveInt("WS_MAX_CONNECTIONS_PER_USER", wsDefaults.MaxConnectionsPerUser),
		WSInboundMessagesPerMinute:      getPositiveInt("WS_INBOUND_MESSAGES_PER_MINUTE", wsDefaults.InboundMessagesPerMinute),
		WSInboundBurst:                  getPositiveInt("WS_INBOUND_BURST", wsDefaults.InboundBurst),
		WSMaxInvalidMessages:            getPositiveInt("WS_MAX_INVALID_MESSAGES", wsDefaults.MaxInvalidMessages),
	}
}

func getPositiveInt(key string, fallback int) int {
	value := platformconfig.GetInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
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
