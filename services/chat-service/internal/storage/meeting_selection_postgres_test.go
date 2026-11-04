package storage_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func selectionScope(kind domain.MeetingConversationKind, id, actor string) storage.MeetingCandidateScope {
	return storage.MeetingCandidateScope{WorkspaceID: mtWS, ActorID: actor, Conversation: domain.MeetingConversation{Kind: kind, ID: id}}
}

func TestMeetingSelectionContextsPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	for _, tt := range []struct {
		name  string
		scope storage.MeetingCandidateScope
		want  int
		err   error
	}{
		{"group", selectionScope(domain.MeetingConversationDM, mtGroup, mtOrganizer), 1, nil},
		{"private channel", selectionScope(domain.MeetingConversationChannel, mtChannel, mtOrganizer), 2, nil},
		{"private denied", selectionScope(domain.MeetingConversationChannel, mtChannel, mtMember), 0, domain.ErrNotFound},
		{"foreign", selectionScope(domain.MeetingConversationChannel, mtForeignCh, mtOrganizer), 0, domain.ErrNotFound},
		{"guest", selectionScope(domain.MeetingConversationChannel, mtChannel, mtGuest), 0, domain.ErrForbidden},
		{"suspended", selectionScope(domain.MeetingConversationChannel, mtChannel, mtSuspended), 0, domain.ErrForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			page, err := store.ListMeetingCandidates(ctx, tt.scope, storage.MeetingPageQuery{})
			mtWant(t, tt.name, err, tt.err)
			if tt.err == nil && len(page.Items) != tt.want {
				t.Fatalf("%+v", page)
			}
		})
	}
	// A public channel remains a contextual membership list, not workspace-wide.
	if _, err := pool.Exec(ctx, `UPDATE chat.channels SET type='public' WHERE id=$1`, mtChannel); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListMeetingCandidates(ctx, selectionScope(domain.MeetingConversationChannel, mtChannel, mtMember), storage.MeetingPageQuery{})
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("public selection: %+v %v", page, err)
	}
	// Prefix matches are case insensitive and '%' is literal, never a wildcard.
	for _, tt := range []struct {
		search string
		want   int
	}{{"meeting", 2}, {"%", 0}, {"missing", 0}} {
		page, err := store.ListMeetingCandidates(ctx, selectionScope(domain.MeetingConversationChannel, mtChannel, mtOrganizer), storage.MeetingPageQuery{Search: tt.search})
		if err != nil || len(page.Items) != tt.want {
			t.Fatalf("search %q: %+v %v", tt.search, page, err)
		}
	}
}

func TestMeetingSelectionDirectDMPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	// Persisted direct membership, never inferred from a label.
	id := "5e110000-0000-4000-8000-000000000041"
	if _, err := pool.Exec(ctx, `INSERT INTO chat.dm_conversations(id,workspace_id,type,created_by,direct_pair_key) VALUES($1,$2,'direct',$3,$4)`, id, mtWS, mtOrganizer, mtOrganizer+":"+mtAttendee); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO chat.dm_members(conversation_id,user_id) SELECT $1,id FROM unnest($2::uuid[]) u(id)`, id, []string{mtOrganizer, mtAttendee}); err != nil {
		t.Fatal(err)
	}
	scope := selectionScope(domain.MeetingConversationDM, id, mtOrganizer)
	page, err := store.ListMeetingCandidates(ctx, scope, storage.MeetingPageQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].UserID != mtAttendee {
		t.Fatalf("direct %+v %v", page, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE chat.dm_members SET status='left', left_at=now() WHERE conversation_id=$1 AND user_id=$2`, id, mtAttendee); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListMeetingCandidates(ctx, scope, storage.MeetingPageQuery{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("removed direct %+v %v", page, err)
	}
	m := mtStore(t, ctx, store, &scope.Conversation, mtDetails(""))
	_, err = store.AddMeetingParticipant(ctx, mtScope(mtOrganizer, m.ID), mtAttendee, domain.MeetingRoleAttendee)
	mtWant(t, "removed direct invitation", err, domain.ErrMeetingParticipantNotEligible)
}

func seedMeetingChannelMembers(t *testing.T, ctx context.Context, pool *pgxpool.Pool, count int) {
	t.Helper()
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("5e540000-0000-4000-8000-%012d", i+1)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM auth.users WHERE id=ANY($1::uuid[])`, ids); err != nil {
			t.Error(err)
		}
	})
	for i, sql := range []string{
		`INSERT INTO auth.users(id,email,display_name) SELECT id,'selection-'||id||'@e.test','Scale Member' FROM unnest($1::uuid[]) u(id)`,
		`INSERT INTO chat.workspace_members(workspace_id,user_id,role) SELECT $2,id,'member' FROM unnest($1::uuid[]) u(id)`,
		`INSERT INTO chat.channel_members(channel_id,user_id) SELECT $2,id FROM unnest($1::uuid[]) u(id)`,
	} {
		if _, err := pool.Exec(ctx, sql, meetingScaleArgs(ids, i)...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMeetingSelectionLargeChannelPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	seedMeetingChannelMembers(t, ctx, pool, 320)
	store := storage.NewPGXMeetingStore(pool)
	scope := selectionScope(domain.MeetingConversationChannel, mtChannel, mtOrganizer)
	seen := map[string]bool{}
	q := storage.MeetingPageQuery{Limit: 37}
	for {
		page, err := store.ListMeetingCandidates(ctx, scope, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 37 {
			t.Fatal("unbounded page")
		}
		for _, item := range page.Items {
			if seen[item.UserID] || item.UserID <= q.After {
				t.Fatal("unstable cursor")
			}
			seen[item.UserID] = true
		}
		if page.NextCursor == "" {
			break
		}
		q.After = page.NextCursor
	}
	if len(seen) != 322 {
		t.Fatalf("candidates %d", len(seen))
	}
	m := mtStore(t, ctx, store, &scope.Conversation, mtDetails(""))
	n, err := store.InviteAllMeetingParticipants(ctx, mtScope(mtOrganizer, m.ID))
	if err != nil || n != 322 {
		t.Fatalf("bulk %d %v", n, err)
	}
	rosterSeen := map[string]bool{}
	q = storage.MeetingPageQuery{Limit: 40}
	for {
		page, err := store.ListMeetingParticipants(ctx, mtScope(mtOrganizer, m.ID), q)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Items {
			if rosterSeen[p.UserID] {
				t.Fatal("duplicate roster")
			}
			rosterSeen[p.UserID] = true
		}
		if page.NextCursor == "" {
			break
		}
		q.After = page.NextCursor
	}
	if len(rosterSeen) != 323 {
		t.Fatalf("roster %d", len(rosterSeen))
	}
}

func TestMeetingSelectionBulkLimitPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	seedMeetingChannelMembers(t, ctx, pool, 501)
	store := storage.NewPGXMeetingStore(pool)
	m := mtStore(t, ctx, store, mtChannelConversation, mtDetails(""))
	before := mtSnapshot(t, ctx, pool, m.ID)
	_, err := store.InviteAllMeetingParticipants(ctx, mtScope(mtOrganizer, m.ID))
	mtWant(t, "bulk cap", err, domain.ErrInvalidInput)
	if mtSnapshot(t, ctx, pool, m.ID) != before {
		t.Fatal("oversized bulk wrote state")
	}
}

func meetingScaleArgs(ids []string, statement int) []any {
	switch statement {
	case 0:
		return []any{ids}
	case 1:
		return []any{ids, mtWS}
	default:
		return []any{ids, mtChannel}
	}
}
