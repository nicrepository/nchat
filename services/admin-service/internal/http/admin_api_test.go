package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// ── Anonymous access ────────────────────────────────────────────────────────

// Nothing privileged is reachable without a session. This is the baseline the
// rest of the console's guarantees rest on.
func TestAdminAPI_AnonymousAccessIsRefusedEverywhere(t *testing.T) {
	harness := newHarness(t, adminStore())

	for _, path := range []string{RouteAdminBootstrap, RouteAdminAudit} {
		response := harness.do(httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d", path, response.Code)
		}
	}
	response := harness.do(httptest.NewRequest(http.MethodPost, RouteAdminSession, nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("session handshake: expected 401, got %d", response.Code)
	}
}

// A deep link is just a URL. The guard runs on the request, not on how the user
// got there, so typing the path directly changes nothing.
func TestAdminAPI_DeepLinkDoesNotBypassTheGuard(t *testing.T) {
	harness := newHarness(t, adminStore())

	request := httptest.NewRequest(http.MethodGet, RouteAdminAudit+"?limit=5", nil)
	request.Header.Set("Referer", testOrigin+"/audit")
	if response := harness.do(request); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

// ── Handshake ───────────────────────────────────────────────────────────────

// A normal NChat user holds a perfectly valid access token. It must buy them
// nothing here: being signed in to the chat is not administrative authority.
func TestCreateSession_OrdinaryUserIsForbidden(t *testing.T) {
	store := adminStore()
	store.principalErr = domain.ErrForbidden
	harness := newHarness(t, store)

	request := httptest.NewRequest(http.MethodPost, RouteAdminSession, nil)
	request.Header.Set("Authorization", "Bearer "+signAccessToken(t, testUserID, testAuthSessionID))
	response := harness.do(request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("a refused handshake must not set a session cookie")
	}
	assertRecorded(t, store, "admin.session.create:denied")
}

func TestCreateSession_RevokedChatSessionIsUnauthorized(t *testing.T) {
	store := adminStore()
	store.principalErr = domain.ErrUnauthorized
	harness := newHarness(t, store)

	request := httptest.NewRequest(http.MethodPost, RouteAdminSession, nil)
	request.Header.Set("Authorization", "Bearer "+signAccessToken(t, testUserID, testAuthSessionID))
	if response := harness.do(request); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestCreateSession_RejectsForgedAndMalformedTokens(t *testing.T) {
	harness := newHarness(t, adminStore())

	tests := map[string]string{
		"no header":     "",
		"not bearer":    "Basic abc",
		"empty bearer":  "Bearer ",
		"garbage":       "Bearer not-a-jwt",
		"wrong secret":  "Bearer " + signWithSecret(t, "another-secret-that-is-32-bytes!!"),
		"wrong issuer":  "Bearer " + signWithIssuer(t, "someone-else"),
		"wrong subject": "Bearer " + signAccessToken(t, "not-a-uuid", testAuthSessionID),
		"wrong sid":     "Bearer " + signAccessToken(t, testUserID, "not-a-uuid"),
	}
	for name, header := range tests {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, RouteAdminSession, nil)
			if header != "" {
				request.Header.Set("Authorization", header)
			}
			if response := harness.do(request); response.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", response.Code)
			}
		})
	}
}

// The cookie carries the whole administrative credential, so every attribute on
// it is a security control: HttpOnly stops an XSS foothold from reading it,
// SameSite=Strict stops a cross-site request from sending it, Secure stops it
// crossing a plaintext hop, and the __Host- prefix stops a sibling subdomain
// from planting one.
func TestCreateSession_CookieCarriesTheFullPolicy(t *testing.T) {
	harness := newHarness(t, adminStore())
	cookie, _ := harness.establish(t)

	if cookie.Name != "__Host-nchat_admin_session" {
		t.Fatalf("expected the __Host- prefix, got %q", cookie.Name)
	}
	if !cookie.HttpOnly {
		t.Fatal("the administrative credential must not be readable from JavaScript")
	}
	if !cookie.Secure {
		t.Fatal("the administrative credential must not cross a plaintext hop")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("expected SameSite=Strict, got %v", cookie.SameSite)
	}
	if cookie.Domain != "" {
		t.Fatalf("a __Host- cookie must carry no Domain, got %q", cookie.Domain)
	}
	if cookie.Path != "/" {
		t.Fatalf("a __Host- cookie must be Path=/, got %q", cookie.Path)
	}
	if cookie.MaxAge != int((15 * time.Minute).Seconds()) {
		t.Fatalf("expected the cookie lifetime to track the idle window, got %d", cookie.MaxAge)
	}
}

