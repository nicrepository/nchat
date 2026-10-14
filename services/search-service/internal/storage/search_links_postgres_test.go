package storage_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/search-service/internal/domain"
	"github.com/nicrepository/nchat/services/search-service/internal/service"
	"github.com/nicrepository/nchat/services/search-service/internal/storage"
)

// Issue #1081. A link is found through the association chat-service recorded
// for a message's current content, under the same visibility as the message,
// and never when its target is condemned. Proven against real PostgreSQL with
// the real migrations, like the rest of the search (SEARCH_TEST_DATABASE_URL).

const (
	runbookURL = "https://docs.example.com/runbook"
	otherWS    = "00000000-0000-4000-8000-0000000000f1"
	otherWSCh  = "c0000000-0000-4000-8000-0000000000f1"
)

// linksEnv is the seeded database every link case reads; each case seeds its
// own hosts, so none depends on another having run.
type linksEnv struct {
	conn  *pgx.Conn
	store *storage.PGXSearchStore
}

func (e linksEnv) links(t *testing.T, user, q string) []domain.LinkResult {
	t.Helper()
	rows, err := e.store.Links(t.Context(), user, q, 100, domain.LinkCursor{})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (e linksEnv) urls(t *testing.T, user, q string) []string {
	t.Helper()
	return ids(e.links(t, user, q), func(r domain.LinkResult) string { return r.URL })
}

var fixtureConversations = []struct{ column, id string }{
	{"channel_id", chPublic}, {"channel_id", chPrivA}, {"channel_id", chPrivB}, {"channel_id", chArchived},
	{"dm_conversation_id", dmAB}, {"dm_conversation_id", dmBC}, {"dm_conversation_id", groupABC},
	{"dm_conversation_id", groupBC}, {"dm_conversation_id", groupLeft},
}

func TestSearchLinksPostgreSQL(t *testing.T) {
	conn := openSearchTestDatabase(t)
	seedSearchFixture(t, conn)
	// The runbook is shared in every conversation of the #900 fixture; who sees
	// which is the existing visibility matrix.
	for _, target := range fixtureConversations {
		seedLinkMessage(t, conn, target.column, target.id, "active", map[string]string{runbookURL: "safe"})
	}
	seedOtherWorkspace(t, conn)
	env := linksEnv{conn: conn, store: storage.NewPGXSearchStore(conn)}
	for _, tc := range []struct {
		name string
		run  func(*testing.T, linksEnv)
	}{
		{"authorization: public, member-private, own DM and group only", linksAuthorization},
		{"guest by membership only; inactive caller and another workspace see nothing", linksOutsiders},
		{"URL, hostname and path all match; LIKE wildcards are literal", linksMatchFields},
		{"a deleted or withheld message lists none of its links", linksInactiveMessages},
		{"edit A to B: A disappears, B appears", linksEdit},
		{"an association of an older fingerprint is never current", linksStaleAssociation},
		{"only a trusted current projection is searchable (chat 000031)", linksTrustedProjectionOnly},
		{"Link Safety: malicious and denylisted never surface; pending and unknown do", linksSafety},
		{"ranking: exact URL, exact host, prefix, then anywhere", linksRanking},
		{"every occurrence pages once: no repeat, no gap", linksPagination},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, env) })
	}
}

func linksAuthorization(t *testing.T, e linksEnv) {
	byConversation := map[string]domain.LinkResult{}
	for _, r := range e.links(t, userA, "docs.example.com") {
		byConversation[r.ConversationID] = r
	}
	assertSameIDs(t, keys(byConversation), chPublic, chPrivA, dmAB, groupABC)
	assertLinkConversation(t, byConversation[dmAB], "dm", "direct", "Bruno B")
	assertLinkConversation(t, byConversation[groupABC], "dm", "group", "Backup Squad")
	r := assertLinkConversation(t, byConversation[chPrivA], "channel", "private", "backup-privado")
	if r.URL != runbookURL || r.Hostname != "docs.example.com" || r.SenderID != userB || r.SenderDisplayName != "Bruno B" ||
		r.MessageID == "" || len(r.TargetKey) != 32 || r.CreatedAt.IsZero() {
		t.Fatalf("link context: %+v", r)
	}
}

func assertLinkConversation(t *testing.T, r domain.LinkResult, kind, conversationType, name string) domain.LinkResult {
	t.Helper()
	if r.ConversationKind != kind || r.ConversationType != conversationType || r.ConversationName != name {
		t.Fatalf("conversation context: %+v", r)
	}
	return r
}

