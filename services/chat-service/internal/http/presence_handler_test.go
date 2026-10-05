package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
)

// The caller's own manual presence (issue #798): identity from the session,
// workspace from the server, a closed body, and a per-user write budget.

type fakePresenceSettings struct {
	mu        sync.Mutex
	override  domain.PresenceOverride
	err       error
	calls     []string
	lastUser  string
	lastWS    string
	lastState string
	lastEnd   time.Time
}

func (f *fakePresenceSettings) Manual(_ context.Context, workspaceID, userID string) (domain.PresenceOverride, error) {
	f.record("get", workspaceID, userID)
	return f.override, f.err
}

func (f *fakePresenceSettings) SetManual(
	_ context.Context, workspaceID, userID, state string, expiresAt time.Time,
) (domain.PresenceOverride, error) {
	f.record("put", workspaceID, userID)
	f.mu.Lock()
	f.lastState, f.lastEnd = state, expiresAt
	f.mu.Unlock()
	if f.err != nil {
		return domain.PresenceOverride{}, f.err
	}
	return domain.PresenceOverride{State: domain.PresenceManualState(state), ExpiresAt: expiresAt}, nil
}

func (f *fakePresenceSettings) ClearManual(_ context.Context, workspaceID, userID string) error {
	f.record("delete", workspaceID, userID)
	return f.err
}

func (f *fakePresenceSettings) record(call, workspaceID, userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.lastWS, f.lastUser = workspaceID, userID
}

type presenceRouteEnv struct {
	router   http.Handler
	settings *fakePresenceSettings
	token    string
}

func newPresenceRouteEnv(t *testing.T) presenceRouteEnv {
	t.Helper()
	return newPresenceRouteEnvWith(t, true)
}

func newPresenceRouteEnvWith(t *testing.T, writable bool) presenceRouteEnv {
	t.Helper()
	settings := &fakePresenceSettings{}
	workspaces := &routeWorkspaceStore{workspace: routeActiveWorkspace()}
	messages := httpapi.NewMessageHandler(workspaces, nil, nil).
		WithPresence(httpapi.NewPresenceHandler(workspaces, settings, nil).WithWritesEnabled(writable))
	router := httpapi.NewRouter(
		sidebarTestConfig(), nil, httpapi.ReadinessState{}, makeTestValidator(t), allowAllSessionValidator{},
		httpapi.NewSidebarHandler(nil), messages, nil, nil, nil, nil, nil,
	)
	return presenceRouteEnv{
		router: router, settings: settings,
		token: makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour),
	}
}

func (e presenceRouteEnv) do(method, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, httpapi.RoutePresenceMe, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		request.Header.Set("Authorization", bearer(e.token))
	}
	recorder := httptest.NewRecorder()
	e.router.ServeHTTP(recorder, request)
	return recorder
}

