package service

import (
	"context"
	"errors"
	"strings"
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
	messagesErr   error
	usersErr      error
	channelsErr   error
}

func (f *fakeStore) Messages(_ context.Context, _ string, _ string, limit int, cursor domain.MessageCursor) ([]domain.MessageResult, error) {
	f.messageCursor = cursor
	return f.messages, f.messagesErr
}
func (f *fakeStore) Users(_ context.Context, _ string, _ string, limit int, cursor domain.NameCursor) ([]domain.UserResult, error) {
	f.nameCursor = cursor
	return f.users, f.usersErr
}
func (f *fakeStore) Channels(_ context.Context, _ string, _ string, limit int, cursor domain.NameCursor) ([]domain.ChannelResult, error) {
	f.nameCursor = cursor
	return f.channels, f.channelsErr
}

func TestSearchMessagesHandlesEmptyCursorAndStoreFailures(t *testing.T) {
	storeErr := errors.New("database unavailable")
	if page, err := New(&fakeStore{}).SearchMessages(context.Background(), "user", "term", 20, ""); err != nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("empty page=%+v err=%v", page, err)
	}
	if _, err := New(&fakeStore{messagesErr: storeErr}).SearchMessages(context.Background(), "user", "term", 20, ""); err == nil || !errors.Is(err, storeErr) {
		t.Fatalf("store err=%v", err)
	}
	if _, err := New(&fakeStore{}).SearchMessages(context.Background(), "user", "term", 20, "invalid"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("cursor err=%v", err)
	}
}

func TestSearchMessagesRejectsUnencodableNextCursor(t *testing.T) {
	store := &fakeStore{messages: []domain.MessageResult{{ID: "not-a-uuid", Score: 1, CreatedAt: time.Now().UTC()}, {ID: "22222222-2222-4222-8222-222222222222"}}}
	if _, err := New(store).SearchMessages(context.Background(), "user", "term", 1, ""); !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("err=%v", err)
	}
}

func TestSearchUsersPaginatesEmptyAndPropagatesStoreError(t *testing.T) {
	rows := []domain.UserResult{{ID: "11111111-1111-4111-8111-111111111111", SortName: "ana"}, {ID: "22222222-2222-4222-8222-222222222222", SortName: "bia"}}
	page, err := New(&fakeStore{users: rows}).SearchUsers(context.Background(), "user", "ana", 1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err := domain.DecodeNameCursor(page.NextCursor, domain.CursorUsers, "ana"); err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if page, err := New(&fakeStore{}).SearchUsers(context.Background(), "user", "ana", 20, ""); err != nil || len(page.Items) != 0 {
		t.Fatalf("empty page=%+v err=%v", page, err)
	}
	storeErr := errors.New("users failed")
	if _, err := New(&fakeStore{usersErr: storeErr}).SearchUsers(context.Background(), "user", "ana", 20, ""); !errors.Is(err, storeErr) {
		t.Fatalf("err=%v", err)
	}
	badRows := []domain.UserResult{{ID: "bad", SortName: "ana"}, {ID: "22222222-2222-4222-8222-222222222222", SortName: "bia"}}
	if _, err := New(&fakeStore{users: badRows}).SearchUsers(context.Background(), "user", "ana", 1, ""); !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("encode err=%v", err)
	}
}

func TestSearchChannelsCoversPaginationCursorEmptyAndErrors(t *testing.T) {
	rows := []domain.ChannelResult{{ID: "11111111-1111-4111-8111-111111111111", SortName: "geral"}, {ID: "22222222-2222-4222-8222-222222222222", SortName: "produto"}}
	page, err := New(&fakeStore{channels: rows}).SearchChannels(context.Background(), "user", "geral", 1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err := domain.DecodeNameCursor(page.NextCursor, domain.CursorChannels, "geral"); err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if page, err := New(&fakeStore{}).SearchChannels(context.Background(), "user", "geral", 20, ""); err != nil || len(page.Items) != 0 {
		t.Fatalf("empty page=%+v err=%v", page, err)
	}
	storeErr := errors.New("channels failed")
	if _, err := New(&fakeStore{channelsErr: storeErr}).SearchChannels(context.Background(), "user", "geral", 20, ""); !errors.Is(err, storeErr) {
		t.Fatalf("err=%v", err)
	}
	if _, err := New(&fakeStore{}).SearchChannels(context.Background(), "user", "geral", 20, strings.Repeat("x", domain.MaxCursorEncodedBytes+1)); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("cursor err=%v", err)
	}
	badRows := []domain.ChannelResult{{ID: "bad", SortName: "geral"}, {ID: "22222222-2222-4222-8222-222222222222", SortName: "produto"}}
	if _, err := New(&fakeStore{channels: badRows}).SearchChannels(context.Background(), "user", "geral", 1, ""); !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("encode err=%v", err)
	}
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
