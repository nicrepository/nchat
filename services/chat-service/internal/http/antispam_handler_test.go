package httpapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
)

// RF-19 admin contract tests (issue #419) for
// GET|PATCH /api/chat/workspaces/{workspaceID}/anti-spam.

const antiSpamPath = "/api/chat/workspaces/" + testWorkspaceID + "/anti-spam"

// antiSpamHandler builds a handler wired to the settings store and authorizer
// under test, with the anti-spam path parameter already set as the router's
// pattern would set it.
func antiSpamHandler(settings *fakeWorkspaceSettingsStore, allowed bool) *httpapi.MessageHandler {
	return makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithEditing(settings, fakeSettingsAuthorizer{allowed: allowed}, fakeEditLimiter{allowed: true})
}

func antiSpamRequest(t *testing.T, method, body, workspaceID string) *http.Request {
	t.Helper()
	request := requestWithUser(method, antiSpamPath, strings.NewReader(body))
	request.SetPathValue("workspaceID", workspaceID)
	return request
}

// patchAntiSpam runs a PATCH against a permissive handler and returns the
// recorder, which is the shape most validation cases need.
func patchAntiSpam(t *testing.T, settings *fakeWorkspaceSettingsStore, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	antiSpamHandler(settings, true).
		UpdateWorkspaceAntiSpam(recorder, antiSpamRequest(t, http.MethodPatch, body, testWorkspaceID))
	return recorder
}

// ── Read ─────────────────────────────────────────────────────────────────────

func TestGetWorkspaceAntiSpam_ReturnsTheCurrentPolicyAndItsBounds(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{stored: 120}
	recorder := httptest.NewRecorder()

	antiSpamHandler(settings, true).
		GetWorkspaceAntiSpam(recorder, antiSpamRequest(t, http.MethodGet, "", testWorkspaceID))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	data, ok := decodeBody(t, recorder)["data"].(map[string]any)
	if !ok {
		t.Fatal("expected a data envelope")
	}
	if got := data["message_rate_limit_per_minute"].(float64); got != 120 {
		t.Fatalf("expected 120, got %v", got)
	}
	// The UI reads its input bounds from here rather than restating them.
	if got := data["min"].(float64); got != domain.MinMessageRateLimitPerMinute {
		t.Fatalf("expected min %d, got %v", domain.MinMessageRateLimitPerMinute, got)
	}
	if got := data["max"].(float64); got != domain.MaxMessageRateLimitPerMinute {
		t.Fatalf("expected max %d, got %v", domain.MaxMessageRateLimitPerMinute, got)
	}
}

func TestGetWorkspaceAntiSpam_UnsetPolicyReadsAsTheDefault(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{stored: 0}
	recorder := httptest.NewRecorder()

	antiSpamHandler(settings, true).
		GetWorkspaceAntiSpam(recorder, antiSpamRequest(t, http.MethodGet, "", testWorkspaceID))

	data := decodeBody(t, recorder)["data"].(map[string]any)
	if got := data["message_rate_limit_per_minute"].(float64); got != domain.DefaultMessageRateLimitPerMinute {
		t.Fatalf("expected the default %d, got %v", domain.DefaultMessageRateLimitPerMinute, got)
	}
}

// ── Write ────────────────────────────────────────────────────────────────────

func TestUpdateWorkspaceAntiSpam_PersistsAValidLimit(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{stored: 60}

	recorder := patchAntiSpam(t, settings, `{"message_rate_limit_per_minute": 30}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if settings.lastRate != 30 {
		t.Fatalf("expected 30 persisted, got %d", settings.lastRate)
	}
	// The actor is taken from the authenticated context, never from the body.
	if settings.lastRateUser != msgTestUserID {
		t.Fatalf("expected the authenticated user as actor, got %q", settings.lastRateUser)
	}
	data := decodeBody(t, recorder)["data"].(map[string]any)
	if got := data["message_rate_limit_per_minute"].(float64); got != 30 {
		t.Fatalf("expected the new value echoed back, got %v", got)
	}
}

func TestUpdateWorkspaceAntiSpam_AcceptsTheBoundaryValues(t *testing.T) {
	for _, value := range []int{domain.MinMessageRateLimitPerMinute, domain.MaxMessageRateLimitPerMinute} {
		settings := &fakeWorkspaceSettingsStore{}
		recorder := patchAntiSpam(t, settings,
			`{"message_rate_limit_per_minute": `+strconv.Itoa(value)+`}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("value %d must be accepted, got %d", value, recorder.Code)
		}
	}
}

