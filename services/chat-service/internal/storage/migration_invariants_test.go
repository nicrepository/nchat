package storage_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readChatMigration(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..", "migrations", "chat", name)
	contents, err := os.ReadFile(path) //nolint:gosec // Test callers pass fixed migration filenames.
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}

func readAllChatUpMigrations(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..", "migrations", "chat", "*.up.sql"))
	if err != nil {
		t.Fatalf("list chat migrations: %v", err)
	}
	var migrations strings.Builder
	for _, path := range paths {
		contents, err := os.ReadFile(path) //nolint:gosec // Glob is restricted to the repository migration directory.
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Base(path), err)
		}
		migrations.Write(contents)
	}
	return migrations.String()
}

func TestChatMigration_SeedCreatesOneIdempotentActivePublicGeneralChannel(t *testing.T) {
	migration := readChatMigration(t, "000001_chat_domain_schema.up.sql")
	if strings.Count(migration, "INSERT INTO chat.channels") != 1 {
		t.Fatal("seed must insert exactly one channel")
	}
	for _, expected := range []string{
		"'geral'",
		"'public'",
		"true",
		"ON CONFLICT (id) DO NOTHING",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("seed migration missing %q", expected)
		}
	}
}

func TestChatMigration_EnforcesWorkspaceCategoryConsistency(t *testing.T) {
	migration := readChatMigration(t, "000002_chat_enforce_channel_workspace_isolation.up.sql")
	for _, expected := range []string{
		"UNIQUE (workspace_id, id)",
		"FOREIGN KEY (workspace_id, category_id)",
		"REFERENCES chat.channel_categories (workspace_id, id)",
		"ON DELETE SET NULL (category_id)",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("category isolation migration missing %q", expected)
		}
	}
}

func TestChatMigration_EnforcesGeneralChannelInvariants(t *testing.T) {
	migration := readChatMigration(t, "000002_chat_enforce_channel_workspace_isolation.up.sql")
	for _, expected := range []string{
		"CHECK (NOT is_general OR (type = 'public' AND status = 'active'))",
		"CREATE CONSTRAINT TRIGGER workspaces_require_general_channel",
		"CREATE CONSTRAINT TRIGGER channels_require_general_channel",
		"DEFERRABLE INITIALLY DEFERRED",
		"workspace must have exactly one active public general channel",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("general channel migration missing %q", expected)
		}
	}
}

func TestChatMigration_DownRemovesGeneralChannelTriggers(t *testing.T) {
	migration := readChatMigration(t, "000002_chat_enforce_channel_workspace_isolation.down.sql")
	for _, expected := range []string{
		"DROP TRIGGER IF EXISTS workspaces_require_general_channel",
		"DROP TRIGGER IF EXISTS channels_require_general_channel",
		"DROP FUNCTION IF EXISTS chat.enforce_workspace_general_channel()",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("down migration missing %q", expected)
		}
	}
}

// RF-17. chat.channel_categories and chat.channels.category_id already existed;
// what 000017 has to add is everything that bounds a category row, plus the index
// the composite FK's referencing side never had.
func TestChatMigration_BoundsChannelCategoryNameAndPosition(t *testing.T) {
	migration := readChatMigration(t, "000017_channel_category_constraints.up.sql")
	for _, expected := range []string{
		"CONSTRAINT channel_categories_name_length_check",
		"CHECK (char_length(btrim(name)) BETWEEN 1 AND 60)",
		"CONSTRAINT channel_categories_name_trimmed_check",
		"CHECK (name = btrim(name))",
		"CONSTRAINT channel_categories_name_no_control_check",
		"CHECK (name !~ '[[:cntrl:]]')",
		"CONSTRAINT channel_categories_name_not_reserved_check",
		"CHECK (lower(btrim(name)) <> 'geral')",
		"CONSTRAINT channel_categories_position_range_check",
		"CHECK (position >= 0 AND position <= 100000)",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("channel category migration missing %q", expected)
		}
	}
}

func TestChatMigration_EnforcesChannelCategoryNameUniquePerWorkspace(t *testing.T) {
	migration := readChatMigration(t, "000017_channel_category_constraints.up.sql")
	for _, expected := range []string{
		"CREATE UNIQUE INDEX channel_categories_workspace_name_uidx",
		"ON chat.channel_categories (workspace_id, lower(btrim(name)))",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("channel category migration missing %q", expected)
		}
	}
}

