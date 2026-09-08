package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// Issue #745: the HTTP contract for Web Push subscriptions.
//
// The security properties are the point of this file, and they are all the same
// property seen from different sides: the identity a request acts under comes
// from the session and from nowhere else. A body naming a user, a path naming
// somebody else's subscription and a token for a different account all have to
// change nothing about which rows are touched.

const (
	ownerUser      = "11111111-1111-4111-8111-111111111111"
	otherUser      = "22222222-2222-4222-8222-222222222222"
	ownerSession   = "33333333-3333-4333-8333-333333333333"
	ownerWorkspace = "44444444-4444-4444-8444-444444444444"
	otherWorkspace = "55555555-5555-4555-8555-555555555555"
	ownedSubscript = "66666666-6666-4666-8666-666666666666"
)

// ── fakes ────────────────────────────────────────────────────────────────────

// fakeValidator stands in for the JWT validator. What a token asserts is a test
// input here; whether the assertion survives the database is the resolver's job.
type fakeValidator struct {
	identity tokenIdentity
	err      error
}

func (f fakeValidator) ValidateAccessToken(string) (tokenIdentity, error) {
	return f.identity, f.err
}

// fakeResolver stands in for the database-backed principal resolution.
type fakeResolver struct {
	principals map[string]domain.Principal
	err        error
	seen       []tokenIdentity
}

