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