func linksOutsiders(t *testing.T, e linksEnv) {
	for _, user := range []string{guestG, suspendedS} {
		if rows := e.links(t, user, "docs.example.com"); len(rows) != 0 {
			t.Fatalf("%s rows=%+v", user, rows)
		}
	}
	if rows := e.links(t, userA, "other-tenant"); len(rows) != 0 {
		t.Fatalf("cross-workspace rows=%+v", rows)
	}
	// A guest reads exactly the channels it is a member of, private included.
	exec(t, e.conn, `INSERT INTO chat.channel_members (channel_id, user_id) VALUES ($1, $2)`, chPrivB, guestG)
	if rows := e.links(t, guestG, "docs.example.com"); len(rows) != 1 || rows[0].ConversationID != chPrivB {
		t.Fatalf("guest member rows=%+v", rows)
	}
}

func linksMatchFields(t *testing.T, e linksEnv) {
	for _, q := range []string{runbookURL, "DOCS.example.com", "/runbook", "runbook"} {
		if rows := e.links(t, userA, q); len(rows) != 4 {
			t.Fatalf("%q rows=%d", q, len(rows))
		}
	}
	if rows := e.links(t, userA, "%"); len(rows) != 0 {
		t.Fatalf("literal percent rows=%+v", rows)
	}
}

func linksInactiveMessages(t *testing.T, e linksEnv) {
	seedLinkMessage(t, e.conn, "channel_id", chPublic, "deleted", map[string]string{"https://gone.example.com/a": "safe"})
	seedLinkMessage(t, e.conn, "channel_id", chPublic, "pending_link_scan", map[string]string{"https://gone.example.com/b": "pending"})
	if rows := e.links(t, userA, "gone.example.com"); len(rows) != 0 {
		t.Fatalf("rows=%+v", rows)
	}
}

func linksEdit(t *testing.T, e linksEnv) {
	id := seedLinkMessage(t, e.conn, "channel_id", chPublic, "active", map[string]string{"https://edit.example.com/old": "safe"})
	editLinkMessage(t, e.conn, id, "https://edit.example.com/new")
	rows := e.links(t, userA, "edit.example.com")
	if len(rows) != 1 || rows[0].URL != "https://edit.example.com/new" || rows[0].MessageID != id {
		t.Fatalf("rows=%+v", rows)
	}
}

func linksStaleAssociation(t *testing.T, e linksEnv) {
	id := seedLinkMessage(t, e.conn, "channel_id", chPublic, "active", map[string]string{"https://current.example.com/": "safe"})
	exec(t, e.conn, `INSERT INTO chat.link_scans (canonical_url, status, decided_at) VALUES ('https://stale.example.com/', 'safe', now())`)
	exec(t, e.conn, `INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint) VALUES ($1, 'https://stale.example.com/', 'fp-old')`, id)
	if rows := e.links(t, userA, "stale.example.com"); len(rows) != 0 {
		t.Fatalf("stale rows=%+v", rows)
	}
	// An older pod editing the body drops the fingerprint (trigger from chat
	// 000031): the previous associations stop matching at once.
	exec(t, e.conn, `UPDATE chat.messages SET body_text='texto sem links' WHERE id=$1`, id)
	if rows := e.links(t, userA, "current.example.com"); len(rows) != 0 {
		t.Fatalf("legacy-edited rows=%+v", rows)
	}
}

// Every projection state chat 000031 knows, each on its own host. Only A, the
// trusted current projection, may be found; B-D are rows a reader must not
// certify, and G is the trigger turning a valid projection into one.
func linksTrustedProjectionOnly(t *testing.T, e linksEnv) {
	valid := seedLinkMessage(t, e.conn, "channel_id", chPublic, "active", map[string]string{"https://proj-a.example.com/": "safe"})
	if rows := e.links(t, userA, "proj-a.example.com"); len(rows) != 1 || rows[0].MessageID != valid {
		t.Fatalf("A valid projection rows=%+v", rows)
	}
	fp := func(v string) *string { return &v }
	for _, tc := range []struct {
		host        string
		version     int64
		fingerprint *string
		association string
	}{
		{"proj-b", 0, fp("fp-b"), "fp-b"},     // B: version 0, fingerprints still equal
		{"proj-c", 0, nil, ""},                // C: no fingerprint, pre-fingerprint '' row
		{"proj-c1", 1, nil, ""},               // C: no fingerprint even with a version
		{"proj-d", 1, fp(""), ""},             // D: empty fingerprint on both sides
		{"proj-e", 1, fp("fp-new"), "fp-old"}, // E: an older fingerprint
	} {
		seedProjection(t, e.conn, "https://"+tc.host+".example.com/", tc.version, tc.fingerprint, tc.association)
		if rows := e.links(t, userA, tc.host+".example.com"); len(rows) != 0 {
			t.Fatalf("%s rows=%+v", tc.host, rows)
		}
	}
	// G: a legacy writer rewrites the body without advancing the version. The
	// trigger resets version and fingerprint; the old row physically stays.
	exec(t, e.conn, `UPDATE chat.messages SET body_text='sem links agora' WHERE id=$1`, valid)
	var version int64
	var fingerprint *string
	var rows int
	if err := e.conn.QueryRow(t.Context(), `SELECT m.link_safety_projection_version, m.link_safety_fingerprint,
	 (SELECT count(*) FROM chat.message_link_scans WHERE message_id=m.id) FROM chat.messages m WHERE m.id=$1`, valid).
		Scan(&version, &fingerprint, &rows); err != nil || version != 0 || fingerprint != nil || rows != 1 {
		t.Fatalf("legacy rewrite: version=%d fingerprint=%v rows=%d err=%v", version, fingerprint, rows, err)
	}
	if found := e.links(t, userA, "proj-a.example.com"); len(found) != 0 {
		t.Fatalf("G legacy-rewritten rows=%+v", found)
	}
}

