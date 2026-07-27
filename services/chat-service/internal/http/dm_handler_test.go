package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

const (
	dmOtherUserID    = "55555555-5555-5555-5555-555555555555"
	dmConversationID = "66666666-6666-6666-6666-666666666666"
)

type fakeDMProvider struct {
	candidates       []domain.DMCandidate
	searchErr        error
	createOutput     service.CreateDirectConversationOutput
	createErr        error
	groupOutput      domain.DMConversation
	groupErr         error
	lastSearchInput  service.SearchDMCandidatesInput
	lastCreateInput  service.CreateDirectConversationInput
	lastGroupInput   service.CreateGroupConversationInput
	searchCalls      int
	createCalls      int
	groupCreateCalls int
}

type fakeDMRateLimiter struct {
	mu     sync.Mutex
	counts map[string]int
	err    error
}

func (f *fakeDMRateLimiter) AllowActionWithLimit(_ context.Context, userID, action string, maxActions, _ int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	if f.counts == nil {
		f.counts = make(map[string]int)
	}
	key := action + ":" + userID
	f.counts[key]++
	return f.counts[key] <= maxActions, nil
}

func (f *fakeDMProvider) SearchDMCandidates(_ context.Context, input service.SearchDMCandidatesInput) ([]domain.DMCandidate, error) {
	f.searchCalls++
	f.lastSearchInput = input
	return f.candidates, f.searchErr
}

func (f *fakeDMProvider) GetOrCreateDirectConversation(_ context.Context, input service.CreateDirectConversationInput) (service.CreateDirectConversationOutput, error) {
	f.createCalls++
	f.lastCreateInput = input
	return f.createOutput, f.createErr
}

func (f *fakeDMProvider) CreateGroupConversation(_ context.Context, input service.CreateGroupConversationInput) (domain.DMConversation, error) {
	f.groupCreateCalls++
	f.lastGroupInput = input
	return f.groupOutput, f.groupErr
}

func dmTestHandler(provider *fakeDMProvider) *httpapi.DMHandler {
	return dmTestHandlerWithLimiter(provider, &fakeDMRateLimiter{})
}

func dmTestHandlerWithLimiter(provider *fakeDMProvider, limiter *fakeDMRateLimiter) *httpapi.DMHandler {
	return httpapi.NewDMHandler(&fakeWorkspaceResolver{workspace: domain.Workspace{ID: testWorkspaceID}}, provider, limiter)
}

