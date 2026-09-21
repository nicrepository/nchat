package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #894: the About block's persisted metadata, against a real PostgreSQL.
//
// Three properties here can only be held by a database, so none of them is
// asserted against a fake anywhere else:
//
//   - migration 000053 is additive. Channels and conversations written before
//     it keep their created_at and read back with no description, and the
//     columns accept a value without any backfill having invented one.
//   - the CHECK constraints are the last line. A value past
//     domain.MaxConversationDescriptionCodePoints is refused by the table even
//     when no service is in the way, and the cap counts code points rather than
//     bytes.
//   - whether a creator is nameable is a join, not a lookup. created_by NULL, a
//     creator who left the workspace and a deleted account are three different
//     rows that all have to produce the same empty name — and produce it
//     without losing the row, which is what distinguishes "no nameable creator"
//     from "no such conversation".
const (
	aboutWorkspace      = "e1000000-0000-4000-8000-000000000001"
	aboutOtherWorkspace = "e1000000-0000-4000-8000-000000000002"

	aboutGeneral      = "e1000000-0000-4000-8000-000000000010"
	aboutOtherGeneral = "e1000000-0000-4000-8000-000000000011"

	aboutDescribedChannel = "e1000000-0000-4000-8000-000000000020"
	aboutLegacyChannel    = "e1000000-0000-4000-8000-000000000021"
	aboutOrphanChannel    = "e1000000-0000-4000-8000-000000000022"
	aboutDepartedChannel  = "e1000000-0000-4000-8000-000000000023"
	aboutDeletedChannel   = "e1000000-0000-4000-8000-000000000024"

	aboutDescribedGroup = "e1000000-0000-4000-8000-000000000030"
	aboutLegacyGroup    = "e1000000-0000-4000-8000-000000000031"

	aboutFounder   = "e1000000-0000-4000-8000-00000000000a"
	aboutNamedOnly = "e1000000-0000-4000-8000-00000000000b"
	aboutDeparted  = "e1000000-0000-4000-8000-00000000000c"
	aboutDeleted   = "e1000000-0000-4000-8000-00000000000d"
)

// The instant every fixture row is written with, so "the migration did not
// touch created_at" is checked against a value the test chose rather than
// against now().
const aboutCreatedAt = "2024-02-03T04:05:06Z"

// aboutFixture seeds two workspaces, four identities in four different states
// of nameability, and the channels and conversations that point at them.
func aboutFixture(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := newLinkScanTestPool(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed about fixture: %v", err)
		}
	}

	// full_name wins over display_name where both are set, and display_name is
	// the fallback where it is not — the domain's one rule for a visual name,
	// exercised by two identities rather than asserted in a comment.
	exec(`INSERT INTO auth.users (id, email, display_name, full_name, status, deleted_at) VALUES
		($1, 'about-894-founder@e.test',  'founder',  'Álvaro Neto', 'active',   NULL),
		($2, 'about-894-named@e.test',    'Só Display', NULL,        'active',   NULL),
		($3, 'about-894-departed@e.test', 'Departed', 'Ex Membro',   'active',   NULL),
		($4, 'about-894-deleted@e.test',  'Deleted',  'Conta Apagada','deleted',  now())
		ON CONFLICT (id) DO NOTHING`,
		aboutFounder, aboutNamedOnly, aboutDeparted, aboutDeleted)

	for _, ws := range []struct{ id, slug, general string }{
		{aboutWorkspace, "about-894", aboutGeneral},
		{aboutOtherWorkspace, "about-894-other", aboutOtherGeneral},
	} {
		// Workspace and its #geral in one statement: a deferred constraint
		// requires every workspace to have one by commit time.
		exec(`WITH created AS (
				INSERT INTO chat.workspaces (id, slug, name, status)
				VALUES ($1, $2, $2, 'active')
				ON CONFLICT (id) DO NOTHING
				RETURNING id
			)
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
			VALUES ($3, $1, 'geral', 'Geral', 'public', true, 'active')
			ON CONFLICT (id) DO NOTHING`, ws.id, ws.slug, ws.general)
	}

	exec(`INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
		($1, $3, 'active'), ($1, $4, 'active'), ($1, $6, 'active'),
		($2, $5, 'active')
		ON CONFLICT DO NOTHING`,
		aboutWorkspace, aboutOtherWorkspace,
		aboutFounder, aboutNamedOnly, aboutDeparted, aboutDeleted)

	// aboutDeparted is a member of the OTHER workspace only: they created a
	// channel here and then left, which is the historical case the panel must
	// not name. aboutDeleted still holds an active membership, so the refusal
	// there is about the account rather than about the workspace.
	exec(`INSERT INTO chat.channels
			(id, workspace_id, slug, display_name, type, status, created_by, created_at, description)
		VALUES ($2,  $1, 'descrito',  'Descrito',  'public',  'active', $7,   $8, $9),
		       ($3,  $1, 'legado',    'Legado',    'public',  'active', $7,   $8, NULL),
		       ($4,  $1, 'orfao',     'Órfão',     'private', 'active', NULL, $8, NULL),
		       ($5,  $1, 'partiu',    'Partiu',    'public',  'active', $10,  $8, NULL),
		       ($6,  $1, 'apagado',   'Apagado',   'public',  'active', $11,  $8, NULL)
		ON CONFLICT (id) DO NOTHING`,
		aboutWorkspace, aboutDescribedChannel, aboutLegacyChannel, aboutOrphanChannel,
		aboutDepartedChannel, aboutDeletedChannel, aboutFounder, aboutCreatedAt,
		"Infraestrutura, redes internas e operações.", aboutDeparted, aboutDeleted)

	exec(`INSERT INTO chat.dm_conversations
			(id, workspace_id, type, status, created_by, title, direct_pair_key, created_at, description)
		VALUES ($2, $1, 'group', 'active', $4, 'Time de Infra', NULL, $5, $6),
		       ($3, $1, 'group', 'active', $7, 'Time Legado',   NULL, $5, NULL)
		ON CONFLICT (id) DO NOTHING`,
		aboutWorkspace, aboutDescribedGroup, aboutLegacyGroup,
		aboutFounder, aboutCreatedAt, "O grupo que cuida da malha.", aboutNamedOnly)

	t.Cleanup(func() { cleanupAboutFixture(pool) })
	return pool
}

