package ws

import (
	"net/http"
	"time"
)

// HandlerConfig defines WebSocket resource controls for a handler instance.
//
// Defaults from DefaultHandlerConfig:
//   - MaxConnectionsPerUser: 5 concurrent connections per authenticated user.
//   - InboundMessagesPerMinute: 60 inbound control messages per connection.
//   - InboundBurst: 10 initial token-bucket messages per connection.
//   - MaxInvalidMessages: 5 malformed or rejected inbound messages before close.
//   - SessionRevalidateInterval: DefaultSessionRevalidateInterval.
type HandlerConfig struct {
	MaxConnectionsPerUser    int
	InboundMessagesPerMinute int
	InboundBurst             int
	MaxInvalidMessages       int

	// Sessions re-checks a live connection's session against the same authority
	// the HTTP routes use. Nil leaves connections validated at the upgrade only.
	Sessions SessionValidator
	// SessionIDFromContext reads the server-asserted session ID the auth
	// middleware put on the request (the validated token's "sid"). Supplied by
	// the caller — httpapi.GetContextSessionID — to keep this package free of a
	// dependency on the HTTP one. Never a value taken from the client.
	SessionIDFromContext func(*http.Request) string
	// SessionRevalidateInterval is the cadence of that re-check. It is a
	// lifecycle interval, not per-message authentication.
	SessionRevalidateInterval time.Duration
}

// DefaultHandlerConfig returns conservative WebSocket resource-control defaults.
// Callers should pass explicit values when deployment-specific tuning is needed.
func DefaultHandlerConfig() HandlerConfig {
	return HandlerConfig{
		MaxConnectionsPerUser:    5,
		InboundMessagesPerMinute: 60,
		InboundBurst:             10,
		MaxInvalidMessages:       5,

		SessionRevalidateInterval: DefaultSessionRevalidateInterval,
	}
}

func (cfg HandlerConfig) withDefaults() HandlerConfig {
	defaults := DefaultHandlerConfig()
	if cfg.MaxConnectionsPerUser <= 0 {
		cfg.MaxConnectionsPerUser = defaults.MaxConnectionsPerUser
	}
	if cfg.InboundMessagesPerMinute <= 0 {
		cfg.InboundMessagesPerMinute = defaults.InboundMessagesPerMinute
	}
	if cfg.InboundBurst <= 0 {
		cfg.InboundBurst = defaults.InboundBurst
	}
	if cfg.MaxInvalidMessages <= 0 {
		cfg.MaxInvalidMessages = defaults.MaxInvalidMessages
	}
	if cfg.SessionRevalidateInterval <= 0 {
		cfg.SessionRevalidateInterval = defaults.SessionRevalidateInterval
	}
	return cfg
}

// maxInboundMessageBytes caps inbound WebSocket frame size.
// Control messages (subscribe/unsubscribe/ping) are small; 4 KiB is generous.
const maxInboundMessageBytes = 4 * 1024
