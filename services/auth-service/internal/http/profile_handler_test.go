package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

type stubProfileReader struct {
	gotUserID string
	profile   domain.SelfProfile
	err       error
}

func (s *stubProfileReader) GetProfile(_ context.Context, userID string) (domain.SelfProfile, error) {
	s.gotUserID = userID
	if s.err != nil {
		return domain.SelfProfile{}, s.err
	}
	return s.profile, nil
}

// stubProfileWriter records what PATCH /auth/me forwarded to the service, so
// tests can assert identity always comes from the session, never the body.
type stubProfileWriter struct {
	gotUserID      string
	gotDisplayName string
	calls          int
	profile        domain.SelfProfile
	err            error

	// Recorded by UpdateProfileFields, tracked separately from the
	// UpdateDisplayName fields above so a test asserting on one call's
	// arguments is never clobbered by the other call happening in the same
	// request.
	gotFieldsUserID   string
	gotJobTitle       *string
	gotBio            *string
	gotTimezone       *string
	gotCustomStatus   *string
	profileFieldCalls int
}

func (s *stubProfileWriter) UpdateDisplayName(_ context.Context, userID, displayName string) (domain.SelfProfile, error) {
	s.gotUserID = userID
	s.gotDisplayName = displayName
	s.calls++
	if s.err != nil {
		return domain.SelfProfile{}, s.err
	}
	return s.profile, nil
}

func (s *stubProfileWriter) UpdateProfileFields(_ context.Context, userID string, jobTitle, bio, timezone, customStatus *string) (domain.SelfProfile, error) {
	s.gotFieldsUserID = userID
	s.gotJobTitle = jobTitle
	s.gotBio = bio
	s.gotTimezone = timezone
	s.gotCustomStatus = customStatus
	s.profileFieldCalls++
	if s.err != nil {
		return domain.SelfProfile{}, s.err
	}
	return s.profile, nil
}

// profileRouter builds a router whose UserAdmin serves GetProfile from the stub.
// UserAdmin embeds SelfProfileReader, so a struct embedding the stub satisfies it
// while leaving the admin methods unused.
type profileUserAdmin struct {
	*stubProfileReader
	*stubProfileWriter
	routerUsersUnused
}

// routerUsersUnused fills the non-profile UserAdmin methods; they are never
// called by the me-profile endpoint.
type routerUsersUnused struct{}

func (routerUsersUnused) CreateUser(context.Context, domain.CreateUserInput) (domain.User, error) {
	return domain.User{}, nil
}
func (routerUsersUnused) UpdateUserStatus(context.Context, string, string, string) (domain.User, error) {
	return domain.User{}, nil
}
func (routerUsersUnused) GetAdminWorkspaceID(context.Context, string) (string, error) {
	return "", domain.ErrForbidden
}
func (routerUsersUnused) ListWorkspaceUsers(context.Context, string, int, string) ([]domain.WorkspaceUser, string, error) {
	return nil, "", nil
}

func profileRouter(t *testing.T, reader *stubProfileReader) http.Handler {
	t.Helper()
	return profileRouterFull(t, reader, &stubProfileWriter{})
}

func profileRouterFull(t *testing.T, reader *stubProfileReader, writer *stubProfileWriter) http.Handler {
	t.Helper()
	users := profileUserAdmin{stubProfileReader: reader, stubProfileWriter: writer}
	return NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		users, nil, nil, nil, nil, nil, routerSessionStub{}, nil, nil, nil)
}

func authedProfileGet(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+mustAccessTokenForRouter(t, avatarUserID, avatarSessionID))
	return req
}

func authedProfilePatch(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/auth/me", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mustAccessTokenForRouter(t, avatarUserID, avatarSessionID))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestGetMyProfile_ReturnsAvatarFromSessionIdentity(t *testing.T) {
	reader := &stubProfileReader{profile: domain.SelfProfile{
		ID: avatarUserID, DisplayName: "Ana", AvatarURL: "/api/auth/avatars/abc.png",
	}}
	router := profileRouter(t, reader)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfileGet(t))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if reader.gotUserID != avatarUserID {
		t.Fatalf("profile must be read for the session user, got %q", reader.gotUserID)
	}
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(env.Data["avatar_url"]) != `"/api/auth/avatars/abc.png"` {
		t.Fatalf("avatar_url mismatch: %s", env.Data["avatar_url"])
	}
	// Minimal contract: no e-mail or other PII.
	for _, forbidden := range []string{"email", "status", "auth_source", "full_name"} {
		if _, ok := env.Data[forbidden]; ok {
			t.Fatalf("profile must not expose %q", forbidden)
		}
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected no-store, got %q", rr.Header().Get("Cache-Control"))
	}
}

