package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type fakeFavoriteStore struct {
	addErr    error
	removeErr error
	listOut   storage.ListFavoritesResult
	listErr   error

	lastAdd        storage.AddFavoriteInput
	lastRemoveUser string
	lastRemoveMsg  string
	lastList       storage.ListFavoritesInput
}

func (f *fakeFavoriteStore) AddFavorite(_ context.Context, in storage.AddFavoriteInput) error {
	f.lastAdd = in
	return f.addErr
}

func (f *fakeFavoriteStore) RemoveFavorite(_ context.Context, userID, messageID string) error {
	f.lastRemoveUser, f.lastRemoveMsg = userID, messageID
	return f.removeErr
}

func (f *fakeFavoriteStore) ListFavorites(_ context.Context, in storage.ListFavoritesInput) (storage.ListFavoritesResult, error) {
	f.lastList = in
	return f.listOut, f.listErr
}

func validFavoriteInput() service.FavoriteMessageInput {
	return service.FavoriteMessageInput{WorkspaceID: " ws-1 ", UserID: " user-1 ", MessageID: " msg-1 "}
}

func TestFavoriteService_FavoriteMessage_TrimsAndDelegates(t *testing.T) {
	store := &fakeFavoriteStore{}
	svc := service.NewFavoriteService(store)
	if err := svc.FavoriteMessage(context.Background(), validFavoriteInput()); err != nil {
		t.Fatalf("FavoriteMessage: %v", err)
	}
	want := storage.AddFavoriteInput{WorkspaceID: "ws-1", UserID: "user-1", MessageID: "msg-1"}
	if store.lastAdd != want {
		t.Fatalf("expected trimmed input %+v, got %+v", want, store.lastAdd)
	}
}

func TestFavoriteService_FavoriteMessage_MissingFieldsReturnInvalidInput(t *testing.T) {
	svc := service.NewFavoriteService(&fakeFavoriteStore{})
	for _, input := range []service.FavoriteMessageInput{
		{UserID: "u", MessageID: "m"},
		{WorkspaceID: "w", MessageID: "m"},
		{WorkspaceID: "w", UserID: "u"},
		{WorkspaceID: "  ", UserID: "u", MessageID: "m"},
	} {
		if err := svc.FavoriteMessage(context.Background(), input); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("input %+v: expected ErrInvalidInput, got %v", input, err)
		}
		if err := svc.UnfavoriteMessage(context.Background(), input); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("unfavorite input %+v: expected ErrInvalidInput, got %v", input, err)
		}
	}
}

func TestFavoriteService_FavoriteMessage_PropagatesNotFound(t *testing.T) {
	svc := service.NewFavoriteService(&fakeFavoriteStore{addErr: domain.ErrNotFound})
	if err := svc.FavoriteMessage(context.Background(), validFavoriteInput()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestFavoriteService_UnfavoriteMessage_Delegates(t *testing.T) {
	store := &fakeFavoriteStore{}
	svc := service.NewFavoriteService(store)
	if err := svc.UnfavoriteMessage(context.Background(), validFavoriteInput()); err != nil {
		t.Fatalf("UnfavoriteMessage: %v", err)
	}
	if store.lastRemoveUser != "user-1" || store.lastRemoveMsg != "msg-1" {
		t.Fatalf("expected trimmed user/message, got %q %q", store.lastRemoveUser, store.lastRemoveMsg)
	}
}

func TestFavoriteService_ListFavorites_MissingFieldsReturnInvalidInput(t *testing.T) {
	svc := service.NewFavoriteService(&fakeFavoriteStore{})
	for _, input := range []service.ListFavoritesInput{
		{UserID: "u"},
		{WorkspaceID: "w"},
	} {
		if _, err := svc.ListFavorites(context.Background(), input); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("input %+v: expected ErrInvalidInput, got %v", input, err)
		}
	}
}

func TestFavoriteService_ListFavorites_InvalidCursorReturnsTypedError(t *testing.T) {
	svc := service.NewFavoriteService(&fakeFavoriteStore{})
	_, err := svc.ListFavorites(context.Background(), service.ListFavoritesInput{
		WorkspaceID: "ws-1", UserID: "user-1", BeforeCursor: "!!!not-a-cursor!!!",
	})
	if !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("expected ErrInvalidCursor, got %v", err)
	}
}

func TestFavoriteService_ListFavorites_DecodesCursorAndEncodesNext(t *testing.T) {
	favoritedAt := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)
	next := storage.MessageCursor{CreatedAt: favoritedAt, ID: "9e0a54f2-58a4-4c8f-9d1b-111111111111"}
	store := &fakeFavoriteStore{listOut: storage.ListFavoritesResult{
		Favorites:  []domain.FavoriteMessage{{Message: domain.Message{ID: "m1"}, FavoritedAt: favoritedAt}},
		NextCursor: &next,
	}}
	svc := service.NewFavoriteService(store)

	before := storage.EncodeCursor(storage.MessageCursor{
		CreatedAt: favoritedAt.Add(time.Hour), ID: "9e0a54f2-58a4-4c8f-9d1b-222222222222",
	})
	out, err := svc.ListFavorites(context.Background(), service.ListFavoritesInput{
		WorkspaceID: "ws-1", UserID: "user-1", BeforeCursor: before, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListFavorites: %v", err)
	}
	if store.lastList.BeforeCursor == nil || store.lastList.Limit != 10 {
		t.Fatalf("expected decoded cursor and limit, got %+v", store.lastList)
	}
	if out.NextCursor != storage.EncodeCursor(next) {
		t.Fatalf("expected encoded next cursor, got %q", out.NextCursor)
	}
	if len(out.Favorites) != 1 || out.Favorites[0].Message.ID != "m1" {
		t.Fatalf("unexpected favorites: %+v", out.Favorites)
	}
}

func TestFavoriteService_ListFavorites_StoreErrorWrapped(t *testing.T) {
	svc := service.NewFavoriteService(&fakeFavoriteStore{listErr: errors.New("db down")})
	if _, err := svc.ListFavorites(context.Background(), service.ListFavoritesInput{
		WorkspaceID: "ws-1", UserID: "user-1",
	}); err == nil {
		t.Fatal("expected error")
	}
}
