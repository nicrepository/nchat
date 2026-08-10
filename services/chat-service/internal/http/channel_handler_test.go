package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

const createdChannelID = "77777777-7777-7777-7777-777777777777"

type fakeChannelProvider struct {
	channel   domain.Channel
	err       error
	lastInput service.CreateChannelInput
	calls     int

	details          service.ChannelDetails
	detailsErr       error
	lastDetailsInput service.ChannelDetailsInput
	detailsCalls     int
}

func (f *fakeChannelProvider) CreateChannel(_ context.Context, input service.CreateChannelInput) (domain.Channel, error) {
	f.calls++
	f.lastInput = input
	return f.channel, f.err
}

func (f *fakeChannelProvider) GetChannelDetails(
	_ context.Context, input service.ChannelDetailsInput,
) (service.ChannelDetails, error) {
	f.detailsCalls++
	f.lastDetailsInput = input
	return f.details, f.detailsErr
}

func channelTestHandler(provider *fakeChannelProvider) *httpapi.ChannelHandler {
	return httpapi.NewChannelHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, &fakeDMRateLimiter{},
	)
}

func createChannelRequest(body string) *http.Request {
	r := requestWithUser(http.MethodPost, httpapi.RouteChannels, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

const validCreateBody = `{"slug":"infra","display_name":"Infraestrutura","type":"public"}`

func TestChannelHandler_Create_RequiresAuthenticatedUser(t *testing.T) {
	provider := &fakeChannelProvider{}
	request := httptest.NewRequest(http.MethodPost, httpapi.RouteChannels, strings.NewReader(validCreateBody))
	request.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	channelTestHandler(provider).Create(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if provider.calls != 0 {
		t.Fatalf("service called %d times for an unauthenticated request", provider.calls)
	}
}

// The caller and the workspace must come from the session, never from the body.
func TestChannelHandler_Create_DerivesCallerAndWorkspaceServerSide(t *testing.T) {
	provider := &fakeChannelProvider{channel: domain.Channel{
		ID: createdChannelID, Slug: "infra", DisplayName: "Infraestrutura", Type: domain.ChannelTypePublic,
	}}

	recorder := httptest.NewRecorder()
	channelTestHandler(provider).Create(recorder, createChannelRequest(validCreateBody))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	want := service.CreateChannelInput{
		WorkspaceID: testWorkspaceID,
		CallerID:    msgTestUserID,
		Slug:        "infra",
		DisplayName: "Infraestrutura",
		Type:        domain.ChannelTypePublic,
	}
	if provider.lastInput != want {
		t.Fatalf("input = %+v, want %+v", provider.lastInput, want)
	}
	body := decodeBody(t, recorder)
	data, _ := body["data"].(map[string]any)
	if data["id"] != createdChannelID || data["slug"] != "infra" {
		t.Fatalf("body = %v", body)
	}
}

// category_id is the one field of the create body that is optional and
// forwarded as-is — authorization for a non-empty value lives in the service
// (see channel_service_test.go), not here; the handler is only a pass-through.
func TestChannelHandler_Create_ForwardsCategoryID(t *testing.T) {
	provider := &fakeChannelProvider{channel: domain.Channel{
		ID: createdChannelID, Slug: "infra", DisplayName: "Infraestrutura", Type: domain.ChannelTypePublic,
	}}
	body := `{"slug":"infra","display_name":"Infraestrutura","type":"public","category_id":"cat-1"}`

	recorder := httptest.NewRecorder()
	channelTestHandler(provider).Create(recorder, createChannelRequest(body))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	if provider.lastInput.CategoryID != "cat-1" {
		t.Fatalf("category_id = %q, want %q", provider.lastInput.CategoryID, "cat-1")
	}
}

// A body that tries to elect its own workspace, creator or general flag is
// rejected outright rather than silently ignored.
func TestChannelHandler_Create_RejectsServerControlledFields(t *testing.T) {
	for _, body := range []string{
		`{"slug":"infra","display_name":"I","type":"public","workspace_id":"` + testWorkspaceID + `"}`,
		`{"slug":"infra","display_name":"I","type":"public","created_by":"` + msgTestUserID + `"}`,
		`{"slug":"infra","display_name":"I","type":"public","is_general":true}`,
	} {
		provider := &fakeChannelProvider{}
		recorder := httptest.NewRecorder()
		channelTestHandler(provider).Create(recorder, createChannelRequest(body))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, recorder.Code)
		}
		if provider.calls != 0 {
			t.Fatalf("body %s reached the service", body)
		}
	}
}

// A denial keeps its own status: a caller the workspace refuses must be able to
// tell "you may not" from "it broke".
func TestChannelHandler_Create_MapsServiceErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "forbidden", err: domain.ErrForbidden, want: http.StatusForbidden},
		{name: "invalid", err: domain.ErrInvalidInput, want: http.StatusBadRequest},
		{name: "duplicate slug", err: domain.ErrDuplicateSlug, want: http.StatusConflict},
		{name: "conflict", err: domain.ErrConflict, want: http.StatusConflict},
		{name: "unknown", err: errors.New("boom"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			channelTestHandler(&fakeChannelProvider{err: test.err}).Create(recorder, createChannelRequest(validCreateBody))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}

func TestChannelHandler_Create_RequiresJSONContentType(t *testing.T) {
	provider := &fakeChannelProvider{}
	request := requestWithUser(http.MethodPost, httpapi.RouteChannels, strings.NewReader(validCreateBody))
	request.Header.Set("Content-Type", "text/plain")

	recorder := httptest.NewRecorder()
	channelTestHandler(provider).Create(recorder, request)

	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", recorder.Code)
	}
	if provider.calls != 0 {
		t.Fatalf("service called %d times", provider.calls)
	}
}

func TestChannelHandler_Create_RateLimitsPerUser(t *testing.T) {
	provider := &fakeChannelProvider{channel: domain.Channel{ID: createdChannelID}}
	limiter := &fakeDMRateLimiter{}
	handler := httpapi.NewChannelHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, limiter,
	)

	for range 10 {
		recorder := httptest.NewRecorder()
		handler.Create(recorder, createChannelRequest(validCreateBody))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201", recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	handler.Create(recorder, createChannelRequest(validCreateBody))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After on a rate-limited response")
	}
}

// Unwired dependencies must answer 503, never panic or pretend to have created
// something.
func TestChannelHandler_Create_UnwiredReturns503(t *testing.T) {
	recorder := httptest.NewRecorder()
	httpapi.NewChannelHandler(nil, nil, nil).Create(recorder, createChannelRequest(validCreateBody))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}
