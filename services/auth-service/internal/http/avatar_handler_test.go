package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const (
	avatarUserID    = "user-1"
	avatarSessionID = "11111111-1111-4111-8111-111111111111"
)

var errNotFoundStub = errors.New("not found")

type stubAvatarManager struct {
	gotUserID string
	uploadURL string
	uploadErr error
	removeErr error
	removed   bool
}

func (s *stubAvatarManager) Upload(_ context.Context, userID string, r io.Reader) (string, error) {
	s.gotUserID = userID
	_, _ = io.Copy(io.Discard, r)
	if s.uploadErr != nil {
		return "", s.uploadErr
	}
	return s.uploadURL, nil
}

func (s *stubAvatarManager) Remove(_ context.Context, userID string) error {
	s.gotUserID = userID
	s.removed = true
	return s.removeErr
}

type stubAvatarReader struct {
	data []byte
	err  error
}

func (s *stubAvatarReader) Open(string) (io.ReadCloser, error) {
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

func avatarRouter(t *testing.T, mgr AvatarManager, reader AvatarReader) http.Handler {
	t.Helper()
	return NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, nil, routerSessionStub{}, nil, mgr, reader, allowAllBootstrapAttempts{})
}

func multipartBody(t *testing.T, field, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = fw.Write(content)
	_ = w.Close()
	return &body, w.FormDataContentType()
}

func authedAvatarRequest(t *testing.T, method string, body io.Reader, contentType string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/api/auth/me/avatar", body)
	// Route registration uses the stripped internal path.
	req.URL.Path = "/auth/me/avatar"
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+mustAccessTokenForRouter(t, avatarUserID, avatarSessionID))
	return req
}

func TestUploadAvatar_RequiresAuth(t *testing.T) {
	router := avatarRouter(t, &stubAvatarManager{}, &stubAvatarReader{})
	body, ct := multipartBody(t, "avatar", "a.png", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/auth/me/avatar", body)
	req.Header.Set("Content-Type", ct)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestUploadAvatar_Success_UsesSessionIdentity(t *testing.T) {
	mgr := &stubAvatarManager{uploadURL: "/api/auth/avatars/abcabcabcabcabcabc.png"}
	router := avatarRouter(t, mgr, &stubAvatarReader{})
	body, ct := multipartBody(t, "avatar", "photo.png", []byte("fake-image"))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodPost, body, ct))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), mgr.uploadURL) {
		t.Fatalf("response missing avatar_url: %s", rr.Body.String())
	}
	// The owner is the token subject, never anything from the body.
	if mgr.gotUserID != avatarUserID {
		t.Fatalf("expected owner %q from session, got %q", avatarUserID, mgr.gotUserID)
	}
}

func TestUploadAvatar_RejectsNonMultipart(t *testing.T) {
	router := avatarRouter(t, &stubAvatarManager{}, &stubAvatarReader{})
	req := authedAvatarRequest(t, http.MethodPost, strings.NewReader(`{"x":1}`), "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rr.Code)
	}
}

func TestUploadAvatar_RejectsWrongFieldName(t *testing.T) {
	router := avatarRouter(t, &stubAvatarManager{}, &stubAvatarReader{})
	body, ct := multipartBody(t, "not_avatar", "a.png", []byte("x"))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodPost, body, ct))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestUploadAvatar_RejectsTwoFiles(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	f1, _ := w.CreateFormFile("avatar", "a.png")
	_, _ = f1.Write([]byte("one"))
	f2, _ := w.CreateFormFile("avatar", "b.png")
	_, _ = f2.Write([]byte("two"))
	_ = w.Close()

	mgr := &stubAvatarManager{uploadURL: "/api/auth/avatars/x.png"}
	router := avatarRouter(t, mgr, &stubAvatarReader{})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodPost, &body, w.FormDataContentType()))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for two files, got %d", rr.Code)
	}
	if mgr.gotUserID != "" {
		t.Fatal("a two-file upload must be rejected before reaching the service")
	}
}

func TestUploadAvatar_TooLargeReturns413(t *testing.T) {
	big := bytes.Repeat([]byte("A"), domain.AvatarMaxUploadBytes+16<<10)
	body, ct := multipartBody(t, "avatar", "a.png", big)
	router := avatarRouter(t, &stubAvatarManager{}, &stubAvatarReader{})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodPost, body, ct))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rr.Code)
	}
}

