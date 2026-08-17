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

func TestChatLinkScanInconclusiveMigrationKeepsTTLIndexVerdictOnly(t *testing.T) {
	up := readChatMigration(t, "000026_link_scan_inconclusive.up.sql")
	if !strings.Contains(up, "WHERE status IN ('safe', 'malicious')") {
		t.Fatal("the verdict TTL index must exclude inconclusive scans")
	}
}

func TestChatLinkScanInconclusiveDownMigrationNeverRequeuesTerminalRows(t *testing.T) {
	down := readChatMigration(t, "000026_link_scan_inconclusive.down.sql")
	if strings.Contains(down, "SET status = 'pending'") || strings.Contains(down, "scan_uuid = NULL") {
		t.Fatal("rollback must not turn inconclusive scans back into provider work")
	}
	for _, expected := range []string{"RAISE EXCEPTION", "WHERE status = 'inconclusive'"} {
		if !strings.Contains(down, expected) {
			t.Fatalf("safe inconclusive rollback must contain %q", expected)
		}
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

// RF-32 (issue #458): the attachment size policy is a whole number of MiB
// inside a fixed range, and the column enforces both halves.
//
// The whole-MiB half is what stops a value like 1572864 (1.5 MiB) from ever
// being stored: the admin UI edits whole MiB, so such a row could not be shown
// there without being changed, and an ordinary save would then overwrite a
// limit nobody edited. Refusing the value is the only non-destructive answer,
// and this is the backstop behind uploadpolicy.Valid.
func TestChatMigration_BoundsWorkspaceMaxUploadBytesToWholeMiB(t *testing.T) {
	migration := readChatMigration(t, "000020_workspace_max_upload_bytes.up.sql")

	for _, fragment := range []string{
		"ADD COLUMN max_upload_bytes BIGINT NOT NULL DEFAULT 262144000",
		"max_upload_bytes BETWEEN 1048576 AND 536870912",
		"max_upload_bytes % 1048576 = 0",
	} {
		if !strings.Contains(migration, fragment) {
			t.Fatalf("migration must contain %q", fragment)
		}
	}

	// The default has to satisfy the constraint it ships with, or every row the
	// column is added to would violate it.
	const defaultBytes = 262144000
	if defaultBytes%1048576 != 0 {
		t.Fatal("the 250 MiB default must be a whole number of MiB")
	}
	if defaultBytes < 1048576 || defaultBytes > 536870912 {
		t.Fatal("the default must sit inside the CHECK bounds")
	}
}

// RF-32: the message <-> attachment edge.
//
// Three properties matter and all three are schema, not application code:
// a link dies with its message, an attachment belongs to at most one message,
// and no foreign key crosses into the files schema — which is what keeps
// migrations/chat and migrations/files independent of each other's order, the
// convention files/000001 states and chat/000001 already follows for auth.
func TestChatMigration_BindsAttachmentsToExactlyOneMessage(t *testing.T) {
	migration := readChatMigration(t, "000021_message_attachments.up.sql")

	for _, fragment := range []string{
		"CREATE TABLE chat.message_attachments",
		"REFERENCES chat.messages (id) ON DELETE CASCADE",
		"PRIMARY KEY (message_id, attachment_id)",
		"UNIQUE (attachment_id)",
		"CHECK (position BETWEEN 0 AND 9)",
	} {
		if !strings.Contains(migration, fragment) {
			t.Fatalf("message attachment migration missing %q", fragment)
		}
	}
	if strings.Contains(migration, "REFERENCES files.") {
		t.Fatal("no foreign key may cross into the files schema")
	}
	// The link table is new, so nothing may be rewritten to create it.
	for _, forbidden := range []string{"UPDATE files.", "DELETE FROM files.", "ALTER TABLE files."} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("migration must not touch files.attachments: %q", forbidden)
		}
	}
}

// The rollback removes the edge and nothing else: no attachment, no message and
// no column owned by another migration.
func TestChatMigration_DownDropsOnlyTheMessageAttachmentEdge(t *testing.T) {
	down := readChatMigration(t, "000021_message_attachments.down.sql")

	if !strings.Contains(down, "DROP TABLE IF EXISTS chat.message_attachments") {
		t.Fatal("rollback must drop chat.message_attachments")
	}
	if strings.Count(down, "DROP ") != 1 {
		t.Fatalf("rollback must contain exactly one DROP: %s", down)
	}
	for _, forbidden := range []string{"DELETE FROM", "TRUNCATE", "ALTER TABLE", "UPDATE "} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("rollback must not contain %q", forbidden)
		}
	}
}

// ── RF-74: workspace moderator and guest channel scope (000022) ──────────────

