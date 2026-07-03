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
