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

// profileRouter builds a router whose UserAdmin serves GetProfile from the stub.
// UserAdmin embeds SelfProfileReader, so a struct embedding the stub satisfies it
// while leaving the admin methods unused.
type profileUserAdmin struct {
	*stubProfileReader
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
func (routerUsersUnused) ResolveAdminWorkspaceID(context.Context, string, string) (string, error) {
	return "", domain.ErrForbidden
}

func profileRouter(t *testing.T, reader *stubProfileReader) http.Handler {
	t.Helper()
	users := profileUserAdmin{stubProfileReader: reader}
	return NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		users, nil, nil, nil, nil, nil, routerSessionStub{}, nil, nil, nil)
}

func authedProfileGet(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+mustAccessTokenForRouter(t, avatarUserID, avatarSessionID))
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
