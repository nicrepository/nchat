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

// Presence storage at the statement level (issue #798). What the SQL means
// against a real database is proven in presence_store_postgres_test.go; these
// prove the arguments, the scoping and how each outcome is reported.

var errPresenceDB = errors.New("database unavailable")

func TestPGXPresenceStore_SetManualIsMembershipScopedAndServerStamped(t *testing.T) {
	mock := newMock(t)
	expires := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	stamped := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)INSERT INTO chat\.user_presence.*clock_timestamp\(\).*FROM chat\.workspace_members wm.*wm\.status = 'active'.*ON CONFLICT \(workspace_id, user_id\)`).
		WithArgs("ws-1", "user-1", "dnd", expires).
		WillReturnRows(pgxmock.NewRows([]string{"manual_state", "manual_expires_at", "manual_updated_at"}).
			AddRow("dnd", expires, stamped))

	got, err := storage.NewPGXPresenceStore(mock).SetManual(context.Background(), "ws-1", "user-1", domain.PresenceManualDoNotDisturb, expires)
	if err != nil {
		t.Fatalf("SetManual: %v", err)
	}
	want := domain.PresenceOverride{State: domain.PresenceManualDoNotDisturb, ExpiresAt: expires, UpdatedAt: stamped}
	if got != want {
		t.Fatalf("SetManual = %+v, want %+v", got, want)
	}
}

func TestPGXPresenceStore_SetManualRefusesANonMember(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`INSERT INTO chat\.user_presence`).
		WithArgs("ws-1", "user-1", "busy", pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	_, err := storage.NewPGXPresenceStore(mock).SetManual(context.Background(), "ws-1", "user-1", domain.PresenceManualBusy, time.Now())
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestPGXPresenceStore_SetManualReportsAFailure(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`INSERT INTO chat\.user_presence`).WithArgs(anyArgs(4)...).WillReturnError(errPresenceDB)
	_, err := storage.NewPGXPresenceStore(mock).SetManual(context.Background(), "ws-1", "user-1", domain.PresenceManualBusy, time.Now())
	if !errors.Is(err, errPresenceDB) {
		t.Fatalf("err = %v", err)
	}
}

func TestPGXPresenceStore_ClearManual(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`(?s)UPDATE chat\.user_presence.*manual_state = NULL.*WHERE workspace_id = \$1::uuid AND user_id = \$2::uuid`).
		WithArgs("ws-1", "user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE chat\.user_presence`).WithArgs(anyArgs(2)...).WillReturnError(errPresenceDB)

	store := storage.NewPGXPresenceStore(mock)
	if err := store.ClearManual(context.Background(), "ws-1", "user-1"); err != nil {
		t.Fatalf("ClearManual: %v", err)
	}
	if err := store.ClearManual(context.Background(), "ws-1", "user-1"); !errors.Is(err, errPresenceDB) {
		t.Fatalf("err = %v", err)
	}
}