func TestDMHandler_SearchCandidates_RequiresAuthenticatedUser(t *testing.T) {
	handler := dmTestHandler(&fakeDMProvider{})
	recorder := httptest.NewRecorder()
	handler.SearchCandidates(recorder, httptest.NewRequest(http.MethodGet, httpapi.RouteDMCandidates+"?query=an", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestDMHandler_SearchCandidates_ValidatesLimit(t *testing.T) {
	for _, limit := range []string{"zero", "0", "-1"} {
		t.Run(limit, func(t *testing.T) {
			provider := &fakeDMProvider{}
			recorder := httptest.NewRecorder()
			dmTestHandler(provider).SearchCandidates(recorder, requestWithUser(http.MethodGet, httpapi.RouteDMCandidates+"?query=an&limit="+limit, nil))
			if recorder.Code != http.StatusBadRequest || provider.searchCalls != 0 {
				t.Fatalf("status=%d calls=%d", recorder.Code, provider.searchCalls)
			}
		})
	}
}

func TestDMHandler_SearchCandidates_ForwardsServerIdentityAndReturnsMinimalResults(t *testing.T) {
	provider := &fakeDMProvider{candidates: []domain.DMCandidate{
		{UserID: dmOtherUserID, DisplayName: "Ana"},
		{UserID: "77777777-7777-7777-7777-777777777777", DisplayName: "André"},
	}}
	recorder := httptest.NewRecorder()
	dmTestHandler(provider).SearchCandidates(recorder, requestWithUser(http.MethodGet, httpapi.RouteDMCandidates+"?query=an&limit=999", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if provider.lastSearchInput.WorkspaceID != testWorkspaceID || provider.lastSearchInput.CallerID != msgTestUserID || provider.lastSearchInput.Query != "an" || provider.lastSearchInput.Limit != 999 {
		t.Fatalf("unexpected input: %+v", provider.lastSearchInput)
	}
	body := recorder.Body.String()
	for _, want := range []string{`"user_id":"` + dmOtherUserID + `"`, `"display_name":"Ana"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response %q missing %q", body, want)
		}
	}
	for _, forbidden := range []string{"email", "password", "provider", "session", "role", "workspace_id"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response exposes %q: %s", forbidden, body)
		}
	}
}

func TestDMHandler_SearchCandidates_ReturnsEmptyArray(t *testing.T) {
	recorder := httptest.NewRecorder()
	dmTestHandler(&fakeDMProvider{}).SearchCandidates(recorder, requestWithUser(http.MethodGet, httpapi.RouteDMCandidates+"?query=zz", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"candidates":[]`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDMHandler_SearchCandidates_MapsWorkspaceResolutionErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "not found", err: domain.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "internal", err: errors.New("database host secret"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeDMProvider{}
			handler := httpapi.NewDMHandler(&fakeWorkspaceResolver{err: test.err}, provider, &fakeDMRateLimiter{})
			recorder := httptest.NewRecorder()
			handler.SearchCandidates(recorder, requestWithUser(http.MethodGet, httpapi.RouteDMCandidates+"?query=an", nil))
			if recorder.Code != test.wantStatus || provider.searchCalls != 0 || strings.Contains(recorder.Body.String(), "database host secret") {
				t.Fatalf("status=%d calls=%d body=%s", recorder.Code, provider.searchCalls, recorder.Body.String())
			}
		})
	}
}

func TestDMHandler_SearchCandidates_MapsValidationAuthorizationAndInternalErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid query", err: domain.ErrInvalidInput, want: http.StatusBadRequest},
		{name: "inactive caller", err: domain.ErrForbidden, want: http.StatusForbidden},
		{name: "store failure", err: errors.New("database host secret"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			dmTestHandler(&fakeDMProvider{searchErr: test.err}).SearchCandidates(recorder, requestWithUser(http.MethodGet, httpapi.RouteDMCandidates+"?query=an", nil))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			if strings.Contains(recorder.Body.String(), "database host secret") {
				t.Fatalf("internal error leaked: %s", recorder.Body.String())
			}
		})
	}
}

func TestDMHandler_GetOrCreateDirect_RequiresAuthenticationAndJSONContentType(t *testing.T) {
	handler := dmTestHandler(&fakeDMProvider{})

	recorder := httptest.NewRecorder()
	handler.GetOrCreateDirect(recorder, httptest.NewRequest(http.MethodPost, httpapi.RouteDMConversations, strings.NewReader(`{"other_user_id":"`+dmOtherUserID+`"}`)))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.GetOrCreateDirect(recorder, requestWithUser(http.MethodPost, httpapi.RouteDMConversations, strings.NewReader(`{"other_user_id":"`+dmOtherUserID+`"}`)))
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content-type status = %d", recorder.Code)
	}
}

// The browser must send a media type this endpoint parses as application/json.
// A duplicated header ("application/json, application/json") is what a client
// that merges its content type twice ends up sending, and it is a rejection —
// the accepted forms stay exactly application/json, with or without parameters.
func TestDMHandler_GetOrCreateDirect_AcceptsOnlyJSONMediaType(t *testing.T) {
	body := `{"other_user_id":"` + dmOtherUserID + `"}`
	tests := []struct {
		name        string
		contentType string
		want        int
	}{
		{name: "json", contentType: "application/json", want: http.StatusOK},
		{name: "json with charset", contentType: "application/json; charset=utf-8", want: http.StatusOK},
		{name: "missing", contentType: "", want: http.StatusUnsupportedMediaType},
		{name: "text plain", contentType: "text/plain", want: http.StatusUnsupportedMediaType},
		{name: "octet stream", contentType: "application/octet-stream", want: http.StatusUnsupportedMediaType},
		{name: "duplicated json", contentType: "application/json, application/json", want: http.StatusUnsupportedMediaType},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeDMProvider{createOutput: service.CreateDirectConversationOutput{
				Conversation: domain.DMConversation{ID: dmConversationID}, Created: true,
			}}
			request := requestWithUser(http.MethodPost, httpapi.RouteDMConversations, strings.NewReader(body))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			recorder := httptest.NewRecorder()
			dmTestHandler(provider).GetOrCreateDirect(recorder, request)

			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d (body=%s)", recorder.Code, test.want, recorder.Body.String())
			}
			wantCalls := 0
			if test.want == http.StatusOK {
				wantCalls = 1
			}
			if provider.createCalls != wantCalls {
				t.Fatalf("createCalls = %d, want %d", provider.createCalls, wantCalls)
			}
		})
	}
}

