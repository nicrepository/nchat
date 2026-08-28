package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/admin-service/internal/http"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

// Transactional authorization for privileged configuration writes (CWE-367).
//
// The finding these specs close: the middleware authorizes when the request
// arrives, the write commits later, and in between an administrator's authority
// can be taken away. A snapshot taken at the door cannot notice that.
//
// Only a real database can settle it. The property is a serialization between
// two transactions, so a mock — which has no locks, no commits and no
// concurrency — can prove the statements are sent and nothing about whether the
// race is closed.
//
// Gated on ADMIN_TEST_DATABASE_URL like the rest of this package's PostgreSQL
// suite, and skipped when it is unset.
//
//	ADMIN_TEST_DATABASE_URL=postgresql://nchat@localhost:5432/nchat_test \
//	  go test ./internal/storage/... -run PostgreSQL

const (
	authzJWTSecret = "test-jwt-secret-for-admin-config-tests"
	authzIssuer    = "nchat-auth"
	authzAudience  = "nchat-api"
	authzOrigin    = "https://admin.nchat.test"
)

// authorizedAdmin is an administrator with a live console session: the cookie
// and CSRF token a browser would be holding.
type authorizedAdmin struct {
	userID string
	cookie *http.Cookie
	csrf   string
}

// grantAdmin makes an account a platform administrator holding one role.
//
// Built from the same rows the platform uses — insertUser and insertSession are
// this package's existing helpers — so the authorization query under test reads
// real data rather than a shape invented here.
func grantAdmin(t *testing.T, pool *pgxpool.Pool, email, roleSlug string) string {
	t.Helper()
	ctx := context.Background()
	userID := insertUser(t, pool, email)
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth.admin_principals (user_id) VALUES ($1::uuid)`, userID); err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth.admin_principal_roles (user_id, role_slug) VALUES ($1::uuid, $2)`,
		userID, roleSlug); err != nil {
		t.Fatalf("grant role: %v", err)
	}
	return userID
}

// authzRouter builds the Admin API over the real stores, so a request runs the
// same middleware, service and transaction a deployment does.
func authzRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	cfg := config.Config{
		ServiceName:        "admin-service",
		Env:                "staging",
		AuthJWTHMACSecret:  authzJWTSecret,
		AuthJWTIssuer:      authzIssuer,
		AuthJWTAudience:    authzAudience,
		SessionIdleTTL:     15 * time.Minute,
		SessionAbsoluteTTL: 8 * time.Hour,
		AllowedOrigins:     []string{authzOrigin},
		DatabaseURL:        "postgres://configured",
	}
	adminStore := storage.NewPGXAdminStore(pool)
	sessions, err := service.NewAdminSessionService(adminStore, cfg.AuthJWTHMACSecret, cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
	if err != nil {
		t.Fatalf("NewAdminSessionService: %v", err)
	}
	validator, err := httpapi.NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		t.Fatalf("NewTokenValidator: %v", err)
	}
	audit := service.NewAuditService(adminStore, slog.New(slog.DiscardHandler))
	return httpapi.NewRouter(cfg, slog.New(slog.DiscardHandler), httpapi.RouterDependencies{
		TokenValidator:  validator,
		Sessions:        sessions,
		Authenticator:   sessions,
		CSRF:            sessions,
		Audit:           httpapi.NewAuditPorts(audit, audit),
		RateLimiter:     httpapi.NewIPRateLimiter(1000, 1000, nil),
		ReadinessPinger: adminStore,
		Configuration:   service.NewConfigService(storage.NewPGXConfigStore(pool), audit),
	})
}

