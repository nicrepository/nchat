package wsutil

import (
	"net/http"
	"strings"
)

const NegotiatedSubprotocol = "nchat.v1"

// SubprotocolHeader returns the credential protocol from either the legacy
// single-token form or the credential + nchat.v1 browser negotiation form.
// Multiple header lines and all other lists fail secure.
func SubprotocolHeader(h http.Header) (string, bool) {
	values, ok := h[http.CanonicalHeaderKey("Sec-WebSocket-Protocol")]
	if !ok {
		return "", false
	}
	if len(values) != 1 {
		return "", true
	}
	protocols := strings.Split(values[0], ",")
	if len(protocols) == 1 {
		return strings.TrimSpace(protocols[0]), true
	}
	if len(protocols) != 2 {
		return "", true
	}
	first := strings.TrimSpace(protocols[0])
	second := strings.TrimSpace(protocols[1])
	if first == NegotiatedSubprotocol {
		return second, true
	}
	if second == NegotiatedSubprotocol {
		return first, true
	}
	return "", true
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