func decodePresenceSettings(t *testing.T, recorder *httptest.ResponseRecorder) (state, expires *string) {
	t.Helper()
	var envelope struct {
		Data struct {
			State     *string `json:"state"`
			ExpiresAt *string `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v; body %s", err, recorder.Body.String())
	}
	return envelope.Data.State, envelope.Data.ExpiresAt
}

func TestPresenceRoute_AutomaticPresenceReadsAsNull(t *testing.T) {
	env := newPresenceRouteEnv(t)
	recorder := env.do(http.MethodGet, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if state, expires := decodePresenceSettings(t, recorder); state != nil || expires != nil {
		t.Fatalf("automatic presence = %v %v, want nulls", state, expires)
	}
	if env.settings.lastUser != testUserID || env.settings.lastWS != testWorkspaceID {
		t.Fatalf("resolved %q/%q, want the session's user and the server's workspace", env.settings.lastUser, env.settings.lastWS)
	}
}

func TestPresenceRoute_SetReturnsTheStoredState(t *testing.T) {
	env := newPresenceRouteEnv(t)
	recorder := env.do(http.MethodPut, `{"state":"dnd","expires_at":"2026-10-01T18:00:00-03:00"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	state, expires := decodePresenceSettings(t, recorder)
	if state == nil || *state != "dnd" || expires == nil || *expires != "2026-10-01T21:00:00Z" {
		t.Fatalf("response = %v %v", state, expires)
	}
	if env.settings.lastState != "dnd" || env.settings.lastUser != testUserID {
		t.Fatalf("service saw %q for %q", env.settings.lastState, env.settings.lastUser)
	}
}

func TestPresenceRoute_ClearReturnsAutomatic(t *testing.T) {
	env := newPresenceRouteEnv(t)
	recorder := env.do(http.MethodDelete, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if state, _ := decodePresenceSettings(t, recorder); state != nil {
		t.Fatalf("after clear = %v", *state)
	}
}

func TestPresenceRoute_TheBodyCannotNameAnybody(t *testing.T) {
	env := newPresenceRouteEnv(t)
	for _, body := range []string{
		`{"state":"busy","expires_at":"2026-10-01T18:00:00Z","user_id":"someone-else"}`,
		`{"state":"busy","expires_at":"2026-10-01T18:00:00Z","workspace_id":"another"}`,
		`{"state":"busy","expires_at":"2026-10-01T18:00:00Z","updated_at":"2020-01-01T00:00:00Z"}`,
		`{"state":"busy","expires_at":"tomorrow"}`,
		`{"state":"busy"`,
		`{"state":"busy","expires_at":"2026-10-01T18:00:00Z"}{}`,
	} {
		if recorder := env.do(http.MethodPut, body); recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", body, recorder.Code)
		}
	}
	if len(env.settings.calls) != 0 {
		t.Fatalf("a refused body reached the service: %v", env.settings.calls)
	}
}

func TestPresenceRoute_ServiceErrorsKeepTheirMeaning(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{domain.ErrInvalidInput, http.StatusBadRequest},
		{domain.ErrForbidden, http.StatusForbidden},
		{errors.New("database unavailable"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		env := newPresenceRouteEnv(t)
		env.settings.err = tc.err
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			recorder := env.do(method, `{"state":"busy","expires_at":"2026-10-01T18:00:00Z"}`)
			if recorder.Code != tc.want {
				t.Fatalf("%v %s: status = %d, want %d", tc.err, method, recorder.Code, tc.want)
			}
		}
	}
}

func TestPresenceRoute_RequiresASession(t *testing.T) {
	env := newPresenceRouteEnv(t)
	env.token = ""
	if recorder := env.do(http.MethodPut, `{"state":"busy","expires_at":"2026-10-01T18:00:00Z"}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestPresenceRoute_WritesAreRateLimited(t *testing.T) {
	env := newPresenceRouteEnv(t)
	limited := false
	for i := 0; i < 40; i++ {
		if env.do(http.MethodDelete, "").Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("forty writes in a minute were all admitted")
	}
}

func TestPresenceHandler_UnwiredAndAnonymous(t *testing.T) {
	recorder := httptest.NewRecorder()
	var unwired *httpapi.PresenceHandler
	unwired.Get(recorder, requestWithUser(http.MethodGet, httpapi.RoutePresenceMe, nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired = %d", recorder.Code)
	}

	handler := httpapi.NewPresenceHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakePresenceSettings{}, nil)
	recorder = httptest.NewRecorder()
	handler.Delete(recorder, httptest.NewRequest(http.MethodDelete, httpapi.RoutePresenceMe, nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", recorder.Code)
	}

	failing := httpapi.NewPresenceHandler(&fakeWorkspaceResolver{err: domain.ErrNotFound}, &fakePresenceSettings{}, nil)
	recorder = httptest.NewRecorder()
	failing.Get(recorder, requestWithUser(http.MethodGet, httpapi.RoutePresenceMe, nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("no workspace = %d", recorder.Code)
	}
}

func TestPresenceUpdateResult(t *testing.T) {
	cases := map[int]string{
		http.StatusOK: "success", http.StatusBadRequest: "invalid", http.StatusForbidden: "denied",
		http.StatusUnauthorized: "denied", http.StatusTooManyRequests: "rate_limited",
		http.StatusInternalServerError: "error", http.StatusServiceUnavailable: "error",
	}
	for status, want := range cases {
		if got := httpapi.ExportPresenceUpdateResult(status); got != want {
			t.Fatalf("result(%d) = %q, want %q", status, got, want)
		}
	}
}

// Closed (the committed default, issue #798 rollout): reads answer and say so,
// writes are refused before anything reaches the service.
func TestPresenceRoute_ClosedGateRefusesWritesAndSaysSo(t *testing.T) {
	env := newPresenceRouteEnvWith(t, false)
	recorder := env.do(http.MethodGet, "")
	var envelope struct {
		Data struct {
			Writable *bool `json:"writable"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.Data.Writable == nil || *envelope.Data.Writable {
		t.Fatalf("closed gate read = %s", recorder.Body.String())
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		recorder := env.do(method, `{"state":"busy","expires_at":"2099-10-01T18:00:00Z"}`)
		if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "manual_presence_unavailable" {
			t.Fatalf("%s with the gate closed = %d %s", method, recorder.Code, recorder.Body.String())
		}
	}
	for _, call := range env.settings.calls {
		if call != "get" {
			t.Fatalf("a closed gate let %q reach the service", call)
		}
	}
}

// A write whose presence facts change could not be opened was not made, and
// says so as retryable (issue #798).
func TestPresenceRoute_UnannouncedWriteIsRefusedAsUnavailable(t *testing.T) {
	env := newPresenceRouteEnv(t)
	env.settings.err = fmt.Errorf("%w: valkey down", domain.ErrPresenceFactsUnavailable)
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		recorder := env.do(method, `{"state":"busy","expires_at":"2099-10-01T18:00:00Z"}`)
		if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "presence_unavailable" {
			t.Fatalf("%s = %d %s", method, recorder.Code, recorder.Body.String())
		}
	}
}