func TestChatMigration_AddsChannelCategoryIndexesAndDropsTheRedundantOne(t *testing.T) {
	migration := readChatMigration(t, "000017_channel_category_constraints.up.sql")
	for _, expected := range []string{
		"CREATE INDEX channel_categories_workspace_position_idx",
		"ON chat.channel_categories (workspace_id, position)",
		// The referencing side of channels_workspace_category_fk: without it a
		// category delete scans every channel to apply ON DELETE SET NULL.
		"CREATE INDEX idx_channels_workspace_category",
		"ON chat.channels (workspace_id, category_id)",
		"WHERE category_id IS NOT NULL",
		// Fully covered by the composite index above.
		"DROP INDEX IF EXISTS chat.idx_channel_categories_workspace",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("channel category migration missing %q", expected)
		}
	}
}

// The rollback must cost no row: bounds come off, categories and channels stay.
func TestChatMigration_ChannelCategoryDownPreservesRowsAndRestoresIndex(t *testing.T) {
	migration := readChatMigration(t, "000017_channel_category_constraints.down.sql")
	for _, expected := range []string{
		"DROP INDEX IF EXISTS chat.idx_channels_workspace_category",
		"DROP INDEX IF EXISTS chat.channel_categories_workspace_position_idx",
		"DROP INDEX IF EXISTS chat.channel_categories_workspace_name_uidx",
		"DROP CONSTRAINT IF EXISTS channel_categories_position_range_check",
		"DROP CONSTRAINT IF EXISTS channel_categories_name_not_reserved_check",
		"DROP CONSTRAINT IF EXISTS channel_categories_name_no_control_check",
		"DROP CONSTRAINT IF EXISTS channel_categories_name_trimmed_check",
		"DROP CONSTRAINT IF EXISTS channel_categories_name_length_check",
		// Restores what the up migration replaced.
		"CREATE INDEX IF NOT EXISTS idx_channel_categories_workspace",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("channel category rollback missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"DROP TABLE",
		"DELETE FROM",
		"UPDATE chat.channels",
		"DROP COLUMN",
	} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("channel category rollback must not contain %q", forbidden)
		}
	}
	if strings.Contains(strings.ToUpper(migration), "DROP SCHEMA") ||
		strings.Contains(strings.ToUpper(migration), "CASCADE") {
		t.Fatal("channel category rollback must not drop the schema or cascade")
	}
}

// The invariants RF-17 depends on but does not introduce: they were established in
// 000001 and 000002 and must keep holding, since the delete path relies on the
// FK clearing category_id rather than on a statement of its own.
func TestChatMigration_ChannelCategoryAssociationStaysWorkspaceBoundAndPreservesChannels(t *testing.T) {
	migration := readChatMigration(t, "000002_chat_enforce_channel_workspace_isolation.up.sql")
	for _, expected := range []string{
		"FOREIGN KEY (workspace_id, category_id)",
		"REFERENCES chat.channel_categories (workspace_id, id)",
		"ON DELETE SET NULL (category_id)",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("category association invariant lost: %q", expected)
		}
	}
	// category_id is nullable in 000001, so a channel with no category is valid
	// and every pre-existing channel stays readable after 000017.
	schema := readChatMigration(t, "000001_chat_domain_schema.up.sql")
	if !strings.Contains(schema, "category_id  UUID        REFERENCES chat.channel_categories (id)") {
		t.Fatal("chat.channels.category_id must remain nullable")
	}
	if strings.Contains(schema, "category_id  UUID        NOT NULL") {
		t.Fatal("chat.channels.category_id must not be NOT NULL")
	}
}

func TestChatMigration_AddsDMConversationTables(t *testing.T) {
	migration := readChatMigration(t, "000003_chat_dm_conversations.up.sql")
	for _, expected := range []string{
		"CREATE TABLE chat.dm_conversations",
		"CREATE TABLE chat.dm_members",
		"direct_pair_key",
		"CHECK (type IN ('direct', 'group'))",
		"CHECK (status IN ('active', 'archived'))",
		"CHECK (role IN ('member'))",
		"CHECK (status IN ('active', 'left'))",
		"CONSTRAINT dm_conversations_direct_pair_key_check CHECK",
		"(type = 'direct' AND direct_pair_key IS NOT NULL)",
		"(type = 'group' AND direct_pair_key IS NULL)",
		"CREATE UNIQUE INDEX idx_dm_conversations_direct_pair_unique",
		"ON chat.dm_conversations (workspace_id, direct_pair_key)",
		"WHERE type = 'direct'",
		"CREATE INDEX idx_dm_conversations_workspace",
		"CREATE INDEX idx_dm_members_user",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("dm migration missing %q", expected)
		}
	}
}

