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

var messageColumns = []string{"id", "conversation_kind", "conversation_id", "conversation_type", "conversation_name", "sender_id", "sender_name", "avatar_url", "body_text", "created_at", "score"}

// The SQL shape is asserted here only as far as the authorization joins go;
// what they admit is proven against a real database in the postgres suite.
func TestMessagesReadEveryConversationThroughTheVisibilityJoins(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	created := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	queryPattern := `(?s)visible_channels AS.*visible_dms AS.*LEFT JOIN visible_channels vc ON vc\.id=m\.channel_id.*LEFT JOIN visible_dms vd ON vd\.id=m\.dm_conversation_id.*m\.status='active'.*m\.search_vector @@ search_query\.query AND \(vc\.id IS NOT NULL OR vd\.id IS NOT NULL\)`
	mock.ExpectQuery(queryPattern).WithArgs("user-1", "termo", 2, false, nil, nil, nil, time.Time{}).WillReturnRows(pgxmock.NewRows(messageColumns).AddRow("m1", "dm", "d1", "group", "Projeto", "u2", "Ana", nil, "termo", created, 0.8))
	rows, err := NewPGXSearchStore(mock).Messages(context.Background(), "user-1", "termo", 2, domain.MessageCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ConversationKind != "dm" || rows[0].ConversationType != "group" || rows[0].Score != 0.8 {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesPassesCursorValues(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	c := domain.MessageCursor{Version: 1, Score: 0.5, CreatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), ID: "22222222-2222-4222-8222-222222222222", RankedAt: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)}
	mock.ExpectQuery("WITH search_scope").WithArgs("user-1", "termo", 2, true, c.Score, c.CreatedAt, c.ID, c.RankedAt).WillReturnRows(pgxmock.NewRows(messageColumns))
	if _, err := NewPGXSearchStore(mock).Messages(context.Background(), "user-1", "termo", 2, c); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesPropagatesQueryScanAndIterationFailures(t *testing.T) {
	testStoreFailures(t, "messages", func(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows, queryErr error) error {
		expect := mock.ExpectQuery("WITH search_scope").WithArgs("user-1", "term", 2, false, nil, nil, nil, time.Time{})
		if queryErr != nil {
			expect.WillReturnError(queryErr)
		} else {
			expect.WillReturnRows(rows)
		}
		_, err := NewPGXSearchStore(mock).Messages(context.Background(), "user-1", "term", 2, domain.MessageCursor{})
		return err
	}, messageColumns, []any{"m1", "channel", "c1", "public", "Geral", "u1", "Ana", nil, "body", time.Now(), 0.8})
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
	}, channelColumns, []any{"c1", "general", "General", "public", nil, 3, true, "general"})
}

var channelColumns = []string{"id", "slug", "display_name", "type", "description", "member_count", "is_general", "sort_name"}
var groupColumns = []string{"id", "title", "participant_count", "last_message_at", "sort_name"}
var fileColumns = []string{"id", "filename", "content_type", "size", "status", "preview_status", "message_id", "conversation_kind", "conversation_id", "conversation_type", "conversation_name", "created_at"}

func TestGroupsPropagatesQueryScanAndIterationFailures(t *testing.T) {
	testStoreFailures(t, "groups", func(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows, queryErr error) error {
		expect := mock.ExpectQuery(`(?s)search_scope AS MATERIALIZED.*visible_dms AS MATERIALIZED.*vd\.type='group'`).WithArgs("user-1", "%term%", 2, false, nil, nil)
		if queryErr != nil {
			expect.WillReturnError(queryErr)
		} else {
			expect.WillReturnRows(rows)
		}
		_, err := NewPGXSearchStore(mock).Groups(context.Background(), "user-1", "term", 2, domain.NameCursor{})
		return err
	}, groupColumns, []any{"g1", "Projeto", 4, nil, "projeto"})
}

func TestFilesPropagatesQueryScanAndIterationFailures(t *testing.T) {
	testStoreFailures(t, "files", func(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows, queryErr error) error {
		expect := mock.ExpectQuery(`(?s)search_scope AS MATERIALIZED.*visible_channels AS MATERIALIZED.*visible_dms AS MATERIALIZED.*FROM visible_channels v\s+CROSS JOIN LATERAL \(\s+SELECT a\.\* FROM files\.attachments a\s+WHERE a\.workspace_id=v\.workspace_id AND a\.destination_kind='channel' AND a\.channel_id=v\.id.*LIKE \$2 ESCAPE '\\'\s+OFFSET 0.*m\.channel_id=v\.id.*UNION ALL.*FROM visible_dms v\s+CROSS JOIN LATERAL.*a\.destination_kind='dm' AND a\.conversation_id=v\.id.*OFFSET 0.*m\.dm_conversation_id=v\.id`).WithArgs("user-1", "%term%", 2, false, nil, nil)
		if queryErr != nil {
			expect.WillReturnError(queryErr)
		} else {
			expect.WillReturnRows(rows)
		}
		_, err := NewPGXSearchStore(mock).Files(context.Background(), "user-1", "term", 2, domain.TimeCursor{})
		return err
	}, fileColumns, []any{"f1", "a.pdf", "application/pdf", int64(10), "clean", "ready", "m1", "channel", "c1", "public", "Geral", time.Now()})
}

