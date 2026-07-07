package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

type fakeFavoriteProvider struct {
	favoriteErr   error
	unfavoriteErr error
	listOut       service.ListFavoritesOutput
	listErr       error

	lastFavoriteInput   service.FavoriteMessageInput
	lastUnfavoriteInput service.FavoriteMessageInput
	lastListInput       service.ListFavoritesInput
}

func (f *fakeFavoriteProvider) FavoriteMessage(_ context.Context, in service.FavoriteMessageInput) error {
	f.lastFavoriteInput = in
	return f.favoriteErr
}

func (f *fakeFavoriteProvider) UnfavoriteMessage(_ context.Context, in service.FavoriteMessageInput) error {
	f.lastUnfavoriteInput = in
	return f.unfavoriteErr
}

func (f *fakeFavoriteProvider) ListFavorites(_ context.Context, in service.ListFavoritesInput) (service.ListFavoritesOutput, error) {
	f.lastListInput = in
	return f.listOut, f.listErr
}

func favoriteHandler(fav *fakeFavoriteProvider) *httpapi.MessageHandler {
	return httpapi.NewMessageHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil,
	).WithFavorites(fav)
}

func favoriteRequest(method string) *http.Request {
	r := requestWithUser(method, "/api/chat/messages/"+testMessageID+"/favorite", nil)
	r.SetPathValue("messageID", testMessageID)
	return r
}

// ── FavoriteMessage / UnfavoriteMessage ──────────────────────────────────────

func TestMessageHandler_FavoriteMessage_WithoutServiceReturns503(t *testing.T) {
	h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil)
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"favorite":   h.FavoriteMessage,
		"unfavorite": h.UnfavoriteMessage,
		"list":       h.ListFavorites,
	} {
		rec := httptest.NewRecorder()
		call(rec, favoriteRequest(http.MethodPost))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: expected 503 without favorite service, got %d", name, rec.Code)
		}
	}
}

