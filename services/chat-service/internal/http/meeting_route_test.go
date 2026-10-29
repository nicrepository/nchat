package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const meetingRouteBody = `{"title":"Planejamento","description":"Pauta","starts_at":"2026-11-03T10:00:00-03:00","ends_at":"2026-11-03T11:00:00-03:00","timezone":"America/Sao_Paulo","conversation":{"kind":"channel","id":"33333333-3333-4333-8333-333333333333"},"participants":[{"user_id":"22222222-2222-4222-8222-222222222222","role":"cohost"}]}`
const meetingRouteID = "44444444-4444-4444-8444-444444444444"
const meetingRoutePath = "/api/chat/meetings/" + meetingRouteID

type meetingRouteProvider struct {
	calls   int
	create  service.CreateMeetingInput
	update  service.UpdateMeetingInput
	scope   storage.MeetingScope
	version int64
	m       domain.Meeting
	replay  bool
	err     error
}

func (p *meetingRouteProvider) CreateMeeting(_ context.Context, in service.CreateMeetingInput) (service.CreateMeetingResult, error) {
	p.calls++
	p.create = in
	return service.CreateMeetingResult{Meeting: p.m, Replayed: p.replay}, p.err
}
func (p *meetingRouteProvider) UpdateMeeting(_ context.Context, in service.UpdateMeetingInput) (domain.Meeting, error) {
	p.calls++
	p.update = in
	return p.m, p.err
}
func (p *meetingRouteProvider) CancelMeeting(_ context.Context, scope storage.MeetingScope, v int64) (domain.Meeting, error) {
	p.calls++
	p.scope = scope
	p.version = v
	return p.m, p.err
}

type meetingRouteEnv struct {
	router     http.Handler
	provider   *meetingRouteProvider
	limiter    *fakeDMRateLimiter
	workspaces *fakeWorkspaceResolver
	auth       string
}

func newMeetingRouteEnv(t *testing.T, sessions httpapi.SessionValidator) meetingRouteEnv {
	t.Helper()
	m, err := domain.NewMeeting(testWorkspaceID, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", nil, domain.MeetingDetails{
		Title: "Planejamento", StartsAt: time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC), Timezone: "America/Sao_Paulo",
	})
	if err != nil {
		t.Fatal(err)
	}
	m.ID = meetingRouteID
	m.CreatedAt = m.StartsAt
	m.UpdatedAt = m.StartsAt
	provider := &meetingRouteProvider{m: m}
	limiter := &fakeDMRateLimiter{}
	workspace := &fakeWorkspaceResolver{workspace: activeWorkspace()}
	handler := httpapi.NewMeetingHandler(workspace, provider, limiter)
	router := httpapi.NewRouter(sidebarTestConfig(), nil, httpapi.ReadinessState{}, makeTestValidator(t), sessions,
		httpapi.NewSidebarHandler(nil), httpapi.NewMessageHandler(nil, nil, nil), nil, nil, nil, nil, handler, nil)
	return meetingRouteEnv{router, provider, limiter, workspace, bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour))}
}
func (e meetingRouteEnv) request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", e.auth)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "meeting-intent")
	r.Header.Set("If-Match", `"1"`)
	return r
}
func (e meetingRouteEnv) serve(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, r)
	return w
}

func TestMeetingRoutesSuccess(t *testing.T) {
	env := newMeetingRouteEnv(t, allowAllSessionValidator{})
	env.provider.m.Conversation = &domain.MeetingConversation{Kind: domain.MeetingConversationChannel, ID: meetingRouteID}
	created := env.serve(env.request(http.MethodPost, httpapi.RouteMeetings, meetingRouteBody))
	if created.Code != 201 || created.Header().Get("ETag") != `"1"` || created.Header().Get("Location") != meetingRoutePath {
		t.Fatalf("create = %d %v %s", created.Code, created.Header(), created.Body.String())
	}
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body.Data["room_key"]; ok {
		t.Fatal("internal room key exposed")
	}
	if created.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private response is cacheable")
	}
}

func TestMeetingRoutesCreateCommand(t *testing.T) {
	env := newMeetingRouteEnv(t, allowAllSessionValidator{})
	env.serve(env.request(http.MethodPost, httpapi.RouteMeetings, meetingRouteBody))
	input := env.provider.create
	if input.ActorID != testUserID || input.WorkspaceID != testWorkspaceID || input.IdempotencyKey != "meeting-intent" || len(input.Participants) != 1 || input.Conversation.Kind != domain.MeetingConversationChannel {
		t.Fatalf("command = %+v", input)
	}
	if input.Details.StartsAt.Format(time.RFC3339) != "2026-11-03T10:00:00-03:00" {
		t.Fatal(input.Details.StartsAt)
	}
}

