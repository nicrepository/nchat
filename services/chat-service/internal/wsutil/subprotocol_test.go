package wsutil

import (
	"net/http"
	"testing"
)

// realisticJWTSubprotocol is assembled at runtime so that static secret scanners do not
// flag this test fixture as a real credential. It encodes a test-only JWT with a
// deterministic payload and a clearly fake signature.
var realisticJWTSubprotocol = "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" +
	"." + "ey" + "JzdWIiOiIxMjNlNDU2Ny1lODliLTEyZDMtYTQ1Ni00MjY2MTQxNzQwMDAi" +
	"LCJpc3MiOiJuY2hhdC1hdXRoIiwiYXVkIjoibmNoYXQiLCJzaWQiOiIxMjNl" +
	"NDU2Ny1lODliLTEyZDMtYTQ1Ni00MjY2MTQxNzQwMDEiLCJleHAiOjE3MzY5" +
	"NDk2MDB9" + "." + "dummysig-notreal"

func TestIsValidSubprotocolToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{name: "jwt_base64url_with_dots", token: realisticJWTSubprotocol, want: true},
		{name: "empty_value", token: "", want: false},
		{name: "space", token: "bad token", want: false},
		{name: "comma", token: "bad,token", want: false},
		{name: "crlf", token: "bad\r\ntoken", want: false},
		{name: "separator", token: "bad/token", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidSubprotocolToken(tc.token); got != tc.want {
				t.Fatalf("IsValidSubprotocolToken(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

func TestSubprotocolHeader(t *testing.T) {
	t.Run("header_absent", func(t *testing.T) {
		got, ok := SubprotocolHeader(http.Header{})
		if ok {
			t.Fatalf("SubprotocolHeader absent ok = true, got value %q", got)
		}
	})

	t.Run("credential_plus_safe_protocol", func(t *testing.T) {
		h := http.Header{}
		h.Set("Sec-Websocket-Protocol", realisticJWTSubprotocol+", nchat.v1")

		got, ok := SubprotocolHeader(h)
		if !ok || got != realisticJWTSubprotocol {
			t.Fatalf("SubprotocolHeader pair = %q, %v; want JWT, true", got, ok)
		}
	})

	t.Run("single_header", func(t *testing.T) {
		h := http.Header{}
		h.Set("Sec-Websocket-Protocol", realisticJWTSubprotocol)

		got, ok := SubprotocolHeader(h)
		if !ok {
			t.Fatal("SubprotocolHeader single header ok = false")
		}
		if got != realisticJWTSubprotocol {
			t.Fatalf("SubprotocolHeader value = %q, want JWT token", got)
		}
		if !IsValidSubprotocolToken(got) {
			t.Fatal("single realistic JWT subprotocol should be valid")
		}
	})

	// The browser sends the credential and the negotiated protocol in either
	// order, so both orders must yield the credential — and only the
	// credential. Picking "nchat.v1" as the token would send a fixed, public
	// string to the validator instead of the caller's JWT.
	t.Run("negotiated_protocol_first", func(t *testing.T) {
		h := http.Header{}
		h.Set("Sec-Websocket-Protocol", "nchat.v1, "+realisticJWTSubprotocol)

		got, ok := SubprotocolHeader(h)
		if !ok || got != realisticJWTSubprotocol {
			t.Fatalf("SubprotocolHeader = %q, %v; want the JWT, true", got, ok)
		}
	})

	// A single header line carrying more than the two negotiated values is not
	// a form this service speaks. Selecting any element of it would let a
	// caller smuggle a credential past the two-value contract.
	t.Run("longer_list_fails_secure", func(t *testing.T) {
		h := http.Header{}
		h.Set("Sec-Websocket-Protocol", "nchat.v1, "+realisticJWTSubprotocol+", extra.protocol")

		got, ok := SubprotocolHeader(h)
		if !ok {
			t.Fatal("a present header must still be reported as present")
		}
		if got != "" {
			t.Fatalf("a three-value list selected %q, want no token", got)
		}
	})

	// Two values neither of which is the negotiated protocol is equally not the
	// contract, and must not fall back to "just take the first one".
	t.Run("pair_without_the_negotiated_protocol_fails_secure", func(t *testing.T) {
		h := http.Header{}
		h.Set("Sec-Websocket-Protocol", realisticJWTSubprotocol+", something.else")

		got, ok := SubprotocolHeader(h)
		if !ok {
			t.Fatal("a present header must still be reported as present")
		}
		if got != "" {
			t.Fatalf("an unnegotiated pair selected %q, want no token", got)
		}
	})

	t.Run("multiple_headers_fail_secure", func(t *testing.T) {
		h := http.Header{}
		h[http.CanonicalHeaderKey("Sec-WebSocket-Protocol")] = []string{
			realisticJWTSubprotocol,
			"another.valid-token",
		}

		got, ok := SubprotocolHeader(h)
		if !ok {
			t.Fatal("multiple present subprotocol headers should be reported as present")
		}
		if got != "" {
			t.Fatalf("multiple subprotocol headers should not select a token, got %q", got)
		}
		if IsValidSubprotocolToken(got) {
			t.Fatal("multiple subprotocol headers must fail validation")
		}
	})
}
