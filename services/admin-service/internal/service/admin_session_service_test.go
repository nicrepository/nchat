package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

const testSecret = "0123456789abcdef0123456789abcdef"

type fakeStore struct {
	principal      domain.AdminPrincipal
	principalErr   error
	session        domain.AdminSession
	createErr      error
	touchErr       error
	createdInput   domain.AdminSessionInput
	touchedHash    string
	touchedTTL     time.Duration
	revokedHash    string
	revokedReason  string
	revokeErr      error
	loadCalls      int
	lastLoadUser   string
	lastLoadAuth   string
	handshakeCalls int
}

func (f *fakeStore) AuthorizeHandshake(ctx context.Context, userID string, authSessionID string) (domain.AdminPrincipal, error) {
	f.handshakeCalls++
	return f.ReauthorizeSession(ctx, userID, authSessionID)
}

func (f *fakeStore) ReauthorizeSession(_ context.Context, userID string, authSessionID string) (domain.AdminPrincipal, error) {
	f.loadCalls++
	f.lastLoadUser = userID
	f.lastLoadAuth = authSessionID
	if f.principalErr != nil {
		return domain.AdminPrincipal{}, f.principalErr
	}
	return f.principal, nil
}

func (f *fakeStore) CreateSession(_ context.Context, input domain.AdminSessionInput) (domain.AdminSession, error) {
	f.createdInput = input
	if f.createErr != nil {
		return domain.AdminSession{}, f.createErr
	}
	session := f.session
	session.IdleExpiresAt = input.IdleExpiresAt
	session.AbsoluteExpiresAt = input.AbsoluteExpiresAt
	return session, nil
}

func (f *fakeStore) TouchSession(_ context.Context, sessionHash string, idleTTL time.Duration) (domain.AdminSession, error) {
	f.touchedHash = sessionHash
	f.touchedTTL = idleTTL
	if f.touchErr != nil {
		return domain.AdminSession{}, f.touchErr
	}
	return f.session, nil
}

func (f *fakeStore) RevokeSession(_ context.Context, sessionHash string, reason string) error {
	f.revokedHash = sessionHash
	f.revokedReason = reason
	return f.revokeErr
}

func adminStore() *fakeStore {
	return &fakeStore{
		principal: domain.AdminPrincipal{
			UserID:       "11111111-1111-1111-1111-111111111111",
			Email:        "admin@example.test",
			DisplayName:  "Admin",
			Capabilities: domain.NewCapabilitySet([]domain.Capability{domain.CapabilityAuditRead}),
		},
		session: domain.AdminSession{
			ID:            "22222222-2222-2222-2222-222222222222",
			UserID:        "11111111-1111-1111-1111-111111111111",
			AuthSessionID: "33333333-3333-3333-3333-333333333333",
		},
	}
}

func newService(t *testing.T, store service.AdminSessionStore) *service.AdminSessionService {
	t.Helper()
	sessions, err := service.NewAdminSessionService(store, testSecret, 15*time.Minute, 8*time.Hour)
	if err != nil {
		t.Fatalf("NewAdminSessionService: %v", err)
	}
	return sessions
}

func TestNewAdminSessionService_RefusesUnsafeConfiguration(t *testing.T) {
	tests := map[string]struct {
		store    service.AdminSessionStore
		secret   string
		idle     time.Duration
		absolute time.Duration
	}{
		"no store":           {nil, testSecret, time.Minute, time.Hour},
		"short secret":       {adminStore(), "short", time.Minute, time.Hour},
		"zero idle":          {adminStore(), testSecret, 0, time.Hour},
		"zero absolute":      {adminStore(), testSecret, time.Minute, 0},
		"idle over absolute": {adminStore(), testSecret, 2 * time.Hour, time.Hour},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := service.NewAdminSessionService(tt.store, tt.secret, tt.idle, tt.absolute); err == nil {
				t.Fatal("expected the service to refuse this policy")
			}
		})
	}
}

