package wsutil

import "net/http"

// SubprotocolHeader returns the single Sec-WebSocket-Protocol value, if present.
// Multiple header values intentionally return an empty token with ok=true so the
// caller can reject the request fail-secure.
func SubprotocolHeader(h http.Header) (string, bool) {
	values, ok := h[http.CanonicalHeaderKey("Sec-WebSocket-Protocol")]
	if !ok {
		return "", false
	}
	if len(values) != 1 {
		return "", true
	}
	return values[0], true
}

// IsValidSubprotocolToken validates the RFC 6455 subprotocol token grammar.
// JWT access tokens are compatible when encoded as unpadded Base64URL segments.
func IsValidSubprotocolToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return false
		}
		switch r {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=', '{', '}':
			return false
		}
	}
	return true
}