// establishAdmin performs the real handshake, so the console session under test
// is one the platform issued rather than a row a spec invented.
func establishAdmin(t *testing.T, router http.Handler, pool *pgxpool.Pool, userID string) authorizedAdmin {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, httpapi.RouteAdminSession, nil)
	request.Header.Set("Authorization", "Bearer "+
		signAuthzToken(t, userID, insertSession(t, pool, userID, time.Hour, 8*time.Hour)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("handshake: expected 201, got %d (%s)", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected exactly one cookie, got %d", len(cookies))
	}
	var envelope struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	return authorizedAdmin{userID: userID, cookie: cookies[0], csrf: envelope.Data.CSRFToken}
}

func signAuthzToken(t *testing.T, userID, sessionID string) string {
	t.Helper()
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub": userID, "sid": sessionID, "iss": authzIssuer,
		"aud": jwt.ClaimStrings{authzAudience},
		"iat": jwt.NewNumericDate(now), "nbf": jwt.NewNumericDate(now),
		"exp": jwt.NewNumericDate(now.Add(15 * time.Minute)), "jti": "jwt-authz",
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(authzJWTSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// blockingBody is a request body that stops on its first read and stays there
// until released.
//
// The synchronization point the finding describes. Every guard — session, CSRF,
// origin, capability — has run by the time a handler asks for the body, so
// blocking here puts the request exactly where the attack wants it: authorized,
// and not yet committed.
type blockingBody struct {
	payload  []byte
	reading  chan struct{}
	release  chan struct{}
	once     sync.Once
	consumed bool
}

func newBlockingBody(t *testing.T, body any) *blockingBody {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	return &blockingBody{
		payload: payload,
		reading: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.reading)
		<-b.release
	})
	if b.consumed {
		return 0, io.EOF
	}
	b.consumed = true
	return copy(p, b.payload), nil
}

// awaitAuthorized blocks until the request has passed every guard and is asking
// for its body. No sleeps: the barrier is the read itself.
func (b *blockingBody) awaitAuthorized(t *testing.T) {
	t.Helper()
	select {
	case <-b.reading:
	case <-time.After(10 * time.Second):
		t.Fatal("request never reached the handler")
	}
}

func (b *blockingBody) proceed() { close(b.release) }

func applyRequest(fixture authorizedAdmin, body io.Reader) *http.Request {
	request := httptest.NewRequest(http.MethodPost, httpapi.RouteAdminConfigApply, body)
	request.AddCookie(fixture.cookie)
	request.Header.Set("Origin", authzOrigin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-NChat-Admin-CSRF", fixture.csrf)
	return request
}

func policyState(t *testing.T, pool *pgxpool.Pool) domain.ConfigDocumentState {
	t.Helper()
	state, err := storage.NewPGXConfigStore(pool).ReadAuthPolicy(context.Background())
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}
	return state
}

func countConfigVersions(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var versions int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM auth.admin_config_versions`).Scan(&versions); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	return versions
}

func countAuditResults(t *testing.T, pool *pgxpool.Pool, action, result string) int {
	t.Helper()
	var events int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM auth.admin_audit_events WHERE action = $1 AND result = $2`,
		action, result).Scan(&events)
	if err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	return events
}

// assertNothingWritten is the security assertion every refusal below shares:
// after an unauthorized attempt, the platform must look exactly as it did.
func assertNothingWritten(t *testing.T, pool *pgxpool.Pool, before domain.ConfigDocumentState, action string) {
	t.Helper()
	after := policyState(t, pool)
	if after.Revision != before.Revision {
		t.Fatalf("revision moved on an unauthorized write: %d -> %d", before.Revision, after.Revision)
	}
	for key, value := range before.Values {
		if !after.Values[key].Equal(value) {
			t.Fatalf("%s changed on an unauthorized write: %+v -> %+v", key, value, after.Values[key])
		}
	}
	if versions := countConfigVersions(t, pool); versions != 0 {
		t.Fatalf("an unauthorized write recorded %d version(s)", versions)
	}
	if successes := countAuditResults(t, pool, action, "success"); successes != 0 {
		t.Fatalf("an unauthorized write was audited as successful %d time(s)", successes)
	}
}

func applyBody(revision int, value int) map[string]any {
	return map[string]any{
		"document":          string(domain.ConfigDocumentAuthPolicy),
		"expected_revision": revision,
		"changes":           map[string]any{string(domain.ConfigKeyDeviceMaxPerUser): value},
	}
}

