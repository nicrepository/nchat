package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestChatMigrations_PostgreSQLInvariants(t *testing.T) {
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}

	connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := pgx.Connect(connectCtx, dsn)
	cancel()
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	ctx := context.Background()
	defer func() {
		if err := conn.Close(ctx); err != nil {
			t.Errorf("close test database: %v", err)
		}
	}()

	var databaseName string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive migration test against non-test database %q", databaseName)
	}

	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`)
	})

	for _, name := range []string{
		"000001_chat_domain_schema.up.sql",
		"000002_chat_enforce_channel_workspace_isolation.up.sql",
		"000003_chat_dm_conversations.up.sql",
		"000004_chat_messages.up.sql",
	} {
		if _, err := conn.Exec(ctx, readChatMigration(t, name)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := conn.Exec(ctx, readChatMigration(t, "000012_message_parent_reply.up.sql")); err != nil {
		t.Fatalf("apply 000012_message_parent_reply.up.sql: %v", err)
	}
	if _, err := conn.Exec(ctx, readChatMigration(t, "000012_message_parent_reply.down.sql")); err != nil {
		t.Fatalf("apply 000012_message_parent_reply.down.sql: %v", err)
	}
	if _, err := conn.Exec(ctx, readChatMigration(t, "000012_message_parent_reply.up.sql")); err != nil {
		t.Fatalf("re-apply 000012_message_parent_reply.up.sql: %v", err)
	}
	for _, name := range []string{
		"000005_chat_message_body_format.up.sql",
		"000006_chat_mentions.up.sql",
		"000007_chat_mention_channel_visibility.up.sql",
		"000008_message_reactions.up.sql",
		"000009_message_reactions_message_id_index.up.sql",
		"000010_message_favorites.up.sql",
		"000011_message_pins.up.sql",
		"000013_message_edit_history.up.sql",
		"000014_cross_channel_message_reference.up.sql",
		"000015_message_forward_idempotency.up.sql",
	} {
		if _, err := conn.Exec(ctx, readChatMigration(t, name)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	t.Run("SQL visibility excludes unauthorized private and cross-workspace channels", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			BEGIN;
			INSERT INTO chat.workspace_members (workspace_id, user_id, status)
			VALUES ('00000000-0000-0000-0000-000000000001', '60000000-0000-0000-0000-000000000001', 'active');
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type)
			VALUES
				('60000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001', 'public-visible', 'Public Visible', 'public'),
				('60000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000001', 'private-hidden', 'Private Hidden', 'private');
			INSERT INTO chat.workspaces (id, slug, name)
			VALUES ('60000000-0000-0000-0000-000000000004', 'visibility-b', 'Visibility B');
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general)
			VALUES ('60000000-0000-0000-0000-000000000005', '60000000-0000-0000-0000-000000000004', 'geral', 'Geral', 'public', true);
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type)
			VALUES ('60000000-0000-0000-0000-000000000006', '60000000-0000-0000-0000-000000000004', 'private-cross', 'Private Cross', 'private');
			INSERT INTO chat.workspace_members (workspace_id, user_id, status)
			VALUES ('60000000-0000-0000-0000-000000000004', '60000000-0000-0000-0000-000000000001', 'suspended');
			INSERT INTO chat.channel_members (channel_id, user_id)
			VALUES ('60000000-0000-0000-0000-000000000006', '60000000-0000-0000-0000-000000000001');
			COMMIT;`)
		if err != nil {
			t.Fatalf("seed visibility cases: %v", err)
		}

		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("open visibility pool: %v", err)
		}
		defer pool.Close()
		store := storage.NewPGXChannelStore(pool)

		channels, err := store.ListVisibleChannelsByUser(ctx, "00000000-0000-0000-0000-000000000001", "60000000-0000-0000-0000-000000000001")
		if err != nil {
			t.Fatalf("list visible channels: %v", err)
		}
		assertChannelIDs(t, channels, map[string]bool{
			"00000000-0000-0000-0000-000000000002": true,
			"60000000-0000-0000-0000-000000000002": true,
		})

		_, err = conn.Exec(ctx, `
			INSERT INTO chat.channel_members (channel_id, user_id)
			VALUES ('60000000-0000-0000-0000-000000000003', '60000000-0000-0000-0000-000000000001')`)
		if err != nil {
			t.Fatalf("add private channel membership: %v", err)
		}
		channels, err = store.ListVisibleChannelsByUser(ctx, "00000000-0000-0000-0000-000000000001", "60000000-0000-0000-0000-000000000001")
		if err != nil {
			t.Fatalf("list visible channels with private membership: %v", err)
		}
		assertChannelIDs(t, channels, map[string]bool{
			"00000000-0000-0000-0000-000000000002": true,
			"60000000-0000-0000-0000-000000000002": true,
			"60000000-0000-0000-0000-000000000003": true,
		})

		_, err = conn.Exec(ctx, `UPDATE chat.workspaces SET status = 'disabled' WHERE id = '00000000-0000-0000-0000-000000000001'`)
		if err != nil {
			t.Fatalf("disable workspace: %v", err)
		}
		channels, err = store.ListVisibleChannelsByUser(ctx, "00000000-0000-0000-0000-000000000001", "60000000-0000-0000-0000-000000000001")
		if err != nil {
			t.Fatalf("list disabled workspace channels: %v", err)
		}
		if len(channels) != 0 {
			t.Fatalf("disabled workspace must return no channels, got %+v", channels)
		}
		_, err = conn.Exec(ctx, `UPDATE chat.workspaces SET status = 'active' WHERE id = '00000000-0000-0000-0000-000000000001'`)
		if err != nil {
			t.Fatalf("restore workspace: %v", err)
		}
	})

	t.Run("seed is idempotent and has one active public general channel", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general)
			VALUES (
				'00000000-0000-0000-0000-000000000002',
				'00000000-0000-0000-0000-000000000001',
				'geral', 'Geral', 'public', true
			)
			ON CONFLICT (id) DO NOTHING`)
		if err != nil {
			t.Fatalf("repeat seed: %v", err)
		}

		var count int
		err = conn.QueryRow(ctx, `
			SELECT count(*)
			FROM chat.channels
			WHERE workspace_id = '00000000-0000-0000-0000-000000000001'
			  AND is_general = true AND type = 'public' AND status = 'active'`).Scan(&count)
		if err != nil {
			t.Fatalf("count seed general channel: %v", err)
		}
		if count != 1 {
			t.Fatalf("expected exactly one seed general channel, got %d", count)
		}
	})

	t.Run("second general channel is rejected", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			INSERT INTO chat.channels (workspace_id, slug, display_name, type, is_general)
			VALUES ('00000000-0000-0000-0000-000000000001', 'geral-2', 'Geral 2', 'public', true)`)
		requirePostgresConstraint(t, err, "idx_channels_one_general_per_workspace")
	})

	for _, channelStatus := range []struct {
		name   string
		type_  string
		status string
	}{
		{name: "private general", type_: "private", status: "active"},
		{name: "archived general", type_: "public", status: "archived"},
	} {
		t.Run(channelStatus.name+" is rejected", func(t *testing.T) {
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() {
				if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
					t.Errorf("rollback private/archived general transaction: %v", err)
				}
			}()
			_, err = tx.Exec(ctx, `
				INSERT INTO chat.workspaces (id, slug, name)
				VALUES ('10000000-0000-0000-0000-000000000001', 'invalid-general', 'Invalid General')`)
			if err != nil {
				t.Fatalf("insert workspace: %v", err)
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO chat.channels (workspace_id, slug, display_name, type, status, is_general)
				VALUES ('10000000-0000-0000-0000-000000000001', 'geral', 'Geral', $1, $2, true)`,
				channelStatus.type_, channelStatus.status)
			requirePostgresConstraint(t, err, "channels_general_must_be_public_active")
		})
	}

	t.Run("cross-workspace category is rejected", func(t *testing.T) {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() {
			if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
				t.Errorf("rollback cross-workspace category transaction: %v", err)
			}
		}()

		_, err = tx.Exec(ctx, `
			INSERT INTO chat.workspaces (id, slug, name)
			VALUES ('20000000-0000-0000-0000-000000000001', 'workspace-b', 'Workspace B');
			INSERT INTO chat.channels (workspace_id, slug, display_name, type, is_general)
			VALUES ('20000000-0000-0000-0000-000000000001', 'geral', 'Geral', 'public', true)`)
		if err != nil {
			t.Fatalf("insert workspace B: %v", err)
		}

		var categoryID string
		err = tx.QueryRow(ctx, `
			INSERT INTO chat.channel_categories (workspace_id, name)
			VALUES ('00000000-0000-0000-0000-000000000001', 'Default Category')
			RETURNING id`).Scan(&categoryID)
		if err != nil {
			t.Fatalf("insert category: %v", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO chat.channels (workspace_id, category_id, slug, display_name, type)
			VALUES ('20000000-0000-0000-0000-000000000001', $1, 'cross-category', 'Cross Category', 'public')`, categoryID)
		requirePostgresConstraint(t, err, "channels_workspace_category_fk")
	})

	t.Run("workspace cannot commit without general channel", func(t *testing.T) {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO chat.workspaces (id, slug, name)
			VALUES ('30000000-0000-0000-0000-000000000001', 'no-general', 'No General')`)
		if err != nil {
			t.Fatalf("insert workspace: %v", err)
		}
		err = tx.Commit(ctx)
		requirePostgresConstraint(t, err, "workspace_general_channel_required")
	})

	t.Run("workspace and general channel can commit together", func(t *testing.T) {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO chat.workspaces (id, slug, name)
			VALUES ('40000000-0000-0000-0000-000000000001', 'with-general', 'With General');
			INSERT INTO chat.channels (workspace_id, slug, display_name, type, is_general)
			VALUES ('40000000-0000-0000-0000-000000000001', 'geral', 'Geral', 'public', true)`)
		if err != nil {
			t.Fatalf("insert workspace and general channel: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit workspace and general channel: %v", err)
		}
		if _, err := conn.Exec(ctx, `DELETE FROM chat.workspaces WHERE id = '40000000-0000-0000-0000-000000000001'`); err != nil {
			t.Fatalf("cleanup workspace: %v", err)
		}
	})

	t.Run("direct dm uniqueness includes archived and remains workspace scoped", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			BEGIN;
			INSERT INTO chat.workspace_members (workspace_id, user_id, status)
			VALUES
				('00000000-0000-0000-0000-000000000001', '70000000-0000-0000-0000-000000000001', 'active'),
				('00000000-0000-0000-0000-000000000001', '70000000-0000-0000-0000-000000000002', 'active')
			ON CONFLICT (workspace_id, user_id) DO UPDATE SET status = EXCLUDED.status;
			INSERT INTO chat.dm_conversations
				(id, workspace_id, type, status, created_by, direct_pair_key)
			VALUES
				('70000000-0000-0000-0000-000000000003',
				 '00000000-0000-0000-0000-000000000001',
				 'direct',
				 'archived',
				 '70000000-0000-0000-0000-000000000001',
				 '36:70000000-0000-0000-0000-00000000000136:70000000-0000-0000-0000-000000000002');
			COMMIT;`)
		if err != nil {
			t.Fatalf("seed archived direct dm: %v", err)
		}

		_, err = conn.Exec(ctx, `
			INSERT INTO chat.dm_conversations
				(workspace_id, type, status, created_by, direct_pair_key)
			VALUES
				('00000000-0000-0000-0000-000000000001',
				 'direct',
				 'active',
				 '70000000-0000-0000-0000-000000000002',
				 '36:70000000-0000-0000-0000-00000000000136:70000000-0000-0000-0000-000000000002')`)
		requirePostgresConstraint(t, err, "idx_dm_conversations_direct_pair_unique")

		_, err = conn.Exec(ctx, `
			BEGIN;
			INSERT INTO chat.workspaces (id, slug, name)
			VALUES ('70000000-0000-0000-0000-000000000004', 'dm-unique-b', 'DM Unique B');
			INSERT INTO chat.channels (workspace_id, slug, display_name, type, is_general)
			VALUES ('70000000-0000-0000-0000-000000000004', 'geral', 'Geral', 'public', true);
			INSERT INTO chat.dm_conversations
				(workspace_id, type, status, created_by, direct_pair_key)
			VALUES
				('70000000-0000-0000-0000-000000000004',
				 'direct',
				 'active',
				 '70000000-0000-0000-0000-000000000001',
				 '36:70000000-0000-0000-0000-00000000000136:70000000-0000-0000-0000-000000000002');
			COMMIT;`)
		if err != nil {
			t.Fatalf("same pair key should be isolated by workspace: %v", err)
		}
	})

	t.Run("concurrent direct dm creation returns one canonical conversation", func(t *testing.T) {
		const (
			workspaceID = "00000000-0000-0000-0000-000000000001"
			userA       = "71000000-0000-0000-0000-000000000001"
			userB       = "71000000-0000-0000-0000-000000000002"
			pairKey     = "concurrent-direct-pair"
		)
		t.Cleanup(func() {
			if _, err := conn.Exec(context.Background(), `DELETE FROM chat.dm_conversations WHERE workspace_id = $1 AND direct_pair_key = $2`, workspaceID, pairKey); err != nil {
				t.Errorf("cleanup concurrent direct dm: %v", err)
			}
		})
		if _, err := conn.Exec(ctx, `
			INSERT INTO chat.workspace_members (workspace_id, user_id, status)
			VALUES ($1, $2, 'active'), ($1, $3, 'active')
			ON CONFLICT (workspace_id, user_id) DO UPDATE SET status = 'active'`, workspaceID, userA, userB); err != nil {
			t.Fatalf("seed concurrent members: %v", err)
		}
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("open concurrent pool: %v", err)
		}
		defer pool.Close()
		store := storage.NewPGXDMStore(pool)
		start := make(chan struct{})
		results := make(chan storage.CreateDirectConversationResult, 2)
		errs := make(chan error, 2)
		var workers sync.WaitGroup
		for range 2 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				result, err := store.CreateDirectConversation(ctx, storage.CreateDirectConversationInput{
					WorkspaceID: workspaceID, CreatedBy: userA, DirectPairKey: pairKey,
					ParticipantUserIDs: []string{userA, userB},
				})
				results <- result
				errs <- err
			}()
		}
		close(start)
		workers.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent create: %v", err)
			}
		}
		var conversationID string
		createdCount := 0
		for result := range results {
			if conversationID == "" {
				conversationID = result.Conversation.ID
			}
			if result.Conversation.ID != conversationID {
				t.Fatalf("different canonical IDs: %q and %q", conversationID, result.Conversation.ID)
			}
			if result.Created {
				createdCount++
			}
		}
		if createdCount != 1 {
			t.Fatalf("created count = %d, want 1", createdCount)
		}
		var count int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM chat.dm_conversations WHERE workspace_id = $1 AND direct_pair_key = $2`, workspaceID, pairKey).Scan(&count); err != nil {
			t.Fatalf("count canonical DMs: %v", err)
		}
		if count != 1 {
			t.Fatalf("canonical DM count = %d, want 1", count)
		}
		if err := conn.QueryRow(ctx, `
			SELECT count(*)
			FROM chat.dm_members dm
			JOIN chat.dm_conversations dc ON dc.id = dm.conversation_id
			WHERE dc.workspace_id = $1 AND dc.direct_pair_key = $2`, workspaceID, pairKey).Scan(&count); err != nil {
			t.Fatalf("count canonical DM members: %v", err)
		}
		if count != 2 {
			t.Fatalf("canonical DM member count = %d, want 2", count)
		}
	})

	t.Run("dm SQL visibility excludes nonparticipants archived and inactive workspace members", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			BEGIN;
			INSERT INTO chat.workspace_members (workspace_id, user_id, status)
			VALUES
				('00000000-0000-0000-0000-000000000001', '80000000-0000-0000-0000-000000000001', 'active'),
				('00000000-0000-0000-0000-000000000001', '80000000-0000-0000-0000-000000000002', 'active'),
				('00000000-0000-0000-0000-000000000001', '80000000-0000-0000-0000-000000000003', 'active')
			ON CONFLICT (workspace_id, user_id) DO UPDATE SET status = EXCLUDED.status;
			INSERT INTO chat.dm_conversations
				(id, workspace_id, type, status, created_by, direct_pair_key)
			VALUES
				('80000000-0000-0000-0000-000000000004',
				 '00000000-0000-0000-0000-000000000001',
				 'direct',
				 'active',
				 '80000000-0000-0000-0000-000000000001',
				 '36:80000000-0000-0000-0000-00000000000136:80000000-0000-0000-0000-000000000002'),
				('80000000-0000-0000-0000-000000000005',
				 '00000000-0000-0000-0000-000000000001',
				 'direct',
				 'active',
				 '80000000-0000-0000-0000-000000000002',
				 '36:80000000-0000-0000-0000-00000000000236:80000000-0000-0000-0000-000000000003'),
				('80000000-0000-0000-0000-000000000006',
				 '00000000-0000-0000-0000-000000000001',
				 'group',
				 'archived',
				 '80000000-0000-0000-0000-000000000001',
				 NULL);
			INSERT INTO chat.dm_members (conversation_id, user_id)
			VALUES
				('80000000-0000-0000-0000-000000000004', '80000000-0000-0000-0000-000000000001'),
				('80000000-0000-0000-0000-000000000004', '80000000-0000-0000-0000-000000000002'),
				('80000000-0000-0000-0000-000000000005', '80000000-0000-0000-0000-000000000002'),
				('80000000-0000-0000-0000-000000000005', '80000000-0000-0000-0000-000000000003'),
				('80000000-0000-0000-0000-000000000006', '80000000-0000-0000-0000-000000000001');
			COMMIT;`)
		if err != nil {
			t.Fatalf("seed dm visibility cases: %v", err)
		}

		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("open dm visibility pool: %v", err)
		}
		defer pool.Close()
		store := storage.NewPGXDMStore(pool)

		conversations, err := store.ListVisibleConversationsByUser(ctx, "00000000-0000-0000-0000-000000000001", "80000000-0000-0000-0000-000000000001")
		if err != nil {
			t.Fatalf("list visible dm conversations: %v", err)
		}
		assertDMConversationIDs(t, conversations, map[string]bool{
			"80000000-0000-0000-0000-000000000004": true,
		})

		if _, err := store.GetVisibleConversationByID(ctx, "00000000-0000-0000-0000-000000000001", "80000000-0000-0000-0000-000000000005", "80000000-0000-0000-0000-000000000001"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("nonparticipant read should be ErrNotFound, got %v", err)
		}
		if _, err := store.GetVisibleConversationByID(ctx, "00000000-0000-0000-0000-000000000001", "80000000-0000-0000-0000-000000000006", "80000000-0000-0000-0000-000000000001"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("archived read should be ErrNotFound, got %v", err)
		}

		_, err = conn.Exec(ctx, `
			UPDATE chat.workspace_members
			SET status = 'suspended'
			WHERE workspace_id = '00000000-0000-0000-0000-000000000001'
			  AND user_id = '80000000-0000-0000-0000-000000000001'`)
		if err != nil {
			t.Fatalf("suspend dm caller: %v", err)
		}
		conversations, err = store.ListVisibleConversationsByUser(ctx, "00000000-0000-0000-0000-000000000001", "80000000-0000-0000-0000-000000000001")
		if err != nil {
			t.Fatalf("list suspended dm caller: %v", err)
		}
		if len(conversations) != 0 {
			t.Fatalf("suspended caller must see no DMs, got %+v", conversations)
		}
	})

	t.Run("message store creates channel and dm messages with nil optional refs", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			BEGIN;
			INSERT INTO chat.workspaces (id, slug, name)
			VALUES ('90000000-0000-0000-0000-000000000001', 'messages-create', 'Messages Create');
			INSERT INTO chat.channels (workspace_id, slug, display_name, type, is_general)
			VALUES ('90000000-0000-0000-0000-000000000001', 'geral', 'Geral', 'public', true);
			INSERT INTO chat.workspace_members (workspace_id, user_id, status)
			VALUES
				('90000000-0000-0000-0000-000000000001', '90000000-0000-0000-0000-000000000002', 'active');
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type)
			VALUES
				('90000000-0000-0000-0000-000000000003', '90000000-0000-0000-0000-000000000001', 'messages-public', 'Messages Public', 'public');
			INSERT INTO chat.dm_conversations (id, workspace_id, type, status, created_by, direct_pair_key)
			VALUES
				('90000000-0000-0000-0000-000000000004',
				 '90000000-0000-0000-0000-000000000001',
				 'group',
				 'active',
				 '90000000-0000-0000-0000-000000000002',
				 NULL);
			INSERT INTO chat.dm_members (conversation_id, user_id)
			VALUES ('90000000-0000-0000-0000-000000000004', '90000000-0000-0000-0000-000000000002');
			COMMIT;`)
		if err != nil {
			t.Fatalf("seed message create cases: %v", err)
		}

		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("open message pool: %v", err)
		}
		defer pool.Close()
		store := storage.NewPGXMessageStore(pool)

		channelMsg, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: "90000000-0000-0000-0000-000000000001",
			ChannelID:   "90000000-0000-0000-0000-000000000003",
			SenderID:    "90000000-0000-0000-0000-000000000002",
			BodyText:    "channel message without refs",
		})
		if err != nil {
			t.Fatalf("create channel message without refs: %v", err)
		}
		if channelMsg.ChannelID != "90000000-0000-0000-0000-000000000003" || channelMsg.SenderID != "90000000-0000-0000-0000-000000000002" {
			t.Fatalf("unexpected channel message: %+v", channelMsg)
		}

		dmMsg, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID:      "90000000-0000-0000-0000-000000000001",
			DMConversationID: "90000000-0000-0000-0000-000000000004",
			SenderID:         "90000000-0000-0000-0000-000000000002",
			BodyText:         "dm message without refs",
		})
		if err != nil {
			t.Fatalf("create dm message without refs: %v", err)
		}
		if dmMsg.DMConversationID != "90000000-0000-0000-0000-000000000004" || dmMsg.SenderID != "90000000-0000-0000-0000-000000000002" {
			t.Fatalf("unexpected dm message: %+v", dmMsg)
		}
	})
}

