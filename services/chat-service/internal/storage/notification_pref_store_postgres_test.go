package storage_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Mute is a per-user preference, and the general channel is not silenceable
// (issue #527). Both properties are enforced in SQL, so both are proved here
// against a real PostgreSQL rather than against a fake.

const muteChannelID = "c1000000-0000-4000-8000-000000000040"

func seedMuteChannel(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
		VALUES ($1, $2, 'infra', 'Infra', 'public', false, 'active')`,
		muteChannelID, chanWorkspace,
	); err != nil {
		t.Fatalf("seed mute channel: %v", err)
	}
}

func mutedTargetIDs(t *testing.T, store *storage.PGXNotificationPrefStore, userID string) []string {
	t.Helper()
	items, err := store.ListMuted(t.Context(), chanWorkspace, userID)
	if err != nil {
		t.Fatalf("ListMuted: %v", err)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.TargetType+":"+item.TargetID)
	}
	return ids
}

// The headline invariant: one member silencing a channel changes nothing for
// anyone else, because the row is keyed by user.
func TestPGXNotificationPrefStoreMuteIsPerUserPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)

	if err := store.Mute(t.Context(), chanWorkspace, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
		t.Fatalf("Mute: %v", err)
	}

	if got := mutedTargetIDs(t, store, chanMember); len(got) != 1 || got[0] != "channel:"+muteChannelID {
		t.Fatalf("muted for the actor = %v, want the one channel", got)
	}
	if got := mutedTargetIDs(t, store, chanOwner); len(got) != 0 {
		t.Fatalf("muted for another user = %v, want none — mute is individual", got)
	}
}

// Muting twice is one row, and unmuting is idempotent too: the UI toggles, and
// a repeated toggle must not become an error or a duplicate.
func TestPGXNotificationPrefStoreMuteIsIdempotentPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)

	for range 2 {
		if err := store.Mute(t.Context(), chanWorkspace, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
			t.Fatalf("Mute: %v", err)
		}
	}
	if got := mutedTargetIDs(t, store, chanMember); len(got) != 1 {
		t.Fatalf("muted = %v, want exactly one row after two mutes", got)
	}

	for range 2 {
		if err := store.Unmute(t.Context(), chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
			t.Fatalf("Unmute: %v", err)
		}
	}
	if got := mutedTargetIDs(t, store, chanMember); len(got) != 0 {
		t.Fatalf("muted = %v, want none after unmuting", got)
	}
}

// The general channel is where everyone is reachable by construction. The
// refusal is in SQL, so it holds for every role and for a caller that reached
// storage without passing the service.
func TestPGXNotificationPrefStoreRefusesGeneralChannelPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXNotificationPrefStore(pool)

	for _, actor := range []struct {
		name   string
		userID string
	}{
		{name: "owner", userID: chanOwner},
		{name: "admin", userID: chanAdmin},
		{name: "member", userID: chanMember},
	} {
		t.Run(actor.name, func(t *testing.T) {
			err := store.Mute(t.Context(), chanWorkspace, actor.userID, storage.NotificationPrefTargetChannel, chanGeneral)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error = %v, want a refusal for #geral", err)
			}
			if got := mutedTargetIDs(t, store, actor.userID); len(got) != 0 {
				t.Fatalf("muted = %v, want #geral never silenced", got)
			}
		})
	}
}

// Visibility is re-checked in the write, so a stale client list cannot mute a
// channel the caller cannot see, and an arbitrary UUID mutes nothing.
func TestPGXNotificationPrefStoreRefusesInvisibleTargetsPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)

	for _, test := range []struct {
		name      string
		userID    string
		channelID string
	}{
		{name: "not a workspace member", userID: chanStranger, channelID: muteChannelID},
		{name: "channel that does not exist", userID: chanMember, channelID: "c1000000-0000-4000-8000-0000000000fe"},
		{name: "channel of another workspace", userID: chanMember, channelID: chanOtherGeneral},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := store.Mute(t.Context(), chanWorkspace, test.userID, storage.NotificationPrefTargetChannel, test.channelID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}
}

// An unknown target kind is refused before any SQL runs: the two kinds are a
// closed set and a third one is not a shape this domain has.
func TestPGXNotificationPrefStoreRejectsUnknownTargetKindPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXNotificationPrefStore(pool)

	if err := store.Mute(t.Context(), chanWorkspace, chanMember, "workspace", muteChannelID); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Mute error = %v, want ErrInvalidInput", err)
	}
	if err := store.Unmute(t.Context(), chanMember, "workspace", muteChannelID); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Unmute error = %v, want ErrInvalidInput", err)
	}
}