func TestDMHandler_GetOrCreateDirect_RejectsMalformedUnknownAndInvalidUUID(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{`},
		{name: "missing other user", body: `{}`},
		{name: "unknown", body: `{"other_user_id":"` + dmOtherUserID + `","role":"owner"}`},
		{name: "caller injection", body: `{"other_user_id":"` + dmOtherUserID + `","current_user_id":"` + dmOtherUserID + `"}`},
		{name: "invalid uuid", body: `{"other_user_id":"not-a-uuid"}`},
		{name: "nil uuid", body: `{"other_user_id":"00000000-0000-0000-0000-000000000000"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeDMProvider{}
			request := requestWithUser(http.MethodPost, httpapi.RouteDMConversations, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			dmTestHandler(provider).GetOrCreateDirect(recorder, request)
			if recorder.Code != http.StatusBadRequest || provider.createCalls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", recorder.Code, provider.createCalls, recorder.Body.String())
			}
		})
	}
}

func TestDMHandler_GetOrCreateDirect_RequiresIdentityInContext(t *testing.T) {
	provider := &fakeDMProvider{}
	request := httptest.NewRequest(http.MethodPost, httpapi.RouteDMConversations, strings.NewReader(`{"other_user_id":"`+dmOtherUserID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	dmTestHandler(provider).GetOrCreateDirect(recorder, request)
	if recorder.Code != http.StatusUnauthorized || provider.createCalls != 0 {
		t.Fatalf("status=%d calls=%d", recorder.Code, provider.createCalls)
	}
}

func TestDMHandler_RateLimitIsSharedAndScopedByOperation(t *testing.T) {
	limiter := &fakeDMRateLimiter{}
	firstProvider, secondProvider := &fakeDMProvider{}, &fakeDMProvider{}
	first := dmTestHandlerWithLimiter(firstProvider, limiter)
	second := dmTestHandlerWithLimiter(secondProvider, limiter)
	for range 30 {
		response := httptest.NewRecorder()
		first.SearchCandidates(response, requestWithUser(http.MethodGet, httpapi.RouteDMCandidates+"?query=an", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("search within budget status=%d", response.Code)
		}
	}
	response := httptest.NewRecorder()
	second.SearchCandidates(response, requestWithUser(http.MethodGet, httpapi.RouteDMCandidates+"?query=an", nil))
	if response.Code != http.StatusTooManyRequests || secondProvider.searchCalls != 0 || response.Header().Get("Retry-After") != "60" {
		t.Fatalf("shared search status=%d calls=%d retry=%q", response.Code, secondProvider.searchCalls, response.Header().Get("Retry-After"))
	}

	for range 10 {
		request := requestWithUser(http.MethodPost, httpapi.RouteDMConversations, strings.NewReader(`{"other_user_id":"`+dmOtherUserID+`"}`))
		request.Header.Set("Content-Type", "application/json")
		response = httptest.NewRecorder()
		second.GetOrCreateDirect(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("create within budget status=%d", response.Code)
		}
	}
	request := requestWithUser(http.MethodPost, httpapi.RouteDMConversations, strings.NewReader(`{"other_user_id":"`+dmOtherUserID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	second.GetOrCreateDirect(response, request)
	if response.Code != http.StatusTooManyRequests || secondProvider.createCalls != 10 {
		t.Fatalf("create over budget status=%d calls=%d", response.Code, secondProvider.createCalls)
	}
}

func TestDMHandler_RateLimiterFailureIsFailClosed(t *testing.T) {
	provider := &fakeDMProvider{}
	handler := dmTestHandlerWithLimiter(provider, &fakeDMRateLimiter{err: errors.New("valkey address secret")})
	recorder := httptest.NewRecorder()
	handler.SearchCandidates(recorder, requestWithUser(http.MethodGet, httpapi.RouteDMCandidates+"?query=an", nil))
	if recorder.Code != http.StatusServiceUnavailable || provider.searchCalls != 0 || strings.Contains(recorder.Body.String(), "valkey") {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, provider.searchCalls, recorder.Body.String())
	}
}

func TestDMHandler_GetOrCreateDirect_ReturnsCreatedAndExistingConversation(t *testing.T) {
	for _, created := range []bool{true, false} {
		t.Run(map[bool]string{true: "created", false: "existing"}[created], func(t *testing.T) {
			provider := &fakeDMProvider{createOutput: service.CreateDirectConversationOutput{
				Conversation: domain.DMConversation{ID: dmConversationID}, Created: created,
			}}
			request := requestWithUser(http.MethodPost, httpapi.RouteDMConversations, strings.NewReader(`{"other_user_id":"`+dmOtherUserID+`"}`))
			request.Header.Set("Content-Type", "application/json; charset=utf-8")
			recorder := httptest.NewRecorder()
			dmTestHandler(provider).GetOrCreateDirect(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if provider.lastCreateInput != (service.CreateDirectConversationInput{WorkspaceID: testWorkspaceID, CallerID: msgTestUserID, OtherUserID: dmOtherUserID}) {
				t.Fatalf("unexpected input: %+v", provider.lastCreateInput)
			}
			body := recorder.Body.String()
			if !strings.Contains(body, `"conversation_id":"`+dmConversationID+`"`) || !strings.Contains(body, `"created":`+map[bool]string{true: "true", false: "false"}[created]) {
				t.Fatalf("unexpected body: %s", body)
			}
		})
	}
}

func TestDMHandler_GetOrCreateDirect_MapsSelfTargetAndInternalErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "self dm", err: domain.ErrInvalidInput, wantStatus: http.StatusBadRequest},
		{name: "ineligible target", err: domain.ErrForbidden, wantStatus: http.StatusNotFound},
		{name: "hidden target", err: domain.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "internal", err: errors.New("postgres constraint detail"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := requestWithUser(http.MethodPost, httpapi.RouteDMConversations, strings.NewReader(`{"other_user_id":"`+dmOtherUserID+`"}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			dmTestHandler(&fakeDMProvider{createErr: test.err}).GetOrCreateDirect(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d", recorder.Code, test.wantStatus)
			}
			if strings.Contains(recorder.Body.String(), "constraint detail") {
				t.Fatalf("internal detail leaked: %s", recorder.Body.String())
			}
		})
	}
}

func TestDMHandler_NilDependenciesReturn503(t *testing.T) {
	recorder := httptest.NewRecorder()
	httpapi.NewDMHandler(nil, nil, nil).SearchCandidates(recorder, requestWithUser(http.MethodGet, httpapi.RouteDMCandidates+"?query=an", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

// ── Ad-hoc group DM creation (RF-02) ──────────────────────────────────────────

const dmSecondUserID = "88888888-8888-8888-8888-888888888888"

func groupRequest(body string) *http.Request {
	request := requestWithUser(http.MethodPost, httpapi.RouteDMGroupConversations, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestDMHandler_CreateGroup_RequiresAuthenticationAndJSONContentType(t *testing.T) {
	handler := dmTestHandler(&fakeDMProvider{})
	body := `{"participant_user_ids":["` + dmOtherUserID + `","` + dmSecondUserID + `"]}`

	recorder := httptest.NewRecorder()
	handler.CreateGroup(recorder, httptest.NewRequest(http.MethodPost, httpapi.RouteDMGroupConversations, strings.NewReader(body)))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.CreateGroup(recorder, requestWithUser(http.MethodPost, httpapi.RouteDMGroupConversations, strings.NewReader(body)))
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content-type status = %d", recorder.Code)
	}
}

// Anything beyond participant_user_ids and title must be refused: accepting a
// client-supplied workspace, caller or membership field would move authorization
// into the browser.
func TestDMHandler_CreateGroup_RejectsMalformedAndInjectedFields(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{`},
		{name: "workspace injection", body: `{"participant_user_ids":["` + dmOtherUserID + `"],"workspace_id":"` + testWorkspaceID + `"}`},
		{name: "caller injection", body: `{"participant_user_ids":["` + dmOtherUserID + `"],"caller_id":"` + dmOtherUserID + `"}`},
		{name: "created_by injection", body: `{"participant_user_ids":["` + dmOtherUserID + `"],"created_by":"` + dmOtherUserID + `"}`},
		{name: "role injection", body: `{"participant_user_ids":["` + dmOtherUserID + `"],"role":"owner"}`},
		{name: "trailing json", body: `{"participant_user_ids":[]}{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeDMProvider{}
			recorder := httptest.NewRecorder()
			dmTestHandler(provider).CreateGroup(recorder, groupRequest(test.body))
			if recorder.Code != http.StatusBadRequest || provider.groupCreateCalls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", recorder.Code, provider.groupCreateCalls, recorder.Body.String())
			}
		})
	}
}

func TestDMHandler_CreateGroup_ForwardsServerIdentityAndReturnsOnlyConversationID(t *testing.T) {
	provider := &fakeDMProvider{groupOutput: domain.DMConversation{
		ID: dmConversationID, WorkspaceID: testWorkspaceID, CreatedBy: msgTestUserID, Title: "Infra",
	}}
	recorder := httptest.NewRecorder()
	dmTestHandler(provider).CreateGroup(recorder, groupRequest(
		`{"participant_user_ids":["`+dmOtherUserID+`","`+dmSecondUserID+`"],"title":"Infra"}`,
	))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	input := provider.lastGroupInput
	if input.WorkspaceID != testWorkspaceID || input.CallerID != msgTestUserID || input.Title != "Infra" {
		t.Fatalf("unexpected input: %+v", input)
	}
	if len(input.ParticipantUserIDs) != 2 || input.ParticipantUserIDs[0] != dmOtherUserID || input.ParticipantUserIDs[1] != dmSecondUserID {
		t.Fatalf("unexpected participants: %+v", input.ParticipantUserIDs)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"conversation_id":"`+dmConversationID+`"`) {
		t.Fatalf("missing conversation id: %s", body)
	}
	for _, forbidden := range []string{"workspace_id", "created_by", "participant", "email", "title"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response exposes %q: %s", forbidden, body)
		}
	}
}

// The title is optional at the transport layer; the service owns trimming and
// the length limit, so an absent field must reach it as an empty string.
func TestDMHandler_CreateGroup_AcceptsAbsentTitle(t *testing.T) {
	provider := &fakeDMProvider{groupOutput: domain.DMConversation{ID: dmConversationID}}
	recorder := httptest.NewRecorder()
	dmTestHandler(provider).CreateGroup(recorder, groupRequest(
		`{"participant_user_ids":["`+dmOtherUserID+`","`+dmSecondUserID+`"]}`,
	))
	if recorder.Code != http.StatusCreated || provider.lastGroupInput.Title != "" {
		t.Fatalf("status=%d title=%q", recorder.Code, provider.lastGroupInput.Title)
	}
}

func TestDMHandler_CreateGroup_MapsValidationAuthorizationAndInternalErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "too few participants", err: domain.ErrInvalidInput, wantStatus: http.StatusBadRequest},
		{name: "ineligible participant", err: domain.ErrForbidden, wantStatus: http.StatusNotFound},
		{name: "hidden participant", err: domain.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "internal", err: errors.New("postgres constraint detail"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			dmTestHandler(&fakeDMProvider{groupErr: test.err}).CreateGroup(recorder, groupRequest(
				`{"participant_user_ids":["`+dmOtherUserID+`","`+dmSecondUserID+`"]}`,
			))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d", recorder.Code, test.wantStatus)
			}
			if strings.Contains(recorder.Body.String(), "constraint detail") {
				t.Fatalf("internal detail leaked: %s", recorder.Body.String())
			}
		})
	}
}

// An ineligible participant — suspended, deleted, unknown or from another
// workspace — must produce one indistinguishable answer that says nothing about
// the account or about which participant was rejected.
func TestDMHandler_CreateGroup_IneligibleParticipantResponseIsOpaque(t *testing.T) {
	recorder := httptest.NewRecorder()
	dmTestHandler(&fakeDMProvider{groupErr: domain.ErrForbidden}).CreateGroup(recorder, groupRequest(
		`{"participant_user_ids":["`+dmOtherUserID+`","`+dmSecondUserID+`"]}`,
	))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{
		"status", "deleted_at", "suspended", "inactive", "workspace_id", "email",
		dmOtherUserID, dmSecondUserID, msgTestUserID, testWorkspaceID,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaks %q: %s", forbidden, body)
		}
	}
}

