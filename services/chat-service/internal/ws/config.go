package ws

// HandlerConfig defines WebSocket resource controls for a handler instance.
//
// Defaults from DefaultHandlerConfig:
//   - MaxConnectionsPerUser: 5 concurrent connections per authenticated user.
//   - InboundMessagesPerMinute: 60 inbound control messages per connection.
//   - InboundBurst: 10 initial token-bucket messages per connection.
//   - MaxInvalidMessages: 5 malformed or rejected inbound messages before close.
type HandlerConfig struct {
	MaxConnectionsPerUser    int
	InboundMessagesPerMinute int
	InboundBurst             int
	MaxInvalidMessages       int
}

// DefaultHandlerConfig returns conservative WebSocket resource-control defaults.
// Callers should pass explicit values when deployment-specific tuning is needed.
func DefaultHandlerConfig() HandlerConfig {
	return HandlerConfig{
		MaxConnectionsPerUser:    5,
		InboundMessagesPerMinute: 60,
		InboundBurst:             10,
		MaxInvalidMessages:       5,
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
	return cfg
}

// maxInboundMessageBytes caps inbound WebSocket frame size.
// Control messages (subscribe/unsubscribe/ping) are small; 4 KiB is generous.
const maxInboundMessageBytes = 4 * 1024