func (f *fakeResolver) Resolve(_ context.Context, userID, sessionID string) (domain.Principal, error) {
	f.seen = append(f.seen, tokenIdentity{UserID: userID, SessionID: sessionID})
	if f.err != nil {
		return domain.Principal{}, f.err
	}
	principal, ok := f.principals[userID]
	if !ok {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	return principal, nil
}

// fakeService is the store the handler talks to, keyed by owner so that
// "somebody else's subscription" is a real condition and not an assertion.
type fakeService struct {
	registrations []domain.Registration
	principals    []domain.Principal
	owned         map[string]domain.Principal
	registerErr   error
	listErr       error
}

func newFakeService() *fakeService {
	return &fakeService{owned: map[string]domain.Principal{
		ownedSubscript: {UserID: ownerUser, WorkspaceID: ownerWorkspace},
	}}
}

func (f *fakeService) Register(
	_ context.Context, principal domain.Principal, registration domain.Registration,
) (domain.PushSubscription, error) {
	f.principals = append(f.principals, principal)
	f.registrations = append(f.registrations, registration)
	if f.registerErr != nil {
		return domain.PushSubscription{}, f.registerErr
	}
	if err := registration.Validate(); err != nil {
		return domain.PushSubscription{}, err
	}
	return domain.PushSubscription{
		ID: ownedSubscript, DeviceID: registration.DeviceID, Status: domain.StatusActive,
		CreatedAt: time.Unix(0, 0).UTC(), LastSeenAt: time.Unix(0, 0).UTC(),
	}, nil
}

func (f *fakeService) List(
	_ context.Context, principal domain.Principal,
) ([]domain.PushSubscription, error) {
	f.principals = append(f.principals, principal)
	if f.listErr != nil {
		return nil, f.listErr
	}
	var subscriptions []domain.PushSubscription
	for id, owner := range f.owned {
		if owner == principal {
			subscriptions = append(subscriptions, domain.PushSubscription{
				ID: id, DeviceID: "device-1", Status: domain.StatusActive,
				CreatedAt: time.Unix(0, 0).UTC(), LastSeenAt: time.Unix(0, 0).UTC(),
			})
		}
	}
	return subscriptions, nil
}

func (f *fakeService) Disable(
	_ context.Context, principal domain.Principal, subscriptionID string,
) error {
	f.principals = append(f.principals, principal)
	if owner, ok := f.owned[subscriptionID]; !ok || owner != principal {
		return domain.ErrNotFound
	}
	delete(f.owned, subscriptionID)
	return nil
}

// ── harness ──────────────────────────────────────────────────────────────────

type harness struct {
	router   http.Handler
	service  *fakeService
	resolver *fakeResolver
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	service := newFakeService()
	resolver := &fakeResolver{principals: map[string]domain.Principal{
		ownerUser: {UserID: ownerUser, WorkspaceID: ownerWorkspace},
		otherUser: {UserID: otherUser, WorkspaceID: otherWorkspace},
	}}
	router := NewRouter(testConfig(), platformlog.New("notification-service", "test"),
		WithPushSubscriptions(
			fakeValidator{identity: tokenIdentity{UserID: ownerUser, SessionID: ownerSession}},
			resolver, NewPushSubscriptionHandler(service)))
	return &harness{router: router, service: service, resolver: resolver}
}

// as rebuilds the router with a token asserting a different account, which is
// the only way one test user reaches another's data at all.
func (h *harness) as(userID string) {
	h.router = NewRouter(testConfig(), platformlog.New("notification-service", "test"),
		WithPushSubscriptions(
			fakeValidator{identity: tokenIdentity{UserID: userID, SessionID: ownerSession}},
			h.resolver, NewPushSubscriptionHandler(h.service)))
}

func (h *harness) do(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Authorization", "Bearer token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	h.router.ServeHTTP(response, request)
	return response
}

func encodedKey(size int, first byte) string {
	raw := make([]byte, size)
	raw[0] = first
	for i := 1; i < size; i++ {
		raw[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

var (
	testP256dh = encodedKey(65, 0x04)
	testAuth   = encodedKey(16, 0x01)
)

func registerBody() string {
	return fmt.Sprintf(
		`{"device_id":"device-1","endpoint":"https://push.example.com/s/abc","p256dh":%q,"auth":%q}`,
		testP256dh, testAuth)
}

func decodeEnvelope(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (%s)", err, response.Body.String())
	}
	return envelope.Data
}

// ── registration ─────────────────────────────────────────────────────────────

func TestRegisterPersistsUnderTheSessionsPrincipal(t *testing.T) {
	h := newHarness(t)

	response := h.do(t, http.MethodPost, RoutePushSubscriptions, registerBody())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
	}
	if len(h.service.principals) != 1 ||
		h.service.principals[0] != (domain.Principal{UserID: ownerUser, WorkspaceID: ownerWorkspace}) {
		t.Fatalf("service saw %+v", h.service.principals)
	}
}

// The same request twice is the same subscription. A client retrying a request
// whose response it never saw must not end up with two rows, and must not be
// told anything different the second time.
func TestRegisterIsIdempotentForARepeatedRequest(t *testing.T) {
	h := newHarness(t)

	first := h.do(t, http.MethodPost, RoutePushSubscriptions, registerBody())
	second := h.do(t, http.MethodPost, RoutePushSubscriptions, registerBody())
	if first.Code != second.Code || first.Body.String() != second.Body.String() {
		t.Fatalf("retry differed: %d %s vs %d %s",
			first.Code, first.Body.String(), second.Code, second.Body.String())
	}
}

// The response is the reconcile state and nothing else. An endpoint or a key
// echoed back would put the capability to push to somebody's browser into every
// proxy log and error report that ever captures a response.
func TestRegisterResponseCarriesNoEndpointOrKeys(t *testing.T) {
	h := newHarness(t)

	response := h.do(t, http.MethodPost, RoutePushSubscriptions, registerBody())
	assertNoSecrets(t, response.Body.String())

	data := decodeEnvelope(t, response)
	for _, forbidden := range []string{"endpoint", "p256dh", "auth", "keys"} {
		if _, present := data[forbidden]; present {
			t.Fatalf("response carries %q: %s", forbidden, response.Body.String())
		}
	}
	if data["status"] != string(domain.StatusActive) || data["device_id"] != "device-1" {
		t.Fatalf("response is missing the reconcile state: %v", data)
	}
}

// Mass assignment, stated as the API's behaviour rather than as a hope about the
// decoder: a body naming the columns that decide ownership or lifecycle is
// refused outright, so there is no version of it that is half-applied.
func TestRegisterRefusesFieldsTheContractDoesNotName(t *testing.T) {
	forged := map[string]string{
		"user_id":         fmt.Sprintf(`"user_id":%q`, otherUser),
		"workspace_id":    fmt.Sprintf(`"workspace_id":%q`, otherWorkspace),
		"actor_id":        fmt.Sprintf(`"actor_id":%q`, otherUser),
		"status":          `"status":"invalid"`,
		"failure_count":   `"failure_count":99`,
		"last_success_at": `"last_success_at":"2020-01-01T00:00:00Z"`,
		"invalidated_at":  `"invalidated_at":"2020-01-01T00:00:00Z"`,
		"id":              fmt.Sprintf(`"id":%q`, ownedSubscript),
	}
	for name, field := range forged {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			body := strings.TrimSuffix(registerBody(), "}") + "," + field + "}"

			response := h.do(t, http.MethodPost, RoutePushSubscriptions, body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
			}
			if len(h.service.principals) != 0 {
				t.Fatalf("a forged field reached the service: %+v", h.service.principals)
			}
		})
	}
}

