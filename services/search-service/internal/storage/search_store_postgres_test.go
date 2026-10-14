package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/search-service/internal/domain"
	"github.com/nicrepository/nchat/services/search-service/internal/service"
	"github.com/nicrepository/nchat/services/search-service/internal/storage"
)

// Issue #900. What the search may return is decided by SQL, so it is proven
// against a real PostgreSQL carrying the real auth, chat and files migrations —
// a mocked driver can only show that a query was sent, not what it admits.
//
// Opt-in through SEARCH_TEST_DATABASE_URL, which must name a *_test database:
// the suite drops and recreates the auth, chat and files schemas it seeds.

const (
	workspaceID = "00000000-0000-0000-0000-000000000001" // seeded by chat 000001
	userA       = "a0000000-0000-4000-8000-00000000000a"
	userB       = "a0000000-0000-4000-8000-00000000000b"
	userC       = "a0000000-0000-4000-8000-00000000000c"
	guestG      = "a0000000-0000-4000-8000-000000000009"
	suspendedS  = "a0000000-0000-4000-8000-000000000005"

	chPublic   = "c0000000-0000-4000-8000-000000000001"
	chPrivA    = "c0000000-0000-4000-8000-000000000002"
	chPrivB    = "c0000000-0000-4000-8000-000000000003"
	chArchived = "c0000000-0000-4000-8000-000000000004"

	dmAB      = "d0000000-0000-4000-8000-000000000001"
	dmBC      = "d0000000-0000-4000-8000-000000000002"
	groupABC  = "d0000000-0000-4000-8000-000000000003"
	groupBC   = "d0000000-0000-4000-8000-000000000004"
	groupLeft = "d0000000-0000-4000-8000-000000000005"
)