// The finding, end to end and deterministically: authorized at the door, role
// revoked while the body is still arriving, and the write must not land.
func TestPostgreSQLConfigAuthorization_RoleRevokedWhileTheBodyArrivesPostgreSQL(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	router := authzRouter(t, pool)

	// A second superuser, so revoking the first one's role is not refused by
	// the last-administrator invariant.
	grantAdmin(t, pool, "keeper@example.test", "platform-superuser")
	userID := grantAdmin(t, pool, "racer@example.test", "platform-superuser")
	fixture := establishAdmin(t, router, pool, userID)
	before := policyState(t, pool)

	body := newBlockingBody(t, applyBody(before.Revision, 9))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(response, applyRequest(fixture, body))
	}()

	// The request is past every guard and waiting for its body.
	body.awaitAuthorized(t)

	// The authority it was admitted with is taken away, and committed.
	if err := storage.NewPGXUserDirectoryStore(pool).
		RevokeAdminRole(context.Background(), userID, "platform-superuser"); err != nil {
		t.Fatalf("revoke role: %v", err)
	}

	body.proceed()
	<-done

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403 after the capability was revoked, got %d (%s)",
			response.Code, response.Body.String())
	}
	assertNothingWritten(t, pool, before, domain.AuditActionConfigUpdate)
	if denials := countAuditResults(t, pool, domain.AuditActionConfigUpdate, "denied"); denials == 0 {
		t.Fatal("the refusal must be recorded in the trail")
	}
}

// The same race against the console session: revoked mid-request, the write
// must be refused as unauthenticated rather than merely unauthorized.
func TestPostgreSQLConfigAuthorization_SessionRevokedWhileTheBodyArrivesPostgreSQL(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	router := authzRouter(t, pool)

	userID := grantAdmin(t, pool, "session-racer@example.test", "platform-superuser")
	fixture := establishAdmin(t, router, pool, userID)
	before := policyState(t, pool)

	body := newBlockingBody(t, applyBody(before.Revision, 11))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(response, applyRequest(fixture, body))
	}()

	body.awaitAuthorized(t)

	if _, err := pool.Exec(context.Background(),
		`UPDATE auth.admin_sessions SET revoked_at = now(), revoked_reason = 'test' WHERE user_id = $1::uuid`,
		userID); err != nil {
		t.Fatalf("revoke admin session: %v", err)
	}

	body.proceed()
	<-done

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after the session was revoked, got %d (%s)",
			response.Code, response.Body.String())
	}
	assertNothingWritten(t, pool, before, domain.AuditActionConfigUpdate)
}

// Suspending the account takes the authority away too, through a different
// table — which is why the suspension path also has to take the anchor.
func TestPostgreSQLConfigAuthorization_PrincipalSuspendedWhileTheBodyArrivesPostgreSQL(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	router := authzRouter(t, pool)

	userID := grantAdmin(t, pool, "suspended-racer@example.test", "platform-superuser")
	fixture := establishAdmin(t, router, pool, userID)
	before := policyState(t, pool)

	body := newBlockingBody(t, applyBody(before.Revision, 12))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(response, applyRequest(fixture, body))
	}()

	body.awaitAuthorized(t)

	if _, err := storage.NewPGXUserDirectoryStore(pool).
		UpdateUserStatus(context.Background(), userID, domain.UserStatusSuspended); err != nil {
		t.Fatalf("suspend user: %v", err)
	}

	body.proceed()
	<-done

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after the account was suspended, got %d (%s)",
			response.Code, response.Body.String())
	}
	assertNothingWritten(t, pool, before, domain.AuditActionConfigUpdate)
}