func TestGetMyProfile_OmitsAvatarWhenUnset(t *testing.T) {
	reader := &stubProfileReader{profile: domain.SelfProfile{ID: avatarUserID, DisplayName: "Ana"}}
	router := profileRouter(t, reader)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfileGet(t))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "avatar_url") {
		t.Fatalf("avatar_url must be omitted when empty: %s", rr.Body.String())
	}
}

func TestGetMyProfile_RequiresAuth(t *testing.T) {
	router := profileRouter(t, &stubProfileReader{})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/auth/me", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestGetMyProfile_NotFoundMapsTo404(t *testing.T) {
	router := profileRouter(t, &stubProfileReader{err: domain.ErrNotFound})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfileGet(t))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestGetMyProfile_InternalErrorMapsTo500(t *testing.T) {
	router := profileRouter(t, &stubProfileReader{err: errors.New("db down")})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfileGet(t))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "db down") {
		t.Fatal("internal error detail must not leak")
	}
}

func TestGetMyProfile_DisabledReturns503(t *testing.T) {
	// A nil users dependency disables the endpoint.
	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, nil, routerSessionStub{}, nil, nil, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/auth/me", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

// The four optional fields must not surface as empty-string JSON noise when
// the server has none set.
func TestGetMyProfile_OmitsProfileFieldsWhenUnset(t *testing.T) {
	reader := &stubProfileReader{profile: domain.SelfProfile{ID: avatarUserID, DisplayName: "Ana"}}
	router := profileRouter(t, reader)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfileGet(t))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	for _, field := range []string{"job_title", "\"bio\"", "timezone", "custom_status"} {
		if strings.Contains(rr.Body.String(), field) {
			t.Fatalf("%s must be omitted when empty: %s", field, rr.Body.String())
		}
	}
}

func TestGetMyProfile_ReturnsCustomStatus(t *testing.T) {
	reader := &stubProfileReader{profile: domain.SelfProfile{
		ID: avatarUserID, DisplayName: "Ana", CustomStatus: "Em reunião",
	}}
	router := profileRouter(t, reader)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfileGet(t))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(env.Data["custom_status"]) != `"Em reunião"` {
		t.Fatalf("expected custom_status, got %s", env.Data["custom_status"])
	}
}

func TestGetMyProfile_MethodNotAllowed(t *testing.T) {
	router := profileRouter(t, &stubProfileReader{})
	req := httptest.NewRequest(http.MethodPost, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+mustAccessTokenForRouter(t, avatarUserID, avatarSessionID))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

// ── PATCH /auth/me (ID 7 — cronograma 19/08) ────────────────────────────────

func TestPatchMyProfile_UpdatesDisplayName_ReturnsPersistedProfile(t *testing.T) {
	writer := &stubProfileWriter{profile: domain.SelfProfile{
		ID: avatarUserID, DisplayName: "Ana Lima", AvatarURL: "/api/auth/avatars/abc.png",
	}}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"display_name":"Ana Lima"}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if writer.gotUserID != avatarUserID {
		t.Fatalf("display_name must be updated for the session user, got %q", writer.gotUserID)
	}
	if writer.gotDisplayName != "Ana Lima" {
		t.Fatalf("expected the request's display_name to reach the service, got %q", writer.gotDisplayName)
	}
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(env.Data["display_name"]) != `"Ana Lima"` {
		t.Fatalf("expected the persisted display_name to be echoed back, got %s", env.Data["display_name"])
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected no-store, got %q", rr.Header().Get("Cache-Control"))
	}
}

