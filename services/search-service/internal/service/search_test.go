package service

import (
	"context"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/search-service/internal/domain"
)

type fakeStore struct {
	messages      []domain.MessageResult
	users         []domain.UserResult
	channels      []domain.ChannelResult
	messageCursor domain.MessageCursor
	nameCursor    domain.NameCursor
}

func (f *fakeStore) Messages(_ context.Context, _ string, _ string, limit int, cursor domain.MessageCursor) ([]domain.MessageResult, error) {
	f.messageCursor = cursor
	return f.messages, nil
}
func (f *fakeStore) Users(_ context.Context, _ string, _ string, limit int, cursor domain.NameCursor) ([]domain.UserResult, error) {
	f.nameCursor = cursor
	return f.users, nil
}
func (f *fakeStore) Channels(_ context.Context, _ string, _ string, limit int, cursor domain.NameCursor) ([]domain.ChannelResult, error) {
	f.nameCursor = cursor
	return f.channels, nil
}

func TestSearchMessagesUsesLimitPlusOneAndBuildsBoundCursor(t *testing.T) {
	store := &fakeStore{messages: []domain.MessageResult{
		{ID: "11111111-1111-4111-8111-111111111111", Score: 2, CreatedAt: time.Now().UTC()},
		{ID: "22222222-2222-4222-8222-222222222222", Score: 1, CreatedAt: time.Now().UTC()},
	}}
	svc := New(store)
	page, err := svc.SearchMessages(context.Background(), "user", "termo", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("unexpected page: %+v", page)
	}
	if _, err := domain.DecodeMessageCursor(page.NextCursor, "outra"); err == nil {
		t.Fatal("next cursor must be bound to query")
	}
}

func TestSearchUsersRejectsChannelCursorBeforeStore(t *testing.T) {
	store := &fakeStore{}
	raw, err := domain.EncodeNameCursor(domain.CursorChannels, "ana", "ana", "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(store).SearchUsers(context.Background(), "user", "ana", 20, raw); err == nil {
		t.Fatal("wrong cursor type must fail")
	}
}
