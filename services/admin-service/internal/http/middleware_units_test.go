package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// ── Guard wiring failures ───────────────────────────────────────────────────

// Each guard must refuse when its dependency is missing. A nil dependency is
// what a half-built router looks like, and "no validator" must never mean
// "no check".
func TestGuardsRefuseWhenTheirDependencyIsMissing(t *testing.T) {
	tests := map[string]http.Handler{
		"bearer":  BearerAuth(nil)(okHandler()),
		"session": RequireAdminSession(nil, sessionCookieName)(okHandler()),
		"csrf":    RequireCSRF(nil, nil)(okHandler()),
	}
	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d", response.Code)
			}
		})
	}
}

// RequireCapability sits after RequireAdminSession. If that ordering is ever
// broken, it must refuse rather than authorize an identity nobody proved.
func TestRequireCapability_WithoutAPrincipalIsUnauthorized(t *testing.T) {
	handler := RequireCapability(domain.CapabilityAuditRead, nil)(okHandler())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

// A denial must still be refused when no recorder is wired: the audit trail is
// evidence, never the thing that makes the decision.
func TestRequireCapability_DeniesWithoutARecorder(t *testing.T) {
	handler := RequireCapability(domain.CapabilityAuditRead, nil)(okHandler())
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(context.WithValue(request.Context(), adminContextKey{}, domain.AuthenticatedAdmin{
		Principal: domain.AdminPrincipal{Capabilities: domain.NewCapabilitySet([]domain.Capability{domain.CapabilityUsersRead})},
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.Code)
	}
}

func TestBootstrap_WithoutAPrincipalIsUnauthorized(t *testing.T) {
	response := httptest.NewRecorder()
	Bootstrap(adminConfig(), nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestSessionHandlers_RefuseWithoutTheirDependencies(t *testing.T) {
	cfg := adminConfig()
	for name, handler := range map[string]http.Handler{
		"create":  CreateAdminSession(nil, nil, cfg, nil),
		"destroy": DestroyAdminSession(nil, nil, cfg, slog.New(slog.DiscardHandler)),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d", response.Code)
			}
		})
	}
}

func TestListAuditEvents_RefusesWithoutAReader(t *testing.T) {
	response := httptest.NewRecorder()
	ListAuditEvents(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

// ── Error mapping ───────────────────────────────────────────────────────────

// The database being down is not an authorization answer. Mapping it to 401 or
// 403 would tell a caller that its credential is bad when it is not, and would
// hide an outage inside what looks like a normal refusal.
func TestWriteAuthError_MapsEachDomainErrorOnce(t *testing.T) {
	tests := map[error]int{
		domain.ErrUnauthorized:     http.StatusUnauthorized,
		domain.ErrForbidden:        http.StatusForbidden,
		domain.ErrUnavailable:      http.StatusServiceUnavailable,
		errors.New("disk on fire"): http.StatusInternalServerError,
	}
	for err, want := range tests {
		response := httptest.NewRecorder()
		writeAuthError(response, err)
		if response.Code != want {
			t.Fatalf("%v: expected %d, got %d", err, want, response.Code)
		}
		if strings.Contains(response.Body.String(), "disk on fire") {
			t.Fatalf("an internal error must not be echoed: %s", response.Body.String())
		}
	}
}

func TestAuditResultFor(t *testing.T) {
	tests := map[error]domain.AuditResult{
		nil:                    domain.AuditResultSuccess,
		domain.ErrForbidden:    domain.AuditResultDenied,
		domain.ErrUnauthorized: domain.AuditResultDenied,
		errors.New("timeout"):  domain.AuditResultError,
	}
	for err, want := range tests {
		if got := auditResultFor(err); got != want {
			t.Fatalf("%v: expected %q, got %q", err, want, got)
		}
	}
}

func TestParseLimit(t *testing.T) {
	tests := map[string]struct {
		want int
		ok   bool
	}{
		"":    {0, true},
		"25":  {25, true},
		"0":   {0, false},
		"-1":  {0, false},
		"abc": {0, false},
		"9e9": {0, false},
	}
	for raw, tt := range tests {
		got, ok := parseLimit(raw)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("parseLimit(%q) = (%d, %v), want (%d, %v)", raw, got, ok, tt.want, tt.ok)
		}
	}
}

// ── Token validator construction ────────────────────────────────────────────

func TestNewTokenValidator_RefusesWeakConfiguration(t *testing.T) {
	tests := map[string][3]string{
		"short secret": {"too-short", testIssuer, testAudience},
		"no issuer":    {testJWTSecret, "   ", testAudience},
		"no audience":  {testJWTSecret, testIssuer, ""},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTokenValidator(args[0], args[1], args[2]); err == nil {
				t.Fatal("expected the validator to refuse")
			}
		})
	}
}