func TestEstablish_MintsAServerGeneratedCredential(t *testing.T) {
	store := adminStore()
	sessions := newService(t, store)

	established, err := sessions.Establish(context.Background(), service.EstablishInput{
		UserID:        "11111111-1111-1111-1111-111111111111",
		AuthSessionID: "33333333-3333-3333-3333-333333333333",
		IPAddress:     "10.0.0.1",
		UserAgent:     "Mozilla/5.0",
	})
	if err != nil {
		t.Fatalf("Establish: %v", err)
	}
	if established.Value == "" {
		t.Fatal("expected a session credential")
	}
	// Session fixation: the stored hash is derived from a value this service
	// generated, so nothing a client sent can become a session identifier.
	if strings.Contains(store.createdInput.SessionHash, established.Value) {
		t.Fatal("the stored hash must not contain the credential")
	}
	if store.createdInput.SessionHash == established.Value {
		t.Fatal("the credential itself must never be persisted")
	}
	if store.createdInput.AbsoluteExpiresAt.Sub(store.createdInput.IdleExpiresAt) != 8*time.Hour-15*time.Minute {
		t.Fatalf("unexpected session windows: idle=%s absolute=%s", store.createdInput.IdleExpiresAt, store.createdInput.AbsoluteExpiresAt)
	}
	if established.CSRFToken == "" || established.CSRFToken == established.Value {
		t.Fatal("expected a distinct CSRF token")
	}
}

// Two handshakes must never produce the same credential, which is what makes a
// pre-planted value useless and rotation on elevation real.
func TestEstablish_CredentialIsUniquePerHandshake(t *testing.T) {
	sessions := newService(t, adminStore())
	first, err := sessions.Establish(context.Background(), service.EstablishInput{UserID: "u", AuthSessionID: "s"})
	if err != nil {
		t.Fatalf("Establish: %v", err)
	}
	second, err := sessions.Establish(context.Background(), service.EstablishInput{UserID: "u", AuthSessionID: "s"})
	if err != nil {
		t.Fatalf("Establish: %v", err)
	}
	if first.Value == second.Value {
		t.Fatal("two handshakes must not share a credential")
	}
}

func TestEstablish_RefusesNonAdministrators(t *testing.T) {
	store := adminStore()
	store.principalErr = domain.ErrForbidden
	sessions := newService(t, store)

	_, err := sessions.Establish(context.Background(), service.EstablishInput{UserID: "u", AuthSessionID: "s"})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if store.createdInput.SessionHash != "" {
		t.Fatal("no session may be created for a refused principal")
	}
}