func TestMessageHandler_FavoriteMessage_InvalidMessageIDReturns400(t *testing.T) {
	h := favoriteHandler(&fakeFavoriteProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/messages/not-a-uuid/favorite", nil)
	r.SetPathValue("messageID", "not-a-uuid")
	h.FavoriteMessage(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestMessageHandler_FavoriteMessage_UnauthenticatedReturns401(t *testing.T) {
	h := favoriteHandler(&fakeFavoriteProvider{})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/chat/messages/"+testMessageID+"/favorite", nil)
	r.SetPathValue("messageID", testMessageID)
	h.FavoriteMessage(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMessageHandler_FavoriteMessage_WorkspaceNotFoundReturns404(t *testing.T) {
	h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{err: domain.ErrNotFound}, &fakeMessageProvider{}, nil).
		WithFavorites(&fakeFavoriteProvider{})
	rec := httptest.NewRecorder()
	h.FavoriteMessage(rec, favoriteRequest(http.MethodPost))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestMessageHandler_FavoriteMessage_SuccessReturns204AndUsesContextUser(t *testing.T) {
	fav := &fakeFavoriteProvider{}
	h := favoriteHandler(fav)
	rec := httptest.NewRecorder()
	h.FavoriteMessage(rec, favoriteRequest(http.MethodPost))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	// Mass-assignment guard: user always comes from the auth context.
	want := service.FavoriteMessageInput{WorkspaceID: testWorkspaceID, UserID: msgTestUserID, MessageID: testMessageID}
	if fav.lastFavoriteInput != want {
		t.Fatalf("expected input %+v, got %+v", want, fav.lastFavoriteInput)
	}
}

func TestMessageHandler_FavoriteMessage_NotFoundAndUnauthorizedAreIdentical404(t *testing.T) {
	h := favoriteHandler(&fakeFavoriteProvider{favoriteErr: domain.ErrNotFound})
	rec := httptest.NewRecorder()
	h.FavoriteMessage(rec, favoriteRequest(http.MethodPost))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestMessageHandler_UnfavoriteMessage_SuccessReturns204(t *testing.T) {
	fav := &fakeFavoriteProvider{}
	h := favoriteHandler(fav)
	rec := httptest.NewRecorder()
	h.UnfavoriteMessage(rec, favoriteRequest(http.MethodDelete))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if fav.lastUnfavoriteInput.UserID != msgTestUserID {
		t.Fatalf("expected context user, got %+v", fav.lastUnfavoriteInput)
	}
}

func TestMessageHandler_UnfavoriteMessage_ServiceErrorReturns500(t *testing.T) {
	h := favoriteHandler(&fakeFavoriteProvider{unfavoriteErr: context.DeadlineExceeded})
	rec := httptest.NewRecorder()
	h.UnfavoriteMessage(rec, favoriteRequest(http.MethodDelete))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

// ── ListFavorites ────────────────────────────────────────────────────────────

func TestMessageHandler_ListFavorites_UnauthenticatedReturns401(t *testing.T) {
	h := favoriteHandler(&fakeFavoriteProvider{})
	rec := httptest.NewRecorder()
	h.ListFavorites(rec, httptest.NewRequest(http.MethodGet, "/api/chat/favorites", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMessageHandler_ListFavorites_InvalidCursorReturns400(t *testing.T) {
	h := favoriteHandler(&fakeFavoriteProvider{})
	rec := httptest.NewRecorder()
	h.ListFavorites(rec, requestWithUser(http.MethodGet, "/api/chat/favorites?before=!!!invalid!!!", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestMessageHandler_ListFavorites_SuccessReturnsCallerScopedPage(t *testing.T) {
	deleted := testMessage()
	deleted.ID = "66666666-6666-6666-6666-666666666666"
	deleted.Status = domain.MessageStatusDeleted
	deleted.BodyText = "secret"
	favoritedAt := time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC)
	fav := &fakeFavoriteProvider{listOut: service.ListFavoritesOutput{
		Favorites: []domain.FavoriteMessage{
			{Message: testMessage(), FavoritedAt: favoritedAt},
			{Message: deleted, FavoritedAt: favoritedAt.Add(-time.Hour)},
		},
		NextCursor: "cursor-123",
	}}
	h := favoriteHandler(fav)
	rec := httptest.NewRecorder()
	h.ListFavorites(rec, requestWithUser(http.MethodGet, "/api/chat/favorites?limit=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fav.lastListInput.UserID != msgTestUserID || fav.lastListInput.Limit != 2 {
		t.Fatalf("expected caller-scoped list input, got %+v", fav.lastListInput)
	}

	body := decodeBody(t, rec)
	data := body["data"].(map[string]any)
	if data["next_cursor"] != "cursor-123" {
		t.Fatalf("expected next_cursor, got %#v", data)
	}
	favorites := data["favorites"].([]any)
	if len(favorites) != 2 {
		t.Fatalf("expected 2 favorites, got %d", len(favorites))
	}
	first := favorites[0].(map[string]any)
	if first["channel_id"] != testChannelID || first["favorited_at"] == "" {
		t.Fatalf("expected channel_id and favorited_at, got %#v", first)
	}
	// RF-14: deleted messages keep the placeholder contract — body suppressed.
	second := favorites[1].(map[string]any)["message"].(map[string]any)
	if second["is_removed"] != true {
		t.Fatalf("expected is_removed for deleted favorite, got %#v", second)
	}
	if _, hasBody := second["body_text"]; hasBody {
		t.Fatalf("deleted favorite must not expose body_text: %#v", second)
	}
}

func TestMessageHandler_ListFavorites_EmptyReturnsEmptyArray(t *testing.T) {
	h := favoriteHandler(&fakeFavoriteProvider{})
	rec := httptest.NewRecorder()
	h.ListFavorites(rec, requestWithUser(http.MethodGet, "/api/chat/favorites", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"favorites":[]`) {
		t.Fatalf("expected favorites: [] in body, got %s", got)
	}
}

func TestMessageHandler_ListFavorites_InvalidCursorFromServiceReturns400(t *testing.T) {
	h := favoriteHandler(&fakeFavoriteProvider{listErr: domain.ErrInvalidCursor})
	rec := httptest.NewRecorder()
	h.ListFavorites(rec, requestWithUser(http.MethodGet, "/api/chat/favorites", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
