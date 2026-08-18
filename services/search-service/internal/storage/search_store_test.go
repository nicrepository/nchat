package storage

import (
	"context"
	"errors"
	"strings"
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

func TestMessagesPropagatesQueryScanAndIterationFailures(t *testing.T) {
	testStoreFailures(t, "messages", func(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows, queryErr error) error {
		expect := mock.ExpectQuery("WITH search_scope").WithArgs("user-1", "term", 2, false, nil, nil, nil)
		if queryErr != nil {
			expect.WillReturnError(queryErr)
		} else {
			expect.WillReturnRows(rows)
		}
		_, err := NewPGXSearchStore(mock).Messages(context.Background(), "user-1", "term", 2, domain.MessageCursor{})
		return err
	}, []string{"id", "channel_id", "channel_name", "sender_id", "sender_name", "body_text", "created_at", "score"}, []any{"m1", "c1", "Geral", "u1", "Ana", "body", time.Now(), 0.8})
}

func TestUsersPropagatesQueryScanAndIterationFailures(t *testing.T) {
	testStoreFailures(t, "users", func(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows, queryErr error) error {
		expect := mock.ExpectQuery("SELECT u.id").WithArgs("user-1", "%term%", 2, false, nil, nil)
		if queryErr != nil {
			expect.WillReturnError(queryErr)
		} else {
			expect.WillReturnRows(rows)
		}
		_, err := NewPGXSearchStore(mock).Users(context.Background(), "user-1", "term", 2, domain.NameCursor{})
		return err
	}, []string{"id", "display_name", "avatar_url", "sort_name"}, []any{"u1", "Ana", nil, "ana"})
}

func TestChannelsPropagatesQueryScanAndIterationFailures(t *testing.T) {
	testStoreFailures(t, "channels", func(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows, queryErr error) error {
		expect := mock.ExpectQuery("WITH search_scope").WithArgs("user-1", "%term%", 2, false, nil, nil)
		if queryErr != nil {
			expect.WillReturnError(queryErr)
		} else {
			expect.WillReturnRows(rows)
		}
		_, err := NewPGXSearchStore(mock).Channels(context.Background(), "user-1", "term", 2, domain.NameCursor{})
		return err
	}, []string{"id", "slug", "display_name", "is_general", "sort_name"}, []any{"c1", "general", "General", true, "general"})
}

func testStoreFailures(t *testing.T, operation string, call func(pgxmock.PgxPoolIface, *pgxmock.Rows, error) error, columns []string, values []any) {
	t.Helper()
	t.Run("query", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		if err := call(mock, nil, errors.New("query failed")); err == nil || !strings.Contains(err.Error(), "query "+operation) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("scan", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		bad := append([]any(nil), values...)
		bad[len(bad)-1] = struct{}{}
		if err := call(mock, pgxmock.NewRows(columns).AddRow(bad...), nil); err == nil || !strings.Contains(err.Error(), "scan ") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestNameCursorValuesArePassedToQueries(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	cursor := domain.NameCursor{Version: 1, Name: "ana", ID: "22222222-2222-4222-8222-222222222222"}
	mock.ExpectQuery("SELECT u.id").WithArgs("user-1", "%ana%", 2, true, cursor.Name, cursor.ID).WillReturnRows(
		pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "sort_name"}),
	)
	if _, err := NewPGXSearchStore(mock).Users(context.Background(), "user-1", "ana", 2, cursor); err != nil {
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
