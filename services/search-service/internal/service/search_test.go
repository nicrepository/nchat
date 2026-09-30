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
	legacy        []domain.LegacyMessageResult
	legacyCursor  domain.LegacyMessageCursor
	legacyErr     error
	messages      []domain.MessageResult
	users         []domain.UserResult
	channels      []domain.ChannelResult
	groups        []domain.GroupResult
	files         []domain.FileResult
	messageCursor domain.MessageCursor
	nameCursor    domain.NameCursor
	fileCursor    domain.TimeCursor
	fetchLimit    int
	messagesErr   error
	usersErr      error
	channelsErr   error
	groupsErr     error
	filesErr      error
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

func (f *fakeStore) LegacyMessages(_ context.Context, _ string, _ string, limit int, cursor domain.LegacyMessageCursor) ([]domain.LegacyMessageResult, error) {
	f.legacyCursor = cursor
	f.fetchLimit = limit
	return f.legacy, f.legacyErr
}
func (f *fakeStore) Groups(_ context.Context, _ string, _ string, limit int, cursor domain.NameCursor) ([]domain.GroupResult, error) {
	f.nameCursor = cursor
	f.fetchLimit = limit
	return f.groups, f.groupsErr
}
func (f *fakeStore) Files(_ context.Context, _ string, _ string, limit int, cursor domain.TimeCursor) ([]domain.FileResult, error) {
	f.fileCursor = cursor
	f.fetchLimit = limit
	return f.files, f.filesErr
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

func TestSearchGroupsPaginatesWithItsOwnCursor(t *testing.T) {
	rows := []domain.GroupResult{{ID: "11111111-1111-4111-8111-111111111111", SortName: "alfa"}, {ID: "22222222-2222-4222-8222-222222222222", SortName: "beta"}}
	store := &fakeStore{groups: rows}
	page, err := New(store).SearchGroups(context.Background(), "user", "a", 1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" || store.fetchLimit != 2 {
		t.Fatalf("page=%+v err=%v limit=%d", page, err, store.fetchLimit)
	}
	if _, err := New(store).SearchGroups(context.Background(), "user", "a", 1, page.NextCursor); err != nil || store.nameCursor.Name != "alfa" {
		t.Fatalf("cursor not forwarded: %+v err=%v", store.nameCursor, err)
	}
	channelCursor, _ := domain.EncodeNameCursor(domain.CursorChannels, "a", "alfa", rows[0].ID)
	if _, err := New(store).SearchGroups(context.Background(), "user", "a", 1, channelCursor); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("channel cursor accepted for groups: %v", err)
	}
	storeErr := errors.New("groups failed")
	if _, err := New(&fakeStore{groupsErr: storeErr}).SearchGroups(context.Background(), "user", "a", 1, ""); !errors.Is(err, storeErr) {
		t.Fatalf("err=%v", err)
	}
}

func TestSearchFilesPaginatesNewestFirstWithBoundCursor(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	rows := []domain.FileResult{{ID: "11111111-1111-4111-8111-111111111111", CreatedAt: created}, {ID: "22222222-2222-4222-8222-222222222222", CreatedAt: created}}
	store := &fakeStore{files: rows}
	page, err := New(store).SearchFiles(context.Background(), "user", "backup", 1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err := New(store).SearchFiles(context.Background(), "user", "backup", 1, page.NextCursor); err != nil || store.fileCursor.ID != rows[0].ID {
		t.Fatalf("cursor not forwarded: %+v err=%v", store.fileCursor, err)
	}
	if _, err := New(store).SearchFiles(context.Background(), "user", "outra", 1, page.NextCursor); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("cursor crossed queries: %v", err)
	}
	storeErr := errors.New("files failed")
	if _, err := New(&fakeStore{filesErr: storeErr}).SearchFiles(context.Background(), "user", "backup", 1, ""); !errors.Is(err, storeErr) {
		t.Fatalf("err=%v", err)
	}
}

// Every page of one search is ranked against the first page's clock, even when
// the wall clock has moved on between requests.
func TestSearchMessagesPinsTheRankingClockAcrossPages(t *testing.T) {
	first := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{messages: []domain.MessageResult{
		{ID: "11111111-1111-4111-8111-111111111111", Score: 2, CreatedAt: first},
		{ID: "22222222-2222-4222-8222-222222222222", Score: 1, CreatedAt: first},
	}}
	svc := New(store)
	svc.now = func() time.Time { return first }
	page, err := svc.SearchMessages(context.Background(), "user", "termo", 1, "")
	if err != nil || !store.messageCursor.RankedAt.Equal(first) {
		t.Fatalf("first page clock=%v err=%v", store.messageCursor.RankedAt, err)
	}
	svc.now = func() time.Time { return first.Add(time.Hour) }
	if _, err := svc.SearchMessages(context.Background(), "user", "termo", 1, page.NextCursor); err != nil || !store.messageCursor.RankedAt.Equal(first) {
		t.Fatalf("second page clock=%v err=%v", store.messageCursor.RankedAt, err)
	}
}

// The legacy and V2 message searches cover different sets, so neither accepts
// the other's cursor.
func TestLegacyAndV2MessageCursorsDoNotCross(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		legacy: []domain.LegacyMessageResult{
			{ID: "11111111-1111-4111-8111-111111111111", Score: 2, CreatedAt: created},
			{ID: "22222222-2222-4222-8222-222222222222", Score: 1, CreatedAt: created},
		},
		messages: []domain.MessageResult{
			{ID: "11111111-1111-4111-8111-111111111111", Score: 2, CreatedAt: created},
			{ID: "22222222-2222-4222-8222-222222222222", Score: 1, CreatedAt: created},
		},
	}
	svc := New(store)
	legacy, err := svc.SearchLegacyMessages(context.Background(), "user", "termo", 1, "")
	if err != nil || len(legacy.Items) != 1 || legacy.NextCursor == "" || store.fetchLimit != 2 {
		t.Fatalf("legacy page=%+v err=%v", legacy, err)
	}
	if _, err := svc.SearchLegacyMessages(context.Background(), "user", "termo", 1, legacy.NextCursor); err != nil || store.legacyCursor.Score != 2 {
		t.Fatalf("legacy cursor not honoured: %+v err=%v", store.legacyCursor, err)
	}
	v2, err := svc.SearchMessages(context.Background(), "user", "termo", 1, "")
	if err != nil || v2.NextCursor == "" {
		t.Fatalf("v2 page=%+v err=%v", v2, err)
	}
	if _, err := svc.SearchMessages(context.Background(), "user", "termo", 1, legacy.NextCursor); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("legacy cursor accepted by v2: %v", err)
	}
	if _, err := svc.SearchLegacyMessages(context.Background(), "user", "termo", 1, v2.NextCursor); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("v2 cursor accepted by legacy: %v", err)
	}
	storeErr := errors.New("legacy failed")
	if _, err := New(&fakeStore{legacyErr: storeErr}).SearchLegacyMessages(context.Background(), "user", "termo", 1, ""); !errors.Is(err, storeErr) {
		t.Fatalf("err=%v", err)
	}
}
