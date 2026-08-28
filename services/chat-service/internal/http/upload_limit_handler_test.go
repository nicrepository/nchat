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

// RF-32 admin contract tests (issue #458) for
// GET|PATCH /api/chat/workspaces/{workspaceID}/upload-limit.
//
// The shape mirrors the anti-spam tests deliberately: the two endpoints are the
// same administrative pattern over a different column, and a divergence in
// either the authorization or the validation surface would show up here.

const uploadLimitPath = "/api/chat/workspaces/" + testWorkspaceID + "/upload-limit"

func uploadLimitHandler(settings *fakeWorkspaceSettingsStore, allowed bool) *httpapi.MessageHandler {
	return makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithEditing(settings, fakeSettingsAuthorizer{allowed: allowed}, fakeEditLimiter{allowed: true})
}

func uploadLimitRequest(t *testing.T, method, body, workspaceID string) *http.Request {
	t.Helper()
	request := requestWithUser(method, uploadLimitPath, strings.NewReader(body))
	request.SetPathValue("workspaceID", workspaceID)
	return request
}

func patchUploadLimit(t *testing.T, settings *fakeWorkspaceSettingsStore, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	uploadLimitHandler(settings, true).
		UpdateWorkspaceUploadLimit(recorder, uploadLimitRequest(t, http.MethodPatch, body, testWorkspaceID))
	return recorder
}

// ── Read ─────────────────────────────────────────────────────────────────────

func TestGetWorkspaceUploadLimit_ReturnsTheCurrentPolicyAndItsBounds(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{storedUploadBytes: 100 << 20}
	recorder := httptest.NewRecorder()

	uploadLimitHandler(settings, true).
		GetWorkspaceUploadLimit(recorder, uploadLimitRequest(t, http.MethodGet, "", testWorkspaceID))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	data, ok := decodeBody(t, recorder)["data"].(map[string]any)
	if !ok {
		t.Fatal("expected a data envelope")
	}
	if got := data["max_upload_bytes"].(float64); got != float64(100<<20) {
		t.Fatalf("expected %d, got %v", 100<<20, got)
	}
	// The UI reads its input bounds from here rather than restating them.
	if got := data["min"].(float64); got != float64(domain.MinMaxUploadBytes) {
		t.Fatalf("expected min %d, got %v", domain.MinMaxUploadBytes, got)
	}
	if got := data["max"].(float64); got != float64(domain.MaxMaxUploadBytes) {
		t.Fatalf("expected max %d, got %v", domain.MaxMaxUploadBytes, got)
	}
}

// A workspace row written before migration 000020 reads as zero, which is not a
// limit. It must read back as the RF-32 default of 250 MiB.
func TestGetWorkspaceUploadLimit_UnsetPolicyReadsAsTheDefault(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{storedUploadBytes: 0}
	recorder := httptest.NewRecorder()

	uploadLimitHandler(settings, true).
		GetWorkspaceUploadLimit(recorder, uploadLimitRequest(t, http.MethodGet, "", testWorkspaceID))

	data := decodeBody(t, recorder)["data"].(map[string]any)
	if got := data["max_upload_bytes"].(float64); got != float64(domain.DefaultMaxUploadBytes) {
		t.Fatalf("expected the default %d, got %v", domain.DefaultMaxUploadBytes, got)
	}
	if domain.DefaultMaxUploadBytes != 262144000 {
		t.Fatalf("RF-32 requires a 250 MiB default, got %d", domain.DefaultMaxUploadBytes)
	}
}

// ── Write ────────────────────────────────────────────────────────────────────

func TestUpdateWorkspaceUploadLimit_PersistsAValidLimit(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{storedUploadBytes: domain.DefaultMaxUploadBytes}

	recorder := patchUploadLimit(t, settings, `{"max_upload_bytes": 104857600}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if settings.lastUploadBytes != 104857600 {
		t.Fatalf("expected 104857600 persisted, got %d", settings.lastUploadBytes)
	}
	// The actor is taken from the authenticated context, never from the body.
	if settings.lastUploadUser != msgTestUserID {
		t.Fatalf("expected the authenticated user as actor, got %q", settings.lastUploadUser)
	}
	data := decodeBody(t, recorder)["data"].(map[string]any)
	if got := data["max_upload_bytes"].(float64); got != 104857600 {
		t.Fatalf("expected the new value echoed back, got %v", got)
	}
}

func TestUpdateWorkspaceUploadLimit_AcceptsTheBoundaryValues(t *testing.T) {
	for _, value := range []int64{
		domain.MinMaxUploadBytes, domain.DefaultMaxUploadBytes, domain.MaxMaxUploadBytes,
	} {
		settings := &fakeWorkspaceSettingsStore{}
		recorder := patchUploadLimit(t, settings,
			`{"max_upload_bytes": `+strconv.FormatInt(value, 10)+`}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("value %d must be accepted, got %d", value, recorder.Code)
		}
	}
}