// seedProjection writes a message in chPublic in a given projection state with
// one association carrying `association` as its fingerprint; a nil
// fingerprint is NULL.
func seedProjection(t *testing.T, conn *pgx.Conn, url string, version int64, fingerprint *string, association string) {
	t.Helper()
	var id string
	if err := conn.QueryRow(t.Context(), `INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text, link_safety_projection_version, link_safety_fingerprint)
	 VALUES ($1, $2, $3, 'link', $4, $5) RETURNING id`, workspaceID, chPublic, userB, version, fingerprint).Scan(&id); err != nil {
		t.Fatalf("seed projection %s: %v", url, err)
	}
	exec(t, conn, `INSERT INTO chat.link_scans (canonical_url, status, decided_at) VALUES ($1, 'safe', now())`, url)
	exec(t, conn, `INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint) VALUES ($1, $2, $3)`, id, url, association)
}

func linksSafety(t *testing.T, e linksEnv) {
	seedLinkMessage(t, e.conn, "channel_id", chPublic, "active", map[string]string{
		"https://safety.example.com/evil":    "malicious",
		"https://safety.example.com/denied":  "safe",
		"https://safety.example.com/pending": "pending",
		"https://safety.example.com/unknown": "unknown",
		"https://safety.example.com/inconc":  "inconclusive",
		"https://safety.example.com/ok":      "safe",
	})
	exec(t, e.conn, `INSERT INTO files.link_fetch_denylist (url_digest, canonical_url, source)
	 VALUES (sha256(convert_to('https://safety.example.com/denied', 'UTF8')), 'https://safety.example.com/denied', 'chat')`)
	assertSameIDs(t, e.urls(t, userA, "safety.example.com"), "https://safety.example.com/pending",
		"https://safety.example.com/unknown", "https://safety.example.com/inconc", "https://safety.example.com/ok")
	// Searching the condemned URL itself is not an oracle either.
	for _, q := range []string{"https://safety.example.com/evil", "evil", "denied"} {
		if rows := e.links(t, userA, q); len(rows) != 0 {
			t.Fatalf("%q rows=%+v", q, rows)
		}
	}
}

func linksRanking(t *testing.T, e linksEnv) {
	for _, url := range []string{"https://rank.example.com/", "https://rank.example.com/a", "https://www.rank.example.com/b"} {
		seedLinkMessage(t, e.conn, "channel_id", chPublic, "active", map[string]string{url: "safe"})
	}
	rank := func(q string) string {
		return strings.Join(ids(e.links(t, userA, q), func(r domain.LinkResult) string { return strconv.Itoa(r.Rank) }), ",")
	}
	if got := rank("https://rank.example.com"); got != "0,2" {
		t.Fatalf("url ranks=%v", got)
	}
	if got := rank("rank.example.com"); got != "1,1,3" {
		t.Fatalf("host ranks=%v", got)
	}
}

// Eight occurrences over three ranks, two URLs in one message, walked three
// at a time must equal the single page, in order.
func linksPagination(t *testing.T, e linksEnv) {
	seedLinkMessage(t, e.conn, "channel_id", chPublic, "active", map[string]string{"https://page.example.com/": "safe", "https://page.example.com/x": "safe"})
	for range 3 {
		seedLinkMessage(t, e.conn, "channel_id", chPrivA, "active", map[string]string{"https://page.example.com/": "safe"})
		seedLinkMessage(t, e.conn, "dm_conversation_id", dmAB, "active", map[string]string{"https://www.page.example.com/y": "safe"})
	}
	key := func(r domain.LinkResult) string { return r.MessageID + r.TargetKey }
	want := ids(e.links(t, userA, "page.example.com"), key)
	if len(want) != 8 {
		t.Fatalf("fixture rows=%d", len(want))
	}
	var walked []string
	svc := service.New(e.store)
	for cursor, more := "", true; more; {
		p, err := svc.SearchLinks(t.Context(), userA, "page.example.com", 3, cursor)
		if err != nil {
			t.Fatal(err)
		}
		walked = append(walked, ids(p.Items, key)...)
		cursor, more = p.NextCursor, p.NextCursor != ""
	}
	if strings.Join(walked, ",") != strings.Join(want, ",") {
		t.Fatalf("paged %v, single page %v", walked, want)
	}
}

