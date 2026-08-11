package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type fakeSidebarPinStore struct {
	pinned  storage.SidebarConversationPin
	err     error
	removed bool
}

func (f *fakeSidebarPinStore) Add(ctx context.Context, in storage.AddSidebarConversationPinInput) (storage.SidebarConversationPin, error) {
	return f.pinned, f.err
}
func (f *fakeSidebarPinStore) Remove(ctx context.Context, workspaceID, userID, conversationType, conversationID string) error {
	f.removed = true
	return f.err
}
func (f *fakeSidebarPinStore) List(ctx context.Context, workspaceID, userID string) ([]storage.SidebarConversationPin, error) {
	return nil, f.err
}

func TestSidebarPinServiceRejectsUnsupportedTypeBeforeStorage(t *testing.T) {
	store := &fakeSidebarPinStore{}
	svc := NewSidebarPinService(store)
	_, err := svc.Pin(context.Background(), SidebarPinInput{WorkspaceID: "ws", UserID: "user", ConversationType: "group", ConversationID: "id"})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected invalid input, got %v", err)
	}
}

func TestSidebarPinServiceReturnsPersistedPin(t *testing.T) {
	want := storage.SidebarConversationPin{ConversationType: "channel", ConversationID: "ch", PinnedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}
	svc := NewSidebarPinService(&fakeSidebarPinStore{pinned: want})
	got, err := svc.Pin(context.Background(), SidebarPinInput{WorkspaceID: "ws", UserID: "user", ConversationType: "channel", ConversationID: "ch"})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestSidebarPinServiceUnpinIsDelegatedIdempotently(t *testing.T) {
	store := &fakeSidebarPinStore{}
	svc := NewSidebarPinService(store)
	if err := svc.Unpin(context.Background(), SidebarPinInput{WorkspaceID: "ws", UserID: "user", ConversationType: "dm", ConversationID: "dm"}); err != nil {
		t.Fatal(err)
	}
	if !store.removed {
		t.Fatal("expected own preference to be removed")
	}
}