func TestChatMigration_DMDownDoesNotDropSchemaCascade(t *testing.T) {
	migration := readChatMigration(t, "000003_chat_dm_conversations.down.sql")
	if strings.Contains(strings.ToUpper(migration), "DROP SCHEMA") {
		t.Fatal("dm down migration must not drop schema")
	}
	if strings.Contains(strings.ToUpper(migration), "CASCADE") {
		t.Fatal("dm down migration must not use cascade")
	}
}

func TestChatMigration_AddsMessagesTable(t *testing.T) {
	migration := readChatMigration(t, "000004_chat_messages.up.sql")
	for _, expected := range []string{
		"CREATE TABLE chat.messages",
		"workspace_id",
		"channel_id",
		"dm_conversation_id",
		"sender_id",
		"body_text",
		"parent_message_id",
		"forwarded_from_message_id",
		"referenced_message_id",
		"edited_at",
		"deleted_at",
		"CHECK (kind   IN ('user', 'system'))",
		"CHECK (status IN ('active', 'deleted'))",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("messages migration missing %q", expected)
		}
	}
}

func TestChatMigration_EnforcesExactlyOneMessageTarget(t *testing.T) {
	migration := readChatMigration(t, "000004_chat_messages.up.sql")
	for _, expected := range []string{
		"CONSTRAINT messages_exactly_one_target CHECK",
		"(channel_id IS NULL) <> (dm_conversation_id IS NULL)",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("messages migration missing exactly-one-target constraint %q", expected)
		}
	}
}

func TestChatMigration_EnforcesWorkspaceMessageTargetConsistency(t *testing.T) {
	migration := readChatMigration(t, "000004_chat_messages.up.sql")
	for _, expected := range []string{
		"CONSTRAINT messages_workspace_channel_fk",
		"FOREIGN KEY (workspace_id, channel_id)",
		"REFERENCES chat.channels (workspace_id, id)",
		"CONSTRAINT messages_workspace_dm_fk",
		"FOREIGN KEY (workspace_id, dm_conversation_id)",
		"REFERENCES chat.dm_conversations (workspace_id, id)",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("messages migration missing workspace-target consistency %q", expected)
		}
	}
}

func TestChatMigration_AddsMessagesIndexes(t *testing.T) {
	migration := readChatMigration(t, "000004_chat_messages.up.sql")
	for _, expected := range []string{
		"CREATE INDEX idx_messages_channel",
		"CREATE INDEX idx_messages_dm",
		"CREATE INDEX idx_messages_sender",
		"CREATE INDEX idx_messages_parent",
		"CREATE INDEX idx_messages_forwarded",
		"CREATE INDEX idx_messages_referenced",
		"WHERE channel_id IS NOT NULL",
		"WHERE dm_conversation_id IS NOT NULL",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("messages migration missing index %q", expected)
		}
	}
}

func TestChatMigration_AddsParentReplyMigration(t *testing.T) {
	up := readChatMigration(t, "000012_message_parent_reply.up.sql")
	for _, expected := range []string{
		"ADD COLUMN IF NOT EXISTS parent_message_id UUID",
		"DROP CONSTRAINT IF EXISTS messages_parent_message_id_fkey",
		"REFERENCES chat.messages (id) ON DELETE SET NULL",
	} {
		if !strings.Contains(up, expected) {
			t.Fatalf("parent reply up migration missing %q", expected)
		}
	}
	redundantParentIndex := "idx_messages_parent" + "_message_id"
	if strings.Contains(up, redundantParentIndex) {
		t.Fatal("parent reply up migration must not create redundant parent_message_id index")
	}

	down := readChatMigration(t, "000012_message_parent_reply.down.sql")
	for _, expected := range []string{
		"DROP CONSTRAINT IF EXISTS messages_parent_message_id_fkey",
		"ADD CONSTRAINT messages_parent_message_id_fkey",
		"FOREIGN KEY (parent_message_id) REFERENCES chat.messages (id)",
	} {
		if !strings.Contains(down, expected) {
			t.Fatalf("parent reply down migration missing %q", expected)
		}
	}
	if strings.Contains(down, "DROP COLUMN") {
		t.Fatal("parent reply down migration must not drop parent_message_id")
	}
	if strings.Contains(down, "CREATE INDEX") {
		t.Fatal("parent reply down migration must not create indexes")
	}
}