// Two handshakes must never yield the same credential: that is what makes a
// pre-planted cookie value worthless and rotation-on-elevation real.
func TestCreateSession_RotatesTheCredentialOnEveryHandshake(t *testing.T) {
	harness := newHarness(t, adminStore())
	first, _ := harness.establish(t)
	second, _ := harness.establish(t)

	if first.Value == second.Value {
		t.Fatal("session fixation: two handshakes shared a credential")
	}
}

// The credential exists in exactly one place in the response: the Set-Cookie
// header, where a script cannot read it. A copy in the body would put it back
// within reach of the XSS the HttpOnly flag exists to survive.
func TestCreateSession_ResponseBodyCarriesNoCredential(t *testing.T) {
	harness := newHarness(t, adminStore())

	request := httptest.NewRequest(http.MethodPost, RouteAdminSession, nil)
	request.Header.Set("Authorization", "Bearer "+signAccessToken(t, testUserID, testAuthSessionID))
	response := harness.do(request)

	cookie := response.Result().Cookies()[0]
	body := response.Body.String()
	if strings.Contains(body, cookie.Value) {
		t.Fatalf("the session credential must not appear in the response body: %s", body)
	}
	if strings.Contains(body, "Bearer") || strings.Contains(body, "access_token") {
		t.Fatalf("the access token must not be echoed: %s", body)
	}
}

func TestCreateSession_RecordsASuccessfulLogin(t *testing.T) {
	store := adminStore()
	harness := newHarness(t, store)
	harness.establish(t)

	assertRecorded(t, store, "admin.session.create:success")
	if store.audit[0].ActorUserID != testUserID {
		t.Fatalf("expected the actor to be recorded, got %+v", store.audit[0])
	}
	if store.audit[0].CorrelationID == "" {
		t.Fatal("expected a correlation id on the audit row")
	}
}

// ── Bootstrap payload ───────────────────────────────────────────────────────

func TestBootstrap_ReturnsIdentityCapabilitiesAndEnvironment(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilityAuditRead, domain.CapabilityUsersRead))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminBootstrap, cookie, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	payload := decodeEnvelope(t, response).Data
	if payload.Identity.UserID != testUserID || payload.Identity.Email != "admin@example.test" {
		t.Fatalf("unexpected identity: %+v", payload.Identity)
	}
	if len(payload.Capabilities) != 2 {
		t.Fatalf("expected the granted capabilities, got %v", payload.Capabilities)
	}
	// The environment is the deployment's, not the request's.
	if payload.Environment != string(config.EnvironmentStaging) {
		t.Fatalf("expected STAGING, got %q", payload.Environment)
	}
	if payload.Build.Service != "admin-service" {
		t.Fatalf("unexpected build payload: %+v", payload.Build)
	}
	if payload.Session.IdleExpiresAt.IsZero() || payload.Session.AbsoluteExpiresAt.IsZero() {
		t.Fatal("expected the session windows to be reported")
	}
	if payload.CSRFToken == "" {
		t.Fatal("expected a CSRF token so a reloaded page can still act")
	}
}

// The bootstrap is the one endpoint every session hits. Anything that leaks
// here leaks to every administrator on every page load, so the payload is
// asserted against the raw JSON rather than against the struct.
func TestBootstrap_LeaksNoCredentialOrInfrastructureDetail(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminBootstrap, cookie, ""))
	body := response.Body.String()

	forbidden := []string{
		"access_token", "refresh_token", "X-NChat-Admin-Token", "ADMIN_BOOTSTRAP_TOKEN",
		"postgres://", "DATABASE_URL", "client_secret", "password", "hmac", testJWTSecret,
		"kubernetes", "session_hash", cookie.Value,
	}
	for _, needle := range forbidden {
		if strings.Contains(strings.ToLower(body), strings.ToLower(needle)) {
			t.Fatalf("bootstrap payload leaked %q: %s", needle, body)
		}
	}

	// And the payload's shape is exactly the allowlist, no more.
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]bool{
		"identity": true, "capabilities": true, "environment": true,
		"build": true, "session": true, "csrf_token": true,
	}
	for key := range envelope.Data {
		if !want[key] {
			t.Fatalf("unexpected field %q in the bootstrap payload", key)
		}
	}
	if len(envelope.Data) != len(want) {
		t.Fatalf("expected %d fields, got %v", len(want), envelope.Data)
	}
}