// Every invalid value is refused, never coerced into range and never written.
func TestUpdateWorkspaceUploadLimit_RejectsInvalidPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "empty object", body: `{}`},
		{name: "invalid json", body: `{"max_upload_bytes":`},
		{name: "unknown field", body: `{"max_upload_bytes": 104857600, "workspace_id": "x"}`},
		{name: "unknown field only", body: `{"edit_window_seconds": 60}`},
		{name: "string value", body: `{"max_upload_bytes": "104857600"}`},
		{name: "decimal value", body: `{"max_upload_bytes": 104857600.5}`},
		{name: "boolean value", body: `{"max_upload_bytes": true}`},
		{name: "null value", body: `{"max_upload_bytes": null}`},
		{name: "zero", body: `{"max_upload_bytes": 0}`},
		{name: "negative", body: `{"max_upload_bytes": -1048576}`},
		{name: "below minimum", body: `{"max_upload_bytes": 1048575}`},
		{name: "above maximum", body: `{"max_upload_bytes": 536870913}`},
		// The policy is a whole number of MiB. Anything else is refused rather
		// than rounded to the nearest one — the defect this guards against is a
		// caller storing 1572864 and the admin UI later saving 2097152 back.
		{name: "one and a half MiB", body: `{"max_upload_bytes": 1572864}`},
		{name: "one byte above a whole MiB", body: `{"max_upload_bytes": 1048577}`},
		{name: "one byte below the ceiling", body: `{"max_upload_bytes": 536870911}`},
		{name: "one byte below the default", body: `{"max_upload_bytes": 262143999}`},
		{name: "int64 overflow", body: `{"max_upload_bytes": 9223372036854775808}`},
		{name: "trailing json", body: `{"max_upload_bytes": 104857600}{"a":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := &fakeWorkspaceSettingsStore{}
			recorder := patchUploadLimit(t, settings, tt.body)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", recorder.Code)
			}
			// Nothing invalid may reach the database.
			if settings.uploadCalls != 0 {
				t.Fatalf("store was called %d times for an invalid payload", settings.uploadCalls)
			}
			if settings.storedUploadBytes != 0 {
				t.Fatalf("an invalid payload changed the policy to %d", settings.storedUploadBytes)
			}
		})
	}
}

func TestUpdateWorkspaceUploadLimit_RejectsAMalformedWorkspaceID(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{}
	recorder := httptest.NewRecorder()

	uploadLimitHandler(settings, true).UpdateWorkspaceUploadLimit(recorder,
		uploadLimitRequest(t, http.MethodPatch, `{"max_upload_bytes": 104857600}`, "not-a-uuid"))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	if settings.uploadCalls != 0 {
		t.Fatal("store was called for a malformed workspace ID")
	}
}

// ── Authorization ────────────────────────────────────────────────────────────

func TestWorkspaceUploadLimit_RequiresAuthentication(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{}
	handler := uploadLimitHandler(settings, true)

	for name, invoke := range map[string]func(http.ResponseWriter, *http.Request){
		"GET":   handler.GetWorkspaceUploadLimit,
		"PATCH": handler.UpdateWorkspaceUploadLimit,
	} {
		t.Run(name, func(t *testing.T) {
			// No user in context: what an unauthenticated request looks like by
			// the time it reaches the handler.
			request := httptest.NewRequest(name, uploadLimitPath,
				strings.NewReader(`{"max_upload_bytes": 104857600}`))
			request.SetPathValue("workspaceID", testWorkspaceID)
			recorder := httptest.NewRecorder()

			invoke(recorder, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", recorder.Code)
			}
		})
	}
	if settings.uploadCalls != 0 || settings.storedUploadBytes != 0 {
		t.Fatal("an unauthenticated request reached the store")
	}
}

// Read and write are gated identically: a readable-but-not-writable split would
// leak a workspace's policy to any authenticated member of any workspace.
func TestWorkspaceUploadLimit_DeniesNonAdmins(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{storedUploadBytes: domain.DefaultMaxUploadBytes}
	handler := uploadLimitHandler(settings, false)

	readRecorder := httptest.NewRecorder()
	handler.GetWorkspaceUploadLimit(readRecorder, uploadLimitRequest(t, http.MethodGet, "", testWorkspaceID))
	if readRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on read, got %d", readRecorder.Code)
	}

	writeRecorder := httptest.NewRecorder()
	handler.UpdateWorkspaceUploadLimit(writeRecorder,
		uploadLimitRequest(t, http.MethodPatch, `{"max_upload_bytes": 1048576}`, testWorkspaceID))
	if writeRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on write, got %d", writeRecorder.Code)
	}
	if settings.uploadCalls != 0 {
		t.Fatal("a non-admin reached the update statement")
	}
	if settings.storedUploadBytes != domain.DefaultMaxUploadBytes {
		t.Fatalf("a non-admin changed the policy to %d", settings.storedUploadBytes)
	}
}

// An admin of another workspace is rejected by the store's own RBAC predicate,
// which returns ErrForbidden when the caller does not administer the workspace
// named in the path.
func TestUpdateWorkspaceUploadLimit_DeniesAnAdminOfAnotherWorkspace(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{
		storedUploadBytes: domain.DefaultMaxUploadBytes,
		uploadErr:         domain.ErrForbidden,
	}

	recorder := patchUploadLimit(t, settings, `{"max_upload_bytes": 1048576}`)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", recorder.Code)
	}
	if settings.storedUploadBytes != domain.DefaultMaxUploadBytes {
		t.Fatalf("the other workspace's policy was changed to %d", settings.storedUploadBytes)
	}
}

func TestWorkspaceUploadLimit_ReturnsServiceUnavailableWhenNotWired(t *testing.T) {
	handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	recorder := httptest.NewRecorder()

	handler.GetWorkspaceUploadLimit(recorder, uploadLimitRequest(t, http.MethodGet, "", testWorkspaceID))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", recorder.Code)
	}
}

// ── Error surface ────────────────────────────────────────────────────────────

func TestWorkspaceUploadLimit_PersistenceFailureDoesNotLeakInternals(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{
		uploadErr: errors.New("pq: relation chat.workspaces does not exist"),
	}

	recorder := patchUploadLimit(t, settings, `{"max_upload_bytes": 104857600}`)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "chat.workspaces") {
		t.Fatal("the driver error must never reach the client")
	}
}
