package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Rename versus a concurrent revocation of the actor's role (issue #527
// security review).
//
// The defect these cover: ChannelService authorized the rename, and the UPDATE
// then ran with no second look, so a role revoked in between still got to write.
// Narrowing that window is not a fix — the authorization has to be *serialized*
// with the mutation, which is what PGXChannelStore.UpdateChannel's
// `FOR SHARE` on the actor's chat.workspace_members row does.
//
// Everything here runs against a real PostgreSQL, because the property under
// test is a lock interaction. Coordination is by lock state, never by sleeping:
// each test puts a transaction in a known blocked state and waits for the
// database itself to report the wait before releasing it.

const (
	renameChannelID = "c1000000-0000-4000-8000-000000000030"
	renameOtherWSCh = "c2000000-0000-4000-8000-000000000030"
)

// seedRenameChannel adds one ordinary renameable channel to the shared
// channel-authorization fixture, in the workspace whose roles it already seeds.
func seedRenameChannel(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
		VALUES ($1, $2, 'infra', 'Infra', 'public', false, 'active'),
		       ($3, $4, 'infra', 'Infra Alheio', 'public', false, 'active')`,
		renameChannelID, chanWorkspace, renameOtherWSCh, chanOtherWorkspace,
	); err != nil {
		t.Fatalf("seed rename channel: %v", err)
	}
}

func channelDisplayName(t *testing.T, pool *pgxpool.Pool, channelID string) string {
	t.Helper()
	var name string
	if err := pool.QueryRow(t.Context(), `SELECT display_name FROM chat.channels WHERE id = $1`, channelID).Scan(&name); err != nil {
		t.Fatalf("read display name: %v", err)
	}
	return name
}

// ── Scenario 1: the revocation reaches the serialization point first ─────────
//
// The revoking transaction holds the actor's membership row, so the rename
// blocks on `FOR SHARE` rather than reading a stale role. When the revocation
// commits, the rename re-evaluates against the new row, finds no manager, and
// refuses. Nothing is written and the caller gets ErrForbidden — which is what
// the handler already maps to 403, so no realtime event is published either
// (the handler publishes only after the store returns successfully).
func TestPGXChannelStoreUpdateChannelRefusesAfterConcurrentRevocationPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedRenameChannel(t, pool)
	store := storage.NewPGXChannelStore(pool)

	// The revocation takes its lock first and holds it uncommitted. This is the
	// real revocation path: chat.workspace_members is demoted/suspended by an
	// UPDATE of a non-key column, which takes FOR NO KEY UPDATE and conflicts
	// with the rename's FOR SHARE.
	revoke, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin revocation: %v", err)
	}
	defer func() { _ = revoke.Rollback(context.Background()) }()
	if _, err := revoke.Exec(t.Context(), `
		UPDATE chat.workspace_members SET role = 'member'
		WHERE workspace_id = $1 AND user_id = $2`,
		chanWorkspace, chanAdmin,
	); err != nil {
		t.Fatalf("revoke admin role: %v", err)
	}

	renamed := make(chan error, 1)
	go func() {
		_, err := store.UpdateChannel(context.Background(), storage.UpdateChannelInput{
			CallerID:    chanAdmin,
			WorkspaceID: chanWorkspace,
			ChannelID:   renameChannelID,
			Slug:        "infra",
			DisplayName: "Plataforma",
			Type:        domain.ChannelTypePublic,
		})
		renamed <- err
	}()

	// The rename is now parked on the membership row the revocation holds. This
	// is the evidence that the two are serialized rather than merely racing.
	waitForBlockedBackend(t, pool)

	if err := revoke.Commit(t.Context()); err != nil {
		t.Fatalf("commit revocation: %v", err)
	}

	if err := <-renamed; !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("rename error = %v, want ErrForbidden after the actor lost authority", err)
	}
	if got := channelDisplayName(t, pool, renameChannelID); got != "Infra" {
		t.Fatalf("display_name = %q, want the original name — the rename must not have been persisted", got)
	}
}

// ── Scenario 2: the rename reaches the serialization point first ─────────────
//
// The inverse order, and the reason this is a serialization rather than a
// permanent failure under contention: while the rename's transaction holds the
// membership FOR SHARE, the revocation waits, the rename commits, and only then
// does the revocation proceed. No deadlock, and the final state is consistent —
// the rename that was authorized at the serialization point survives.
//
// The store opens and commits its own transaction, so the rename's window is
// reproduced here by taking the very locks PGXChannelStore.UpdateChannel takes,
// in the order it takes them (channel row, then the actor's membership), and
// performing the same UPDATE inside it. The real store path is what scenario 1
// exercises end to end; what this one has to hold still is the window itself.
func TestPGXChannelStoreUpdateChannelHoldsOffRevocationWhileRenamingPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedRenameChannel(t, pool)

	rename, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin rename: %v", err)
	}
	defer func() { _ = rename.Rollback(context.Background()) }()

	// Step 1: the canonical channel lock, the same first statement every
	// membership mutation takes.
	var lockedID string
	if err := rename.QueryRow(t.Context(),
		`SELECT id FROM chat.channels WHERE id = $1::uuid FOR UPDATE`, renameChannelID,
	).Scan(&lockedID); err != nil {
		t.Fatalf("lock channel: %v", err)
	}
	// Step 2: the actor's authority, re-derived and held.
	var authorized bool
	if err := rename.QueryRow(t.Context(), `
		SELECT true
		FROM chat.workspace_members wm
		JOIN chat.workspaces w ON w.id = wm.workspace_id AND w.status = 'active'
		WHERE wm.workspace_id = $1::uuid
		  AND wm.user_id = $2::uuid
		  AND wm.status = 'active'
		  AND wm.role IN ('owner', 'admin')
		FOR SHARE OF wm`,
		chanWorkspace, chanAdmin,
	).Scan(&authorized); err != nil {
		t.Fatalf("authorize actor: %v", err)
	}

	// The revocation arrives second and must wait for the held share lock.
	revoked := make(chan error, 1)
	go func() {
		_, err := pool.Exec(context.Background(), `
			UPDATE chat.workspace_members SET role = 'member'
			WHERE workspace_id = $1 AND user_id = $2`,
			chanWorkspace, chanAdmin)
		revoked <- err
	}()
	waitForBlockedBackend(t, pool)

	// Step 3: the write, still inside the authorized window.
	if _, err := rename.Exec(t.Context(), `
		UPDATE chat.channels SET display_name = $3, updated_at = now()
		WHERE workspace_id = $1 AND id = $2 AND status = 'active' AND is_general = false`,
		chanWorkspace, renameChannelID, "Plataforma",
	); err != nil {
		t.Fatalf("rename channel: %v", err)
	}
	if err := rename.Commit(t.Context()); err != nil {
		t.Fatalf("commit rename: %v", err)
	}

	if err := <-revoked; err != nil {
		t.Fatalf("revocation failed after the rename released its lock: %v", err)
	}
	if got := channelDisplayName(t, pool, renameChannelID); got != "Plataforma" {
		t.Fatalf("display_name = %q, want the rename that held authority to have survived", got)
	}
	var role string
	if err := pool.QueryRow(t.Context(), `
		SELECT role FROM chat.workspace_members WHERE workspace_id = $1 AND user_id = $2`,
		chanWorkspace, chanAdmin,
	).Scan(&role); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role != "member" {
		t.Fatalf("role = %q, want the revocation to have completed afterwards", role)
	}
}

// ── The authorization matrix, decided by the database ────────────────────────
//
// Same table the service enforces, exercised against the store so it holds even
// for a caller that somehow reached storage without the service's check.
func TestPGXChannelStoreUpdateChannelAuthorizationMatrixPostgreSQL(t *testing.T) {
	for _, test := range []struct {
		name        string
		callerID    string
		workspaceID string
		channelID   string
		wantErr     error
	}{
		{name: "owner", callerID: chanOwner, workspaceID: chanWorkspace, channelID: renameChannelID},
		{name: "admin", callerID: chanAdmin, workspaceID: chanWorkspace, channelID: renameChannelID},
		{name: "moderator", callerID: chanModerator, workspaceID: chanWorkspace, channelID: renameChannelID, wantErr: domain.ErrForbidden},
		{name: "member", callerID: chanMember, workspaceID: chanWorkspace, channelID: renameChannelID, wantErr: domain.ErrForbidden},
		{name: "guest", callerID: chanGuest, workspaceID: chanWorkspace, channelID: renameChannelID, wantErr: domain.ErrForbidden},
		{name: "suspended membership", callerID: chanSuspended, workspaceID: chanWorkspace, channelID: renameChannelID, wantErr: domain.ErrForbidden},
		{name: "not a member", callerID: chanStranger, workspaceID: chanWorkspace, channelID: renameChannelID, wantErr: domain.ErrForbidden},
		{name: "owner of another workspace", callerID: chanForeignOwner, workspaceID: chanWorkspace, channelID: renameChannelID, wantErr: domain.ErrForbidden},
		{name: "disabled workspace", callerID: chanDisabledOwner, workspaceID: chanDisabledWS, channelID: chanDisabledGenrl, wantErr: domain.ErrForbidden},
		// The channel is pinned before the membership is read, so a channel that
		// does not exist is not-found for everyone — it never becomes a way to
		// probe whether a UUID exists.
		{name: "channel that does not exist", callerID: chanOwner, workspaceID: chanWorkspace, channelID: "c1000000-0000-4000-8000-0000000000ff", wantErr: domain.ErrNotFound},
		// A channel of another workspace is locked and then matches nothing, so
		// it is indistinguishable from one that was never there.
		{name: "channel of another workspace", callerID: chanOwner, workspaceID: chanWorkspace, channelID: renameOtherWSCh, wantErr: domain.ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := newChannelAuthzPool(t)
			seedRenameChannel(t, pool)
			store := storage.NewPGXChannelStore(pool)

			_, err := store.UpdateChannel(t.Context(), storage.UpdateChannelInput{
				CallerID:    test.callerID,
				WorkspaceID: test.workspaceID,
				ChannelID:   test.channelID,
				Slug:        "infra",
				DisplayName: "Plataforma",
				Type:        domain.ChannelTypePublic,
			})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				// Nothing was written, whichever refusal it was.
				if got := channelDisplayName(t, pool, renameChannelID); got != "Infra" {
					t.Fatalf("display_name = %q, want the original after a refusal", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateChannel: %v", err)
			}
			if got := channelDisplayName(t, pool, renameChannelID); got != "Plataforma" {
				t.Fatalf("display_name = %q, want the rename to have been persisted", got)
			}
		})
	}
}

// #geral stays immutable at the storage boundary too, for an owner: the UPDATE
// carries is_general = false, so the service's guard is not the only thing
// standing between an owner and the general channel.
func TestPGXChannelStoreUpdateChannelRefusesGeneralChannelPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXChannelStore(pool)

	_, err := store.UpdateChannel(t.Context(), storage.UpdateChannelInput{
		CallerID:    chanOwner,
		WorkspaceID: chanWorkspace,
		ChannelID:   chanGeneral,
		Slug:        "geral",
		DisplayName: "Plataforma",
		Type:        domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound for #geral", err)
	}
	if got := channelDisplayName(t, pool, chanGeneral); got != "geral" {
		t.Fatalf("display_name = %q, want #geral untouched", got)
	}
}

// A rename must not disturb what the channel already is: same id, same slug,
// same type, same category, and the per-user sidebar pin left alone.
func TestPGXChannelStoreUpdateChannelPreservesIdentityAndPinPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedRenameChannel(t, pool)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO auth.users (id, email, display_name) VALUES ($1, 'owner@example.test', 'Owner')
		ON CONFLICT (id) DO NOTHING`, chanOwner); err != nil {
		t.Fatalf("seed pin owner: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.sidebar_conversation_pins (user_id, workspace_id, channel_id)
		VALUES ($1, $2, $3)`,
		chanOwner, chanWorkspace, renameChannelID,
	); err != nil {
		t.Fatalf("seed sidebar pin: %v", err)
	}
	pinnedAt := sidebarPinInstant(t, pool, chanOwner, renameChannelID)

	store := storage.NewPGXChannelStore(pool)
	updated, err := store.UpdateChannel(t.Context(), storage.UpdateChannelInput{
		CallerID:    chanOwner,
		WorkspaceID: chanWorkspace,
		ChannelID:   renameChannelID,
		Slug:        "infra",
		DisplayName: "Plataforma",
		Type:        domain.ChannelTypePublic,
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if updated.Channel.ID != renameChannelID || updated.Channel.Slug != "infra" ||
		updated.Channel.Type != domain.ChannelTypePublic {
		t.Fatalf("rename changed more than the name: %+v", updated.Channel)
	}
	// The pin is a per-user preference on the same channel_id. A rename must
	// leave both the row and its instant alone, so the sidebar keeps its order.
	if after := sidebarPinInstant(t, pool, chanOwner, renameChannelID); !after.Equal(pinnedAt) {
		t.Fatalf("pinned_at = %v, want the rename to leave it at %v", after, pinnedAt)
	}
}

func sidebarPinInstant(t *testing.T, pool *pgxpool.Pool, userID, channelID string) time.Time {
	t.Helper()
	var pinnedAt time.Time
	if err := pool.QueryRow(t.Context(), `
		SELECT pinned_at FROM chat.sidebar_conversation_pins
		WHERE user_id = $1 AND channel_id = $2`, userID, channelID,
	).Scan(&pinnedAt); err != nil {
		t.Fatalf("read sidebar pin: %v", err)
	}
	return pinnedAt
}