func TestFilesPassesCursorValues(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	c := domain.TimeCursor{Version: 1, CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), ID: "22222222-2222-4222-8222-222222222222"}
	mock.ExpectQuery("WITH search_scope").WithArgs("user-1", "%a%", 2, true, c.CreatedAt, c.ID).WillReturnRows(pgxmock.NewRows(fileColumns))
	if _, err := NewPGXSearchStore(mock).Files(context.Background(), "user-1", "a", 2, c); err != nil {
		t.Fatal(err)
	}
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

func TestChannelsReadOnlyVisibleChannels(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(`(?s)c\.status='active'.*cm\.user_id=\$1.*c\.type='public' AND scope\.role IN.*FROM visible_channels vc`).WithArgs("user-1", "%geral%", 2, false, nil, nil).WillReturnRows(pgxmock.NewRows(channelColumns).AddRow("c1", "geral", "Geral", "public", nil, 12, true, "geral"))
	rows, err := NewPGXSearchStore(mock).Channels(context.Background(), "user-1", "geral", 2, domain.NameCursor{})
	if err != nil || len(rows) != 1 || rows[0].Slug != "geral" || rows[0].MemberCount != 12 {
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

// Messages keep the authorization CTEs inlined: the GIN index narrows the
// rows there, and materializing measured slower (docs/api/search.md).
func TestMessagesKeepTheScopeInlined(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(`^WITH search_scope AS \(`).WithArgs("user-1", "termo", 2, false, nil, nil, nil, time.Time{}).WillReturnRows(pgxmock.NewRows(messageColumns))
	if _, err := NewPGXSearchStore(mock).Messages(context.Background(), "user-1", "termo", 2, domain.MessageCursor{}); err != nil {
		t.Fatal(err)
	}
}

var legacyColumns = []string{"id", "channel_id", "channel_name", "sender_id", "sender_name", "body_text", "created_at", "score"}

// The legacy search reads public channels only, through the shared
// visibility CTE, with the pre-#900 ranking (3-argument rank, no clock).
func TestLegacyMessagesReadPublicChannelsOnly(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	pattern := `(?s)^WITH search_scope AS \(.*visible_channels AS \(.*chat\.message_search_rank\(m\.search_vector,search_query\.query,m\.created_at\) AS score\s+FROM chat\.messages m JOIN visible_channels vc ON vc\.id=m\.channel_id AND vc\.type='public'\s+JOIN auth\.users u`
	created := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(pattern).WithArgs("user-1", "termo", 2, false, nil, nil, nil).WillReturnRows(pgxmock.NewRows(legacyColumns).AddRow("m1", "c1", "Geral", "u2", "Ana", "termo", created, 0.8))
	rows, err := NewPGXSearchStore(mock).LegacyMessages(context.Background(), "user-1", "termo", 2, domain.LegacyMessageCursor{})
	if err != nil || len(rows) != 1 || rows[0].ChannelID != "c1" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	c := domain.LegacyMessageCursor{Version: 1, Score: 0.5, CreatedAt: created, ID: "22222222-2222-4222-8222-222222222222"}
	mock.ExpectQuery("WITH search_scope").WithArgs("user-1", "termo", 2, true, c.Score, c.CreatedAt, c.ID).WillReturnRows(pgxmock.NewRows(legacyColumns))
	if _, err := NewPGXSearchStore(mock).LegacyMessages(context.Background(), "user-1", "termo", 2, c); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyMessagesPropagatesQueryScanAndIterationFailures(t *testing.T) {
	testStoreFailures(t, "legacy messages", func(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows, queryErr error) error {
		expect := mock.ExpectQuery("WITH search_scope").WithArgs("user-1", "term", 2, false, nil, nil, nil)
		if queryErr != nil {
			expect.WillReturnError(queryErr)
		} else {
			expect.WillReturnRows(rows)
		}
		_, err := NewPGXSearchStore(mock).LegacyMessages(context.Background(), "user-1", "term", 2, domain.LegacyMessageCursor{})
		return err
	}, legacyColumns, []any{"m1", "c1", "Geral", "u1", "Ana", "body", time.Now(), 0.8})
}