func TestPGXPresenceStore_ManualReadsOnlyALiveState(t *testing.T) {
	mock := newMock(t)
	expires := time.Now().Add(time.Hour).UTC()
	columns := []string{"manual_state", "manual_expires_at", "manual_updated_at"}
	mock.ExpectQuery(`(?s)FROM chat\.user_presence.*manual_expires_at > clock_timestamp\(\)`).
		WithArgs("ws-1", "user-1").WillReturnRows(pgxmock.NewRows(columns).AddRow("brb", expires, expires))
	mock.ExpectQuery(`FROM chat\.user_presence`).WithArgs("ws-1", "user-1").WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM chat\.user_presence`).WithArgs("ws-1", "user-1").WillReturnError(errPresenceDB)

	store := storage.NewPGXPresenceStore(mock)
	if got, err := store.Manual(context.Background(), "ws-1", "user-1"); err != nil || got.State != domain.PresenceManualBeRightBack {
		t.Fatalf("Manual = %+v, %v", got, err)
	}
	if got, err := store.Manual(context.Background(), "ws-1", "user-1"); err != nil || got != (domain.PresenceOverride{}) {
		t.Fatalf("no row = %+v, %v; want automatic", got, err)
	}
	if _, err := store.Manual(context.Background(), "ws-1", "user-1"); !errors.Is(err, errPresenceDB) {
		t.Fatalf("err = %v", err)
	}
}

func TestPGXPresenceStore_ContextsIsOneStatementForEveryUser(t *testing.T) {
	mock := newMock(t)
	expires := time.Now().Add(time.Hour).UTC()
	lease := expires.Add(-30 * time.Minute)
	columns := []string{"user_id", "manual_state", "manual_expires_at", "manual_updated_at", "direct_call", "lease_until"}
	mock.ExpectQuery(`(?s)chat\.call_participant_leases.*FROM unnest\(\$2::uuid\[\]\).*LEFT JOIN chat\.user_presence p`).
		WithArgs("ws-1", []string{"u-dnd", "u-call", "u-lease", "u-none"}).
		WillReturnRows(pgxmock.NewRows(columns).
			AddRow("u-dnd", ptr("dnd"), &expires, &expires, false, nil).
			AddRow("u-call", nil, nil, nil, true, &lease).
			AddRow("u-lease", nil, nil, nil, false, &lease).
			AddRow("u-none", nil, nil, nil, false, nil))

	got, err := storage.NewPGXPresenceStore(mock).Contexts(context.Background(), "ws-1", []string{"u-dnd", "u-call", "u-lease", "u-none"})
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	if got["u-dnd"].Override.State != domain.PresenceManualDoNotDisturb || got["u-dnd"].Activity != "" {
		t.Fatalf("u-dnd = %+v", got["u-dnd"])
	}
	// A direct call is not timed, whatever leases the person also holds.
	if got["u-call"].Activity != domain.PresenceActivityInCall || got["u-call"].Override.State != "" || !got["u-call"].ActivityUntil.IsZero() {
		t.Fatalf("u-call = %+v", got["u-call"])
	}
	// A lease-backed participation holds until its latest live lease ends.
	if got["u-lease"].Activity != domain.PresenceActivityInCall || !got["u-lease"].ActivityUntil.Equal(lease) {
		t.Fatalf("u-lease = %+v", got["u-lease"])
	}
	if _, present := got["u-none"]; present {
		t.Fatalf("a user with no context is in the answer: %+v", got)
	}
}

func TestPGXPresenceStore_ContextsReportsFailures(t *testing.T) {
	store := storage.NewPGXPresenceStore(newMock(t))
	if got, err := store.Contexts(context.Background(), "ws-1", nil); err != nil || len(got) != 0 {
		t.Fatalf("empty = %+v, %v", got, err)
	}

	mock := newMock(t)
	mock.ExpectQuery(`FROM unnest`).WithArgs(anyArgs(2)...).WillReturnError(errPresenceDB)
	if _, err := storage.NewPGXPresenceStore(mock).Contexts(context.Background(), "ws-1", []string{"u"}); !errors.Is(err, errPresenceDB) {
		t.Fatalf("query err = %v", err)
	}

	mock = newMock(t)
	mock.ExpectQuery(`FROM unnest`).WithArgs(anyArgs(2)...).WillReturnRows(pgxmock.NewRows([]string{"user_id"}).AddRow("u"))
	if _, err := storage.NewPGXPresenceStore(mock).Contexts(context.Background(), "ws-1", []string{"u"}); err == nil {
		t.Fatal("a malformed row was accepted")
	}

	mock = newMock(t)
	mock.ExpectQuery(`FROM unnest`).WithArgs(anyArgs(2)...).WillReturnRows(
		pgxmock.NewRows([]string{"user_id", "manual_state", "manual_expires_at", "manual_updated_at", "direct_call", "lease_until"}).
			AddRow("u", nil, nil, nil, false, nil).RowError(0, errPresenceDB))
	if _, err := storage.NewPGXPresenceStore(mock).Contexts(context.Background(), "ws-1", []string{"u"}); !errors.Is(err, errPresenceDB) {
		t.Fatalf("iteration err = %v", err)
	}
}

func TestPGXPresenceStore_DoNotDisturbUsers(t *testing.T) {
	store := storage.NewPGXPresenceStore(newMock(t))
	if got, err := store.DoNotDisturbUsers(context.Background(), "ws-1", nil); err != nil || len(got) != 0 {
		t.Fatalf("empty = %+v, %v", got, err)
	}

	mock := newMock(t)
	mock.ExpectQuery(`(?s)FROM chat\.user_presence.*user_id = ANY\(\$2::uuid\[\]\).*manual_state = 'dnd' AND manual_expires_at > clock_timestamp\(\)`).
		WithArgs("ws-1", []string{"a", "b"}).
		WillReturnRows(pgxmock.NewRows([]string{"user_id"}).AddRow("b"))
	got, err := storage.NewPGXPresenceStore(mock).DoNotDisturbUsers(context.Background(), "ws-1", []string{"a", "b"})
	if err != nil || !got["b"] || got["a"] {
		t.Fatalf("DoNotDisturbUsers = %+v, %v", got, err)
	}

	mock = newMock(t)
	mock.ExpectQuery(`FROM chat\.user_presence`).WithArgs(anyArgs(2)...).WillReturnError(errPresenceDB)
	if _, err := storage.NewPGXPresenceStore(mock).DoNotDisturbUsers(context.Background(), "ws-1", []string{"a"}); !errors.Is(err, errPresenceDB) {
		t.Fatalf("err = %v", err)
	}

	mock = newMock(t)
	mock.ExpectQuery(`FROM chat\.user_presence`).WithArgs(anyArgs(2)...).WillReturnRows(pgxmock.NewRows([]string{"user_id"}).AddRow("a").RowError(0, errPresenceDB))
	if _, err := storage.NewPGXPresenceStore(mock).DoNotDisturbUsers(context.Background(), "ws-1", []string{"a"}); !errors.Is(err, errPresenceDB) {
		t.Fatalf("iteration err = %v", err)
	}

	mock = newMock(t)
	mock.ExpectQuery(`FROM chat\.user_presence`).WithArgs(anyArgs(2)...).WillReturnRows(pgxmock.NewRows([]string{"user_id", "extra"}).AddRow("a", 1))
	if _, err := storage.NewPGXPresenceStore(mock).DoNotDisturbUsers(context.Background(), "ws-1", []string{"a"}); err == nil {
		t.Fatal("a malformed row was accepted")
	}
}

func TestPGXPresenceStore_LastSeen(t *testing.T) {
	at := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)
	mock := newMock(t)
	mock.ExpectExec(`(?s)INSERT INTO chat\.user_presence \(workspace_id, user_id, last_seen_at\).*GREATEST`).
		WithArgs("ws-1", "user-1", at).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO chat\.user_presence`).WithArgs(anyArgs(3)...).WillReturnError(errPresenceDB)
	mock.ExpectQuery(`SELECT last_seen_at`).WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"last_seen_at"}).AddRow(&at))
	mock.ExpectQuery(`SELECT last_seen_at`).WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"last_seen_at"}).AddRow(nil))
	mock.ExpectQuery(`SELECT last_seen_at`).WithArgs("ws-1", "user-1").WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT last_seen_at`).WithArgs("ws-1", "user-1").WillReturnError(errPresenceDB)

	store := storage.NewPGXPresenceStore(mock)
	ctx := context.Background()
	if err := store.MarkLastSeen(ctx, "ws-1", "user-1", at); err != nil {
		t.Fatalf("MarkLastSeen: %v", err)
	}
	if err := store.MarkLastSeen(ctx, "ws-1", "user-1", at); !errors.Is(err, errPresenceDB) {
		t.Fatalf("MarkLastSeen err = %v", err)
	}
	if got, found, err := store.LastSeen(ctx, "ws-1", "user-1"); err != nil || !found || !got.Equal(at) {
		t.Fatalf("LastSeen = %v %v %v", got, found, err)
	}
	if _, found, err := store.LastSeen(ctx, "ws-1", "user-1"); err != nil || found {
		t.Fatalf("NULL last seen = %v %v", found, err)
	}
	if _, found, err := store.LastSeen(ctx, "ws-1", "user-1"); err != nil || found {
		t.Fatalf("no row = %v %v", found, err)
	}
	if _, _, err := store.LastSeen(ctx, "ws-1", "user-1"); !errors.Is(err, errPresenceDB) {
		t.Fatalf("LastSeen err = %v", err)
	}
}

func ptr[T any](value T) *T { return &value }

func anyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}