func TestPatchMyProfile_UpdatesProfileFields_ReturnsPersistedProfile(t *testing.T) {
	writer := &stubProfileWriter{profile: domain.SelfProfile{
		ID: avatarUserID, DisplayName: "Ana Lima", JobTitle: "Engenheira",
		Bio: "Focada em backend.", Timezone: "America/Sao_Paulo",
		CustomStatus: "Em reunião",
	}}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t,
		`{"job_title":"Engenheira","bio":"Focada em backend.","timezone":"America/Sao_Paulo","custom_status":"Em reunião"}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if writer.gotFieldsUserID != avatarUserID {
		t.Fatalf("profile fields must be updated for the session user, got %q", writer.gotFieldsUserID)
	}
	if writer.gotJobTitle == nil || *writer.gotJobTitle != "Engenheira" {
		t.Fatalf("expected job_title to reach the service, got %v", writer.gotJobTitle)
	}
	if writer.gotBio == nil || *writer.gotBio != "Focada em backend." {
		t.Fatalf("expected bio to reach the service, got %v", writer.gotBio)
	}
	if writer.gotTimezone == nil || *writer.gotTimezone != "America/Sao_Paulo" {
		t.Fatalf("expected timezone to reach the service, got %v", writer.gotTimezone)
	}
	if writer.gotCustomStatus == nil || *writer.gotCustomStatus != "Em reunião" {
		t.Fatalf("expected custom_status to reach the service, got %v", writer.gotCustomStatus)
	}
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(env.Data["job_title"]) != `"Engenheira"` {
		t.Fatalf("expected job_title echoed back, got %s", env.Data["job_title"])
	}
}

// custom_status can be changed alone without touching job_title, bio or timezone.
func TestPatchMyProfile_CustomStatusOnly_DoesNotTouchOtherFields(t *testing.T) {
	writer := &stubProfileWriter{profile: domain.SelfProfile{ID: avatarUserID, CustomStatus: "Em reunião"}}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"custom_status":"Em reunião"}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if writer.gotCustomStatus == nil || *writer.gotCustomStatus != "Em reunião" {
		t.Fatalf("expected custom_status to reach the service, got %v", writer.gotCustomStatus)
	}
	if writer.gotJobTitle != nil || writer.gotBio != nil || writer.gotTimezone != nil {
		t.Fatalf("expected other fields to remain untouched, got job_title=%v bio=%v timezone=%v", writer.gotJobTitle, writer.gotBio, writer.gotTimezone)
	}
}

// The bug this guards against: PatchMyProfile used to call UpdateProfileFields
// unconditionally on every request, so a display-name-only save (from the
// "Nome de exibição" screen) would silently clear job_title/bio/timezone/
// custom_status because it decoded to Go's zero value "" when
// absent from the JSON. UpdateProfileFields must not be called at all when
// none of its four fields were provided.
func TestPatchMyProfile_DisplayNameOnly_DoesNotTouchProfileFields(t *testing.T) {
	writer := &stubProfileWriter{profile: domain.SelfProfile{ID: avatarUserID, DisplayName: "Ana Lima"}}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"display_name":"Ana Lima"}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if writer.profileFieldCalls != 0 {
		t.Fatalf("expected UpdateProfileFields not to be called, got %d calls", writer.profileFieldCalls)
	}
}

// The symmetric case: a details-only save must not touch display_name.
func TestPatchMyProfile_ProfileFieldsOnly_DoesNotTouchDisplayName(t *testing.T) {
	writer := &stubProfileWriter{profile: domain.SelfProfile{ID: avatarUserID}}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"job_title":"Engenheira"}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if writer.calls != 0 {
		t.Fatalf("expected UpdateDisplayName not to be called, got %d calls", writer.calls)
	}
}

// A body providing both groups in one request is unusual (the two screens
// never do this) but must still work correctly: each group is persisted by
// its own call.
func TestPatchMyProfile_BothGroupsProvided_CallsBoth(t *testing.T) {
	writer := &stubProfileWriter{profile: domain.SelfProfile{ID: avatarUserID}}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"display_name":"Ana Lima","job_title":"Engenheira"}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if writer.calls != 1 {
		t.Fatalf("expected UpdateDisplayName called once, got %d", writer.calls)
	}
	if writer.profileFieldCalls != 1 {
		t.Fatalf("expected UpdateProfileFields called once, got %d", writer.profileFieldCalls)
	}
}

// Explicitly sending "" for an optional field must reach the service as a
// non-nil pointer to "" (clear it) — not as a nil pointer (leave it alone).
// Those two are different requests with different meanings, and only the
// decoder can tell them apart.
func TestPatchMyProfile_ClearingCustomStatus_IsDistinctFromOmittingIt(t *testing.T) {
	writer := &stubProfileWriter{profile: domain.SelfProfile{ID: avatarUserID}}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"custom_status":""}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if writer.gotCustomStatus == nil {
		t.Fatal("expected custom_status to reach the service as a non-nil pointer (clear), got nil (untouched)")
	}
	if *writer.gotCustomStatus != "" {
		t.Fatalf("expected empty custom_status, got %q", *writer.gotCustomStatus)
	}
	if writer.gotJobTitle != nil || writer.gotBio != nil || writer.gotTimezone != nil {
		t.Fatalf("expected other fields to remain untouched, got job_title=%v bio=%v timezone=%v", writer.gotJobTitle, writer.gotBio, writer.gotTimezone)
	}
}

// A body with none of the recognized fields (valid JSON, just empty)
// cannot have been a deliberate save from either screen.
func TestPatchMyProfile_RejectsEmptyObjectBody(t *testing.T) {
	writer := &stubProfileWriter{profile: domain.SelfProfile{ID: avatarUserID}}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{}`))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rr.Code, rr.Body.String())
	}
	if writer.calls != 0 || writer.profileFieldCalls != 0 {
		t.Fatal("service must not be called for an empty body")
	}
}