func TestChatMigration_CrossChannelReferencePreservesUnavailableTombstone(t *testing.T) {
	up := readChatMigration(t, "000014_cross_channel_message_reference.up.sql")
	if !strings.Contains(up, "DROP CONSTRAINT IF EXISTS messages_referenced_message_id_fkey") {
		t.Fatal("RF-09 up migration must allow the opaque source ID to outlive its origin")
	}
	down := readChatMigration(t, "000014_cross_channel_message_reference.down.sql")
	for _, expected := range []string{
		"ADD CONSTRAINT messages_referenced_message_id_fkey",
		"FOREIGN KEY (referenced_message_id) REFERENCES chat.messages (id) NOT VALID",
	} {
		if !strings.Contains(down, expected) {
			t.Fatalf("RF-09 down migration missing %q", expected)
		}
	}
}

func TestChatMigration_MessageForwardIdempotencyIsScopedAndPrivate(t *testing.T) {
	up := readChatMigration(t, "000015_message_forward_idempotency.up.sql")
	for _, expected := range []string{
		"forward_idempotency_key VARCHAR(128)",
		"workspace_id, sender_id, channel_id, forward_idempotency_key",
		"WHERE forward_idempotency_key IS NOT NULL",
	} {
		if !strings.Contains(up, expected) {
			t.Fatalf("forward idempotency migration missing %q", expected)
		}
	}
	down := readChatMigration(t, "000015_message_forward_idempotency.down.sql")
	for _, expected := range []string{
		"DROP INDEX IF EXISTS chat.messages_forward_idempotency_uidx",
		"DROP COLUMN IF EXISTS forward_idempotency_key",
	} {
		if !strings.Contains(down, expected) {
			t.Fatalf("forward idempotency rollback missing %q", expected)
		}
	}
}

func TestChatMigration_MessagesDownDoesNotDropSchemaCascade(t *testing.T) {
	migration := readChatMigration(t, "000004_chat_messages.down.sql")
	if strings.Contains(strings.ToUpper(migration), "DROP SCHEMA") {
		t.Fatal("messages down migration must not drop schema")
	}
	for _, expected := range []string{
		"DROP TABLE IF EXISTS chat.messages",
		"DROP CONSTRAINT IF EXISTS dm_conversations_workspace_id_id_unique",
		"DROP CONSTRAINT IF EXISTS channels_workspace_id_id_unique",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("messages down migration missing %q", expected)
		}
	}
}

func TestChatMigration_AddsVersionedMessageBodyFormat(t *testing.T) {
	migration := readChatMigration(t, "000005_chat_message_body_format.up.sql")
	for _, expected := range []string{
		"ADD COLUMN body_format",
		"NOT NULL DEFAULT 'v1'",
		"CHECK (body_format IN ('v1', 'v2'))",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("body format migration missing %q", expected)
		}
	}
	down := readChatMigration(t, "000005_chat_message_body_format.down.sql")
	if !strings.Contains(down, "DROP COLUMN IF EXISTS body_format") {
		t.Fatal("body format down migration must remove body_format")
	}
}

func TestChatMigration_AddsV3MentionsAndDirectedOutbox(t *testing.T) {
	migration := readChatMigration(t, "000006_chat_mentions.up.sql")
	for _, expected := range []string{
		"CHECK (body_format IN ('v1', 'v2', 'v3'))",
		"CREATE TABLE chat.notification_outbox",
		"recipient_user_id UUID",
		"UNIQUE (message_id, recipient_user_id, kind)",
		"WHERE status = 'pending'",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("mentions migration missing %q", expected)
		}
	}
	down := readChatMigration(t, "000006_chat_mentions.down.sql")
	if !strings.Contains(down, "DROP TABLE IF EXISTS chat.notification_outbox") ||
		!strings.Contains(down, "CHECK (body_format IN ('v1', 'v2'))") {
		t.Fatal("mentions down migration must drop outbox and restore v2 constraint")
	}
}