// A token signed with an algorithm the service did not choose is the classic
// JWT confusion attack. It must be refused before the signature is even
// considered valid.
func TestValidateAccessToken_RefusesAnUnexpectedAlgorithm(t *testing.T) {
	validator, err := NewTokenValidator(testJWTSecret, testIssuer, testAudience)
	if err != nil {
		t.Fatalf("NewTokenValidator: %v", err)
	}
	// alg=none, with the claims a valid token would carry.
	const noneToken = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." +
		"eyJzdWIiOiIxMTExMTExMS0xMTExLTExMTEtMTExMS0xMTExMTExMTExMTEiLCJzaWQiOiIzMzMzMzMzMy0zMzMzLTMzMzMtMzMzMy0zMzMzMzMzMzMzMzMifQ."
	if _, err := validator.ValidateAccessToken(noneToken); err == nil {
		t.Fatal("expected an unsigned token to be refused")
	}
}

// ── CSRF unit behaviour ─────────────────────────────────────────────────────

func TestIsSafeMethod(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if !isSafeMethod(method) {
			t.Fatalf("%s must be treated as safe", method)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if isSafeMethod(method) {
			t.Fatalf("%s must not be treated as safe", method)
		}
	}
}

func TestOriginAllowed_RejectsMalformedValues(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Host = "admin.example.test"

	for _, origin := range []string{"", "not a url", "://", "https://"} {
		request.Header.Set("Origin", origin)
		if originAllowed(request, nil) {
			t.Fatalf("origin %q must not be accepted", origin)
		}
	}
	request.Header.Del("Origin")
	for _, referer := range []string{"", "/relative/path", "javascript:alert(1)"} {
		request.Header.Set("Referer", referer)
		if originAllowed(request, nil) {
			t.Fatalf("referer %q must not be accepted", referer)
		}
	}
}

func TestRequireCSRF_WithoutAPrincipalIsUnauthorized(t *testing.T) {
	validator := &staticCSRF{valid: true}
	handler := RequireCSRF(validator, []string{testOrigin})(okHandler())

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Origin", testOrigin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestRequireCSRF_SafeMethodsPassThrough(t *testing.T) {
	handler := RequireCSRF(&staticCSRF{}, nil)(okHandler())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

type staticCSRF struct{ valid bool }

func (s *staticCSRF) ValidateCSRF(string, string) bool { return s.valid }

// ── Rate limiting ───────────────────────────────────────────────────────────

// The handshake is the one route reachable with a chat token alone, so it is
// the one worth brute-forcing.
func TestIPRateLimiter_RefusesOnceTheBucketIsEmpty(t *testing.T) {
	limiter := NewIPRateLimiter(1, 2, nil)
	handler := limiter.Middleware(okHandler())

	for attempt := 1; attempt <= 2; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("attempt %d: expected 200, got %d", attempt, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", response.Code)
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("expected a Retry-After header")
	}
}

func TestIPRateLimiter_RefillsOverTime(t *testing.T) {
	limiter := NewIPRateLimiter(60, 1, nil)
	now := time.Now()
	limiter.now = func() time.Time { return now }

	if !limiter.allow("1.2.3.4") {
		t.Fatal("expected the first request to pass")
	}
	if limiter.allow("1.2.3.4") {
		t.Fatal("expected the second request to be refused")
	}
	now = now.Add(time.Minute)
	if !limiter.allow("1.2.3.4") {
		t.Fatal("expected the bucket to refill")
	}
}

func TestIPRateLimiter_FallsBackToDefaultsForNonsenseConfiguration(t *testing.T) {
	limiter := NewIPRateLimiter(0, -1, nil)
	if limiter.limitPerMinute <= 0 || limiter.burst <= 0 {
		t.Fatalf("expected positive defaults, got %+v", limiter)
	}
}

func TestMinFloat(t *testing.T) {
	if minFloat(1, 2) != 1 || minFloat(2, 1) != 1 {
		t.Fatal("unexpected minimum")
	}
}

// ── Readiness ───────────────────────────────────────────────────────────────

func TestReadyz_ReportsTheDatabaseWhenTheAdminAPIIsConfigured(t *testing.T) {
	cfg := adminConfig()

	response := httptest.NewRecorder()
	Readyz(cfg, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("a configured admin api without a pool must be unready, got %d", response.Code)
	}

	response = httptest.NewRecorder()
	Readyz(cfg, pingerFunc(func(context.Context) error { return nil })).
		ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected ready, got %d", response.Code)
	}

	response = httptest.NewRecorder()
	Readyz(cfg, pingerFunc(func(context.Context) error { return errors.New("down") })).
		ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected unready, got %d", response.Code)
	}
}

