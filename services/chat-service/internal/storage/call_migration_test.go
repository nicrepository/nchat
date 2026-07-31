package storage_test

import (
	"strings"
	"testing"
)

func TestChatMigration_AddsAuthoritativeCallLifecycle(t *testing.T) {
	up := readChatMigration(t, "000019_call_lifecycle.up.sql")
	for _, expected := range []string{
		"CREATE TABLE chat.calls",
		"request_id UUID NOT NULL",
		"caller_id UUID NOT NULL",
		"callee_id UUID NOT NULL",
		"CHECK (caller_id <> callee_id)",
		"CHECK (call_type IN ('audio', 'video'))",
		"CHECK (status IN ('ringing', 'active', 'declined', 'cancelled', 'timed_out', 'ended'))",
		"UNIQUE (workspace_id, caller_id, request_id)",
		"CREATE INDEX calls_due_ringing_idx",
		"WHERE status = 'ringing'",
	} {
		if !strings.Contains(up, expected) {
			t.Fatalf("call lifecycle migration missing %q", expected)
		}
	}

	down := readChatMigration(t, "000019_call_lifecycle.down.sql")
	if !strings.Contains(down, "DROP TABLE IF EXISTS chat.calls") {
		t.Fatal("call lifecycle rollback must drop only chat.calls")
	}
	if strings.Contains(strings.ToUpper(down), "DROP SCHEMA") ||
		strings.Contains(strings.ToUpper(down), "CASCADE") {
		t.Fatal("call lifecycle rollback must not drop the schema or cascade")
	}
}