func TestChatMigration_AddsMessageReactionsWithRollback(t *testing.T) {
	migration := readChatMigration(t, "000008_message_reactions.up.sql")
	for _, expected := range []string{
		"CREATE TABLE chat.message_reactions",
		"FOREIGN KEY (message_id)",
		"REFERENCES chat.messages (id) ON DELETE CASCADE",
		"FOREIGN KEY (user_id)",
		"REFERENCES auth.users (id) ON DELETE CASCADE",
		"PRIMARY KEY (message_id, user_id, emoji)",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("reaction migration missing %q", expected)
		}
	}
	down := readChatMigration(t, "000008_message_reactions.down.sql")
	if !strings.Contains(down, "DROP TABLE IF EXISTS chat.message_reactions") || strings.Contains(strings.ToUpper(down), "DROP SCHEMA") {
		t.Fatal("reaction rollback must only drop message_reactions")
	}
}

func TestChatMigration_AddsMessageReactionLookupIndex(t *testing.T) {
	migrations := readAllChatUpMigrations(t)
	if !strings.Contains(migrations, "CREATE INDEX IF NOT EXISTS message_reactions_message_id_idx") ||
		!strings.Contains(migrations, "ON chat.message_reactions (message_id, created_at)") {
		t.Fatal("chat migrations must add the message reaction lookup index")
	}
	down := readChatMigration(t, "000009_message_reactions_message_id_index.down.sql")
	if !strings.Contains(down, "DROP INDEX IF EXISTS chat.message_reactions_message_id_idx") {
		t.Fatal("reaction lookup index migration must have a schema-qualified rollback")
	}
}

func TestChatMigration_AddsMessageFavoritesWithRollback(t *testing.T) {
	migration := readChatMigration(t, "000010_message_favorites.up.sql")
	for _, expected := range []string{
		"CREATE TABLE chat.message_favorites",
		"FOREIGN KEY (user_id)",
		"REFERENCES auth.users (id) ON DELETE CASCADE",
		"FOREIGN KEY (message_id)",
		"REFERENCES chat.messages (id) ON DELETE CASCADE",
		"PRIMARY KEY (user_id, message_id)",
		"CREATE INDEX message_favorites_user_created_idx",
		"ON chat.message_favorites (user_id, created_at DESC, message_id DESC)",
		"CREATE INDEX message_favorites_message_id_idx",
	} {
		if !strings.Contains(migration, expected) {
			t.Fatalf("favorites migration missing %q", expected)
		}
	}
	down := readChatMigration(t, "000010_message_favorites.down.sql")
	if !strings.Contains(down, "DROP TABLE IF EXISTS chat.message_favorites") || strings.Contains(strings.ToUpper(down), "DROP SCHEMA") {
		t.Fatal("favorites rollback must only drop message_favorites")
	}
}

func TestChatMigration_AddsMessageEditHistoryWithRollback(t *testing.T) {
	up := readChatMigration(t, "000013_message_edit_history.up.sql")
	for _, expected := range []string{
		"ADD COLUMN edit_count INTEGER NOT NULL DEFAULT 0",
		"ADD COLUMN edit_window_seconds INTEGER NULL DEFAULT 900",
		"CREATE TABLE chat.message_edit_history",
		"REFERENCES chat.messages (id) ON DELETE CASCADE",
		"REFERENCES auth.users (id)",
		"ON chat.message_edit_history (message_id, versioned_at DESC)",
	} {
		if !strings.Contains(up, expected) {
			t.Fatalf("edit-history migration missing %q", expected)
		}
	}
	down := readChatMigration(t, "000013_message_edit_history.down.sql")
	for _, expected := range []string{
		"DROP TABLE IF EXISTS chat.message_edit_history",
		"DROP COLUMN IF EXISTS edit_window_seconds",
		"DROP COLUMN IF EXISTS edit_count",
	} {
		if !strings.Contains(down, expected) {
			t.Fatalf("edit-history rollback missing %q", expected)
		}
	}
	if strings.Contains(down, "DROP COLUMN IF EXISTS edited_at") {
		t.Fatal("000013 rollback must preserve edited_at owned by 000004")
	}
}
