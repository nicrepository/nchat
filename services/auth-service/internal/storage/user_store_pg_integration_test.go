package storage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// The workspace user listing spans two schemas: the membership that scopes it
// lives in chat, the accounts it returns live in auth. The tests below run
// against both, applied from the real migrations, because the properties they
// check — that paging is total, that it never crosses a workspace, and that the
// plan can be served by an index — are properties of the actual DDL. A mock can
// confirm the SQL we send; only a database can confirm what it does.
//
// Gated on AUTH_TEST_DATABASE_URL like the avatar integration test beside it,
// and skipped when unset.

// applyChatMigrations resets the chat schema and applies every up migration, so
// chat.workspace_members carries its real primary key and indexes.
func applyChatMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var db string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&db); err != nil {
		t.Fatalf("current database: %v", err)
	}
	if !strings.HasSuffix(db, "_test") {
		t.Fatalf("refusing to run destructive migration on non-test database %q", db)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })

	_, currentFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..")
	dir := filepath.Join(root, "migrations", "chat")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			names = append(names, e.Name())
		}
	}
	// Numeric prefixes, applied in order: a later migration alters what an
	// earlier one created.
	sort.Strings(names)
	for _, name := range names {
		sql, readErr := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // repo-local migrations
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if _, execErr := pool.Exec(ctx, string(sql)); execErr != nil {
			t.Fatalf("apply %s: %v", name, execErr)
		}
	}
}

// listingFixture is the two-tenant world the paging tests read.
type listingFixture struct {
	workspaceA string
	workspaceB string
	// idsA is every user id in workspace A, in the order the listing must
	// return them: ascending by id.
	idsA []string
	idsB []string
}

// seedTwoWorkspaces creates two workspaces whose member ids interleave.
//
// Interleaving is the point: if the workspace filter were ever dropped from the
// query or weakened by the cursor, a page of A would pick up B's rows, and with
// ids sorted apart that could still look plausible. Alternating them means the
// very next row after any of A's is one of B's, so a leak is immediate.
func seedTwoWorkspaces(t *testing.T, pool *pgxpool.Pool, perWorkspace int) listingFixture {
	t.Helper()
	ctx := context.Background()

	f := listingFixture{
		workspaceA: "aaaaaaaa-0000-4000-8000-000000000001",
		workspaceB: "bbbbbbbb-0000-4000-8000-000000000002",
	}
	for _, ws := range []struct{ id, slug string }{
		{f.workspaceA, "workspace-a"},
		{f.workspaceB, "workspace-b"},
	} {
		// A workspace must have a general channel by the end of the
		// transaction that created it — the constraint trigger is deferred
		// precisely so the pair can be inserted together. Creating them in one
		// transaction here is what the application does too.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO chat.workspaces (id, slug, name, status)
			VALUES ($1::uuid, $2, $2, 'active')`, ws.id, ws.slug); err != nil {
			t.Fatalf("insert workspace %s: %v", ws.slug, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO chat.channels (workspace_id, slug, display_name, type, status, is_general)
			VALUES ($1::uuid, 'geral', 'Geral', 'public', 'active', true)`, ws.id); err != nil {
			t.Fatalf("insert general channel for %s: %v", ws.slug, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit workspace %s: %v", ws.slug, err)
		}
	}

	for i := 0; i < perWorkspace; i++ {
		for _, ws := range []struct {
			id   string
			list *[]string
			tag  string
		}{
			{f.workspaceA, &f.idsA, "a"},
			{f.workspaceB, &f.idsB, "b"},
		} {
			// Ids alternate between the workspaces: ...-0000, ...-0001, ...
			// with even ones in A and odd ones in B.
			seq := i * 2
			if ws.tag == "b" {
				seq++
			}
			userID := fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
			if _, err := pool.Exec(ctx, `
				INSERT INTO auth.users (id, email, display_name, status, auth_source)
				VALUES ($1::uuid, $2, $3, 'active', 'manual')`,
				userID,
				fmt.Sprintf("user-%s-%d@example.com", ws.tag, i),
				// Names descend as ids ascend, so any leftover ordering by name
				// would produce a visibly different sequence.
				fmt.Sprintf("User %s %03d", strings.ToUpper(ws.tag), perWorkspace-i),
			); err != nil {
				t.Fatalf("insert user %s: %v", userID, err)
			}
			if _, err := pool.Exec(ctx, `
				INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
				VALUES ($1::uuid, $2::uuid, 'member', 'active')`, ws.id, userID); err != nil {
				t.Fatalf("insert membership %s: %v", userID, err)
			}
			*ws.list = append(*ws.list, userID)
		}
	}
	// Without statistics the planner estimates one row everywhere and picks a
	// plan on noise, which would make any assertion about the plan meaningless.
	if _, err := pool.Exec(ctx, `ANALYZE chat.workspace_members, chat.workspaces, auth.users`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	sort.Strings(f.idsA)
	sort.Strings(f.idsB)
	return f
}

func newListingFixture(t *testing.T, perWorkspace int) (*storage.PGXUserStore, listingFixture) {
	t.Helper()
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	applyChatMigrations(t, pool)
	return storage.NewPGXUserStore(pool), seedTwoWorkspaces(t, pool, perWorkspace)
}

// Paging a workspace larger than one page must return every member exactly
// once, in id order, and stop — the two failure modes of a keyset paginator
// being a row returned twice and a row skipped at a page boundary.
func TestPGXUserStore_ListWorkspaceUsers_PagesWholeWorkspaceExactlyOnce(t *testing.T) {
	const perWorkspace = 25
	const pageSize = 10
	store, fixture := newListingFixture(t, perWorkspace)

	var got []string
	seen := map[string]int{}
	after := ""
	for page := 0; ; page++ {
		if page > perWorkspace {
			t.Fatal("paging did not terminate")
		}
		// limit+1, exactly as the service asks for it.
		users, err := store.ListWorkspaceUsers(context.Background(), fixture.workspaceA, pageSize+1, after)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(users) == 0 {
			break
		}
		if len(users) > pageSize+1 {
			t.Fatalf("page %d returned %d rows, more than the limit allows", page, len(users))
		}
		if len(users) <= pageSize {
			// Last page: no lookahead row came back.
			for _, u := range users {
				got = append(got, u.ID)
				seen[u.ID]++
			}
			break
		}
		for _, u := range users[:pageSize] {
			got = append(got, u.ID)
			seen[u.ID]++
		}
		after = users[pageSize-1].ID
	}

	if len(got) != perWorkspace {
		t.Fatalf("expected %d users across all pages, got %d", perWorkspace, len(got))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("user %s appeared %d times", id, count)
		}
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("pages must arrive in ascending id order, got %v", got)
	}
	for i, want := range fixture.idsA {
		if got[i] != want {
			t.Fatalf("row %d: expected %s, got %s", i, want, got[i])
		}
	}
}

