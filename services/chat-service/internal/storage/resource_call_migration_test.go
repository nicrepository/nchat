package storage_test

import (
	"strings"
	"testing"
)

func TestResourceCallMigrationAddsTargetsAndExpiringPresence(t *testing.T) {
	up := readChatMigration(t, "000028_resource_call_lifecycle.up.sql")
	for _, expected := range []string{
		"ADD COLUMN target_type TEXT",
		"ADD COLUMN target_id UUID",
		"target_type = 'user'",
		"target_type IN ('channel', 'dm')",
		"calls_one_active_resource_idx",
		"CREATE TABLE chat.call_participant_leases",
		"PRIMARY KEY (call_id, user_id)",
		"ON DELETE CASCADE",
	} {
		if !strings.Contains(up, expected) {
			t.Fatalf("resource call migration missing %q", expected)
		}
	}
	down := readChatMigration(t, "000028_resource_call_lifecycle.down.sql")
	if !strings.Contains(down, "DROP TABLE IF EXISTS chat.call_participant_leases") ||
		!strings.Contains(down, "DROP COLUMN target_type") {
		t.Fatal("resource call rollback does not remove only its added schema")
	}
}