// A capability list the client edits changes its own sidebar and nothing else:
// there is no request field the server reads it back from.
func TestBootstrap_ClientSuppliedCapabilitiesAreIgnored(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilityUsersRead))
	cookie, _ := harness.establish(t)

	request := harness.authenticated(t, http.MethodGet, RouteAdminBootstrap+"?capabilities=admin.superuser", cookie, "")
	request.Header.Set("X-Admin-Capabilities", "admin.superuser")
	response := harness.do(request)

	payload := decodeEnvelope(t, response).Data
	if len(payload.Capabilities) != 1 || payload.Capabilities[0] != string(domain.CapabilityUsersRead) {
		t.Fatalf("client-supplied capabilities must be ignored, got %v", payload.Capabilities)
	}
}

// ── Revocation ──────────────────────────────────────────────────────────────

func TestBootstrap_RevokedSessionIsUnauthorized(t *testing.T) {
	store := adminStore()
	harness := newHarness(t, store)
	cookie, _ := harness.establish(t)

	store.revoked = true

	if response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminBootstrap, cookie, "")); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after revocation, got %d", response.Code)
	}
}

// The role is removed while the session is still perfectly valid. Access must
// end on the very next request, not at the next login.
func TestBootstrap_CapabilityRemovedMidSessionIsForbidden(t *testing.T) {
	store := adminStore()
	harness := newHarness(t, store)
	cookie, _ := harness.establish(t)

	store.principal.Capabilities = domain.NewCapabilitySet(nil)

	if response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminBootstrap, cookie, "")); response.Code != http.StatusForbidden {
		t.Fatalf("expected 403 after the role was removed, got %d", response.Code)
	}
}

func TestBootstrap_UnderlyingLoginRevokedIsUnauthorized(t *testing.T) {
	store := adminStore()
	harness := newHarness(t, store)
	cookie, _ := harness.establish(t)

	store.principalErr = domain.ErrUnauthorized

	if response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminBootstrap, cookie, "")); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

// An old cookie from a session that was already logged out must not work again.
func TestSessionReplayAfterLogoutIsRefused(t *testing.T) {
	store := adminStore()
	harness := newHarness(t, store)
	cookie, csrf := harness.establish(t)

	if response := harness.do(harness.authenticated(t, http.MethodDelete, RouteAdminSession, cookie, csrf)); response.Code != http.StatusNoContent {
		t.Fatalf("logout: expected 204, got %d", response.Code)
	}
	if response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminBootstrap, cookie, "")); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected the replayed cookie to be refused, got %d", response.Code)
	}
}

func TestDestroySession_ClearsTheCookieAndRecordsTheEvent(t *testing.T) {
	store := adminStore()
	harness := newHarness(t, store)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodDelete, RouteAdminSession, cookie, csrf))
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%s)", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 || cookies[0].Value != "" {
		t.Fatalf("expected the cookie to be expired, got %+v", cookies)
	}
	assertRecorded(t, store, "admin.session.create:success", "admin.session.destroy:success")
}

// ── CSRF ────────────────────────────────────────────────────────────────────

func TestDestroySession_RequiresTheCSRFToken(t *testing.T) {
	harness := newHarness(t, adminStore())
	cookie, csrf := harness.establish(t)

	tests := map[string]string{
		"absent":     "",
		"forged":     "deadbeef",
		"transplant": harness.auth.CSRFToken("another-session"),
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			response := harness.do(harness.authenticated(t, http.MethodDelete, RouteAdminSession, cookie, token))
			if response.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d", response.Code)
			}
		})
	}
	if response := harness.do(harness.authenticated(t, http.MethodDelete, RouteAdminSession, cookie, csrf)); response.Code != http.StatusNoContent {
		t.Fatalf("the real token must still work, got %d", response.Code)
	}
}