func TestResolveMessageReferences_PostgreSQLAuthorization(t *testing.T) {
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	var databaseName string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive reference test against non-test database %q", databaseName)
	}
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })
	if _, err := conn.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id UUID PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema required by chat foreign keys: %v", err)
	}
	if _, err := conn.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	const (
		workspaceA  = "a1000000-0000-0000-0000-000000000001"
		workspaceB  = "b1000000-0000-0000-0000-000000000001"
		reader      = "a1000000-0000-0000-0000-000000000002"
		author      = "a1000000-0000-0000-0000-000000000003"
		privateCh   = "a1000000-0000-0000-0000-000000000004"
		destCh      = "a1000000-0000-0000-0000-000000000005"
		archivedCh  = "a1000000-0000-0000-0000-000000000006"
		crossCh     = "b1000000-0000-0000-0000-000000000002"
		dmID        = "a1000000-0000-0000-0000-000000000007"
		privateMsg  = "a1000000-0000-0000-0000-000000000008"
		dmMsg       = "a1000000-0000-0000-0000-000000000009"
		deletedMsg  = "a1000000-0000-0000-0000-00000000000a"
		archivedMsg = "a1000000-0000-0000-0000-00000000000b"
		crossMsg    = "b1000000-0000-0000-0000-000000000003"
		destMsg     = "a1000000-0000-0000-0000-00000000000c"
	)
	seed := &pgx.Batch{}
	seed.Queue(`INSERT INTO chat.workspaces (id, slug, name) VALUES ($1, 'rf09-a', 'RF09 A'), ($2, 'rf09-b', 'RF09 B')`, workspaceA, workspaceB)
	seed.Queue(`INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
		($1, $3, 'active'), ($1, $4, 'active'), ($2, $3, 'active'), ($2, $4, 'active')`, workspaceA, workspaceB, reader, author)
	seed.Queue(`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status, is_general) VALUES
		($3, $1, 'private-source', 'Private Source', 'private', 'active', false),
		($4, $1, 'destination', 'Destination', 'public', 'active', true),
		($5, $1, 'archived-source', 'Archived Source', 'private', 'archived', false),
		($6, $2, 'cross-source', 'Cross Source', 'public', 'active', true)`,
		workspaceA, workspaceB, privateCh, destCh, archivedCh, crossCh)
	seed.Queue(`INSERT INTO chat.channel_members (channel_id, user_id) VALUES
		($1, $3), ($1, $4), ($2, $3), ($2, $4)`, privateCh, archivedCh, reader, author)
	seed.Queue(`INSERT INTO chat.dm_conversations (id, workspace_id, type, status, created_by)
		VALUES ($1, $2, 'group', 'active', $3)`, dmID, workspaceA, author)
	seed.Queue(`INSERT INTO chat.dm_members (conversation_id, user_id) VALUES ($1, $2), ($1, $3)`, dmID, reader, author)
	seed.Queue(`INSERT INTO chat.messages
		(id, workspace_id, channel_id, dm_conversation_id, sender_id, body_text, body_format, status, deleted_at)
		VALUES
			($3, $1, $5, NULL, $2, 'private body', 'v3', 'active', NULL),
			($4, $1, NULL, $6, $2, 'dm body', 'v2', 'active', NULL),
			($7, $1, $5, NULL, $2, '', 'v1', 'deleted', now()),
			($8, $1, $9, NULL, $2, 'archived body', 'v1', 'active', NULL),
			($10, $11, $12, NULL, $2, 'cross body', 'v1', 'active', NULL),
			($13, $1, $14, NULL, $15, 'destination body', 'v1', 'active', NULL)`,
		workspaceA, author, privateMsg, dmMsg, privateCh, dmID, deletedMsg, archivedMsg, archivedCh,
		crossMsg, workspaceB, crossCh, destMsg, destCh, reader)
	seed.Queue(`UPDATE chat.messages SET referenced_message_id = $1 WHERE id = $2`, privateMsg, destMsg)
	if err := conn.SendBatch(ctx, seed).Close(); err != nil {
		t.Fatalf("seed RF-09 authorization cases: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open reference pool: %v", err)
	}
	defer pool.Close()
	store := storage.NewPGXMessageStore(pool)
	ids := []string{privateMsg, dmMsg, deletedMsg, archivedMsg, crossMsg}
	resolved, err := store.ResolveMessageReferences(ctx, workspaceA, reader, ids)
	if err != nil {
		t.Fatalf("resolve authorized references: %v", err)
	}
	if len(resolved) != 2 || resolved[privateMsg].BodyText != "private body" || resolved[dmMsg].BodyText != "dm body" {
		t.Fatalf("unexpected authorized references: %+v", resolved)
	}
	destinationRefs, err := store.ListReferencedMessageIDs(ctx, workspaceA, reader, destCh, "", []string{destMsg})
	if err != nil {
		t.Fatalf("list destination references: %v", err)
	}
	if destinationRefs[destMsg] != privateMsg {
		t.Fatalf("destination reference = %q, want %q", destinationRefs[destMsg], privateMsg)
	}

	if _, err := conn.Exec(ctx, `DELETE FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`, privateCh, reader); err != nil {
		t.Fatalf("revoke private channel access: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE chat.dm_members SET status = 'left', left_at = now()
		WHERE conversation_id = $1 AND user_id = $2`, dmID, reader); err != nil {
		t.Fatalf("revoke DM access: %v", err)
	}
	resolved, err = store.ResolveMessageReferences(ctx, workspaceA, reader, ids)
	if err != nil {
		t.Fatalf("resolve revoked references: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("revoked reader received source metadata: %+v", resolved)
	}

	if _, err := conn.Exec(ctx, `DELETE FROM chat.messages WHERE id = $1`, privateMsg); err != nil {
		t.Fatalf("hard-delete referenced origin: %v", err)
	}
	var retained string
	if err := conn.QueryRow(ctx, `SELECT referenced_message_id::text FROM chat.messages WHERE id = $1`, destMsg).Scan(&retained); err != nil {
		t.Fatalf("read retained opaque reference: %v", err)
	}
	if retained != privateMsg {
		t.Fatalf("retained reference = %q, want %q", retained, privateMsg)
	}
}

func requirePostgresConstraint(t *testing.T, err error, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected constraint %s to reject statement", constraint)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected PostgreSQL error for %s, got %v", constraint, err)
	}
	if pgErr.ConstraintName != constraint {
		t.Fatalf("expected constraint %s, got %s (%v)", constraint, pgErr.ConstraintName, err)
	}
}

func assertChannelIDs(t *testing.T, channels []domain.Channel, expected map[string]bool) {
	t.Helper()
	if len(channels) != len(expected) {
		t.Fatalf("expected %d channels, got %+v", len(expected), channels)
	}
	for _, channel := range channels {
		if !expected[channel.ID] {
			t.Fatalf("unexpected channel returned: %+v", channel)
		}
	}
}

func assertDMConversationIDs(t *testing.T, conversations []domain.DMConversation, expected map[string]bool) {
	t.Helper()
	if len(conversations) != len(expected) {
		t.Fatalf("expected %d conversations, got %+v", len(expected), conversations)
	}
	for _, conversation := range conversations {
		if !expected[conversation.ID] {
			t.Fatalf("unexpected conversation returned: %+v", conversation)
		}
	}
}