// seedLinkMessage writes a message by userB the way chat-service does: the
// targets, then the associations stamped with the message's fingerprint.
func seedLinkMessage(t *testing.T, conn *pgx.Conn, column, target, status string, urls map[string]string) string {
	t.Helper()
	var id string
	var channelID, dmID any
	if column == "channel_id" {
		channelID = target
	} else {
		dmID = target
	}
	if err := conn.QueryRow(t.Context(), `INSERT INTO chat.messages (workspace_id, channel_id, dm_conversation_id, sender_id, body_text, status, link_safety_projection_version)
	 VALUES ($1, $2, $3, $4, 'link', $5, 1) RETURNING id`, workspaceID, channelID, dmID, userB, status).Scan(&id); err != nil {
		t.Fatalf("seed link message: %v", err)
	}
	exec(t, conn, `UPDATE chat.messages SET link_safety_fingerprint=$2 WHERE id=$1`, id, "fp-"+id)
	for url, verdict := range urls {
		exec(t, conn, `INSERT INTO chat.link_scans (canonical_url, status, decided_at)
		 VALUES ($1, $2, CASE WHEN $2 <> 'pending' THEN now() END) ON CONFLICT DO NOTHING`, url, verdict)
		exec(t, conn, `INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint) VALUES ($1, $2, $3)`, id, url, "fp-"+id)
	}
	return id
}

// editLinkMessage replaces the body's only link as chat-service's edit does:
// associations replaced and the fingerprint advanced in one transaction.
func editLinkMessage(t *testing.T, conn *pgx.Conn, id, url string) {
	t.Helper()
	exec(t, conn, `BEGIN`)
	exec(t, conn, `UPDATE chat.messages SET body_text='editado', link_safety_projection_version=link_safety_projection_version+1,
	 link_safety_fingerprint=$2 WHERE id=$1`, id, "fp2-"+id)
	exec(t, conn, `DELETE FROM chat.message_link_scans WHERE message_id=$1`, id)
	exec(t, conn, `INSERT INTO chat.link_scans (canonical_url, status, decided_at) VALUES ($1, 'safe', now()) ON CONFLICT DO NOTHING`, url)
	exec(t, conn, `INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint) VALUES ($1, $2, $3)`, id, url, "fp2-"+id)
	exec(t, conn, `COMMIT`)
}

// seedOtherWorkspace is a second tenant where userA is a member of a public
// channel carrying a link: it is outside the caller's resolved workspace.
func seedOtherWorkspace(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	exec(t, conn, `BEGIN`)
	exec(t, conn, `INSERT INTO chat.workspaces (id, slug, name) VALUES ($1, 'other', 'Other')`, otherWS)
	exec(t, conn, `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status, is_general) VALUES ($1, $2, 'outro', 'outro', 'public', 'active', true)`, otherWSCh, otherWS)
	exec(t, conn, `COMMIT`)
	exec(t, conn, `INSERT INTO chat.workspace_members (workspace_id, user_id, role, status) VALUES ($1, $2, 'member', 'active')`, otherWS, userA)
	exec(t, conn, `INSERT INTO chat.channel_members (channel_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, otherWSCh, userA)
	var id string
	// A trusted projection, so only the workspace boundary can keep it out.
	if err := conn.QueryRow(t.Context(), `INSERT INTO chat.messages (workspace_id, channel_id, sender_id, body_text, link_safety_fingerprint, link_safety_projection_version)
	 VALUES ($1, $2, $3, 'link', 'fp-x', 1) RETURNING id`, otherWS, otherWSCh, userA).Scan(&id); err != nil {
		t.Fatalf("seed other workspace message: %v", err)
	}
	exec(t, conn, `INSERT INTO chat.link_scans (canonical_url, status, decided_at) VALUES ('https://other-tenant.example.com/', 'safe', now())`)
	exec(t, conn, `INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint) VALUES ($1, 'https://other-tenant.example.com/', 'fp-x')`, id)
}

func exec(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("exec: %v\n%s", err, sql)
	}
}