// A cross-site request that somehow carried the cookie is still refused: the
// Origin does not belong to this deployment.
func TestDestroySession_RejectsForeignOrigins(t *testing.T) {
	harness := newHarness(t, adminStore())
	cookie, csrf := harness.establish(t)

	for _, origin := range []string{"https://evil.test", "null", "http://admin.nchat.test"} {
		request := httptest.NewRequest(http.MethodDelete, RouteAdminSession, nil)
		request.AddCookie(cookie)
		request.Header.Set(csrfHeaderName, csrf)
		request.Header.Set("Origin", origin)
		if response := harness.do(request); response.Code != http.StatusForbidden {
			t.Fatalf("origin %q: expected 403, got %d", origin, response.Code)
		}
	}
}

// A request with no Origin and no Referer is refused rather than trusted:
// browsers do send Origin on exactly the requests this guard exists for.
func TestDestroySession_RejectsRequestWithoutAnOrigin(t *testing.T) {
	harness := newHarness(t, adminStore())
	cookie, csrf := harness.establish(t)

	request := httptest.NewRequest(http.MethodDelete, RouteAdminSession, nil)
	request.AddCookie(cookie)
	request.Header.Set(csrfHeaderName, csrf)
	if response := harness.do(request); response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.Code)
	}
}

// A same-origin request is accepted without the operator having to restate the
// deployment's own hostname in configuration.
func TestDestroySession_AcceptsSameOriginByReferer(t *testing.T) {
	harness := newHarness(t, adminStore())
	cookie, csrf := harness.establish(t)

	request := httptest.NewRequest(http.MethodDelete, RouteAdminSession, nil)
	request.Host = "admin.example.test"
	request.AddCookie(cookie)
	request.Header.Set(csrfHeaderName, csrf)
	request.Header.Set("Referer", "https://admin.example.test/audit")
	if response := harness.do(request); response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%s)", response.Code, response.Body.String())
	}
}

// ── Capability enforcement ──────────────────────────────────────────────────

// A partial administrator — real, active, holding a real capability — must not
// reach an endpoint guarded by a capability they were not granted.
func TestAuditEndpoint_PartialAdminIsForbidden(t *testing.T) {
	store := adminStore(domain.CapabilityUsersRead)
	harness := newHarness(t, store)
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminAudit, cookie, ""))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	assertRecorded(t, store, "admin.session.create:success", "admin.authorization.deny:denied")
	denial := store.audit[len(store.audit)-1]
	if denial.Metadata["capability"] != string(domain.CapabilityAuditRead) {
		t.Fatalf("expected the required capability on the denial row, got %+v", denial.Metadata)
	}
}