func TestMeetingRoutesReplayResponse(t *testing.T) {
	env := newMeetingRouteEnv(t, allowAllSessionValidator{})
	created := env.serve(env.request(http.MethodPost, httpapi.RouteMeetings, meetingRouteBody))
	env.provider.replay = true
	replay := env.serve(env.request(http.MethodPost, httpapi.RouteMeetings, meetingRouteBody))
	if replay.Code != 200 || replay.Header().Get("Idempotency-Replayed") != "true" || replay.Body.String() != created.Body.String() {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
}

func TestMeetingRoutesMutationCommands(t *testing.T) {
	env := newMeetingRouteEnv(t, allowAllSessionValidator{})
	env.provider.m.Version = 2
	update := env.serve(env.request(http.MethodPatch, meetingRoutePath, `{"description":""}`))
	if update.Code != 200 || update.Header().Get("ETag") != `"2"` {
		t.Fatalf("update = %d %s", update.Code, update.Body.String())
	}
	input := env.provider.update
	if input.Scope.MeetingID != meetingRouteID || input.Scope.ActorID != testUserID || input.ExpectedVersion != 1 || input.Patch.Description == nil || *input.Patch.Description != "" || input.Patch.Title != nil {
		t.Fatalf("patch command = %+v", input)
	}
}

func TestMeetingRoutesCancelCommand(t *testing.T) {
	env := newMeetingRouteEnv(t, allowAllSessionValidator{})
	env.provider.m.Version = 2
	env.provider.m.Status = domain.MeetingStatusCancelled
	cancel := env.serve(env.request(http.MethodPost, meetingRoutePath+"/cancel", `{}`))
	if cancel.Code != 200 || env.provider.version != 1 || env.provider.scope.WorkspaceID != testWorkspaceID {
		t.Fatalf("cancel = %d %s", cancel.Code, cancel.Body.String())
	}
}

func TestMeetingRoutesRequireAuthentication(t *testing.T) {
	routes := []struct{ method, path, body string }{{"POST", httpapi.RouteMeetings, meetingRouteBody}, {"PATCH", meetingRoutePath, `{}`}, {"POST", meetingRoutePath + "/cancel", `{}`}}
	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			env := newMeetingRouteEnv(t, allowAllSessionValidator{})
			r := env.request(route.method, route.path, route.body)
			r.Header.Del("Authorization")
			w := env.serve(r)
			if w.Code != 401 || env.provider.calls != 0 {
				t.Fatalf("unauthenticated = %d calls %d", w.Code, env.provider.calls)
			}
		})
	}
	env := newMeetingRouteEnv(t, denySessionValidator{err: domain.ErrInvalidToken})
	if w := env.serve(env.request("POST", httpapi.RouteMeetings, meetingRouteBody)); w.Code != 401 {
		t.Fatalf("revoked session = %d", w.Code)
	}
}

func assertMeetingRouteRejected(t *testing.T, env meetingRouteEnv, r *http.Request, status int) {
	t.Helper()
	w := env.serve(r)
	if w.Code != status || env.provider.calls != 0 {
		t.Fatalf("rejected request = %d, want %d, calls %d: %s", w.Code, status, env.provider.calls, w.Body.String())
	}
}

func TestMeetingRoutesRejectMassAssignment(t *testing.T) {
	fields := []string{`"organizer_user_id":"forged"`, `"actor_user_id":"forged"`, `"created_by":"forged"`, `"room_key":"forged"`, `"status":"ended"`, `"version":9`, `"created_at":"2026-01-01T00:00:00Z"`, `"updated_at":"2026-01-01T00:00:00Z"`, `"workspace_id":"foreign"`, `"id":"forged"`}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			env := newMeetingRouteEnv(t, allowAllSessionValidator{})
			for _, method := range []string{"POST", "PATCH"} {
				path := httpapi.RouteMeetings
				if method == "PATCH" {
					path = meetingRoutePath
				}
				assertMeetingRouteRejected(t, env, env.request(method, path, `{`+field+`}`), 400)
			}
		})
	}
	env := newMeetingRouteEnv(t, allowAllSessionValidator{})
	for _, field := range []string{`"conversation":{}`, `"participants":[]`} {
		assertMeetingRouteRejected(t, env, env.request("PATCH", meetingRoutePath, `{`+field+`}`), 400)
	}
}