func TestUpdateWorkspaceAntiSpam_RejectsInvalidPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "empty object", body: `{}`},
		{name: "invalid json", body: `{"message_rate_limit_per_minute":`},
		{name: "unknown field", body: `{"message_rate_limit_per_minute": 30, "workspace_id": "x"}`},
		{name: "unknown field only", body: `{"edit_window_seconds": 60}`},
		{name: "string value", body: `{"message_rate_limit_per_minute": "30"}`},
		{name: "decimal value", body: `{"message_rate_limit_per_minute": 30.5}`},
		{name: "boolean value", body: `{"message_rate_limit_per_minute": true}`},
		{name: "null value", body: `{"message_rate_limit_per_minute": null}`},
		{name: "zero", body: `{"message_rate_limit_per_minute": 0}`},
		{name: "negative", body: `{"message_rate_limit_per_minute": -1}`},
		{name: "below minimum", body: `{"message_rate_limit_per_minute": 0}`},
		{name: "above maximum", body: `{"message_rate_limit_per_minute": 601}`},
		{name: "int64 overflow", body: `{"message_rate_limit_per_minute": 9223372036854775808}`},
		{name: "trailing json", body: `{"message_rate_limit_per_minute": 30}{"a":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := &fakeWorkspaceSettingsStore{}
			recorder := patchAntiSpam(t, settings, tt.body)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", recorder.Code)
			}
			// Nothing invalid may reach the database.
			if settings.rateCalls != 0 {
				t.Fatalf("store was called %d times for an invalid payload", settings.rateCalls)
			}
		})
	}
}

func TestUpdateWorkspaceAntiSpam_RejectsAMalformedWorkspaceID(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{}
	recorder := httptest.NewRecorder()

	antiSpamHandler(settings, true).UpdateWorkspaceAntiSpam(recorder,
		antiSpamRequest(t, http.MethodPatch, `{"message_rate_limit_per_minute": 30}`, "not-a-uuid"))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	if settings.rateCalls != 0 {
		t.Fatal("store was called for a malformed workspace ID")
	}
}

// ── Authorization ────────────────────────────────────────────────────────────

func TestWorkspaceAntiSpam_RequiresAuthentication(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{}
	handler := antiSpamHandler(settings, true)

	for name, invoke := range map[string]func(http.ResponseWriter, *http.Request){
		"GET":   handler.GetWorkspaceAntiSpam,
		"PATCH": handler.UpdateWorkspaceAntiSpam,
	} {
		t.Run(name, func(t *testing.T) {
			// No user in context: what an unauthenticated request looks like by
			// the time it reaches the handler.
			request := httptest.NewRequest(name, antiSpamPath, strings.NewReader(`{"message_rate_limit_per_minute": 30}`))
			request.SetPathValue("workspaceID", testWorkspaceID)
			recorder := httptest.NewRecorder()

			invoke(recorder, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", recorder.Code)
			}
		})
	}
	if settings.rateCalls != 0 || settings.stored != 0 {
		t.Fatal("an unauthenticated request reached the store")
	}
}

func TestWorkspaceAntiSpam_DeniesNonAdmins(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{stored: 60}
	handler := antiSpamHandler(settings, false)

	readRecorder := httptest.NewRecorder()
	handler.GetWorkspaceAntiSpam(readRecorder, antiSpamRequest(t, http.MethodGet, "", testWorkspaceID))
	if readRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on read, got %d", readRecorder.Code)
	}

	writeRecorder := httptest.NewRecorder()
	handler.UpdateWorkspaceAntiSpam(writeRecorder,
		antiSpamRequest(t, http.MethodPatch, `{"message_rate_limit_per_minute": 1}`, testWorkspaceID))
	if writeRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on write, got %d", writeRecorder.Code)
	}
	if settings.rateCalls != 0 {
		t.Fatal("a non-admin reached the update statement")
	}
	if settings.stored != 60 {
		t.Fatalf("a non-admin changed the policy to %d", settings.stored)
	}
}

// An admin of another workspace is rejected by the store's own RBAC predicate,
// which returns ErrForbidden when the caller does not administer the workspace
// named in the path. This asserts that outcome surfaces as 403 rather than as
// a success or a leaked internal error.
func TestUpdateWorkspaceAntiSpam_DeniesAnAdminOfAnotherWorkspace(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{stored: 60, rateErr: domain.ErrForbidden}

	recorder := patchAntiSpam(t, settings, `{"message_rate_limit_per_minute": 5}`)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", recorder.Code)
	}
	if settings.stored != 60 {
		t.Fatalf("the other workspace's policy was changed to %d", settings.stored)
	}
}

func TestWorkspaceAntiSpam_ReturnsServiceUnavailableWhenNotWired(t *testing.T) {
	handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	recorder := httptest.NewRecorder()

	handler.GetWorkspaceAntiSpam(recorder, antiSpamRequest(t, http.MethodGet, "", testWorkspaceID))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", recorder.Code)
	}
}

// ── Error surface ────────────────────────────────────────────────────────────

func TestWorkspaceAntiSpam_PersistenceFailureDoesNotLeakInternals(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{rateErr: errors.New("pq: relation chat.workspaces does not exist")}

	recorder := patchAntiSpam(t, settings, `{"message_rate_limit_per_minute": 30}`)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "chat.workspaces") || strings.Contains(body, "pq:") {
		t.Fatalf("internal detail leaked in the error body: %s", body)
	}
}

func TestGetWorkspaceAntiSpam_MissingWorkspaceIsNotFound(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{getErr: domain.ErrNotFound}
	recorder := httptest.NewRecorder()

	antiSpamHandler(settings, true).
		GetWorkspaceAntiSpam(recorder, antiSpamRequest(t, http.MethodGet, "", testWorkspaceID))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recorder.Code)
	}
}