// Not one row of the other workspace, on any page — even though their ids
// interleave, so B's rows sit between A's throughout the index.
func TestPGXUserStore_ListWorkspaceUsers_NeverCrossesWorkspaces(t *testing.T) {
	const perWorkspace = 25
	store, fixture := newListingFixture(t, perWorkspace)

	foreign := map[string]bool{}
	for _, id := range fixture.idsB {
		foreign[id] = true
	}

	after := ""
	for page := 0; page <= perWorkspace; page++ {
		users, err := store.ListWorkspaceUsers(context.Background(), fixture.workspaceA, 11, after)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(users) == 0 {
			return
		}
		for _, u := range users {
			if foreign[u.ID] {
				t.Fatalf("workspace B's user %s appeared in a listing of workspace A", u.ID)
			}
		}
		if len(users) <= 10 {
			return
		}
		after = users[9].ID
	}
	t.Fatal("paging did not terminate")
}

// A cursor naming a user of the other workspace cannot reach that workspace's
// rows: it is only a position, and the workspace filter is applied regardless.
func TestPGXUserStore_ListWorkspaceUsers_ForeignCursorStaysInsideTheWorkspace(t *testing.T) {
	store, fixture := newListingFixture(t, 10)

	users, err := store.ListWorkspaceUsers(context.Background(), fixture.workspaceA, 100, fixture.idsB[0])
	if err != nil {
		t.Fatalf("ListWorkspaceUsers: %v", err)
	}
	for _, u := range users {
		for _, foreign := range fixture.idsB {
			if u.ID == foreign {
				t.Fatalf("a cursor from workspace B surfaced B's user %s", u.ID)
			}
		}
	}
}