func TestSearchAuthorizationPostgreSQL(t *testing.T) {
	conn := openSearchTestDatabase(t)
	seedSearchFixture(t, conn)
	store := storage.NewPGXSearchStore(conn)
	svc := service.New(store)
	ctx := t.Context()

	t.Run("channels: public and member-private only", func(t *testing.T) {
		rows, err := store.Channels(ctx, userA, "backup", 50, domain.NameCursor{})
		if err != nil {
			t.Fatal(err)
		}
		got := ids(rows, func(r domain.ChannelResult) string { return r.ID })
		assertSameIDs(t, got, chPublic, chPrivA)
		for _, r := range rows {
			if r.ID == chPrivA && (r.Type != "private" || r.MemberCount != 2 || r.Description == nil) {
				t.Fatalf("private channel metadata: %+v", r)
			}
		}
	})

	t.Run("channels: a guest sees neither public nor private without membership", func(t *testing.T) {
		rows, err := store.Channels(ctx, guestG, "backup", 50, domain.NameCursor{})
		if err != nil || len(rows) != 0 {
			t.Fatalf("guest rows=%+v err=%v", rows, err)
		}
	})

	t.Run("messages: every readable conversation and nothing else", func(t *testing.T) {
		rows, err := store.Messages(ctx, userA, "backup", 50, domain.MessageCursor{RankedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		byConversation := map[string]domain.MessageResult{}
		for _, r := range rows {
			byConversation[r.ConversationID] = r
		}
		assertSameIDs(t, keys(byConversation), chPublic, chPrivA, dmAB, groupABC)
		if m := byConversation[dmAB]; m.ConversationKind != "dm" || m.ConversationType != "direct" || m.ConversationName != "Bruno B" {
			t.Fatalf("direct message context: %+v", m)
		}
		if m := byConversation[groupABC]; m.ConversationKind != "dm" || m.ConversationType != "group" || m.ConversationName != "Backup Squad" {
			t.Fatalf("group message context: %+v", m)
		}
		if m := byConversation[chPrivA]; m.ConversationKind != "channel" || m.ConversationType != "private" || m.SenderDisplayName != "Bruno B" {
			t.Fatalf("private channel message context: %+v", m)
		}
	})

	// The legacy endpoint (pre-#900 clients route every row to a channel) must
	// never return a message it cannot express: private channel, DM or group.
	t.Run("legacy messages: public channels only, for the same caller", func(t *testing.T) {
		rows, err := store.LegacyMessages(ctx, userA, "backup", 50, domain.LegacyMessageCursor{})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ChannelID != chPublic || rows[0].ChannelName != "backup-publico" || rows[0].SenderDisplayName != "Bruno B" {
			t.Fatalf("legacy rows=%+v", rows)
		}
		guest, err := store.LegacyMessages(ctx, guestG, "backup", 50, domain.LegacyMessageCursor{})
		if err != nil || len(guest) != 0 {
			t.Fatalf("guest legacy rows=%+v err=%v", guest, err)
		}
		page, err := svc.SearchLegacyMessages(ctx, userA, "backup", 1, "")
		if err != nil || len(page.Items) != 1 || page.NextCursor != "" {
			t.Fatalf("legacy page=%+v err=%v", page, err)
		}
	})

	t.Run("groups: only groups with an active participation", func(t *testing.T) {
		rows, err := store.Groups(ctx, userA, "backup", 50, domain.NameCursor{})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ID != groupABC || rows[0].ParticipantCount != 3 || rows[0].LastMessageAt == nil {
			t.Fatalf("groups=%+v", rows)
		}
	})

	t.Run("files: inherit the message's visibility", func(t *testing.T) {
		rows, err := store.Files(ctx, userA, "backup", 50, domain.TimeCursor{})
		if err != nil {
			t.Fatal(err)
		}
		// #geral-like chPublic carries two live files (backup-01, backup_final).
		conversations := map[string]bool{}
		for _, r := range rows {
			conversations[r.ConversationID] = true
		}
		assertSameIDs(t, keys(conversations), chPublic, chPrivA, dmAB, groupABC)
		if len(rows) != 5 {
			t.Fatalf("files=%d, want 5", len(rows))
		}
		for _, r := range rows {
			if strings.Contains(r.Filename, "apagado") || strings.Contains(r.Filename, "pendente") {
				t.Fatalf("deleted or unsent attachment listed: %+v", r)
			}
			if r.ConversationID == groupABC && (r.ConversationName != "Backup Squad" || r.Status != "clean" || r.MessageID == "") {
				t.Fatalf("group file context: %+v", r)
			}
		}
	})

	t.Run("LIKE wildcards in the term are literal", func(t *testing.T) {
		underscore, err := store.Files(ctx, userA, "_", 50, domain.TimeCursor{})
		if err != nil || len(underscore) != 1 || underscore[0].Filename != "backup_final.pdf" {
			t.Fatalf("files matching a literal underscore: %+v err=%v", underscore, err)
		}
		percent, err := store.Files(ctx, userA, "%", 50, domain.TimeCursor{})
		if err != nil || len(percent) != 0 {
			t.Fatalf("files matching a literal percent: %+v err=%v", percent, err)
		}
		groups, err := store.Groups(ctx, userA, "%", 50, domain.NameCursor{})
		if err != nil || len(groups) != 0 {
			t.Fatalf("groups matching a literal percent: %+v err=%v", groups, err)
		}
	})

	t.Run("an inactive caller finds nothing anywhere", func(t *testing.T) {
		msgs, err1 := store.Messages(ctx, suspendedS, "backup", 50, domain.MessageCursor{RankedAt: time.Now()})
		chans, err2 := store.Channels(ctx, suspendedS, "backup", 50, domain.NameCursor{})
		users, err3 := store.Users(ctx, suspendedS, "b", 50, domain.NameCursor{})
		files, err4 := store.Files(ctx, suspendedS, "backup", 50, domain.TimeCursor{})
		if len(msgs)+len(chans)+len(users)+len(files) != 0 || err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			t.Fatalf("suspended caller got results: %d %d %d %d", len(msgs), len(chans), len(users), len(files))
		}
	})

	t.Run("pagination walks every visible message once", func(t *testing.T) {
		seen := map[string]bool{}
		cursor := ""
		for page := 0; page < 10; page++ {
			p, err := svc.SearchMessages(ctx, userA, "backup", 1, cursor)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range p.Items {
				if seen[m.ID] {
					t.Fatalf("message %s returned twice", m.ID)
				}
				seen[m.ID] = true
			}
			if cursor = p.NextCursor; cursor == "" {
				break
			}
		}
		if len(seen) != 4 {
			t.Fatalf("paged %d messages, want 4", len(seen))
		}
	})

	t.Run("a cursor never crosses query or type", func(t *testing.T) {
		p, err := svc.SearchFiles(ctx, userA, "backup", 1, "")
		if err != nil || p.NextCursor == "" {
			t.Fatalf("page=%+v err=%v", p, err)
		}
		if _, err := svc.SearchFiles(ctx, userA, "relatorio", 1, p.NextCursor); err == nil {
			t.Fatal("file cursor accepted for another query")
		}
		if _, err := svc.SearchMessages(ctx, userA, "backup", 1, p.NextCursor); err == nil {
			t.Fatal("file cursor accepted as a message cursor")
		}
	})
}

func openSearchTestDatabase(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("SEARCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SEARCH_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	var name string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil || !strings.HasSuffix(name, "_test") {
		t.Fatalf("refusing destructive search suite against %q (err=%v)", name, err)
	}
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS files CASCADE; DROP SCHEMA IF EXISTS chat CASCADE; DROP SCHEMA IF EXISTS auth CASCADE`); err != nil {
		t.Fatalf("reset schemas: %v", err)
	}
	if _, err := conn.Exec(ctx, readUpMigrations(t)); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return conn
}

// readUpMigrations returns every up migration in the order scripts/db/migrate.sh
// applies them: the sorted paths, which is domain first, then ordinal.
func readUpMigrations(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "migrations", "*", "*.up.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("list migrations: %v", err)
	}
	sort.Strings(paths)
	var sql strings.Builder
	for _, path := range paths {
		contents, err := os.ReadFile(path) //nolint:gosec // Glob is confined to the repository migrations directory.
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sql.Write(contents)
		sql.WriteString("\n")
	}
	return sql.String()
}

func seedSearchFixture(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	statements := []string{
		`INSERT INTO auth.users (id, email, display_name, full_name, status) VALUES
		 ('` + userA + `', 'a@x.test', 'ana', 'Ana A', 'active'),
		 ('` + userB + `', 'b@x.test', 'bruno', 'Bruno B', 'active'),
		 ('` + userC + `', 'c@x.test', 'carla', 'Carla C', 'active'),
		 ('` + guestG + `', 'g@x.test', 'gabi', 'Gabi Guest', 'active'),
		 ('` + suspendedS + `', 's@x.test', 'saulo', 'Saulo S', 'active')`,
		`INSERT INTO chat.workspace_members (workspace_id, user_id, role, status) VALUES
		 ('` + workspaceID + `', '` + userA + `', 'member', 'active'),
		 ('` + workspaceID + `', '` + userB + `', 'member', 'active'),
		 ('` + workspaceID + `', '` + userC + `', 'member', 'active'),
		 ('` + workspaceID + `', '` + guestG + `', 'guest', 'active'),
		 ('` + workspaceID + `', '` + suspendedS + `', 'member', 'suspended')`,
		`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status, description) VALUES
		 ('` + chPublic + `', '` + workspaceID + `', 'backup-publico', 'backup-publico', 'public', 'active', NULL),
		 ('` + chPrivA + `', '` + workspaceID + `', 'backup-privado', 'backup-privado', 'private', 'active', 'Rotina de backup'),
		 ('` + chPrivB + `', '` + workspaceID + `', 'backup-secreto', 'backup-secreto', 'private', 'active', NULL),
		 ('` + chArchived + `', '` + workspaceID + `', 'backup-arquivado', 'backup-arquivado', 'public', 'archived', NULL)`,
		`INSERT INTO chat.channel_members (channel_id, user_id) VALUES
		 ('` + chPrivA + `', '` + userA + `'), ('` + chPrivA + `', '` + userB + `'), ('` + chPrivB + `', '` + userB + `')
		 ON CONFLICT DO NOTHING`,
		`INSERT INTO chat.dm_conversations (id, workspace_id, type, title, created_by, direct_pair_key) VALUES
		 ('` + dmAB + `', '` + workspaceID + `', 'direct', NULL, '` + userA + `', 'ab'),
		 ('` + dmBC + `', '` + workspaceID + `', 'direct', NULL, '` + userB + `', 'bc'),
		 ('` + groupABC + `', '` + workspaceID + `', 'group', 'Backup Squad', '` + userA + `', NULL),
		 ('` + groupBC + `', '` + workspaceID + `', 'group', 'Backup Oculto', '` + userB + `', NULL),
		 ('` + groupLeft + `', '` + workspaceID + `', 'group', 'Backup Antigo', '` + userB + `', NULL)`,
		`INSERT INTO chat.dm_members (conversation_id, user_id, status, left_at) VALUES
		 ('` + dmAB + `', '` + userA + `', 'active', NULL), ('` + dmAB + `', '` + userB + `', 'active', NULL),
		 ('` + dmBC + `', '` + userB + `', 'active', NULL), ('` + dmBC + `', '` + userC + `', 'active', NULL),
		 ('` + groupABC + `', '` + userA + `', 'active', NULL), ('` + groupABC + `', '` + userB + `', 'active', NULL), ('` + groupABC + `', '` + userC + `', 'active', NULL),
		 ('` + groupBC + `', '` + userB + `', 'active', NULL), ('` + groupBC + `', '` + userC + `', 'active', NULL),
		 ('` + groupLeft + `', '` + userA + `', 'left', now()), ('` + groupLeft + `', '` + userB + `', 'active', NULL)`,
	}
	ctx := t.Context()
	for _, sql := range statements {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v\n%s", err, sql)
		}
	}
	for _, target := range []struct{ column, id string }{
		{"channel_id", chPublic}, {"channel_id", chPrivA}, {"channel_id", chPrivB}, {"channel_id", chArchived},
		{"dm_conversation_id", dmAB}, {"dm_conversation_id", dmBC}, {"dm_conversation_id", groupABC},
		{"dm_conversation_id", groupBC}, {"dm_conversation_id", groupLeft},
	} {
		var messageID string
		sql := `INSERT INTO chat.messages (workspace_id, ` + target.column + `, sender_id, body_text) VALUES ($1, $2, $3, 'checklist do backup semanal') RETURNING id` //nolint:gosec // column is a fixed test literal.
		if err := conn.QueryRow(ctx, sql, workspaceID, target.id, userB).Scan(&messageID); err != nil {
			t.Fatalf("seed message in %s: %v", target.id, err)
		}
		seedAttachment(t, conn, messageID, target.column, target.id, "backup-"+target.id[len(target.id)-2:]+".pdf", "clean", false)
		if target.id == chPublic {
			seedAttachment(t, conn, messageID, target.column, target.id, "backup-apagado.pdf", "clean", true)
			seedAttachment(t, conn, messageID, target.column, target.id, "backup_final.pdf", "clean", false)
		}
	}
}

func seedAttachment(t *testing.T, conn *pgx.Conn, messageID, column, target, filename, status string, deleted bool) {
	t.Helper()
	kind, attachmentColumn := "channel", "channel_id"
	if column == "dm_conversation_id" {
		kind, attachmentColumn = "dm", "conversation_id"
	}
	var attachmentID string
	sql := `INSERT INTO files.attachments (workspace_id, uploader_id, destination_kind, ` + attachmentColumn + `, original_filename, declared_mime,
	 size_bytes, storage_provider, storage_object_key, envelope_version, wrapped_dek, kek_key_id, dek_wrap_version, status, deleted_at)
	 VALUES ($1, $2, $3, $4, $5, 'application/pdf', 2048, 'seaweedfs', gen_random_uuid()::text, 1, '\x00', 'test-kek', 2, $6, CASE WHEN $7 THEN now() END)
	 RETURNING id` //nolint:gosec // attachmentColumn is a fixed test literal.
	if err := conn.QueryRow(t.Context(), sql, workspaceID, userB, kind, target, filename, status, deleted).Scan(&attachmentID); err != nil {
		t.Fatalf("seed attachment %s: %v", filename, err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO chat.message_attachments (message_id, attachment_id) VALUES ($1, $2)`, messageID, attachmentID); err != nil {
		t.Fatalf("link attachment %s: %v", filename, err)
	}
}

func ids[T any](rows []T, id func(T) string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, id(r))
	}
	return out
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func assertSameIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}