// Malformed timezone values are the service's job to reject (real IANA-name
// validation lives there, not in the handler); this just confirms the error
// still flows through the same 400 mapping as any other ErrInvalidInput.
func TestPatchMyProfile_InvalidTimezoneMapsTo400(t *testing.T) {
	writer := &stubProfileWriter{err: domain.ErrInvalidInput}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"timezone":"Mars/Olympus_Mons"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestPatchMyProfile_RequiresAuth(t *testing.T) {
	router := profileRouterFull(t, &stubProfileReader{}, &stubProfileWriter{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/auth/me", strings.NewReader(`{"display_name":"Ana"}`))
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// A caller-supplied id/user_id/workspace_id/role/status/auth_source/email must
// never reach the service: patchMyProfileRequest has no field for any of them,
// and DisallowUnknownFields rejects the request outright rather than silently
// dropping the extra field — this is what stops mass assignment here.
func TestPatchMyProfile_RejectsUnknownFields(t *testing.T) {
	for _, body := range []string{
		`{"display_name":"Ana","id":"other-user"}`,
		`{"display_name":"Ana","user_id":"other-user"}`,
		`{"display_name":"Ana","workspace_id":"ws-1"}`,
		`{"display_name":"Ana","role":"admin"}`,
		`{"display_name":"Ana","status":"suspended"}`,
		`{"display_name":"Ana","auth_source":"oidc"}`,
		`{"display_name":"Ana","email":"other@example.com"}`,
		`{"display_name":"Ana","status_text":"Em reunião"}`,
		`{"display_name":"Ana","status_emoji":"📅"}`,
	} {
		t.Run(body, func(t *testing.T) {
			writer := &stubProfileWriter{profile: domain.SelfProfile{ID: avatarUserID}}
			router := profileRouterFull(t, &stubProfileReader{}, writer)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, authedProfilePatch(t, body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d (%s)", body, rr.Code, rr.Body.String())
			}
			if writer.calls != 0 || writer.profileFieldCalls != 0 {
				t.Fatalf("service must not be called when the payload has an unknown field: %s", body)
			}
		})
	}
}

func TestPatchMyProfile_RejectsInvalidJSON(t *testing.T) {
	router := profileRouterFull(t, &stubProfileReader{}, &stubProfileWriter{})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `not json`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestPatchMyProfile_RejectsEmptyBody(t *testing.T) {
	router := profileRouterFull(t, &stubProfileReader{}, &stubProfileWriter{})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, ``))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// A second JSON value appended after the first must not be silently ignored —
// it could otherwise be used to smuggle an admin-shaped object past a naive
// single-decode check.
func TestPatchMyProfile_RejectsTrailingData(t *testing.T) {
	writer := &stubProfileWriter{profile: domain.SelfProfile{ID: avatarUserID}}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"display_name":"Ana"}{"role":"admin"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestPatchMyProfile_ServiceValidationErrorMapsTo400(t *testing.T) {
	writer := &stubProfileWriter{err: domain.ErrInvalidInput}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"display_name":""}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// An inactive or deleted user maps the same way the avatar write endpoints
// already map it: the session was valid but the account is not.
func TestPatchMyProfile_NotFoundMapsTo403(t *testing.T) {
	writer := &stubProfileWriter{err: domain.ErrNotFound}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"display_name":"Ana"}`))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestPatchMyProfile_InternalErrorMapsTo500(t *testing.T) {
	writer := &stubProfileWriter{err: errors.New("db down")}
	router := profileRouterFull(t, &stubProfileReader{}, writer)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"display_name":"Ana"}`))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "db down") {
		t.Fatal("internal error detail must not leak")
	}
}

func TestPatchMyProfile_DisabledReturns503(t *testing.T) {
	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, nil, routerSessionStub{}, nil, nil, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedProfilePatch(t, `{"display_name":"Ana"}`))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}