// Rollback is a privileged write too, and must not be a way around the check.
func TestPostgreSQLConfigAuthorization_RollbackAfterRevocationPostgreSQL(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	router := authzRouter(t, pool)

	grantAdmin(t, pool, "rollback-keeper@example.test", "platform-superuser")
	userID := grantAdmin(t, pool, "rollback-racer@example.test", "platform-superuser")
	fixture := establishAdmin(t, router, pool, userID)

	// A version to revert, written while the administrator still holds the role.
	initial := policyState(t, pool)
	applied, err := storage.NewPGXConfigStore(pool).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: initial.Revision,
		ActorUserID:      userID,
		Changes: []domain.ConfigChange{{
			Key:  domain.ConfigKeyDeviceMaxPerUser,
			From: initial.Values[domain.ConfigKeyDeviceMaxPerUser],
			To:   domain.IntValue(20),
		}},
		Authorization: domain.MutationAuthorization{
			SessionID: adminSessionID(t, pool, userID), UserID: userID,
			Capability: domain.CapabilityConfigManage,
		},
	})
	if err != nil {
		t.Fatalf("seed version: %v", err)
	}
	before := policyState(t, pool)

	body := newBlockingBody(t, map[string]any{"expected_revision": before.Revision, "reason": "revert"})
	request := httptest.NewRequest(http.MethodPost,
		"/config/versions/"+versionPath(applied.Version.ID)+"/rollback", body)
	request.AddCookie(fixture.cookie)
	request.Header.Set("Origin", authzOrigin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-NChat-Admin-CSRF", fixture.csrf)

	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(response, request)
	}()

	body.awaitAuthorized(t)
	if err := storage.NewPGXUserDirectoryStore(pool).
		RevokeAdminRole(context.Background(), userID, "platform-superuser"); err != nil {
		t.Fatalf("revoke role: %v", err)
	}
	body.proceed()
	<-done

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for the rollback, got %d (%s)", response.Code, response.Body.String())
	}
	after := policyState(t, pool)
	if after.Revision != before.Revision {
		t.Fatalf("the rollback moved the revision: %d -> %d", before.Revision, after.Revision)
	}
	if !after.Values[domain.ConfigKeyDeviceMaxPerUser].Equal(domain.IntValue(20)) {
		t.Fatalf("the rollback changed the value: %+v", after.Values[domain.ConfigKeyDeviceMaxPerUser])
	}
	if versions := countConfigVersions(t, pool); versions != 1 {
		t.Fatalf("the refused rollback recorded a version: %d total", versions)
	}
	if successes := countAuditResults(t, pool, domain.AuditActionConfigRollback, "success"); successes != 0 {
		t.Fatal("a refused rollback must not be audited as successful")
	}
}

func adminSessionID(t *testing.T, pool *pgxpool.Pool, userID string) string {
	t.Helper()
	var sessionID string
	err := pool.QueryRow(context.Background(),
		`SELECT id::text FROM auth.admin_sessions WHERE user_id = $1::uuid AND revoked_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`, userID).Scan(&sessionID)
	if err != nil {
		t.Fatalf("read admin session: %v", err)
	}
	return sessionID
}

func versionPath(id int64) string {
	return strconv.FormatInt(id, 10)
}

// revoke-first: the revocation commits before the write's transaction takes the
// anchor, so the write is refused. This is the ordering the finding is about.
func TestPostgreSQLConfigAuthorization_RevokeFirstRefusesTheWritePostgreSQL(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXConfigStore(pool)
	ctx := context.Background()

	grantAdmin(t, pool, "keeper2@example.test", "platform-superuser")
	userID := grantAdmin(t, pool, "revoked@example.test", "platform-superuser")
	sessionID := seedAdminSession(t, pool, userID)
	before := policyState(t, pool)

	if err := storage.NewPGXUserDirectoryStore(pool).
		RevokeAdminRole(ctx, userID, "platform-superuser"); err != nil {
		t.Fatalf("revoke role: %v", err)
	}

	_, err := store.ApplyAuthPolicy(ctx, domain.ConfigApplyInput{
		ExpectedRevision: before.Revision,
		ActorUserID:      userID,
		Changes: []domain.ConfigChange{{
			Key: domain.ConfigKeyDeviceMaxPerUser, From: before.Values[domain.ConfigKeyDeviceMaxPerUser], To: domain.IntValue(9),
		}},
		Authorization: domain.MutationAuthorization{
			SessionID: sessionID, UserID: userID, Capability: domain.CapabilityConfigManage,
		},
	})

	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected the write to be forbidden, got %v", err)
	}
	after := policyState(t, pool)
	if after.Revision != before.Revision {
		t.Fatalf("a forbidden write moved the revision: %d -> %d", before.Revision, after.Revision)
	}
	if versions := countConfigVersions(t, pool); versions != 0 {
		t.Fatalf("a forbidden write recorded %d version(s)", versions)
	}
}

