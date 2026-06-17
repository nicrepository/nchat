package storage

import (
	"strings"
	"testing"
)

func TestMessageColumns_CastsUUIDColumnsToText(t *testing.T) {
	cols := messageColumns("")
	if !strings.HasPrefix(cols, "id::text, workspace_id::text") {
		t.Fatalf("messageColumns must cast id and workspace_id first for pgx string scanning, got:\n%s", cols)
	}
	if !strings.Contains(cols, "sender_id::text") {
		t.Fatalf("messageColumns must cast sender_id for pgx string scanning, got:\n%s", cols)
	}
}

func TestMessageColumns_CastsAliasedUUIDColumnsToText(t *testing.T) {
	cols := messageColumns("m")
	if !strings.HasPrefix(cols, "m.id::text, m.workspace_id::text") {
		t.Fatalf("messageColumns must cast aliased id and workspace_id first for pgx string scanning, got:\n%s", cols)
	}
	if !strings.Contains(cols, "m.sender_id::text") {
		t.Fatalf("messageColumns must cast aliased sender_id for pgx string scanning, got:\n%s", cols)
	}
}