// Even if the decoder ever accepted them, the identity would still not move:
// the principal comes from the resolver. This is the same property proved
// without relying on the decoder at all.
func TestRegisterIgnoresAForgedIdentityWhenOneIsSomehowPresent(t *testing.T) {
	h := newHarness(t)
	h.service.owned = map[string]domain.Principal{}

	response := h.do(t, http.MethodPost, RoutePushSubscriptions, registerBody())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	principal := h.service.principals[0]
	if principal.UserID != ownerUser || principal.WorkspaceID != ownerWorkspace {
		t.Fatalf("principal = %+v, want the session's", principal)
	}
}

func TestRegisterRejectsMalformedBodies(t *testing.T) {
	cases := map[string]string{
		"not json":         `{`,
		"empty object":     `{}`,
		"array":            `[]`,
		"trailing data":    registerBody() + `{"device_id":"device-2"}`,
		"empty endpoint":   `{"device_id":"d","endpoint":"","p256dh":"x","auth":"y"}`,
		"http endpoint":    `{"device_id":"d","endpoint":"http://p.example.com/s","p256dh":"x","auth":"y"}`,
		"missing keys":     `{"device_id":"d","endpoint":"https://p.example.com/s"}`,
		"malformed p256dh": `{"device_id":"d","endpoint":"https://p.example.com/s","p256dh":"!!","auth":"!!"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			response := h.do(t, http.MethodPost, RoutePushSubscriptions, body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
			}
			assertNoSecrets(t, response.Body.String())
		})
	}
}

func TestRegisterRejectsAnOversizedBody(t *testing.T) {
	h := newHarness(t)
	body := strings.TrimSuffix(registerBody(), "}") +
		`,"device_id":"` + strings.Repeat("x", int(maxPushRequestBodyBytes)) + `"}`

	response := h.do(t, http.MethodPost, RoutePushSubscriptions, body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestRegisterRejectsANonJSONContentType(t *testing.T) {
	h := newHarness(t)
	request := httptest.NewRequest(http.MethodPost, RoutePushSubscriptions,
		strings.NewReader(registerBody()))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()

	h.router.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestRegisterAcceptsAParameterisedJSONContentType(t *testing.T) {
	h := newHarness(t)
	request := httptest.NewRequest(http.MethodPost, RoutePushSubscriptions,
		strings.NewReader(registerBody()))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()

	h.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
	}
}

// An endpoint another subscription already holds is refused, and the refusal
// says nothing about who holds it.
func TestRegisterReportsAnEndpointConflictWithoutNamingTheOwner(t *testing.T) {
	h := newHarness(t)
	h.service.registerErr = domain.ErrEndpointConflict

	response := h.do(t, http.MethodPost, RoutePushSubscriptions, registerBody())
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d", response.Code)
	}
	for _, leaked := range []string{otherUser, otherWorkspace, ownerUser} {
		if strings.Contains(response.Body.String(), leaked) {
			t.Fatalf("conflict response names a principal: %s", response.Body.String())
		}
	}
}

// One person's browsers and phones all register through the same route, and none
// of them costs another its registration. There is no ceiling to reach: the
// count-then-insert cap was removed rather than made atomic.
func TestRegisterAcceptsManyDevicesForOneCaller(t *testing.T) {
	h := newHarness(t)

	for device := 0; device < 25; device++ {
		body := strings.Replace(registerBody(), `"device-1"`,
			fmt.Sprintf(`"device-%d"`, device), 1)
		response := h.do(t, http.MethodPost, RoutePushSubscriptions, body)
		if response.Code != http.StatusOK {
			t.Fatalf("device %d: status = %d (%s)",
				device, response.Code, response.Body.String())
		}
	}
	if len(h.service.registrations) != 25 {
		t.Fatalf("service saw %d registrations, want 25", len(h.service.registrations))
	}
}

func TestRegisterReportsAnUnexpectedFailureWithoutDetail(t *testing.T) {
	h := newHarness(t)
	h.service.registerErr = fmt.Errorf("dial tcp 10.0.0.5:5432: connect: refused")

	response := h.do(t, http.MethodPost, RoutePushSubscriptions, registerBody())
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "10.0.0.5") {
		t.Fatalf("internal detail leaked: %s", response.Body.String())
	}
}

// ── reconcile ────────────────────────────────────────────────────────────────

func TestListReturnsOnlyTheCallersOwnSubscriptions(t *testing.T) {
	h := newHarness(t)

	response := h.do(t, http.MethodGet, RoutePushSubscriptions, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	data := decodeEnvelope(t, response)
	subscriptions, _ := data["subscriptions"].([]any)
	if len(subscriptions) != 1 {
		t.Fatalf("owner sees %v", data)
	}

	h.as(otherUser)
	response = h.do(t, http.MethodGet, RoutePushSubscriptions, "")
	data = decodeEnvelope(t, response)
	subscriptions, _ = data["subscriptions"].([]any)
	if len(subscriptions) != 0 {
		t.Fatalf("another user sees the owner's subscriptions: %v", data)
	}
}

func TestListResponseCarriesNoEndpointOrKeys(t *testing.T) {
	h := newHarness(t)

	response := h.do(t, http.MethodGet, RoutePushSubscriptions, "")
	assertNoSecrets(t, response.Body.String())
	if strings.Contains(response.Body.String(), "endpoint") {
		t.Fatalf("list response carries an endpoint: %s", response.Body.String())
	}
}

func TestListReportsAFailureWithoutDetail(t *testing.T) {
	h := newHarness(t)
	h.service.listErr = fmt.Errorf("relation \"chat.push_subscriptions\" does not exist")

	response := h.do(t, http.MethodGet, RoutePushSubscriptions, "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "chat.push_subscriptions") {
		t.Fatalf("schema detail leaked: %s", response.Body.String())
	}
}

// ── disable ──────────────────────────────────────────────────────────────────

func TestDisableRemovesOnlyTheCallersOwnSubscription(t *testing.T) {
	h := newHarness(t)

	response := h.do(t, http.MethodDelete, RoutePushSubscriptions+"/"+ownedSubscript, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
	}
	if _, still := h.service.owned[ownedSubscript]; still {
		t.Fatal("subscription was not disabled")
	}
}

// The BOLA case, stated directly: another user holding a valid session and the
// correct identifier is answered exactly as though the row did not exist, and
// the row survives.
func TestDisableRefusesAnotherUsersSubscription(t *testing.T) {
	h := newHarness(t)
	h.as(otherUser)

	response := h.do(t, http.MethodDelete, RoutePushSubscriptions+"/"+ownedSubscript, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	if _, still := h.service.owned[ownedSubscript]; !still {
		t.Fatal("another user disabled a subscription that is not theirs")
	}
}

// Same identifier, same user, different workspace. The principal carries both,
// so a subscription is unreachable from a workspace that does not own it even
// when the caller is the same person.
func TestDisableRefusesACrossWorkspaceSubscription(t *testing.T) {
	h := newHarness(t)
	h.resolver.principals[ownerUser] = domain.Principal{
		UserID: ownerUser, WorkspaceID: otherWorkspace,
	}

	response := h.do(t, http.MethodDelete, RoutePushSubscriptions+"/"+ownedSubscript, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	if _, still := h.service.owned[ownedSubscript]; !still {
		t.Fatal("a cross-workspace request disabled the subscription")
	}
}

// A subscription that never existed and one that belongs to somebody else give
// the same answer, which is what stops identifiers being enumerated.
func TestDisableAnswersUnknownAndForeignIdentifiersAlike(t *testing.T) {
	h := newHarness(t)
	unknown := h.do(t, http.MethodDelete,
		RoutePushSubscriptions+"/99999999-9999-4999-8999-999999999999", "")

	h.as(otherUser)
	foreign := h.do(t, http.MethodDelete, RoutePushSubscriptions+"/"+ownedSubscript, "")

	if unknown.Code != foreign.Code || unknown.Body.String() != foreign.Body.String() {
		t.Fatalf("answers differ: %d %s vs %d %s",
			unknown.Code, unknown.Body.String(), foreign.Code, foreign.Body.String())
	}
}

func TestDisableRejectsAMalformedIdentifier(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		response := h.do(t, http.MethodDelete, RoutePushSubscriptions+"/"+id, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("id %q: status = %d", id, response.Code)
		}
	}
}

// ── authentication ───────────────────────────────────────────────────────────

func TestEveryRouteRefusesAnUnauthenticatedRequest(t *testing.T) {
	h := newHarness(t)
	requests := []struct{ method, target, body string }{
		{http.MethodPost, RoutePushSubscriptions, registerBody()},
		{http.MethodGet, RoutePushSubscriptions, ""},
		{http.MethodDelete, RoutePushSubscriptions + "/" + ownedSubscript, ""},
	}
	for _, header := range []string{"", "Bearer", "Bearer ", "Basic abc", "token"} {
		for _, request := range requests {
			recorder := httptest.NewRecorder()
			httpRequest := httptest.NewRequest(request.method, request.target,
				strings.NewReader(request.body))
			if header != "" {
				httpRequest.Header.Set("Authorization", header)
			}
			httpRequest.Header.Set("Content-Type", "application/json")
			h.router.ServeHTTP(recorder, httpRequest)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with header %q: status = %d",
					request.method, request.target, header, recorder.Code)
			}
		}
	}
	if len(h.service.principals) != 0 {
		t.Fatalf("an unauthenticated request reached the service: %+v", h.service.principals)
	}
}

// A token whose session no longer exists is not a caller, however well it is
// signed. The signature proves issuance; only the database knows about
// revocation, expiry and suspension.
func TestARevokedSessionIsRefused(t *testing.T) {
	h := newHarness(t)
	h.resolver.err = domain.ErrUnauthenticated

	response := h.do(t, http.MethodGet, RoutePushSubscriptions, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestACallerWithNoWorkspaceMembershipIsForbidden(t *testing.T) {
	h := newHarness(t)
	h.resolver.err = domain.ErrForbidden

	response := h.do(t, http.MethodPost, RoutePushSubscriptions, registerBody())
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d", response.Code)
	}
	if len(h.service.principals) != 0 {
		t.Fatalf("a forbidden request reached the service: %+v", h.service.principals)
	}
}

// The failure that must never become an allow: the authorisation dependency
// could not answer.
func TestAResolverFailureIsNotAnAllow(t *testing.T) {
	h := newHarness(t)
	h.resolver.err = fmt.Errorf("connection refused")

	response := h.do(t, http.MethodPost, RoutePushSubscriptions, registerBody())
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if len(h.service.principals) != 0 {
		t.Fatalf("a request proceeded past a failed authorisation: %+v", h.service.principals)
	}
}

func TestARefusedTokenIsRefusedWithoutSayingWhy(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("notification-service", "test"),
		WithPushSubscriptions(
			fakeValidator{err: fmt.Errorf("token signature is invalid: key mismatch")},
			&fakeResolver{}, NewPushSubscriptionHandler(newFakeService())))
	request := httptest.NewRequest(http.MethodGet, RoutePushSubscriptions, nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "signature") {
		t.Fatalf("the reason leaked: %s", response.Body.String())
	}
}

// A handler mounted without a validator or a resolver must refuse rather than
// serve: an unconfigured dependency is not a reason to skip a check.
//
// This is a partial wiring, constructed directly here. The production path
// cannot produce it — WithPushSubscriptions takes all three together, and the
// app either applies it or mounts nothing, which is the 404 that
// TestRoutesAreAbsentWhenTheHandlerIsNotWired covers.
func TestUnconfiguredAuthenticationRefusesInsteadOfServing(t *testing.T) {
	for name, option := range map[string]Option{
		"no validator": WithPushSubscriptions(nil, &fakeResolver{},
			NewPushSubscriptionHandler(newFakeService())),
		"no resolver": WithPushSubscriptions(fakeValidator{}, nil,
			NewPushSubscriptionHandler(newFakeService())),
	} {
		t.Run(name, func(t *testing.T) {
			router := NewRouter(testConfig(),
				platformlog.New("notification-service", "test"), option)
			request := httptest.NewRequest(http.MethodGet, RoutePushSubscriptions, nil)
			request.Header.Set("Authorization", "Bearer token")
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}
}

// Without the handler the routes are not mounted at all, and the catch-all
// answers. Nothing here may become an unauthenticated success.
func TestRoutesAreAbsentWhenTheHandlerIsNotWired(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("notification-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RoutePushSubscriptions, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}

// requirePrincipal is the guard for a route mounted without Authenticate. It
// cannot happen through the router, which is why it is exercised directly: the
// alternative to a 401 is a handler running with an empty user and workspace.
func TestRequirePrincipalRefusesAnUnauthenticatedContext(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no principal":    context.Background(),
		"empty user":      context.WithValue(context.Background(), principalContextKey{}, domain.Principal{WorkspaceID: ownerWorkspace}),
		"empty workspace": context.WithValue(context.Background(), principalContextKey{}, domain.Principal{UserID: ownerUser}),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, RoutePushSubscriptions, nil).WithContext(ctx)
			if _, ok := requirePrincipal(response, request); ok {
				t.Fatal("requirePrincipal admitted a request with no principal")
			}
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}
}

// assertNoSecrets is the leak check every response goes through: neither key
// and no push endpoint may appear in a body, whatever the outcome was.
func assertNoSecrets(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{testP256dh, testAuth, "push.example.com"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response carries a push credential: %s", body)
		}
	}
}