// The role CHECK admits exactly the five RF-74 roles and nothing else. A widened
// constraint is the one part of this feature that cannot be un-widened by a code
// change, so the accepted set is asserted literally.
func TestChatMigration_AcceptsExactlyTheFiveRF74WorkspaceRoles(t *testing.T) {
	migration := readChatMigration(t, "000022_workspace_moderator_and_guest_channel_scope.up.sql")

	if !strings.Contains(migration, "CHECK (role IN ('owner', 'admin', 'moderator', 'member', 'guest'))") {
		t.Fatal("the workspace role CHECK must accept exactly owner/admin/moderator/member/guest")
	}
	// The per-channel role is a different scope and must not be redefined here.
	// The function below reads chat.channel_members; nothing may alter it.
	if strings.Contains(migration, "ALTER TABLE chat.channel_members") {
		t.Fatal("the workspace role migration must not alter chat.channel_members")
	}
	// Widening the accepted set must not reclassify anybody into it.
	for _, forbidden := range []string{"UPDATE chat.workspace_members", "DELETE FROM", "TRUNCATE"} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("the up migration must not rewrite membership rows: %q", forbidden)
		}
	}
}

// chat.channel_visible_to_user is the single definition of channel read access
// shared by chat-service, file-service and media-service. The RF-74 guest rule
// lives inside it: an explicit chat.channel_members row admits any role, and a
// public channel admits only the roles on the allowlist — which is an allowlist
// precisely so an unrecognised role is denied rather than treated as a member.
func TestChatMigration_ChannelVisibilityFunctionScopesGuestsToTheirChannels(t *testing.T) {
	migration := readChatMigration(t, "000022_workspace_moderator_and_guest_channel_scope.up.sql")

	for _, fragment := range []string{
		"CREATE OR REPLACE FUNCTION chat.channel_visible_to_user(p_channel_id UUID, p_user_id UUID)",
		"JOIN chat.workspace_members wm",
		"wm.status = 'active'",
		"LEFT JOIN chat.channel_members cm",
		"cm.user_id IS NOT NULL",
		"wm.role IN ('owner', 'admin', 'moderator', 'member')",
		"c.type = 'public'",
	} {
		if !strings.Contains(migration, fragment) {
			t.Fatalf("channel visibility function missing %q", fragment)
		}
	}
	// "not a guest" would admit any unknown role; the allowlist is the point.
	if strings.Contains(migration, "<> 'guest'") || strings.Contains(migration, "!= 'guest'") {
		t.Fatal("the role test must be an allowlist, not a guest denylist")
	}
}

// The rollback restores the previous policy without destroying data. It has to
// move workspace moderators to a role the old CHECK accepts, and 'member' — the
// nearest role with no management authority — is the only direction a rollback
// may take.
func TestChatMigration_DownDemotesModeratorsAndRestoresThePreviousPolicy(t *testing.T) {
	down := readChatMigration(t, "000022_workspace_moderator_and_guest_channel_scope.down.sql")

	for _, fragment := range []string{
		"UPDATE chat.workspace_members",
		"SET role = 'member'",
		"WHERE role = 'moderator'",
		"CHECK (role IN ('owner', 'admin', 'member', 'guest'))",
		"CREATE OR REPLACE FUNCTION chat.channel_visible_to_user",
	} {
		if !strings.Contains(down, fragment) {
			t.Fatalf("rollback missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"DELETE FROM", "TRUNCATE", "DROP TABLE", "DROP FUNCTION"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("rollback must not destroy data or the shared function: %q", forbidden)
		}
	}
	// A rollback that widened access silently would be worse than one that
	// failed: the restored body must admit public channels for everyone again,
	// which is only correct because it is the pre-RF-74 policy.
	if !strings.Contains(down, "c.is_general = true OR c.type = 'public' OR cm.user_id IS NOT NULL") {
		t.Fatal("rollback must restore the exact pre-RF-74 visibility body")
	}
}

// Every query in this package that decides channel read access must ask the
// shared function rather than restating the predicate. A reintroduced inline
// copy is how the guest scope would come apart one store at a time — silently,
// and only for the paths that copy went into.
func TestChatStores_ChannelReadAccessIsOnlyTheSharedFunction(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve storage package path")
	}
	sources, err := filepath.Glob(filepath.Join(filepath.Dir(currentFile), "*.go"))
	if err != nil {
		t.Fatalf("list storage sources: %v", err)
	}
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		contents, err := os.ReadFile(path) //nolint:gosec // Glob is restricted to this package.
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Base(path), err)
		}
		for _, inlined := range []string{
			"c.type = 'public' OR cm.user_id IS NOT NULL",
			"c.is_general = true OR c.type = 'public'",
		} {
			if strings.Contains(string(contents), inlined) {
				t.Fatalf("%s restates the channel read policy (%q); call chat.channel_visible_to_user instead",
					filepath.Base(path), inlined)
			}
		}
	}
}
