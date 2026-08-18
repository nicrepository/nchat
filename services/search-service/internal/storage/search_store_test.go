package storage

import (
	"context"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/search-service/internal/domain"
	"github.com/pashagolub/pgxmock/v2"
)

func TestMessagesFiltersWorkspacePublicChannelsAndUsesFTS(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	created := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	queryPattern := `(?s)c\.type='public'.*c\.status='active'.*m\.status='active'.*m\.search_vector IS NOT NULL.*search_vector @@ search_query\.query`
	mock.ExpectQuery(queryPattern).WithArgs("user-1", "termo", 2, false, nil, nil, nil).WillReturnRows(pgxmock.NewRows([]string{"id", "channel_id", "channel_name", "sender_id", "sender_name", "body_text", "created_at", "score"}).AddRow("m1", "c1", "Geral", "u2", "Ana", "termo", created, 0.8))
	rows, err := NewPGXSearchStore(mock).Messages(context.Background(), "user-1", "termo", 2, domain.MessageCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "m1" || rows[0].Score != 0.8 {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelsQueryCannotReturnPrivateOrArchivedRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(`(?s)ch\.type='public'.*ch\.status='active'`).WithArgs("user-1", "%geral%", 2, false, nil, nil).WillReturnRows(pgxmock.NewRows([]string{"id", "slug", "display_name", "is_general", "sort_name"}).AddRow("c1", "geral", "Geral", true, "geral"))
	rows, err := NewPGXSearchStore(mock).Channels(context.Background(), "user-1", "geral", 2, domain.NameCursor{})
	if err != nil || len(rows) != 1 || rows[0].Slug != "geral" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUsersReturnsOnlyPublicProfileProjection(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT u.id").WithArgs("user-1", "%ana%", 2, false, nil, nil).WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "sort_name"}).AddRow("u1", "Ana", nil, "ana"))
	rows, err := NewPGXSearchStore(mock).Users(context.Background(), "user-1", "ana", 2, domain.NameCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].DisplayName != "Ana" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
