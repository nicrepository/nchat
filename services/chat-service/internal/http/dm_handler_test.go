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
	candidates      []domain.DMCandidate
	searchErr       error
	createOutput    service.CreateDirectConversationOutput
	createErr       error
	lastSearchInput service.SearchDMCandidatesInput
	lastCreateInput service.CreateDirectConversationInput
	searchCalls     int
	createCalls     int
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