func TestAuditEndpoint_GrantedCapabilityIsAllowed(t *testing.T) {
	store := adminStore(domain.CapabilityAuditRead)
	store.auditEntries = []domain.AuditEntry{{
		ID: 7, OccurredAt: time.Now().UTC(), ActorUserID: testUserID,
		ActorEmail: "admin@example.test", Action: domain.AuditActionSessionCreate,
		Result: domain.AuditResultSuccess, CorrelationID: "req-7",
	}}
	harness := newHarness(t, store)
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminAudit, cookie, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Events []struct {
				ID     string `json:"id"`
				Action string `json:"action"`
			} `json:"events"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data.Events) != 1 || envelope.Data.Events[0].ID != "7" {
		t.Fatalf("unexpected events: %+v", envelope.Data.Events)
	}
}

// The superuser grant is what lets an operator hold full administration without
// enumerating every capability.
func TestAuditEndpoint_SuperuserIsAllowed(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser))
	cookie, _ := harness.establish(t)

	if response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminAudit, cookie, "")); response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestAuditEndpoint_RejectsAMalformedLimit(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilityAuditRead))
	cookie, _ := harness.establish(t)

	for _, raw := range []string{"abc", "0", "-1", "1e5"} {
		response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminAudit+"?limit="+raw, cookie, ""))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("limit %q: expected 400, got %d", raw, response.Code)
		}
	}
}

func TestAuditEndpoint_StoreFailureIsAnInternalError(t *testing.T) {
	store := adminStore(domain.CapabilityAuditRead)
	store.auditListErr = errors.New("audit store unavailable")
	harness := newHarness(t, store)
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminAudit, cookie, ""))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "admin_audit_events") {
		t.Fatalf("an internal error must not leak the query: %s", response.Body.String())
	}
}

// ── CORS ────────────────────────────────────────────────────────────────────

func TestCORS_EchoesOnlyAllowlistedOrigins(t *testing.T) {
	harness := newHarness(t, adminStore())

	allowed := httptest.NewRequest(http.MethodOptions, RouteAdminBootstrap, nil)
	allowed.Header.Set("Origin", testOrigin)
	allowed.Header.Set("Access-Control-Request-Method", "GET")
	response := harness.do(allowed)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", response.Code)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Fatalf("expected the origin to be echoed, got %q", response.Header().Get("Access-Control-Allow-Origin"))
	}
	if response.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("expected credentials to be allowed for the console origin")
	}

	denied := httptest.NewRequest(http.MethodOptions, RouteAdminBootstrap, nil)
	denied.Header.Set("Origin", "https://evil.test")
	denied.Header.Set("Access-Control-Request-Method", "GET")
	response = harness.do(denied)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.Code)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("a foreign origin must never be echoed")
	}
}

// The wildcard is the one value that must never appear alongside credentials.
func TestCORS_NeverEmitsAWildcard(t *testing.T) {
	harness := newHarness(t, adminStore(), withConfig(func(cfg *config.Config) { cfg.AllowedOrigins = nil }))

	request := httptest.NewRequest(http.MethodGet, RouteAdminBootstrap, nil)
	request.Header.Set("Origin", "https://evil.test")
	response := harness.do(request)

	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("expected no CORS header, got %q", response.Header().Get("Access-Control-Allow-Origin"))
	}
}

// ── Response hygiene ────────────────────────────────────────────────────────

func TestAdminAPI_ResponsesAreNotCacheable(t *testing.T) {
	harness := newHarness(t, adminStore())
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminBootstrap, cookie, ""))
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected no-store, got %q", response.Header().Get("Cache-Control"))
	}
	if response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("expected clickjacking protection, got %q", response.Header().Get("X-Frame-Options"))
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected nosniff")
	}
	if response.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("expected HSTS")
	}
}

func TestAdminAPI_MethodNotAllowed(t *testing.T) {
	harness := newHarness(t, adminStore())

	response := harness.do(httptest.NewRequest(http.MethodPut, RouteAdminSession, nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", response.Code)
	}
	if allow := response.Header().Get("Allow"); !strings.Contains(allow, "POST") || !strings.Contains(allow, "DELETE") {
		t.Fatalf("unexpected Allow header %q", allow)
	}
}

// ── Degraded wiring ─────────────────────────────────────────────────────────

// A pod without its dependencies must refuse the Admin API, not serve it
// unguarded. This is the failure mode that would otherwise be silent.
func TestAdminAPI_UnwiredServiceRefusesEveryPrivilegedRoute(t *testing.T) {
	router := NewRouter(adminConfig(), slog.New(slog.DiscardHandler))

	tests := []struct{ method, path string }{
		{http.MethodPost, RouteAdminSession},
		{http.MethodDelete, RouteAdminSession},
		{http.MethodGet, RouteAdminBootstrap},
		{http.MethodGet, RouteAdminAudit},
	}
	for _, tt := range tests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(tt.method, tt.path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: expected 503, got %d", tt.method, tt.path, response.Code)
		}
	}
}

func assertRecorded(t *testing.T, store *stubStore, want ...string) {
	t.Helper()
	got := store.recordedActions()
	if len(got) != len(want) {
		t.Fatalf("expected audit trail %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected audit trail %v, got %v", want, got)
		}
	}
}

// ── Origin isolation at the API layer ──────────────────────────────────────

// The routing table is what keeps the chat host from reaching admin-service at
// all. This is the second barrier, for the case where a browser on the chat
// origin addresses the administrative host directly: the handshake carries an
// Authorization header, so a browser must preflight it, and the preflight has
// to fail.
func TestCORS_ChatOriginCannotPreflightTheHandshake(t *testing.T) {
	consoleOrigin := "https://admin.nchat.test"
	chatOrigin := "https://nchat.test"
	harness := newHarness(t, adminStore(), withConfig(func(cfg *config.Config) {
		cfg.AllowedOrigins = []string{consoleOrigin}
	}))

	request := httptest.NewRequest(http.MethodOptions, RouteAdminSession, nil)
	request.Host = "admin.nchat.test"
	request.Header.Set("Origin", chatOrigin)
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "authorization")
	response := harness.do(request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected the chat origin's preflight to be refused, got %d", response.Code)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("the chat origin must never be echoed, got %q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("credentials must not be allowed for a foreign origin, got %q", got)
	}
}

// The deployed topology configures no allowlist at all — the console and the
// Admin API share a host, so nothing is cross-origin. In that shape no origin
// is granted anything.
func TestCORS_EmptyAllowlistGrantsNoOrigin(t *testing.T) {
	harness := newHarness(t, adminStore(), withConfig(func(cfg *config.Config) {
		cfg.AllowedOrigins = nil
	}))

	for _, origin := range []string{"https://nchat.test", "https://admin.nchat.test", "null"} {
		request := httptest.NewRequest(http.MethodOptions, RouteAdminSession, nil)
		request.Header.Set("Origin", origin)
		request.Header.Set("Access-Control-Request-Method", http.MethodPost)
		response := harness.do(request)

		if response.Code != http.StatusForbidden {
			t.Fatalf("origin %q: expected 403, got %d", origin, response.Code)
		}
		if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("origin %q: expected no CORS grant, got %q", origin, got)
		}
	}
}

// Even if the routing and the preflight were both bypassed, a handshake carried
// from the chat origin still fails the CSRF origin check on the mutating
// routes, and the session cookie itself is __Host- and SameSite=Strict.
func TestDestroySession_ChatOriginIsRefused(t *testing.T) {
	harness := newHarness(t, adminStore())
	cookie, csrf := harness.establish(t)

	request := httptest.NewRequest(http.MethodDelete, RouteAdminSession, nil)
	request.Host = "admin.nchat.test"
	request.AddCookie(cookie)
	request.Header.Set(csrfHeaderName, csrf)
	request.Header.Set("Origin", "https://nchat.test")

	if response := harness.do(request); response.Code != http.StatusForbidden {
		t.Fatalf("expected the chat origin to be refused, got %d", response.Code)
	}
}

// ── Audit correlation identity ─────────────────────────────────────────────

// The correlation ID an audit row is indexed by must be the server's, never the
// caller's. Otherwise the subject of an investigation chooses the identifier
// the investigation is filed under.
func TestAudit_CorrelationIDIsNotClientControlled(t *testing.T) {
	const forged = "attacker-controlled-value"
	store := adminStore()
	harness := newHarness(t, store)

	request := httptest.NewRequest(http.MethodPost, RouteAdminSession, nil)
	request.Header.Set("Authorization", "Bearer "+signAccessToken(t, testUserID, testAuthSessionID))
	request.Header.Set("X-Request-ID", forged)
	response := harness.do(request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", response.Code, response.Body.String())
	}
	if len(store.audit) != 1 {
		t.Fatalf("expected one audit event, got %d", len(store.audit))
	}
	recorded := store.audit[0].CorrelationID
	if recorded == forged {
		t.Fatal("the audit row was indexed by a caller-supplied identifier")
	}
	if recorded == "" {
		t.Fatal("the audit row must carry a correlation id")
	}
	// The response carries the same trustworthy value, so an operator holding a
	// report can find exactly the row it belongs to.
	if got := response.Header().Get("X-Request-ID"); got != recorded {
		t.Fatalf("response header %q does not match the audit correlation id %q", got, recorded)
	}
	if got := response.Header().Get("X-Request-ID"); got == forged {
		t.Fatal("the response echoed the forged identifier")
	}
}

// The same holds for a denial, which is the record most likely to be the
// subject of a later review.
func TestAudit_DenialCorrelationIDIsNotClientControlled(t *testing.T) {
	const forged = "attacker-controlled-value"
	store := adminStore(domain.CapabilityUsersRead)
	harness := newHarness(t, store)
	cookie, _ := harness.establish(t)

	request := harness.authenticated(t, http.MethodGet, RouteAdminAudit, cookie, "")
	request.Header.Set("X-Request-ID", forged)
	response := harness.do(request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.Code)
	}
	denial := store.audit[len(store.audit)-1]
	if denial.CorrelationID == forged || denial.CorrelationID == "" {
		t.Fatalf("unexpected correlation id on the denial row: %q", denial.CorrelationID)
	}
	if got := response.Header().Get("X-Request-ID"); got != denial.CorrelationID {
		t.Fatalf("response header %q does not match the denial correlation id %q", got, denial.CorrelationID)
	}
}
