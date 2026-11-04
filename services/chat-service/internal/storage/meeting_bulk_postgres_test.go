package storage_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestMeetingSelectionBulkSemanticsPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	group := &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: mtGroup}
	m := mtStore(t, ctx, store, group, mtDetails(""))
	scope := mtScope(mtOrganizer, m.ID)
	// Explicit partial selection uses the same mutation as future scheduling.
	p, err := store.AddMeetingParticipant(ctx, scope, mtAttendee, domain.MeetingRoleAttendee)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveMeetingParticipant(ctx, scope, mtAttendee); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err := pool.QueryRow(ctx, `SELECT status='active' FROM chat.dm_members WHERE conversation_id=$1 AND user_id=$2`, mtGroup, mtAttendee).Scan(&active); err != nil || !active {
		t.Fatal("meeting removal changed DM membership", err)
	}
	n, err := store.InviteAllMeetingParticipants(ctx, scope)
	if err != nil || n != 1 {
		t.Fatalf("group all %d %v", n, err)
	}
	before := mtSnapshot(t, ctx, pool, m.ID)
	n, err = store.InviteAllMeetingParticipants(ctx, scope)
	if err != nil || n != 0 || before != mtSnapshot(t, ctx, pool, m.ID) {
		t.Fatal("bulk retry changed state", err)
	}
	// A repeated role change preserves the row, including RSVP and timestamps.
	same, err := store.ChangeMeetingParticipantRole(ctx, scope, p.UserID, domain.MeetingRoleAttendee)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.ChangeMeetingParticipantRole(ctx, scope, p.UserID, domain.MeetingRoleAttendee)
	if err != nil || same != again {
		t.Fatal("role no-op rewrote row", err)
	}
	read, _, err := store.GetMeeting(ctx, scope)
	if err != nil || read.Version != 1 || read.UpdatedAt != m.UpdatedAt {
		t.Fatal("roster changed metadata version", err)
	}
	// Cohost invite-all skips existing cohosts without changing their roles.
	ch := mtCreate(t, ctx, store, mtChannelConversation, domain.MeetingJoinInvitedOnly)
	before = mtSnapshot(t, ctx, pool, ch.ID)
	n, err = store.InviteAllMeetingParticipants(ctx, mtScope(mtCohost, ch.ID))
	if err != nil || n != 0 || before != mtSnapshot(t, ctx, pool, ch.ID) {
		t.Fatal("cohost bulk changed existing roles", err)
	}
	for _, actor := range []string{mtAttendee, mtMember, mtForeign} {
		_, err = store.InviteAllMeetingParticipants(ctx, mtScope(actor, ch.ID))
		if actor == mtAttendee {
			mtWant(t, "attendee bulk", err, domain.ErrForbidden)
		} else {
			mtWant(t, "invisible bulk", err, domain.ErrNotFound)
		}
	}
	if _, err = store.TransitionMeeting(ctx, mtScope(mtOrganizer, ch.ID), 1, domain.MeetingStatusCancelled); err != nil {
		t.Fatal(err)
	}
	_, err = store.InviteAllMeetingParticipants(ctx, mtScope(mtOrganizer, ch.ID))
	mtWant(t, "terminal bulk", err, domain.ErrMeetingInvalidState)
	plain := mtStore(t, ctx, store, nil, mtDetails(""))
	_, err = store.InviteAllMeetingParticipants(ctx, mtScope(mtOrganizer, plain.ID))
	mtWant(t, "bulk without conversation", err, domain.ErrInvalidInput)
}

// Count actual statements, including every queued batch statement. Optionally
// fail after the real insert so rollback is proven against PostgreSQL state.
type selectionPool struct {
	storage.Pool
	statements atomic.Int64
	failInsert bool
}
type selectionTx struct {
	pgx.Tx
	pool *selectionPool
}

func (p *selectionPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &selectionTx{tx, p}, nil
}
func (tx *selectionTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.pool.statements.Add(1)
	return tx.Tx.Query(ctx, sql, args...)
}
func (tx *selectionTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.pool.statements.Add(1)
	return tx.Tx.QueryRow(ctx, sql, args...)
}
func (tx *selectionTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	tx.pool.statements.Add(int64(len(b.QueuedQueries)))
	return tx.Tx.SendBatch(ctx, b)
}
func (tx *selectionTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.pool.statements.Add(1)
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	if err == nil && tx.pool.failInsert && strings.Contains(sql, "INSERT INTO chat.meeting_participants") {
		return pgconn.CommandTag{}, errors.New("injected failure after insert")
	}
	return tag, err
}

func TestMeetingSelectionBulkRollbackPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtStore(t, ctx, store, mtChannelConversation, mtDetails(""))
	scope := mtScope(mtOrganizer, m.ID)
	before := mtSnapshot(t, ctx, pool, m.ID)
	fault := &selectionPool{Pool: pool, failInsert: true}
	if _, err := storage.NewPGXMeetingStore(fault).InviteAllMeetingParticipants(ctx, scope); err == nil {
		t.Fatal("injected failure ignored")
	}
	if before != mtSnapshot(t, ctx, pool, m.ID) {
		t.Fatal("failed bulk left partial roster")
	}
	n, err := store.InviteAllMeetingParticipants(ctx, scope)
	if err != nil || n != 2 {
		t.Fatalf("retry after rollback %d %v", n, err)
	}
}

func TestMeetingSelectionConstantQueriesPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	counter := &selectionPool{Pool: pool}
	store := storage.NewPGXMeetingStore(counter)
	scope := selectionScope(domain.MeetingConversationChannel, mtChannel, mtOrganizer)
	_, err := store.ListMeetingCandidates(ctx, scope, storage.MeetingPageQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	small := counter.statements.Swap(0)
	m := mtStore(t, ctx, storage.NewPGXMeetingStore(pool), mtChannelConversation, mtDetails(""))
	_, err = store.InviteAllMeetingParticipants(ctx, mtScope(mtOrganizer, m.ID))
	if err != nil {
		t.Fatal(err)
	}
	bulkSmall := counter.statements.Swap(0)
	seedMeetingChannelMembers(t, ctx, pool, 320)
	_, err = store.ListMeetingCandidates(ctx, scope, storage.MeetingPageQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	large := counter.statements.Swap(0)
	_, err = store.InviteAllMeetingParticipants(ctx, mtScope(mtOrganizer, m.ID))
	if err != nil {
		t.Fatal(err)
	}
	bulkLarge := counter.statements.Load()
	if small != large || bulkSmall != bulkLarge {
		t.Fatalf("N+1: page %d/%d bulk %d/%d", small, large, bulkSmall, bulkLarge)
	}
	t.Logf("2 vs 322 candidates: page %d statements, bulk %d statements", large, bulkLarge)
}

func TestMeetingSelectionCurrentEligibilityPostgreSQL(t *testing.T) {
	for _, tt := range []struct{ name, sql string }{
		{"inactive", `UPDATE auth.users SET status='suspended' WHERE id=$1`},
		{"deleted", `UPDATE auth.users SET deleted_at=now() WHERE id=$1`},
		{"removed workspace", `UPDATE chat.workspace_members SET status='left' WHERE user_id=$1`},
		{"guest", `UPDATE chat.workspace_members SET role='guest' WHERE user_id=$1`},
		{"removed channel", `DELETE FROM chat.channel_members WHERE user_id=$1`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			store := storage.NewPGXMeetingStore(pool)
			if _, err := pool.Exec(ctx, tt.sql, mtAttendee); err != nil {
				t.Fatal(err)
			}
			page, err := store.ListMeetingCandidates(ctx, selectionScope(domain.MeetingConversationChannel, mtChannel, mtOrganizer), storage.MeetingPageQuery{})
			if err != nil || len(page.Items) != 1 || page.Items[0].UserID != mtCohost {
				t.Fatalf("ineligible candidate %+v %v", page, err)
			}
			m := mtStore(t, ctx, store, mtChannelConversation, mtDetails(""))
			_, err = store.AddMeetingParticipant(ctx, mtScope(mtOrganizer, m.ID), mtAttendee, domain.MeetingRoleAttendee)
			mtWant(t, "single ineligible", err, domain.ErrMeetingParticipantNotEligible)
			n, err := store.InviteAllMeetingParticipants(ctx, mtScope(mtOrganizer, m.ID))
			if err != nil || n != 1 {
				t.Fatalf("bulk ineligible %d %v", n, err)
			}
		})
	}
}
