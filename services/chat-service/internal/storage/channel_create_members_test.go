package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #1025 statement-shape tests. The properties themselves — atomicity,
// concurrency, idempotency under a real unique index — are proved against
// PostgreSQL in channel_create_members_postgres_test.go; these pin the order of
// the statements and every error branch without a database.

const (
	claimSQL       = `INSERT INTO chat\.channel_creation_requests`
	readClaimSQL   = `SELECT request_hash, channel_id::text`
	replaySQL      = `(?s)FROM chat\.channels c.*channel_visible_to_user`
	initialMembers = `(?s)WITH eligible AS .*INSERT INTO chat\.channel_members.*SELECT count\(\*\) FROM eligible`
)

func keyedPrivateInput(invitees ...string) storage.CreateChannelInput {
	return storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra", Type: domain.ChannelTypePrivate,
		CreatedBy: "user-1", EnsureCreatorMemberRole: domain.ChannelRoleMember,
		InitialMemberIDs: invitees, IdempotencyKey: "key-1", RequestHash: "hash-1",
	}
}

func newMockPool(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
		mock.Close()
	})
	return mock
}

func anyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func privateChannelRow(id string) *pgxmock.Rows {
	now := time.Now()
	return pgxmock.NewRows(channelCols()).
		AddRow(id, "ws-1", "", "infra", "Infra", "private", "active", false, 0, "user-1", now, now)
}

func TestCreateChannelForActiveMember_KeyedCreationClaimsThenCreatesWithClaimedID(t *testing.T) {
	mock := newMockPool(t)
	mock.ExpectBegin()
	mock.ExpectQuery(claimSQL).WithArgs("ws-1", "user-1", "key-1", "hash-1").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id"}).AddRow("ch-claimed"))
	args := authorizedContextArgs()
	args[8] = func() *string { id := "ch-claimed"; return &id }()
	mock.ExpectQuery(`WITH authorized_context`).WithArgs(args...).WillReturnRows(privateChannelRow("ch-claimed"))
	expectConversationCreatedEvent(mock, "ch-claimed", "")
	mock.ExpectExec(`INSERT INTO chat.channel_members`).WithArgs("ch-claimed", "user-1", "member").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(initialMembers).WithArgs("ws-1", "ch-claimed", []string{"u-2", "u-3"}, "member").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectCommit()

	result, err := storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), keyedPrivateInput("u-2", "u-3"))
	if err != nil || result.Replayed || result.Channel.ID != "ch-claimed" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestCreateChannelForActiveMember_ClaimedKeyWithSameHashReplays(t *testing.T) {
	mock := newMockPool(t)
	mock.ExpectBegin()
	mock.ExpectQuery(claimSQL).WithArgs(anyArgs(4)...).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(readClaimSQL).WithArgs("ws-1", "user-1", "key-1").
		WillReturnRows(pgxmock.NewRows([]string{"request_hash", "channel_id"}).AddRow("hash-1", "ch-old"))
	mock.ExpectQuery(replaySQL).WithArgs("ch-old", "ws-1", "user-1").WillReturnRows(privateChannelRow("ch-old"))
	mock.ExpectRollback()

	result, err := storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), keyedPrivateInput("u-2"))
	if err != nil || !result.Replayed || result.Channel.ID != "ch-old" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestCreateChannelForActiveMember_KeyErrors(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]struct {
		expect func(pgxmock.PgxPoolIface)
		want   error
	}{
		"claim fails": {
			expect: func(m pgxmock.PgxPoolIface) { m.ExpectQuery(claimSQL).WithArgs(anyArgs(4)...).WillReturnError(boom) },
			want:   boom,
		},
		"read claim fails": {
			expect: func(m pgxmock.PgxPoolIface) {
				m.ExpectQuery(claimSQL).WithArgs(anyArgs(4)...).WillReturnError(pgx.ErrNoRows)
				m.ExpectQuery(readClaimSQL).WithArgs(anyArgs(3)...).WillReturnError(boom)
			},
			want: boom,
		},
		"same key, other request": {
			expect: func(m pgxmock.PgxPoolIface) {
				m.ExpectQuery(claimSQL).WithArgs(anyArgs(4)...).WillReturnError(pgx.ErrNoRows)
				m.ExpectQuery(readClaimSQL).WithArgs(anyArgs(3)...).
					WillReturnRows(pgxmock.NewRows([]string{"request_hash", "channel_id"}).AddRow("hash-other", "ch-old"))
			},
			want: domain.ErrIdempotencyKeyReused,
		},
		"replay of a channel the actor no longer reads": {
			expect: func(m pgxmock.PgxPoolIface) {
				m.ExpectQuery(claimSQL).WithArgs(anyArgs(4)...).WillReturnError(pgx.ErrNoRows)
				m.ExpectQuery(readClaimSQL).WithArgs(anyArgs(3)...).
					WillReturnRows(pgxmock.NewRows([]string{"request_hash", "channel_id"}).AddRow("hash-1", "ch-old"))
				m.ExpectQuery(replaySQL).WithArgs(anyArgs(3)...).WillReturnError(pgx.ErrNoRows)
			},
			want: domain.ErrForbidden,
		},
		"replay read fails": {
			expect: func(m pgxmock.PgxPoolIface) {
				m.ExpectQuery(claimSQL).WithArgs(anyArgs(4)...).WillReturnError(pgx.ErrNoRows)
				m.ExpectQuery(readClaimSQL).WithArgs(anyArgs(3)...).
					WillReturnRows(pgxmock.NewRows([]string{"request_hash", "channel_id"}).AddRow("hash-1", "ch-old"))
				m.ExpectQuery(replaySQL).WithArgs(anyArgs(3)...).WillReturnError(boom)
			},
			want: boom,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mock := newMockPool(t)
			mock.ExpectBegin()
			tc.expect(mock)
			mock.ExpectRollback()

			result, err := storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), keyedPrivateInput())
			if !errors.Is(err, tc.want) || result.Replayed {
				t.Fatalf("result = %+v, err = %v, want %v", result, err, tc.want)
			}
		})
	}
}

func TestCreateChannelForActiveMember_InitialMemberFailuresRollBack(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]struct {
		rows *pgxmock.Rows
		err  error
		want error
	}{
		"one invitee ineligible": {rows: pgxmock.NewRows([]string{"count"}).AddRow(1), want: domain.ErrForbidden},
		"statement fails":        {err: boom, want: boom},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mock := newMockPool(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`WITH authorized_context`).WithArgs(authorizedContextArgs()...).WillReturnRows(privateChannelRow("ch-1"))
			expectConversationCreatedEvent(mock, "ch-1", "")
			mock.ExpectExec(`INSERT INTO chat.channel_members`).WithArgs(anyArgs(3)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			query := mock.ExpectQuery(initialMembers).WithArgs(anyArgs(4)...)
			if tc.err != nil {
				query.WillReturnError(tc.err)
			} else {
				query.WillReturnRows(tc.rows)
			}
			mock.ExpectRollback()

			input := keyedPrivateInput("u-2", "u-3")
			input.IdempotencyKey = ""
			if _, err := storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), input); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}