func TestUploadAvatar_UnsupportedImageReturns415(t *testing.T) {
	mgr := &stubAvatarManager{uploadErr: domain.ErrAvatarUnsupported}
	router := avatarRouter(t, mgr, &stubAvatarReader{})
	body, ct := multipartBody(t, "avatar", "a.png", []byte("not-an-image"))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodPost, body, ct))
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rr.Code)
	}
}

func TestUploadAvatar_InactiveUserForbidden(t *testing.T) {
	mgr := &stubAvatarManager{uploadErr: domain.ErrNotFound}
	router := avatarRouter(t, mgr, &stubAvatarReader{})
	body, ct := multipartBody(t, "avatar", "a.png", []byte("img"))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodPost, body, ct))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for inactive user, got %d", rr.Code)
	}
}

func TestDeleteAvatar_SuccessAndIdempotent(t *testing.T) {
	mgr := &stubAvatarManager{}
	router := avatarRouter(t, mgr, &stubAvatarReader{})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodDelete, nil, ""))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if !mgr.removed || mgr.gotUserID != avatarUserID {
		t.Fatalf("remove not called with session identity: %+v", mgr)
	}
}

func TestDeleteAvatar_RequiresAuth(t *testing.T) {
	router := avatarRouter(t, &stubAvatarManager{}, &stubAvatarReader{})
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/avatar", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestServeAvatar_ReturnsImageWithHardenedHeaders(t *testing.T) {
	reader := &stubAvatarReader{data: []byte("\x89PNG\r\n\x1a\n stored bytes")}
	router := avatarRouter(t, &stubAvatarManager{}, reader)
	req := httptest.NewRequest(http.MethodGet, "/auth/avatars/deadbeefdeadbeef.png", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != domain.AvatarContentType {
		t.Fatalf("expected %q, got %q", domain.AvatarContentType, got)
	}
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff header")
	}
	if rr.Header().Get("Content-Disposition") != "inline" {
		t.Fatal("missing inline content-disposition")
	}
	if !strings.Contains(rr.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("expected immutable cache, got %q", rr.Header().Get("Cache-Control"))
	}
}

func TestDeleteAvatar_ForbiddenForInactiveUser(t *testing.T) {
	mgr := &stubAvatarManager{removeErr: domain.ErrNotFound}
	router := avatarRouter(t, mgr, &stubAvatarReader{})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodDelete, nil, ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestUploadAvatar_InternalErrorReturns500(t *testing.T) {
	mgr := &stubAvatarManager{uploadErr: errNotFoundStub} // a generic (non-sentinel) error
	router := avatarRouter(t, mgr, &stubAvatarReader{})
	body, ct := multipartBody(t, "avatar", "a.png", []byte("img"))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodPost, body, ct))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for an unexpected error, got %d", rr.Code)
	}
	// The error body must not echo any uploaded content.
	if strings.Contains(rr.Body.String(), "img") {
		t.Fatal("error response must not contain uploaded bytes")
	}
}

func TestServeAvatar_NotFound(t *testing.T) {
	reader := &stubAvatarReader{err: errNotFoundStub}
	router := avatarRouter(t, &stubAvatarManager{}, reader)
	req := httptest.NewRequest(http.MethodGet, "/auth/avatars/deadbeefdeadbeef.png", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestAvatarEndpoints_DisabledReturn503(t *testing.T) {
	// avatars=nil, reader=nil disables the feature; every endpoint reports 503.
	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, nil, routerSessionStub{}, nil, nil, nil, allowAllBootstrapAttempts{})

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/auth/me/avatar"},
		{http.MethodDelete, "/auth/me/avatar"},
		{http.MethodGet, "/auth/avatars/deadbeefdeadbeef.png"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		if c.method != http.MethodGet {
			req.Header.Set("Authorization", "Bearer "+mustAccessTokenForRouter(t, avatarUserID, avatarSessionID))
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: expected 503, got %d", c.method, c.path, rr.Code)
		}
	}
}

func TestUploadAvatar_MethodNotAllowedForGet(t *testing.T) {
	router := avatarRouter(t, &stubAvatarManager{}, &stubAvatarReader{})
	req := httptest.NewRequest(http.MethodGet, "/auth/me/avatar", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}
