package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

// The `app` parameter is the only client input in the redirect decision. It
// selects a label, and the label selects a server-side URI.
func TestOIDCLogin_PassesTheRequestedApplicationThrough(t *testing.T) {
	tests := map[string]domain.OIDCAppContext{
		"":      domain.OIDCAppChat,
		"chat":  domain.OIDCAppChat,
		"admin": domain.OIDCAppAdmin,
	}
	for raw, want := range tests {
		manager := &fakeOIDCManager{loginLocation: "https://keycloak.example.com/auth?state=s"}
		target := "/auth/oidc/keycloak/login"
		if raw != "" {
			target += "?app=" + raw
		}

		response := httptest.NewRecorder()
		httpapi.OIDCLogin(manager).ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))

		if response.Code != http.StatusFound {
			t.Fatalf("app=%q: expected 302, got %d", raw, response.Code)
		}
		if manager.gotLoginApp != want {
			t.Fatalf("app=%q: service received %q, want %q", raw, manager.gotLoginApp, want)
		}
	}
}

// Anything outside the closed set is refused before the service is reached, and
// in particular nothing that looks like a destination is accepted. This is the
// open-redirect test: a caller cannot propose where to be sent, only which of
// two names to use.
func TestOIDCLogin_RefusesAnyApplicationOutsideTheClosedSet(t *testing.T) {
	for _, raw := range []string{
		"administrator",
		"Admin",
		"https%3A%2F%2Fevil.test",
		"%2F%2Fevil.test",
		"%2Foidc-callback",
		"chat%2Cadmin",
	} {
		manager := &fakeOIDCManager{loginLocation: "https://keycloak.example.com/auth"}
		response := httptest.NewRecorder()
		httpapi.OIDCLogin(manager).ServeHTTP(
			response,
			httptest.NewRequest(http.MethodGet, "/auth/oidc/keycloak/login?app="+raw, nil),
		)

		if response.Code != http.StatusBadRequest {
			t.Fatalf("app=%q: expected 400, got %d", raw, response.Code)
		}
		if manager.loginCalls != 0 {
			t.Fatalf("app=%q: a refused label must not start a login run", raw)
		}
		if location := response.Header().Get("Location"); location != "" {
			t.Fatalf("app=%q: a refused label must not produce a redirect, got %q", raw, location)
		}
	}
}

// Extra query parameters are not a way in: the handler reads exactly one name,
// and a `returnTo`-style parameter is simply not consulted.
func TestOIDCLogin_IgnoresAnyOtherQueryParameter(t *testing.T) {
	manager := &fakeOIDCManager{loginLocation: "https://keycloak.example.com/auth"}
	response := httptest.NewRecorder()
	httpapi.OIDCLogin(manager).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet,
			"/auth/oidc/keycloak/login?app=admin&returnTo=https%3A%2F%2Fevil.test&redirect_uri=https%3A%2F%2Fevil.test", nil),
	)

	if response.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", response.Code)
	}
	if manager.gotLoginApp != domain.OIDCAppAdmin {
		t.Fatalf("expected the admin context, got %q", manager.gotLoginApp)
	}
	if location := response.Header().Get("Location"); location != manager.loginLocation {
		t.Fatalf("the redirect must be the provider URL the service built, got %q", location)
	}
}