func TestMeetingRoutesRejectInvalidPayloads(t *testing.T) {
	for _, tt := range []struct{ name, method, path, body string }{
		{"malformed", "POST", httpapi.RouteMeetings, `{`},
		{"trailing JSON", "POST", httpapi.RouteMeetings, meetingRouteBody + `{}`},
		{"oversize", "POST", httpapi.RouteMeetings, `{"description":"` + strings.Repeat("x", 1<<16) + `"}`},
		{"local time", "POST", httpapi.RouteMeetings, `{"starts_at":"2026-11-03T10:00:00"}`},
		{"invalid time", "POST", httpapi.RouteMeetings, `{"starts_at":"bad"}`},
		{"participant unknown field", "POST", httpapi.RouteMeetings, `{"participants":[{"user_id":"x","rsvp":"accepted"}]}`},
		{"conversation unknown field", "POST", httpapi.RouteMeetings, `{"conversation":{"id":"x","workspace_id":"y"}}`},
		{"null patch", "PATCH", meetingRoutePath, `null`},
		{"wrong patch field type", "PATCH", meetingRoutePath, `{"title":false}`},
		{"invalid patch time", "PATCH", meetingRoutePath, `{"starts_at":"bad"}`},
		{"cancel field", "POST", meetingRoutePath + "/cancel", `{"status":"ended"}`},
		{"null cancel", "POST", meetingRoutePath + "/cancel", `null`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := newMeetingRouteEnv(t, allowAllSessionValidator{})
			w := env.serve(env.request(tt.method, tt.path, tt.body))
			if w.Code != 400 || env.provider.calls != 0 {
				t.Fatalf("invalid request = %d calls %d", w.Code, env.provider.calls)
			}
		})
	}
	for _, field := range []string{"title", "description", "starts_at", "ends_at", "timezone", "join_policy"} {
		t.Run(field+" null", func(t *testing.T) {
			env := newMeetingRouteEnv(t, allowAllSessionValidator{})
			w := env.serve(env.request("PATCH", meetingRoutePath, `{"`+field+`":null}`))
			if w.Code != 400 || env.provider.calls != 0 {
				t.Fatalf("null field = %d", w.Code)
			}
		})
	}
}

func TestMeetingRoutesHeaderValidation(t *testing.T) {
	cases := []struct {
		name, method, path, header, value string
		status                            int
	}{
		{"missing key", "POST", httpapi.RouteMeetings, "Idempotency-Key", "", 400},
		{"long key", "POST", httpapi.RouteMeetings, "Idempotency-Key", strings.Repeat("x", 129), 400},
		{"control in key", "POST", httpapi.RouteMeetings, "Idempotency-Key", "\tkey", 400},
		{"invalid key", "POST", httpapi.RouteMeetings, "Idempotency-Key", "a b", 400},
		{"missing precondition", "PATCH", meetingRoutePath, "If-Match", "", 428},
		{"missing cancel precondition", "POST", meetingRoutePath + "/cancel", "If-Match", "", 428},
		{"content type", "POST", httpapi.RouteMeetings, "Content-Type", "text/plain", 415},
	}
	for _, v := range []string{`1`, `"0"`, `"-1"`, `W/"1"`, `*`, `"1", "2"`, `"01"`, `"9223372036854775808"`} {
		cases = append(cases, struct {
			name, method, path, header, value string
			status                            int
		}{v, "PATCH", meetingRoutePath, "If-Match", v, 400})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			env := newMeetingRouteEnv(t, allowAllSessionValidator{})
			r := env.request(tt.method, tt.path, `{}`)
			r.Header.Del(tt.header)
			if tt.value != "" {
				r.Header.Set(tt.header, tt.value)
			}
			assertMeetingRouteRejected(t, env, r, tt.status)
		})
	}
}

func TestMeetingRoutesRejectRepeatedHeaders(t *testing.T) {
	for _, header := range []string{"Idempotency-Key", "If-Match"} {
		t.Run("repeated "+header, func(t *testing.T) {
			env := newMeetingRouteEnv(t, allowAllSessionValidator{})
			method, path := "POST", httpapi.RouteMeetings
			if header == "If-Match" {
				method, path = "PATCH", meetingRoutePath
			}
			r := env.request(method, path, `{}`)
			r.Header.Add(header, `"2"`)
			if w := env.serve(r); w.Code != 400 {
				t.Fatalf("repeated header = %d", w.Code)
			}
		})
	}
}

