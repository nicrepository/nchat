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

// Every projection of a message must carry its priority (issue #821). The
// shared column list is the one place that can be true for all of them at once
// — the list, the get, the create's outer SELECT, the edit's read-back, the
// favourites and pins listings all read through it — so a projection that
// forgot priority would be a projection that stopped matching its scan.
func TestMessageColumns_ProjectsPriority(t *testing.T) {
	for _, alias := range []string{"", "m"} {
		prefix := ""
		if alias != "" {
			prefix = alias + "."
		}
		if !strings.Contains(messageColumns(alias), prefix+"priority") {
			t.Fatalf("messageColumns(%q) must project %spriority, got:\n%s", alias, prefix, messageColumns(alias))
		}
	}
}

// The column is projected raw rather than through a COALESCE. It is NOT NULL
// with a default in the database, so defaulting it again in SQL would only hide
// a schema that had stopped matching this assumption.
func TestMessageColumns_ReadsPriorityDirectly(t *testing.T) {
	if strings.Contains(messageColumns("m"), "COALESCE(m.priority") {
		t.Error("priority is NOT NULL DEFAULT 'standard'; a COALESCE here would mask a schema drift")
	}
}