// cleanupAboutFixture removes both workspaces whole, so their channels,
// conversations and memberships go with them by cascade — deleting a general
// channel first would trip the invariant that requires one.
func cleanupAboutFixture(pool *pgxpool.Pool) {
	ctx := context.Background()
	for _, id := range []string{aboutWorkspace, aboutOtherWorkspace} {
		_, _ = pool.Exec(ctx, `DELETE FROM chat.workspaces WHERE id = $1`, id)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM auth.users WHERE id = ANY($1::uuid[])`,
		[]string{aboutFounder, aboutNamedOnly, aboutDeparted, aboutDeleted})
}

func TestConversationAboutPostgreSQL(t *testing.T) {
	pool := aboutFixture(t)
	channels := storage.NewPGXChannelStore(pool)
	dms := storage.NewPGXDMStore(pool)
	ctx := t.Context()

	t.Run("channel description round-trips and its creator is named", func(t *testing.T) {
		about, err := channels.GetChannelAbout(ctx, aboutWorkspace, aboutDescribedChannel)
		if err != nil {
			t.Fatalf("GetChannelAbout: %v", err)
		}
		if about.Description != "Infraestrutura, redes internas e operações." {
			t.Fatalf("Description = %q", about.Description)
		}
		if about.CreatorDisplayName != "Álvaro Neto" {
			t.Fatalf("CreatorDisplayName = %q, want the creator's full_name", about.CreatorDisplayName)
		}
	})

	t.Run("a channel written before the column reads as described by nobody", func(t *testing.T) {
		about, err := channels.GetChannelAbout(ctx, aboutWorkspace, aboutLegacyChannel)
		if err != nil {
			t.Fatalf("GetChannelAbout: %v", err)
		}
		if about.Description != "" {
			t.Fatalf("Description = %q, want empty for a NULL column", about.Description)
		}
		// The row is still fully readable: an absent description is a state, not
		// a failure, and it does not cost the creator either.
		if about.CreatorDisplayName != "Álvaro Neto" {
			t.Fatalf("CreatorDisplayName = %q", about.CreatorDisplayName)
		}
	})

	t.Run("group description round-trips and display_name is the fallback name", func(t *testing.T) {
		about, err := dms.GetConversationAbout(ctx, aboutWorkspace, aboutDescribedGroup)
		if err != nil {
			t.Fatalf("GetConversationAbout: %v", err)
		}
		if about.Description != "O grupo que cuida da malha." {
			t.Fatalf("Description = %q", about.Description)
		}
		if about.CreatorDisplayName != "Álvaro Neto" {
			t.Fatalf("CreatorDisplayName = %q", about.CreatorDisplayName)
		}

		legacy, err := dms.GetConversationAbout(ctx, aboutWorkspace, aboutLegacyGroup)
		if err != nil {
			t.Fatalf("GetConversationAbout legacy: %v", err)
		}
		if legacy.Description != "" {
			t.Fatalf("legacy Description = %q, want empty", legacy.Description)
		}
		if legacy.CreatorDisplayName != "Só Display" {
			t.Fatalf("legacy CreatorDisplayName = %q, want the display_name fallback", legacy.CreatorDisplayName)
		}
	})

	// The three ways a creator stops being nameable. Each keeps the row — the
	// join is LEFT — so the caller learns "nobody to name" rather than "no such
	// channel", and no UUID is ever what comes back instead of a name.
	for _, tc := range []struct {
		name      string
		channelID string
	}{
		{"created_by was never recorded", aboutOrphanChannel},
		{"the creator left the workspace", aboutDepartedChannel},
		{"the creator's account was deleted", aboutDeletedChannel},
	} {
		t.Run("no name when "+tc.name, func(t *testing.T) {
			about, err := channels.GetChannelAbout(ctx, aboutWorkspace, tc.channelID)
			if err != nil {
				t.Fatalf("GetChannelAbout: %v", err)
			}
			if about.CreatorDisplayName != "" {
				t.Fatalf("CreatorDisplayName = %q, want empty", about.CreatorDisplayName)
			}
		})
	}

	t.Run("another workspace's id resolves nothing", func(t *testing.T) {
		if _, err := channels.GetChannelAbout(ctx, aboutOtherWorkspace, aboutDescribedChannel); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-workspace channel err = %v, want ErrNotFound", err)
		}
		if _, err := dms.GetConversationAbout(ctx, aboutOtherWorkspace, aboutDescribedGroup); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("cross-workspace conversation err = %v, want ErrNotFound", err)
		}
	})

	t.Run("migration 000053 left created_at alone", func(t *testing.T) {
		var channelCreated, conversationCreated string
		if err := pool.QueryRow(ctx, `
			SELECT to_char(c.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
			       to_char(dc.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
			FROM chat.channels c, chat.dm_conversations dc
			WHERE c.id = $1 AND dc.id = $2`,
			aboutDescribedChannel, aboutDescribedGroup,
		).Scan(&channelCreated, &conversationCreated); err != nil {
			t.Fatalf("read created_at: %v", err)
		}
		if channelCreated != aboutCreatedAt || conversationCreated != aboutCreatedAt {
			t.Fatalf("created_at = %q / %q, want %q for both", channelCreated, conversationCreated, aboutCreatedAt)
		}
	})

	t.Run("the cap is enforced by the table, in code points", func(t *testing.T) {
		// Accented characters: a byte-counting cap would refuse this, and a
		// code-point one must not. Exactly at the limit is the boundary the
		// service will share with the database the day a write path exists.
		atCap := strings.Repeat("é", domain.MaxConversationDescriptionCodePoints)
		if _, err := pool.Exec(ctx,
			`UPDATE chat.channels SET description = $2 WHERE id = $1`, aboutLegacyChannel, atCap,
		); err != nil {
			t.Fatalf("a description of exactly the cap must be accepted: %v", err)
		}

		overCap := atCap + "é"
		if _, err := pool.Exec(ctx,
			`UPDATE chat.channels SET description = $2 WHERE id = $1`, aboutLegacyChannel, overCap,
		); err == nil {
			t.Fatal("channels accepted a description past the cap")
		}
		if _, err := pool.Exec(ctx,
			`UPDATE chat.dm_conversations SET description = $2 WHERE id = $1`, aboutLegacyGroup, overCap,
		); err == nil {
			t.Fatal("dm_conversations accepted a description past the cap")
		}

		// Leave the fixture as the other subtests expect to find it.
		if _, err := pool.Exec(ctx,
			`UPDATE chat.channels SET description = NULL WHERE id = $1`, aboutLegacyChannel,
		); err != nil {
			t.Fatalf("restore legacy channel: %v", err)
		}
	})
}