// A deployment that only serves health and version stays ready without a
// database, so enabling this service everywhere does not require the Admin API.
func TestReadyz_HealthOnlyDeploymentStaysReady(t *testing.T) {
	cfg := config.Config{ServiceName: "admin-service", Env: "test"}
	response := httptest.NewRecorder()
	Readyz(cfg, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

type pingerFunc func(context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }

// ── Method routing ──────────────────────────────────────────────────────────

func TestMethodRouter_AnswersOptionsWithoutABody(t *testing.T) {
	router := methodRouter(map[string]http.Handler{http.MethodPost: okHandler()})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodOptions, "/", nil))

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", response.Code)
	}
	if !strings.Contains(response.Header().Get("Allow"), "POST") {
		t.Fatalf("unexpected Allow header %q", response.Header().Get("Allow"))
	}
}

// ── Readiness through the assembled router ─────────────────────────────────

// A fully wired admin-service reports itself ready. This is the counterpart to
// the fail-closed cases below: without it, "readyz is 503" would be indis-
// tinguishable from "the harness never wired a pool".
func TestRouter_ReadyzReportsReadyWhenTheStoreAnswers(t *testing.T) {
	harness := newHarness(t, adminStore())

	response := harness.do(httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"postgres"`) {
		t.Fatalf("expected the database check to be reported: %s", response.Body.String())
	}
}

// The database being unreachable must take the pod out of rotation. Production
// semantics are unchanged: the check is derived from the store instance, and an
// instance that cannot answer is not ready.
func TestRouter_ReadyzReportsUnreadyWhenTheStoreFails(t *testing.T) {
	store := adminStore()
	store.pingErr = errors.New("connection refused")
	harness := newHarness(t, store)

	response := harness.do(httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "connection refused") {
		t.Fatalf("readiness must not echo the driver error: %s", response.Body.String())
	}
}

// ── Logout failure ─────────────────────────────────────────────────────────

// A revocation the database refused must still end the browser's session, still
// be recorded as an error in the audit trail, and still leave something in the
// service log for an operator — without putting the credential in it.
func TestDestroySession_LogsARevocationFailureWithoutTheCredential(t *testing.T) {
	store := adminStore()
	store.revokeErr = errors.New("connection refused")
	logged := &bytes.Buffer{}
	harness := newHarness(t, store, withLogger(slog.New(slog.NewJSONHandler(logged, nil))))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodDelete, RouteAdminSession, cookie, csrf))

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", response.Code)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("the cookie must be cleared even when revocation failed, got %+v", cookies)
	}
	assertRecorded(t, store, "admin.session.create:success", "admin.session.destroy:error")

	line := logged.String()
	if !strings.Contains(line, "admin session revocation failed") {
		t.Fatalf("expected the failure to be logged, got %q", line)
	}
	if !strings.Contains(line, testUserID) || !strings.Contains(line, testAdminSession) {
		t.Fatalf("expected the actor and session identifiers in the log, got %q", line)
	}
	for _, forbidden := range []string{cookie.Value, "connection refused", "Authorization", "Cookie", testJWTSecret} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("log leaked %q: %s", forbidden, line)
		}
	}
}

// ── Rate limiter eviction ──────────────────────────────────────────────────

// The map must stay bounded, and it must stay bounded by evicting the least
// recently used bucket rather than by clearing itself. A reset would make
// overflowing the limiter the cheapest way past it.
func TestIPRateLimiter_EvictsTheOldestInsteadOfForgivingEveryone(t *testing.T) {
	limiter := NewIPRateLimiter(1, 1, nil)
	now := time.Now()
	limiter.now = func() time.Time { return now }

	// An active client spends its whole budget and is now being refused.
	if !limiter.allow("active") {
		t.Fatal("expected the first request to pass")
	}
	if limiter.allow("active") {
		t.Fatal("expected the active client to be out of budget")
	}

	// An attacker fills the map with throwaway keys, each one newer than the
	// last, and keeps the active client's bucket fresh by touching it.
	for i := 0; i < rateLimiterMaxBuckets+500; i++ {
		now = now.Add(time.Millisecond)
		limiter.allow("spray-" + strconv.Itoa(i))
		limiter.allow("active")
	}

	if len(limiter.buckets) > rateLimiterMaxBuckets {
		t.Fatalf("the map must stay bounded, got %d buckets", len(limiter.buckets))
	}
	// The whole point: the spray did not hand the active client a fresh budget.
	if limiter.allow("active") {
		t.Fatal("overflowing the limiter must not forgive an active client")
	}
	if _, ok := limiter.buckets["active"]; !ok {
		t.Fatal("an actively spending client must not be evicted")
	}
}

// A recent hit moves a client to the front of the queue, so the client evicted
// next is genuinely the one that has gone longest without spending.
func TestIPRateLimiter_TouchingABucketProtectsItFromEviction(t *testing.T) {
	limiter := NewIPRateLimiter(600, 5, nil)
	now := time.Now()
	limiter.now = func() time.Time { return now }

	for i := 0; i < rateLimiterMaxBuckets; i++ {
		now = now.Add(time.Millisecond)
		limiter.allow("client-" + strconv.Itoa(i))
	}
	// client-0 is the oldest and would be evicted next — unless it is touched.
	now = now.Add(time.Millisecond)
	limiter.allow("client-0")

	now = now.Add(time.Millisecond)
	limiter.allow("newcomer")

	if _, ok := limiter.buckets["client-0"]; !ok {
		t.Fatal("the touched bucket must survive: it is no longer the least recently used")
	}
	if _, ok := limiter.buckets["client-1"]; ok {
		t.Fatal("expected the new least recently used bucket to be evicted instead")
	}
	if len(limiter.buckets) != rateLimiterMaxBuckets {
		t.Fatalf("expected the map to stay at its ceiling, got %d", len(limiter.buckets))
	}
}

// The map and the list describe one set. If they ever disagreed, the ceiling
// would stop being enforceable.
func TestIPRateLimiter_MapAndListStayInStep(t *testing.T) {
	limiter := NewIPRateLimiter(600, 5, nil)
	now := time.Now()
	limiter.now = func() time.Time { return now }

	for i := 0; i < rateLimiterMaxBuckets+250; i++ {
		now = now.Add(time.Millisecond)
		limiter.allow("client-" + strconv.Itoa(i))
	}

	if limiter.lru.Len() != len(limiter.buckets) {
		t.Fatalf("list has %d entries, map has %d", limiter.lru.Len(), len(limiter.buckets))
	}
	for element := limiter.lru.Front(); element != nil; element = element.Next() {
		key, _ := element.Value.(string)
		bucket, ok := limiter.buckets[key]
		if !ok {
			t.Fatalf("list holds %q, which the map does not", key)
		}
		if bucket.element != element {
			t.Fatalf("bucket %q points at a different list element", key)
		}
	}
}

// A bucket idle past the TTL is reset when it is touched again, rather than
// inheriting a budget from an hour ago.
func TestIPRateLimiter_ExpiredBucketStartsOver(t *testing.T) {
	limiter := NewIPRateLimiter(60, 1, nil)
	now := time.Now()
	limiter.now = func() time.Time { return now }

	if !limiter.allow("client") {
		t.Fatal("expected the first request to pass")
	}
	if limiter.allow("client") {
		t.Fatal("expected the budget to be spent")
	}

	now = now.Add(2 * rateLimiterBucketTTL)
	if !limiter.allow("client") {
		t.Fatal("an expired bucket must start over")
	}
	if limiter.lru.Len() != len(limiter.buckets) {
		t.Fatalf("expiry must remove from both structures, list=%d map=%d", limiter.lru.Len(), len(limiter.buckets))
	}
}

// Eviction under concurrency must not corrupt either structure or drop the
// ceiling. Run with -race, which is where this test earns its place.
func TestIPRateLimiter_StaysBoundedUnderConcurrentKeys(t *testing.T) {
	limiter := NewIPRateLimiter(600, 5, nil)

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				limiter.allow("w" + strconv.Itoa(worker) + "-" + strconv.Itoa(i))
			}
		}(worker)
	}
	wg.Wait()

	limiter.mu.Lock()
	size := len(limiter.buckets)
	listLen := limiter.lru.Len()
	limiter.mu.Unlock()
	if size > rateLimiterMaxBuckets {
		t.Fatalf("the map must stay bounded under concurrency, got %d", size)
	}
	if listLen != size {
		t.Fatalf("list and map disagree after concurrent use: list=%d map=%d", listLen, size)
	}
}

// A pool that exists can lose the database and get it back; pgx reconnects and
// Ping reports whichever is true right now. That is a different situation from
// a pool that could never be opened at startup — which is a startup failure,
// not a readiness state — and readiness has to track the first one live.
func TestReadyz_FollowsTheDatabaseBackAndForth(t *testing.T) {
	cfg := adminConfig()
	var down bool
	handler := Readyz(cfg, pingerFunc(func(context.Context) error {
		if down {
			return errors.New("connection refused")
		}
		return nil
	}))

	probe := func() int {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))
		return response.Code
	}

	if got := probe(); got != http.StatusOK {
		t.Fatalf("healthy database: expected 200, got %d", got)
	}
	down = true
	if got := probe(); got != http.StatusServiceUnavailable {
		t.Fatalf("database lost: expected 503, got %d", got)
	}
	down = false
	if got := probe(); got != http.StatusOK {
		t.Fatalf("database recovered: expected 200, got %d", got)
	}
}