// mutation-first: the write takes the anchor and holds it; a revocation that
// starts afterwards must wait rather than slipping between the check and the
// commit. The write wins the order, and the revocation applies after it.
//
// This is what proves the fix is a serialization and not lucky timing.
func TestPostgreSQLConfigAuthorization_MutationFirstBlocksTheRevocationPostgreSQL(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	ctx := context.Background()

	grantAdmin(t, pool, "keeper3@example.test", "platform-superuser")
	userID := grantAdmin(t, pool, "winner@example.test", "platform-superuser")
	sessionID := seedAdminSession(t, pool, userID)
	before := policyState(t, pool)

	revoked := writeWhileRevoking(t, pool, userID)

	// The write was serialized before the revocation, so it stands. Nothing can
	// retroactively unmake a commit that won the order — the guarantee is that
	// no write is serialized *after* a revocation, which the next step checks.
	after := policyState(t, pool)
	if after.Revision != before.Revision+1 {
		t.Fatalf("expected the write to have landed, revision %d -> %d", before.Revision, after.Revision)
	}
	if !after.Values[domain.ConfigKeyDeviceMaxPerUser].Equal(domain.IntValue(9)) {
		t.Fatalf("expected the written value, got %+v", after.Values[domain.ConfigKeyDeviceMaxPerUser])
	}
	if err := <-revoked; err != nil {
		t.Fatalf("revocation after the write: %v", err)
	}

	// And the revocation applied afterwards, so the next write is refused.
	_, err := storage.NewPGXConfigStore(pool).ApplyAuthPolicy(ctx, domain.ConfigApplyInput{
		ExpectedRevision: after.Revision,
		Changes: []domain.ConfigChange{{
			Key: domain.ConfigKeyDeviceMaxPerUser, From: domain.IntValue(9), To: domain.IntValue(10),
		}},
		Authorization: domain.MutationAuthorization{
			SessionID: sessionID, UserID: userID, Capability: domain.CapabilityConfigManage,
		},
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected the revocation to be in force afterwards, got %v", err)
	}
}

// writeWhileRevoking holds a write open between taking the anchor and
// committing — the window the attack needs — and starts a revocation inside it.
//
// It asserts the revocation cannot finish while the anchor is held, then
// commits and returns the channel carrying the revocation's own result.
func writeWhileRevoking(t *testing.T, pool *pgxpool.Pool, userID string) <-chan error {
	t.Helper()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT 1 FROM auth.admin_principals WHERE user_id = $1::uuid FOR UPDATE`, userID); err != nil {
		t.Fatalf("take anchor: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE auth.auth_policy_settings SET max_devices_per_user = 9, revision = revision + 1 WHERE id = 1`); err != nil {
		t.Fatalf("write config: %v", err)
	}

	revoked := make(chan error, 1)
	go func() {
		revoked <- storage.NewPGXUserDirectoryStore(pool).
			RevokeAdminRole(context.Background(), userID, "platform-superuser")
	}()

	// The revocation must not be able to finish while the anchor is held. The
	// wait proves it is blocked rather than merely slow: without the anchor it
	// would complete in microseconds.
	select {
	case err := <-revoked:
		t.Fatalf("the revocation passed through a held anchor: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return revoked
}

// seedAdminSession inserts a live console session directly, for the specs that
// drive the store rather than the HTTP surface.
func seedAdminSession(t *testing.T, pool *pgxpool.Pool, userID string) string {
	t.Helper()
	authSessionID := insertSession(t, pool, userID, time.Hour, 8*time.Hour)
	var sessionID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO auth.admin_sessions
		    (user_id, auth_session_id, session_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1::uuid, $2::uuid, $3, now() + interval '15 minutes', now() + interval '8 hours')
		RETURNING id::text`, userID, authSessionID, "hash-"+userID).Scan(&sessionID)
	if err != nil {
		t.Fatalf("insert admin session: %v", err)
	}
	return sessionID
}
