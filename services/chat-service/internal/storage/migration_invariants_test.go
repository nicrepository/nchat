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