// A burst of group creations must be stopped by its own budget, and stopping it
// must not consume the direct-DM budget.
func TestDMHandler_CreateGroup_RateLimitIsIndependentFromDirectCreate(t *testing.T) {
	limiter := &fakeDMRateLimiter{}
	provider := &fakeDMProvider{groupOutput: domain.DMConversation{ID: dmConversationID}}
	handler := dmTestHandlerWithLimiter(provider, limiter)
	body := `{"participant_user_ids":["` + dmOtherUserID + `","` + dmSecondUserID + `"]}`

	for range 5 {
		recorder := httptest.NewRecorder()
		handler.CreateGroup(recorder, groupRequest(body))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("within budget status=%d", recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	handler.CreateGroup(recorder, groupRequest(body))
	if recorder.Code != http.StatusTooManyRequests || provider.groupCreateCalls != 5 || recorder.Header().Get("Retry-After") != "60" {
		t.Fatalf("over budget status=%d calls=%d retry=%q", recorder.Code, provider.groupCreateCalls, recorder.Header().Get("Retry-After"))
	}

	directRequest := requestWithUser(http.MethodPost, httpapi.RouteDMConversations, strings.NewReader(`{"other_user_id":"`+dmOtherUserID+`"}`))
	directRequest.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.GetOrCreateDirect(recorder, directRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("direct create status=%d after group budget exhaustion", recorder.Code)
	}
}
