package httpapi

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// memAvatarUserStore is a tiny in-memory stand-in for the avatar_url column, so
// the integration exercises the real filesystem store, the real image pipeline,
// and the real HTTP handlers without needing PostgreSQL (the DB half is covered
// by the storage-level PG test).
type memAvatarUserStore struct {
	mu     sync.Mutex
	urls   map[string]string
	active map[string]bool
}

func newMemAvatarUserStore() *memAvatarUserStore {
	return &memAvatarUserStore{urls: map[string]string{}, active: map[string]bool{avatarUserID: true}}
}

func (m *memAvatarUserStore) SetAvatarURL(_ context.Context, userID, url string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active[userID] {
		return "", domain.ErrNotFound
	}
	prev := m.urls[userID]
	m.urls[userID] = url
	return prev, nil
}

func (m *memAvatarUserStore) ClearAvatarURL(_ context.Context, userID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active[userID] {
		return "", domain.ErrNotFound
	}
	prev := m.urls[userID]
	delete(m.urls, userID)
	return prev, nil
}

func (m *memAvatarUserStore) get(userID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.urls[userID]
}

func jpegBytes(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// TestAvatarOperationalChain drives the full upload → persist → serve → replace
// → remove lifecycle through the real HTTP router, filesystem store and image
// pipeline. It is the operational proof the Code Quality Review asked for.
func TestAvatarOperationalChain(t *testing.T) {
	dir := t.TempDir()
	fsStore, err := storage.NewFilesystemAvatarStore(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	users := newMemAvatarUserStore()
	avatarSvc := service.NewAvatarService(fsStore, users, "/api/auth/avatars")

	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, nil, routerSessionStub{}, nil, avatarSvc, fsStore, allowAllBootstrapAttempts{})

	upload := func(t *testing.T, payload []byte) string {
		t.Helper()
		body, ct := multipartBody(t, "avatar", "photo.jpg", payload)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodPost, body, ct))
		if rr.Code != http.StatusOK {
			t.Fatalf("upload: expected 200, got %d (%s)", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `/api/auth/avatars/`) {
			t.Fatalf("upload response missing same-origin url: %s", rr.Body.String())
		}
		return users.get(avatarUserID)
	}

	// 1) Upload a valid JPEG → same-origin URL persisted.
	url1 := upload(t, jpegBytes(t, 300, 200, color.RGBA{R: 200, G: 40, B: 40, A: 255}))
	if !strings.HasPrefix(url1, "/api/auth/avatars/") || strings.Contains(url1, dir) {
		t.Fatalf("persisted url must be root-relative and leak no path: %q", url1)
	}

	// 2) Serve it: 200, image/png, nosniff, decodable image.
	object := strings.TrimPrefix(url1, "/api/auth/avatars/")
	served := serveAvatar(t, router, object)
	if served.Code != http.StatusOK {
		t.Fatalf("serve: expected 200, got %d", served.Code)
	}
	if served.Header().Get("Content-Type") != domain.AvatarContentType {
		t.Fatalf("serve content-type: %q", served.Header().Get("Content-Type"))
	}
	if served.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("serve missing nosniff")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(served.Body.Bytes()))
	if err != nil || format != "png" {
		t.Fatalf("served bytes not a png: format=%q err=%v", format, err)
	}
	if cfg.Width != domain.AvatarOutputSize || cfg.Height != domain.AvatarOutputSize {
		t.Fatalf("served png wrong size: %dx%d", cfg.Width, cfg.Height)
	}

	// 3) Replace: new URL, old object gone.
	url2 := upload(t, jpegBytes(t, 128, 128, color.RGBA{R: 20, G: 180, B: 60, A: 255}))
	if url2 == url1 {
		t.Fatal("replacement must yield a fresh URL")
	}
	if serveAvatar(t, router, object).Code != http.StatusNotFound {
		t.Fatal("old avatar object must be gone after replacement")
	}

	// 4) Remove: DB cleared, new object gone, fallback is initials (no URL).
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authedAvatarRequest(t, http.MethodDelete, nil, ""))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", rr.Code)
	}
	if users.get(avatarUserID) != "" {
		t.Fatalf("avatar_url should be cleared, got %q", users.get(avatarUserID))
	}
	object2 := strings.TrimPrefix(url2, "/api/auth/avatars/")
	if serveAvatar(t, router, object2).Code != http.StatusNotFound {
		t.Fatal("removed avatar object must be gone")
	}
}

func serveAvatar(t *testing.T, router http.Handler, object string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/avatars/"+object, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}