// Members who left are excluded and suspended ones are kept, across a page
// boundary — the paging rewrite must not have quietly changed who is listed.
func TestPGXUserStore_ListWorkspaceUsers_MembershipFiltersSurvivePaging(t *testing.T) {
	store, fixture := newListingFixture(t, 10)
	pool := connectAuthTestDB(t)

	left := fixture.idsA[0]
	suspended := fixture.idsA[1]
	if _, err := pool.Exec(context.Background(), `
		UPDATE chat.workspace_members SET status = 'left'
		WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, fixture.workspaceA, left); err != nil {
		t.Fatalf("mark left: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE chat.workspace_members SET status = 'suspended'
		WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, fixture.workspaceA, suspended); err != nil {
		t.Fatalf("mark suspended: %v", err)
	}

	users, err := store.ListWorkspaceUsers(context.Background(), fixture.workspaceA, 100, "")
	if err != nil {
		t.Fatalf("ListWorkspaceUsers: %v", err)
	}
	found := map[string]bool{}
	for _, u := range users {
		found[u.ID] = true
	}
	if found[left] {
		t.Fatal("a member who left must not be listed")
	}
	if !found[suspended] {
		t.Fatal("a suspended member must still be listed — reactivating them is the point of the screen")
	}
}

// ── The plan, read from PostgreSQL ─────────────────────────────────────────

// explainListing returns the JSON plan PostgreSQL produces for the listing.
//
// The SQL is repeated here rather than exported from the store: this test asks
// whether a query of this shape can be served by the index, and a copy that
// drifted from the original would fail the shape assertions in the unit tests
// long before it reached here.
func explainListing(t *testing.T, pool *pgxpool.Pool, workspaceID, afterUserID string) string {
	t.Helper()
	var after any
	if afterUserID != "" {
		after = afterUserID
	}

	// A seeded test table is small enough that a sequential scan is genuinely
	// the cheaper plan, and the planner is right to pick it. Disabling it asks
	// the question this test is actually about — whether an index-backed plan
	// exists at all — rather than which plan wins at this row count.
	if _, err := pool.Exec(context.Background(), `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `RESET enable_seqscan`) })

	var plan []byte
	err := pool.QueryRow(context.Background(), `
		EXPLAIN (FORMAT JSON)
		SELECT u.id::text, u.email::text, u.display_name, COALESCE(u.full_name, ''),
		       u.status, u.auth_source, u.created_at
		FROM chat.workspace_members wm
		JOIN auth.users u
		  ON u.id = wm.user_id AND u.deleted_at IS NULL
		WHERE wm.workspace_id = $1::uuid
		  AND wm.status <> 'left'
		  AND ($2::uuid IS NULL OR wm.user_id > $2::uuid)
		ORDER BY wm.user_id
		LIMIT $3`,
		workspaceID, after, 51,
	).Scan(&plan)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	return string(plan)
}

// nodeTypes walks the plan tree and collects every "Node Type".
func nodeTypes(t *testing.T, planJSON string) []string {
	t.Helper()
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(planJSON), &parsed); err != nil {
		t.Fatalf("parse plan: %v (%s)", err, planJSON)
	}
	var out []string
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		if nt, ok := node["Node Type"].(string); ok {
			out = append(out, nt)
		}
		if kids, ok := node["Plans"].([]any); ok {
			for _, kid := range kids {
				if m, ok := kid.(map[string]any); ok {
					walk(m)
				}
			}
		}
	}
	for _, root := range parsed {
		if plan, ok := root["Plan"].(map[string]any); ok {
			walk(plan)
		}
	}
	return out
}

// The finding was that the ordering could not be served by an index, so every
// page sorted the workspace's whole membership. This asserts the opposite
// directly, from the planner: the membership side is read through the primary
// key, in order, with no Sort above it.
func TestPGXUserStore_ListWorkspaceUsers_PlanIsIndexBackedWithoutSort(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	applyChatMigrations(t, pool)
	fixture := seedTwoWorkspaces(t, pool, 25)

	for _, tc := range []struct {
		name  string
		after string
	}{
		{"first page", ""},
		{"resumed page", fixture.idsA[9]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := explainListing(t, pool, fixture.workspaceA, tc.after)

			if !strings.Contains(plan, "workspace_members_pkey") {
				t.Fatalf("the membership scan must use the (workspace_id, user_id) primary key:\n%s", plan)
			}
			for _, node := range nodeTypes(t, plan) {
				// A Sort anywhere means the ordering was not delivered by the
				// index — which is the entire cost problem this change fixes.
				if node == "Sort" || node == "Incremental Sort" {
					t.Fatalf("the plan must not sort; the index supplies the order:\n%s", plan)
				}
			}
		})
	}
}