func TestEstablish_RefusesRevokedChatSession(t *testing.T) {
	store := adminStore()
	store.principalErr = domain.ErrUnauthorized
	sessions := newService(t, store)

	if _, err := sessions.Establish(context.Background(), service.EstablishInput{UserID: "u", AuthSessionID: "s"}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

// An administrator holding no capability could authorize nothing. Refusing the
// handshake keeps a useless privileged session from existing at all.
func TestEstablish_RefusesPrincipalWithoutCapabilities(t *testing.T) {
	store := adminStore()
	store.principal.Capabilities = domain.NewCapabilitySet(nil)
	sessions := newService(t, store)

	if _, err := sessions.Establish(context.Background(), service.EstablishInput{UserID: "u", AuthSessionID: "s"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestEstablish_TruncatesUserAgent(t *testing.T) {
	store := adminStore()
	sessions := newService(t, store)

	if _, err := sessions.Establish(context.Background(), service.EstablishInput{
		UserID: "u", AuthSessionID: "s", UserAgent: strings.Repeat("a", 1000),
	}); err != nil {
		t.Fatalf("Establish: %v", err)
	}
	if len([]rune(store.createdInput.UserAgent)) != 255 {
		t.Fatalf("expected the user agent to be truncated, got %d runes", len([]rune(store.createdInput.UserAgent)))
	}
}

func TestEstablish_PropagatesStoreFailure(t *testing.T) {
	store := adminStore()
	store.createErr = errors.New("database down")
	sessions := newService(t, store)

	if _, err := sessions.Establish(context.Background(), service.EstablishInput{UserID: "u", AuthSessionID: "s"}); err == nil {
		t.Fatal("expected the store failure to surface")
	}
}

func TestAuthenticate_ReReadsCapabilitiesOnEveryCall(t *testing.T) {
	store := adminStore()
	sessions := newService(t, store)

	established, err := sessions.Establish(context.Background(), service.EstablishInput{UserID: "u", AuthSessionID: "s"})
	if err != nil {
		t.Fatalf("Establish: %v", err)
	}
	before := store.loadCalls

	admin, err := sessions.Authenticate(context.Background(), established.Value)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if store.loadCalls != before+1 {
		t.Fatal("the principal must be re-read from the database on every request")
	}
	if !admin.Principal.Capabilities.Has(domain.CapabilityAuditRead) {
		t.Fatal("expected the stored capability to be present")
	}
	if store.touchedTTL != 15*time.Minute {
		t.Fatalf("expected the idle window to be renewed with the policy TTL, got %s", store.touchedTTL)
	}
	// The session is looked up by hash, never by the raw credential.
	if store.touchedHash == established.Value {
		t.Fatal("the raw credential must never be used as a lookup key")
	}
}

// Removing the last role mid-session must end administrative access on the very
// next request, not at the next login.
func TestAuthenticate_CapabilityRemovedDuringSessionDenies(t *testing.T) {
	store := adminStore()
	sessions := newService(t, store)
	established, _ := sessions.Establish(context.Background(), service.EstablishInput{UserID: "u", AuthSessionID: "s"})

	store.principal.Capabilities = domain.NewCapabilitySet(nil)
	if _, err := sessions.Authenticate(context.Background(), established.Value); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden after the role was removed, got %v", err)
	}
}

func TestAuthenticate_RejectsRevokedOrExpiredSession(t *testing.T) {
	store := adminStore()
	store.touchErr = domain.ErrUnauthorized
	sessions := newService(t, store)

	if _, err := sessions.Authenticate(context.Background(), "anything"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if store.loadCalls != 0 {
		t.Fatal("a dead session must not reach the principal lookup")
	}
}

func TestAuthenticate_RejectsEmptyCredentialWithoutTouchingTheStore(t *testing.T) {
	store := adminStore()
	sessions := newService(t, store)

	if _, err := sessions.Authenticate(context.Background(), ""); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if store.touchedHash != "" {
		t.Fatal("an empty credential must not produce a database lookup")
	}
}

func TestAuthenticate_RejectsWhenUnderlyingLoginIsRevoked(t *testing.T) {
	store := adminStore()
	sessions := newService(t, store)
	established, _ := sessions.Establish(context.Background(), service.EstablishInput{UserID: "u", AuthSessionID: "s"})

	store.principalErr = domain.ErrUnauthorized
	if _, err := sessions.Authenticate(context.Background(), established.Value); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestRevoke(t *testing.T) {
	store := adminStore()
	sessions := newService(t, store)

	if err := sessions.Revoke(context.Background(), "value", "admin_logout"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if store.revokedHash == "value" || store.revokedHash == "" {
		t.Fatalf("expected the hash to be revoked, got %q", store.revokedHash)
	}
	if store.revokedReason != "admin_logout" {
		t.Fatalf("unexpected reason %q", store.revokedReason)
	}
}

func TestRevoke_EmptyValueIsANoOp(t *testing.T) {
	store := adminStore()
	sessions := newService(t, store)

	if err := sessions.Revoke(context.Background(), "", "admin_logout"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if store.revokedHash != "" {
		t.Fatal("an empty credential must not reach the store")
	}
}

func TestCSRFToken_IsBoundToOneSession(t *testing.T) {
	sessions := newService(t, adminStore())

	first := sessions.CSRFToken("session-a")
	second := sessions.CSRFToken("session-b")

	if first == second {
		t.Fatal("a CSRF token must not be transplantable between sessions")
	}
	if !sessions.ValidateCSRF("session-a", first) {
		t.Fatal("expected the token to validate for its own session")
	}
	if sessions.ValidateCSRF("session-b", first) {
		t.Fatal("a token from another session must be refused")
	}
	if sessions.ValidateCSRF("session-a", "") || sessions.ValidateCSRF("session-a", "forged") {
		t.Fatal("an absent or forged token must be refused")
	}
}

// The handshake and the per-request check are two different predicates: the
// strict one runs once at sign-in, the relaxed one on every later request. If
// they were ever collapsed, either a stale chat token would buy a privileged
// session or a working administrator would be evicted by the chat's idle timer.
func TestEstablishAndAuthenticateUseDifferentAuthorizationChecks(t *testing.T) {
	store := adminStore()
	sessions := newService(t, store)

	established, err := sessions.Establish(context.Background(), service.EstablishInput{UserID: "u", AuthSessionID: "s"})
	if err != nil {
		t.Fatalf("Establish: %v", err)
	}
	if store.handshakeCalls != 1 {
		t.Fatalf("expected the handshake to use the strict check, got %d calls", store.handshakeCalls)
	}

	if _, err := sessions.Authenticate(context.Background(), established.Value); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if store.handshakeCalls != 1 {
		t.Fatal("a later request must not re-run the handshake check")
	}
}

func TestIdleTTLIsExposedForTheCookieLifetime(t *testing.T) {
	if got := newService(t, adminStore()).IdleTTL(); got != 15*time.Minute {
		t.Fatalf("expected 15m, got %s", got)
	}
}