func TestMeetingRoutesMapErrorsWithoutPrivateDetails(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrNotFound, 404, "not_found"}, {domain.ErrForbidden, 403, "forbidden"},
		{domain.ErrMeetingParticipantNotEligible, 400, "bad_request"}, {domain.ErrMeetingStaleVersion, 412, "stale_version"},
		{domain.ErrIdempotencyKeyReused, 409, "idempotency_key_reused"}, {domain.ErrMeetingInvalidState, 409, "conflict"},
		{errors.New("private database value"), 500, "internal_error"},
	} {
		t.Run(tt.code, func(t *testing.T) {
			env := newMeetingRouteEnv(t, allowAllSessionValidator{})
			env.provider.err = tt.err
			w := env.serve(env.request("POST", httpapi.RouteMeetings, meetingRouteBody))
			if w.Code != tt.status || errorCode(t, w) != tt.code || strings.Contains(w.Body.String(), "private database value") {
				t.Fatalf("error = %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMeetingRoutesRateLimitAndWorkspaceFailures(t *testing.T) {
	env := newMeetingRouteEnv(t, allowAllSessionValidator{})
	env.limiter.counts = map[string]int{"meeting_create:" + testUserID: 10}
	w := env.serve(env.request("POST", httpapi.RouteMeetings, meetingRouteBody))
	if w.Code != 429 || w.Header().Get("Retry-After") != "60" || env.provider.calls != 0 {
		t.Fatalf("limit = %d", w.Code)
	}
	env.limiter.err = errors.New("limiter unavailable")
	if w = env.serve(env.request("PATCH", meetingRoutePath, `{}`)); w.Code != 503 {
		t.Fatalf("limiter failure = %d", w.Code)
	}
	env.limiter.err = nil
	env.workspaces.err = domain.ErrNotFound
	if w = env.serve(env.request("PATCH", meetingRoutePath, `{}`)); w.Code != 404 {
		t.Fatalf("workspace failure = %d", w.Code)
	}
	env.workspaces.err = errors.New("database unavailable")
	if w = env.serve(env.request("PATCH", meetingRoutePath, `{}`)); w.Code != 500 {
		t.Fatalf("workspace failure = %d", w.Code)
	}
}

func TestMeetingRoutesRejectInvalidResourceIDsAndWorkspaceSelection(t *testing.T) {
	env := newMeetingRouteEnv(t, allowAllSessionValidator{})
	for _, id := range []string{"bad", "00000000-0000-0000-0000-000000000000"} {
		if w := env.serve(env.request("PATCH", "/api/chat/meetings/"+id, `{}`)); w.Code != 400 {
			t.Fatalf("bad id = %d", w.Code)
		}
	}
	w := env.serve(env.request("POST", httpapi.RouteMeetings+"?workspace_id=foreign", meetingRouteBody))
	if w.Code != 201 || env.provider.create.WorkspaceID != testWorkspaceID {
		t.Fatalf("client selected workspace: %+v", env.provider.create)
	}
	w = env.serve(env.request("POST", "/api/chat/workspaces/foreign/meetings", meetingRouteBody))
	if w.Code != 404 {
		t.Fatalf("unexpected workspace route = %d", w.Code)
	}
}

func TestMeetingRoutesOptionalCreateFields(t *testing.T) {
	env := newMeetingRouteEnv(t, allowAllSessionValidator{})
	body := `{"title":"Planejamento","starts_at":"2026-11-03T13:00:00Z","ends_at":"2026-11-03T14:00:00Z","timezone":"UTC"}`
	w := env.serve(env.request("POST", httpapi.RouteMeetings, body))
	if w.Code != 201 || env.provider.create.Conversation != nil || len(env.provider.create.Participants) != 0 {
		t.Fatalf("optional fields = %+v, %d", env.provider.create, w.Code)
	}
}

func TestMeetingHandlerRequiresWiring(t *testing.T) {
	w := httptest.NewRecorder()
	httpapi.NewMeetingHandler(nil, nil, nil).Create(w, httptest.NewRequest("POST", httpapi.RouteMeetings, nil))
	if w.Code != 503 {
		t.Fatalf("unwired handler = %d", w.Code)
	}
}

func TestMeetingRoutesMutationErrors(t *testing.T) {
	for _, route := range []struct{ method, path string }{{"PATCH", meetingRoutePath}, {"POST", meetingRoutePath + "/cancel"}} {
		t.Run(route.path, func(t *testing.T) {
			env := newMeetingRouteEnv(t, allowAllSessionValidator{})
			env.provider.err = domain.ErrMeetingStaleVersion
			w := env.serve(env.request(route.method, route.path, `{}`))
			if w.Code != 412 || errorCode(t, w) != "stale_version" {
				t.Fatalf("mutation error = %d %s", w.Code, w.Body.String())
			}
		})
	}
}
