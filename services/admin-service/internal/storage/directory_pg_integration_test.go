package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

// The directory's search and its role filters are SQL, and a mock can only
// confirm which string was sent with which binds. What a search *matches* is
// decided by PostgreSQL, so it takes PostgreSQL to prove it.
//
// This suite exists because a unit test that asserts the pattern likePattern
// produced is asserting the wrong thing twice over: it re-states the helper's
// own output, and it says nothing about how the server reads the backslashes in
// it. Every case below asserts the rows the query returned.
//
// Gated on ADMIN_TEST_DATABASE_URL like the rest of this package's PostgreSQL
// tests, and skipped when it is unset:
//
//	ADMIN_TEST_DATABASE_URL=postgresql://nchat@localhost:5432/nchat_test \
//	  go test ./internal/storage/... -run PostgreSQL

// seedDirectory builds a workspace, a channel and a cast of users whose names
// are chosen to catch a wildcard that stopped being literal.
type directoryFixture struct {
	workspaceID string
	channelID   string
	generalID   string
	// literal is the account whose name really contains "100%_off".
	literal string
	// decoys would each match "100%_off" if % or _ were still wildcards, and
	// must not be returned by a search for it.
	decoyPercent   string
	decoyUnderline string
	backslash      string
	owner          string
	guest          string
	moderator      string
	outsider       string
}

func seedDirectory(t *testing.T, pool *pgxpool.Pool) directoryFixture {
	t.Helper()
	ctx := context.Background()
	applyMigrations(t, pool, "auth", "chat")

	fixture := directoryFixture{}
	fixture.literal = insertNamedUser(t, pool, "literal@example.test", `100%_off`)
	// "100X_off" matches "100%_off" only if % is a wildcard; "100%Yoff" matches
	// only if _ is. One of each, so a regression in either direction is caught.
	fixture.decoyPercent = insertNamedUser(t, pool, "decoy-percent@example.test", `100X_off`)
	fixture.decoyUnderline = insertNamedUser(t, pool, "decoy-underline@example.test", `100%Yoff`)
	fixture.backslash = insertNamedUser(t, pool, "backslash@example.test", `back\slash`)
	fixture.owner = insertNamedUser(t, pool, "owner@example.test", "Olivia Owner")
	fixture.guest = insertNamedUser(t, pool, "guest@example.test", "Gabi Guest")
	fixture.moderator = insertNamedUser(t, pool, "moderator@example.test", "Moe Moderator")
	fixture.outsider = insertNamedUser(t, pool, "outsider@example.test", "Otto Outsider")

	fixture.workspaceID, fixture.generalID = insertWorkspace(t, pool, "probe", "Probe")
	for userID, role := range map[string]string{
		fixture.owner:     "owner",
		fixture.guest:     "guest",
		fixture.moderator: "moderator",
		fixture.literal:   "member",
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
			VALUES ($1::uuid, $2::uuid, $3, 'active')`,
			fixture.workspaceID, userID, role); err != nil {
			t.Fatalf("insert workspace member: %v", err)
		}
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat.channels (workspace_id, slug, display_name, type, status, created_by)
		VALUES ($1::uuid, 'probe-channel', 'Probe Channel', 'private', 'active', $2::uuid)
		RETURNING id::text`, fixture.workspaceID, fixture.owner).Scan(&fixture.channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		VALUES ($1::uuid, $2::uuid, 'moderator')`,
		fixture.channelID, fixture.moderator); err != nil {
		t.Fatalf("insert channel moderator: %v", err)
	}
	return fixture
}

// insertWorkspace creates a workspace and its #geral channel in one
// transaction.
//
// chat.workspaces_require_general_channel is a DEFERRABLE INITIALLY DEFERRED
// constraint trigger: a workspace without an active public general channel is
// rejected at commit, not at insert. Seeding them separately is therefore not a
// shortcut this schema allows, and doing it properly here is what keeps the
// fixture a real workspace rather than one the platform would refuse.
func insertWorkspace(t *testing.T, pool *pgxpool.Pool, slug, name string) (string, string) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin workspace seed: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var workspaceID, generalID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO chat.workspaces (slug, name, status)
		VALUES ($1, $2, 'active') RETURNING id::text`, slug, name).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO chat.channels (workspace_id, slug, display_name, type, status, is_general)
		VALUES ($1::uuid, 'geral', 'Geral', 'public', 'active', true)
		RETURNING id::text`, workspaceID).Scan(&generalID); err != nil {
		t.Fatalf("insert general channel: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit workspace seed: %v", err)
	}
	return workspaceID, generalID
}

func insertNamedUser(t *testing.T, pool *pgxpool.Pool, email, displayName string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO auth.users (email, display_name, full_name, status, auth_source, email_verified_at)
		VALUES ($1, $2, $2, 'active', 'manual', now())
		RETURNING id::text`, email, displayName).Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", email, err)
	}
	return id
}

func searchDirectory(t *testing.T, store *storage.PGXUserDirectoryStore, term string) map[string]string {
	t.Helper()
	page, err := store.ListUsers(context.Background(), domain.AdminUserFilter{Query: term, Limit: 100})
	if err != nil {
		t.Fatalf("ListUsers(%q): %v", term, err)
	}
	found := make(map[string]string, len(page.Items))
	for _, user := range page.Items {
		found[user.ID] = user.DisplayName
	}
	return found
}

// A search for a term containing % and _ finds the account whose name really
// contains them, and nothing that would only match if they were still
// wildcards.
//
// This is the case the escaping exists for. Both decoys are in the fixture, so
// the assertion fails loudly the moment either character stops being literal.
func TestPostgreSQL_SearchTreatsWildcardsAsLiteral(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXUserDirectoryStore(pool)

	found := searchDirectory(t, store, `100%_off`)
	if _, ok := found[fixture.literal]; !ok {
		t.Fatalf("the literal name must be found, got %v", found)
	}
	if name, ok := found[fixture.decoyPercent]; ok {
		t.Fatalf("%% must not match any run of characters: %q was returned", name)
	}
	if name, ok := found[fixture.decoyUnderline]; ok {
		t.Fatalf("_ must not match a single character: %q was returned", name)
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly the literal match, got %v", found)
	}
}

// Searching for a bare % returns the accounts whose names contain a literal
// percent sign — not every account, which is what an unescaped pattern would
// have produced.
func TestPostgreSQL_SearchForBarePercentIsNotMatchAll(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXUserDirectoryStore(pool)

	all := searchDirectory(t, store, "")
	found := searchDirectory(t, store, `%`)
	if len(found) >= len(all) {
		t.Fatalf("a bare %% must not behave as match-all: %d of %d returned", len(found), len(all))
	}
	for _, id := range []string{fixture.literal, fixture.decoyUnderline} {
		if _, ok := found[id]; !ok {
			t.Fatalf("a name containing a literal %% must be found, got %v", found)
		}
	}
	if _, ok := found[fixture.owner]; ok {
		t.Fatal("a name with no percent sign must not match a search for one")
	}
}

// Searching for a bare _ returns the accounts whose names contain a literal
// underscore — not every account with at least one character.
func TestPostgreSQL_SearchForBareUnderscoreIsNotSingleCharWildcard(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXUserDirectoryStore(pool)

	all := searchDirectory(t, store, "")
	found := searchDirectory(t, store, `_`)
	if len(found) >= len(all) {
		t.Fatalf("a bare _ must not behave as a wildcard: %d of %d returned", len(found), len(all))
	}
	for _, id := range []string{fixture.literal, fixture.decoyPercent} {
		if _, ok := found[id]; !ok {
			t.Fatalf("a name containing a literal _ must be found, got %v", found)
		}
	}
	if _, ok := found[fixture.owner]; ok {
		t.Fatal("a name with no underscore must not match a search for one")
	}
}

// A backslash is the escape character itself. Searching for one must find the
// name that contains it and must not corrupt the rest of the pattern.
func TestPostgreSQL_SearchForBackslashIsLiteral(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXUserDirectoryStore(pool)

	found := searchDirectory(t, store, `\`)
	if _, ok := found[fixture.backslash]; !ok {
		t.Fatalf("a name containing a backslash must be found, got %v", found)
	}
	if len(found) != 1 {
		t.Fatalf("only the name with a backslash must match, got %v", found)
	}
	// The same character in the middle of a longer term must not break the
	// pattern either.
	if partial := searchDirectory(t, store, `back\sl`); len(partial) != 1 {
		t.Fatalf("a backslash inside a longer term must still match literally, got %v", partial)
	}
}

// The channel directory shares likePattern, so it shares the guarantee. Fixing
// one query and leaving the other would be exactly the divergence a shared
// helper is supposed to prevent.
func TestPostgreSQL_ChannelSearchTreatsWildcardsAsLiteral(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	ctx := context.Background()

	var literalChannel string
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat.channels (workspace_id, slug, display_name, type, status)
		VALUES ($1::uuid, 'promo-100', '100%_off', 'public', 'active')
		RETURNING id::text`, fixture.workspaceID).Scan(&literalChannel); err != nil {
		t.Fatalf("insert literal channel: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.channels (workspace_id, slug, display_name, type, status)
		VALUES ($1::uuid, 'promo-decoy', '100X_off', 'public', 'active')`,
		fixture.workspaceID); err != nil {
		t.Fatalf("insert decoy channel: %v", err)
	}

	page, err := storage.NewPGXChannelDirectoryStore(pool).
		ListChannels(ctx, domain.AdminChannelFilter{Query: `100%_off`, Limit: 100})
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != literalChannel {
		names := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			names = append(names, item.DisplayName)
		}
		t.Fatalf("expected only the literal channel, got %v", names)
	}
}

// ---------------------------------------------------------------------------
// Filters
// ---------------------------------------------------------------------------

// The directory is platform-wide, so the role filter reads as "holds at least
// one active membership with this role, in any active workspace".
func TestPostgreSQL_WorkspaceRoleFilterSelectsByRealMembership(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXUserDirectoryStore(pool)

	expected := map[string]string{
		"owner":     fixture.owner,
		"guest":     fixture.guest,
		"moderator": fixture.moderator,
		"member":    fixture.literal,
	}
	for role, userID := range expected {
		t.Run(role, func(t *testing.T) {
			page, err := store.ListUsers(context.Background(),
				domain.AdminUserFilter{WorkspaceRole: role, Limit: 100})
			if err != nil {
				t.Fatalf("ListUsers: %v", err)
			}
			if len(page.Items) != 1 || page.Items[0].ID != userID {
				t.Fatalf("expected exactly the %s, got %+v", role, page.Items)
			}
		})
	}

	// 'admin' is a role the schema allows and nobody holds here: an empty page,
	// not an error and not everybody.
	page, err := store.ListUsers(context.Background(),
		domain.AdminUserFilter{WorkspaceRole: "admin", Limit: 100})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("expected nobody, got %+v", page.Items)
	}
}

// A person with two memberships appears once. EXISTS rather than a join is what
// keeps the page size honest and the cursor from skipping rows.
func TestPostgreSQL_WorkspaceRoleFilterDoesNotDuplicate(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	ctx := context.Background()

	second, _ := insertWorkspace(t, pool, "probe-two", "Probe Two")
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		VALUES ($1::uuid, $2::uuid, 'owner', 'active')`, second, fixture.owner); err != nil {
		t.Fatalf("insert second membership: %v", err)
	}

	page, err := storage.NewPGXUserDirectoryStore(pool).
		ListUsers(ctx, domain.AdminUserFilter{WorkspaceRole: "owner", Limit: 100})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("two memberships must not produce two rows, got %d", len(page.Items))
	}
}

// A membership that is not active, or in a workspace that is not active, does
// not make somebody an owner today.
func TestPostgreSQL_WorkspaceRoleFilterIgnoresInactiveMemberships(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`UPDATE chat.workspace_members SET status = 'left' WHERE user_id = $1::uuid`,
		fixture.owner); err != nil {
		t.Fatalf("deactivate membership: %v", err)
	}
	page, err := storage.NewPGXUserDirectoryStore(pool).
		ListUsers(ctx, domain.AdminUserFilter{WorkspaceRole: "owner", Limit: 100})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("a membership that ended must not still select, got %+v", page.Items)
	}
}

// The role filter narrows the same set the other filters do; combining them
// yields the intersection rather than replacing one with the other.
func TestPostgreSQL_WorkspaceRoleFilterCombinesWithSearch(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXUserDirectoryStore(pool)

	page, err := store.ListUsers(context.Background(), domain.AdminUserFilter{
		WorkspaceRole: "member", Query: `100%_off`, Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != fixture.literal {
		t.Fatalf("expected the intersection, got %+v", page.Items)
	}

	empty, err := store.ListUsers(context.Background(), domain.AdminUserFilter{
		WorkspaceRole: "owner", Query: `100%_off`, Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(empty.Items) != 0 {
		t.Fatalf("the intersection is empty and must be reported as such, got %+v", empty.Items)
	}
}

// "Administers a channel" is the creator or a channel moderator, and nothing
// else. A workspace owner who is neither must not match, because governing a
// workspace is not administering one of its channels.
func TestPostgreSQL_AdministeredByFilterFollowsTheRealModel(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	byCreator, err := store.ListChannels(ctx,
		domain.AdminChannelFilter{AdministeredBy: fixture.owner, Limit: 100})
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	// The owner created exactly one channel, and is not a moderator of #geral,
	// which they did not create either.
	if len(byCreator.Items) != 1 || byCreator.Items[0].ID != fixture.channelID {
		t.Fatalf("expected the created channel only, got %+v", byCreator.Items)
	}

	byModerator, err := store.ListChannels(ctx,
		domain.AdminChannelFilter{AdministeredBy: fixture.moderator, Limit: 100})
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(byModerator.Items) != 1 || byModerator.Items[0].ID != fixture.channelID {
		t.Fatalf("expected the moderated channel only, got %+v", byModerator.Items)
	}

	// A workspace guest administers nothing.
	none, err := store.ListChannels(ctx,
		domain.AdminChannelFilter{AdministeredBy: fixture.guest, Limit: 100})
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(none.Items) != 0 {
		t.Fatalf("a person who administers nothing must match nothing, got %+v", none.Items)
	}
}

// ---------------------------------------------------------------------------
// Membership
// ---------------------------------------------------------------------------

func channelMemberIDs(t *testing.T, pool *pgxpool.Pool, channelID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT user_id::text FROM chat.channel_members WHERE channel_id = $1::uuid ORDER BY user_id`, channelID)
	if err != nil {
		t.Fatalf("read channel members: %v", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan member: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

// An eligible target is admitted, as an ordinary member, and a repeat of the
// same request adds nobody.
func TestPostgreSQL_AddChannelMembersAdmitsAndIsIdempotent(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	change, err := store.AddChannelMembers(ctx, fixture.channelID, []string{fixture.owner})
	if err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	if change.Added != 1 || change.AlreadyMembers != 0 {
		t.Fatalf("unexpected change %+v", change)
	}

	var role string
	if err := pool.QueryRow(ctx,
		`SELECT role FROM chat.channel_members WHERE channel_id = $1::uuid AND user_id = $2::uuid`,
		fixture.channelID, fixture.owner).Scan(&role); err != nil {
		t.Fatalf("read inserted role: %v", err)
	}
	// Never 'moderator': an add endpoint that could create one would be a
	// privilege grant wearing the shape of a membership change.
	if role != "member" {
		t.Fatalf("an administratively added member must join as 'member', got %q", role)
	}

	repeat, err := store.AddChannelMembers(ctx, fixture.channelID, []string{fixture.owner})
	if err != nil {
		t.Fatalf("repeat AddChannelMembers: %v", err)
	}
	if repeat.Added != 0 || repeat.AlreadyMembers != 1 {
		t.Fatalf("a repeat must add nobody, got %+v", repeat)
	}
}

// A guest is a workspace member and is therefore eligible. That is not an
// oversight: being added to a channel is the only way a guest reaches one.
func TestPostgreSQL_AddChannelMembersAdmitsAGuest(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)

	change, err := storage.NewPGXChannelDirectoryStore(pool).
		AddChannelMembers(context.Background(), fixture.channelID, []string{fixture.guest})
	if err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	if change.Added != 1 {
		t.Fatalf("a guest must be admissible, got %+v", change)
	}
}

// Somebody who is not a member of the channel's workspace cannot be admitted,
// and the refusal takes the whole request with it.
func TestPostgreSQL_AddChannelMembersRefusesAnOutsider(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	before := channelMemberIDs(t, pool, fixture.channelID)
	_, err := store.AddChannelMembers(ctx, fixture.channelID, []string{fixture.outsider})
	if err == nil {
		t.Fatal("an outsider must not be admitted")
	}
	// A mixed request is all-or-nothing: the eligible target is not admitted
	// either.
	if _, err := store.AddChannelMembers(ctx, fixture.channelID,
		[]string{fixture.owner, fixture.outsider}); err == nil {
		t.Fatal("a mixed request must be refused as a whole")
	}
	after := channelMemberIDs(t, pool, fixture.channelID)
	if len(after) != len(before) {
		t.Fatalf("nothing must have been written: %v -> %v", before, after)
	}
}

// A suspended account is not admitted anywhere, even from the platform scope.
func TestPostgreSQL_AddChannelMembersRefusesASuspendedAccount(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`UPDATE auth.users SET status = 'suspended' WHERE id = $1::uuid`, fixture.owner); err != nil {
		t.Fatalf("suspend user: %v", err)
	}
	if _, err := storage.NewPGXChannelDirectoryStore(pool).
		AddChannelMembers(ctx, fixture.channelID, []string{fixture.owner}); err == nil {
		t.Fatal("a suspended account must not be admitted")
	}
}

// An archived channel takes no new members, from either service.
func TestPostgreSQL_AddChannelMembersRefusesAnArchivedChannel(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`UPDATE chat.channels SET status = 'archived' WHERE id = $1::uuid`, fixture.channelID); err != nil {
		t.Fatalf("archive channel: %v", err)
	}
	if _, err := storage.NewPGXChannelDirectoryStore(pool).
		AddChannelMembers(ctx, fixture.channelID, []string{fixture.owner}); err == nil {
		t.Fatal("an archived channel must take no new members")
	}
}

// The picker offers only people the add would really admit: workspace members,
// active, not already in the channel — decided by the database, not by the
// console.
func TestPostgreSQL_MemberCandidatesOfferOnlyAdmissiblePeople(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	byID := func(candidates []domain.ChannelMemberCandidate) map[string]string {
		found := make(map[string]string, len(candidates))
		for _, candidate := range candidates {
			found[candidate.UserID] = candidate.DisplayName
		}
		return found
	}

	all, err := store.ListMemberCandidates(ctx, fixture.channelID, "", 50)
	if err != nil {
		t.Fatalf("ListMemberCandidates: %v", err)
	}
	found := byID(all)
	// The owner, the guest and the member belong to the workspace and are not in
	// the channel.
	for _, id := range []string{fixture.owner, fixture.guest, fixture.literal} {
		if _, ok := found[id]; !ok {
			t.Fatalf("an eligible workspace member must be offered, got %v", found)
		}
	}
	// The moderator is already a member of this channel.
	if _, ok := found[fixture.moderator]; ok {
		t.Fatal("somebody already in the channel must not be offered")
	}
	// The outsider belongs to no workspace at all.
	if _, ok := found[fixture.outsider]; ok {
		t.Fatal("somebody outside the workspace must never be offered")
	}

	// A guest is offered, and the workspace role travels so the operator can see
	// what they are about to add.
	for _, candidate := range all {
		if candidate.UserID == fixture.guest && candidate.WorkspaceRole != "guest" {
			t.Fatalf("the workspace role must be reported, got %q", candidate.WorkspaceRole)
		}
	}
}

// The search narrows by human identifiers, server-side, with the same literal
// treatment of wildcards the directory has.
func TestPostgreSQL_MemberCandidatesSearchByHumanIdentifiers(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	for _, term := range []string{"Olivia", "olivia", "owner@example.test"} {
		candidates, err := store.ListMemberCandidates(ctx, fixture.channelID, term, 50)
		if err != nil {
			t.Fatalf("ListMemberCandidates(%q): %v", term, err)
		}
		if len(candidates) != 1 || candidates[0].UserID != fixture.owner {
			t.Fatalf("%q should have found exactly the owner, got %+v", term, candidates)
		}
	}

	// A wildcard typed by a person is a literal, here as everywhere else.
	if candidates, err := store.ListMemberCandidates(ctx, fixture.channelID, "%", 50); err != nil {
		t.Fatalf("ListMemberCandidates: %v", err)
	} else if len(candidates) != 1 || candidates[0].UserID != fixture.literal {
		t.Fatalf("a bare %% must not behave as match-all, got %+v", candidates)
	}
}

// Adding somebody removes them from the offer, without a second query deciding
// it.
func TestPostgreSQL_MemberCandidatesDropSomebodyJustAdded(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	if _, err := store.AddChannelMembers(ctx, fixture.channelID, []string{fixture.owner}); err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	candidates, err := store.ListMemberCandidates(ctx, fixture.channelID, "Olivia", 50)
	if err != nil {
		t.Fatalf("ListMemberCandidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("a new member must stop being a candidate, got %+v", candidates)
	}
}

// The archived case matters because the add refuses it: the picker must not
// offer people the mutation would then reject.
func TestPostgreSQL_MemberCandidatesAreEmptyForAnArchivedChannel(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`UPDATE chat.channels SET status = 'archived' WHERE id = $1::uuid`, fixture.channelID); err != nil {
		t.Fatalf("archive channel: %v", err)
	}
	candidates, err := storage.NewPGXChannelDirectoryStore(pool).
		ListMemberCandidates(ctx, fixture.channelID, "", 50)
	if err != nil {
		t.Fatalf("ListMemberCandidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("an archived channel takes no new members and must offer none, got %+v", candidates)
	}
}

// A soft-deleted account is invisible to the directory, which is why
// status=deleted is not a filter this endpoint accepts: it could never return a
// row.
func TestPostgreSQL_DirectoryNeverReturnsSoftDeletedAccounts(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXUserDirectoryStore(pool)
	ctx := context.Background()

	before, err := store.ListUsers(ctx, domain.AdminUserFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE auth.users SET status = 'deleted', deleted_at = now() WHERE id = $1::uuid`,
		fixture.outsider); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	after, err := store.ListUsers(ctx, domain.AdminUserFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(after.Items) != len(before.Items)-1 {
		t.Fatalf("a soft-deleted account must leave the directory: %d -> %d", len(before.Items), len(after.Items))
	}
	for _, user := range after.Items {
		if user.ID == fixture.outsider {
			t.Fatal("a soft-deleted account must not be listed")
		}
	}
	// The detail view agrees with the listing.
	if _, err := store.GetUser(ctx, fixture.outsider); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a soft-deleted account, got %v", err)
	}
}

func TestPostgreSQL_RemoveChannelMemberIsIdempotentAndSparesGeneral(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	removed, err := store.RemoveChannelMember(ctx, fixture.channelID, fixture.moderator)
	if err != nil {
		t.Fatalf("RemoveChannelMember: %v", err)
	}
	if !removed.Removed {
		t.Fatalf("expected a removal, got %+v", removed)
	}
	if ids := channelMemberIDs(t, pool, fixture.channelID); len(ids) != 0 {
		t.Fatalf("the membership should be gone, got %v", ids)
	}

	repeat, err := store.RemoveChannelMember(ctx, fixture.channelID, fixture.moderator)
	if err != nil {
		t.Fatalf("a repeat removal must succeed, got %v", err)
	}
	if repeat.Removed {
		t.Fatal("nothing was removed, and the result must say so")
	}

	// #geral is refused, mirroring chat-service.
	if _, err := store.RemoveChannelMember(ctx, fixture.generalID, fixture.owner); err == nil {
		t.Fatal("removing somebody from #geral must be refused")
	}
}

// ---------------------------------------------------------------------------
// Per-user audit history
// ---------------------------------------------------------------------------

func appendAudit(t *testing.T, store *storage.PGXAdminStore, actor, action, resource string) {
	t.Helper()
	if err := store.AppendAudit(context.Background(), domain.AuditEvent{
		ActorUserID: actor,
		Action:      action,
		Resource:    resource,
		Result:      domain.AuditResultSuccess,
	}); err != nil {
		t.Fatalf("append audit: %v", err)
	}
}

func auditResources(entries []domain.AuditEntry) []string {
	resources := make([]string, 0, len(entries))
	for _, entry := range entries {
		resources = append(resources, entry.Resource)
	}
	return resources
}

// The regression this filter exists for.
//
// Ana's event is buried behind more recent platform activity than the limit
// will return, so the global trail cannot show it. Her own history must, which
// is only true if the WHERE clause runs before the ORDER BY and the LIMIT — a
// filter applied afterwards, or in the browser, would find nothing.
func TestPostgreSQL_UserAuditHistoryFiltersBeforeTheLimit(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXAdminStore(pool)
	ctx := context.Background()

	anaResource := domain.AuditUserResource(fixture.literal)
	appendAudit(t, store, fixture.owner, domain.AuditActionUserStatusUpdate, anaResource)

	// Bury it: 120 later events, none of them hers, against a limit of 50.
	const noise = 120
	const limit = 50
	for i := 0; i < noise; i++ {
		appendAudit(t, store, fixture.owner, domain.AuditActionChannelStatus,
			"admin.channel:"+fixture.channelID)
	}

	global, err := store.ListAuditEvents(ctx, domain.AuditFilter{Limit: limit})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(global) != limit {
		t.Fatalf("expected a full page of %d, got %d", limit, len(global))
	}
	for _, entry := range global {
		if entry.Resource == anaResource {
			t.Fatal("the fixture is wrong: her event must be outside the global window")
		}
	}

	filtered, err := store.ListAuditEvents(ctx, domain.AuditFilter{Resource: anaResource, Limit: limit})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Resource != anaResource {
		t.Fatalf("her history must contain exactly her event, got %v", auditResources(filtered))
	}
}

// One person's history contains nobody else's events.
func TestPostgreSQL_UserAuditHistoryDoesNotLeakAnotherPerson(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXAdminStore(pool)
	ctx := context.Background()

	anaResource := domain.AuditUserResource(fixture.literal)
	ottoResource := domain.AuditUserResource(fixture.outsider)
	appendAudit(t, store, fixture.owner, domain.AuditActionUserStatusUpdate, anaResource)
	appendAudit(t, store, fixture.owner, domain.AuditActionUserSessionsRevoke, ottoResource)
	appendAudit(t, store, fixture.owner, domain.AuditActionUserRoleGrant, ottoResource)

	ana, err := store.ListAuditEvents(ctx, domain.AuditFilter{Resource: anaResource, Limit: 50})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(ana) != 1 {
		t.Fatalf("expected only her own event, got %v", auditResources(ana))
	}

	otto, err := store.ListAuditEvents(ctx, domain.AuditFilter{Resource: ottoResource, Limit: 50})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(otto) != 2 {
		t.Fatalf("expected his two events, got %v", auditResources(otto))
	}
	for _, entry := range otto {
		if entry.Resource != ottoResource {
			t.Fatalf("somebody else's event leaked into his history: %q", entry.Resource)
		}
	}

	// The unfiltered trail still has everything.
	all, err := store.ListAuditEvents(ctx, domain.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("the global trail must be unchanged, got %v", auditResources(all))
	}
}

// Every mutation this feature performs on an account is findable in that
// account's history — the four producers agree on the key, and the filter finds
// all four.
func TestPostgreSQL_UserAuditHistoryFindsEveryUserMutation(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXAdminStore(pool)
	ctx := context.Background()

	resource := domain.AuditUserResource(fixture.literal)
	actions := []string{
		domain.AuditActionUserStatusUpdate,
		domain.AuditActionUserSessionsRevoke,
		domain.AuditActionUserRoleGrant,
		domain.AuditActionUserRoleRevoke,
	}
	for _, action := range actions {
		appendAudit(t, store, fixture.owner, action, resource)
	}

	entries, err := store.ListAuditEvents(ctx, domain.AuditFilter{Resource: resource, Limit: 50})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	found := make(map[string]bool, len(entries))
	for _, entry := range entries {
		found[entry.Action] = true
	}
	for _, action := range actions {
		if !found[action] {
			t.Fatalf("%s is missing from the user's history", action)
		}
	}
	// Newest first, like the global trail.
	if entries[0].Action != domain.AuditActionUserRoleRevoke {
		t.Fatalf("expected the newest event first, got %q", entries[0].Action)
	}
}

// A history for somebody with no administrative events is empty, not an error
// and not everybody's.
func TestPostgreSQL_UserAuditHistoryIsEmptyForAQuietAccount(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXAdminStore(pool)
	ctx := context.Background()

	appendAudit(t, store, fixture.owner, domain.AuditActionUserStatusUpdate,
		domain.AuditActionUserStatusUpdate)
	entries, err := store.ListAuditEvents(ctx,
		domain.AuditFilter{Resource: domain.AuditUserResource(fixture.guest), Limit: 50})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected an empty history, got %v", auditResources(entries))
	}
}

// The index added by migration 000011 can serve the filter, the ordering and
// the limit together — no separate sort step.
//
// The assertion forces the planner rather than reading its choice: at fixture
// size a sequential scan of two hundred rows is genuinely cheaper, and testing
// which plan it picks would be testing the cost model against a table nobody
// deploys. What matters is the property the migration claims — that the index
// *covers* this query shape — which is exactly what the forced plan shows, and
// what stops being true if somebody changes the index's column order.
func TestPostgreSQL_ResourceIndexCoversTheFilterAndTheOrdering(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXAdminStore(pool)
	ctx := context.Background()

	resource := domain.AuditUserResource(fixture.literal)
	appendAudit(t, store, fixture.owner, domain.AuditActionUserStatusUpdate, resource)
	for i := 0; i < 200; i++ {
		appendAudit(t, store, fixture.owner, domain.AuditActionChannelStatus,
			"admin.channel:"+fixture.channelID)
	}
	if _, err := pool.Exec(ctx, `ANALYZE auth.admin_audit_events`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer connection.Release()
	// Session-scoped, on this connection only, and released with it.
	if _, err := connection.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	rows, err := connection.Query(ctx, `
		EXPLAIN SELECT e.id FROM auth.admin_audit_events AS e
		WHERE e.resource = $1
		ORDER BY e.occurred_at DESC, e.id DESC
		LIMIT 50`, resource)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if !strings.Contains(plan.String(), "idx_admin_audit_events_resource") {
		t.Fatalf("the resource index must be able to serve this query, plan was:\n%s", plan.String())
	}
	// The index carries the ordering, so the plan needs no sort of its own.
	if strings.Contains(plan.String(), "Sort") {
		t.Fatalf("the index should carry the ordering, plan was:\n%s", plan.String())
	}
}

// ---------------------------------------------------------------------------
// member_count under concurrency
// ---------------------------------------------------------------------------

// waitForLockWaiters blocks until `want` backends are waiting on a lock.
//
// It observes the condition rather than assuming elapsed time: the test needs
// to know that both workers have really reached the channel lock and stopped
// there, and pg_stat_activity says so. Polling here is a read of the actual
// state, not a guess about scheduling.
func waitForLockWaiters(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND wait_event_type = 'Lock'
			  AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatalf("read lock waiters: %v", err)
		}
		if waiting >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d transactions to queue on the channel lock", want)
}

// runConcurrently forces both operations to be contending for the same channel
// at the same instant, then returns the totals they reported.
//
// Simply starting two goroutines is not enough — they finish so quickly that
// they usually serialize by accident, and the test then passes even with a
// shared lock. So a third transaction takes the channel row exclusively first;
// both workers block on it; the helper waits until PostgreSQL confirms both are
// queued; only then is the holder released. From that moment the two are
// genuinely racing, and the lock mode is the only thing that decides the
// outcome.
//
// TestPostgreSQL_ConcurrentAddsReportConsecutiveTotals fails if the store goes
// back to FOR SHARE, which is what makes this a test of the fix rather than a
// description of it.
//
// The timeouts are safety nets so a lost wake-up fails instead of hanging the
// suite; they are never the synchronisation.
func runConcurrently(t *testing.T, pool *pgxpool.Pool, channelID string, first, second func() (int, error)) (int, int) {
	t.Helper()
	ctx := context.Background()

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	released := false
	defer func() {
		if !released {
			_ = holder.Rollback(ctx)
		}
	}()
	if _, err := holder.Exec(ctx,
		`SELECT id FROM chat.channels WHERE id = $1::uuid FOR UPDATE`, channelID); err != nil {
		t.Fatalf("hold channel lock: %v", err)
	}

	type outcome struct {
		count int
		err   error
	}
	results := make([]outcome, 2)
	done := make(chan int, 2)
	for index, operation := range []func() (int, error){first, second} {
		go func(index int, operation func() (int, error)) {
			count, err := operation()
			results[index] = outcome{count: count, err: err}
			done <- index
		}(index, operation)
	}

	// Both workers are now queued behind the holder.
	waitForLockWaiters(t, pool, 2)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release channel lock: %v", err)
	}
	released = true

	for range 2 {
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("a concurrent membership mutation did not finish; the channel lock may not be released")
		}
	}
	for index, result := range results {
		if result.err != nil {
			t.Fatalf("operation %d failed: %v", index, result.err)
		}
	}
	return results[0].count, results[1].count
}

func memberCount(t *testing.T, pool *pgxpool.Pool, channelID string) int {
	t.Helper()
	var total int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat.channel_members WHERE channel_id = $1::uuid`, channelID).Scan(&total); err != nil {
		t.Fatalf("count members: %v", err)
	}
	return total
}

// sortedPair returns the two counts in ascending order, because which
// administrator wins the race is not part of the contract — only that the two
// answers are consecutive and the second one is the committed total.
func sortedPair(a, b int) (int, int) {
	if a > b {
		return b, a
	}
	return a, b
}

// Two administrators adding different people to the same channel at the same
// moment must not both report the same total.
//
// Before the channel row lock this was the failure: both transactions counted
// their own insert against the same committed snapshot, both answered N+1, and
// the database held N+2. One response was a lie about a number the API
// documents as the resulting total.
func TestPostgreSQL_ConcurrentAddsReportConsecutiveTotals(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	before := memberCount(t, pool, fixture.channelID)

	first, second := runConcurrently(t, pool, fixture.channelID,
		func() (int, error) {
			change, err := store.AddChannelMembers(ctx, fixture.channelID, []string{fixture.owner})
			return change.MemberCount, err
		},
		func() (int, error) {
			change, err := store.AddChannelMembers(ctx, fixture.channelID, []string{fixture.guest})
			return change.MemberCount, err
		},
	)

	low, high := sortedPair(first, second)
	if low != before+1 || high != before+2 {
		t.Fatalf("expected %d and %d in some order, got %d and %d", before+1, before+2, first, second)
	}
	if final := memberCount(t, pool, fixture.channelID); final != before+2 {
		t.Fatalf("expected %d members committed, got %d", before+2, final)
	}
}

// Removals have the same shape and the same failure mode.
func TestPostgreSQL_ConcurrentRemovalsReportConsecutiveTotals(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	if _, err := store.AddChannelMembers(ctx, fixture.channelID,
		[]string{fixture.owner, fixture.guest}); err != nil {
		t.Fatalf("seed members: %v", err)
	}
	before := memberCount(t, pool, fixture.channelID)

	first, second := runConcurrently(t, pool, fixture.channelID,
		func() (int, error) {
			change, err := store.RemoveChannelMember(ctx, fixture.channelID, fixture.owner)
			return change.MemberCount, err
		},
		func() (int, error) {
			change, err := store.RemoveChannelMember(ctx, fixture.channelID, fixture.guest)
			return change.MemberCount, err
		},
	)

	low, high := sortedPair(first, second)
	if low != before-2 || high != before-1 {
		t.Fatalf("expected %d and %d in some order, got %d and %d", before-2, before-1, first, second)
	}
	if final := memberCount(t, pool, fixture.channelID); final != before-2 {
		t.Fatalf("expected %d members committed, got %d", before-2, final)
	}
}

// An add racing a removal is the mixed case: the two totals must still describe
// a real sequence rather than two independent guesses.
func TestPostgreSQL_ConcurrentAddAndRemovalAgreeOnTheSequence(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	if _, err := store.AddChannelMembers(ctx, fixture.channelID, []string{fixture.guest}); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	before := memberCount(t, pool, fixture.channelID)

	added, removed := runConcurrently(t, pool, fixture.channelID,
		func() (int, error) {
			change, err := store.AddChannelMembers(ctx, fixture.channelID, []string{fixture.owner})
			return change.MemberCount, err
		},
		func() (int, error) {
			change, err := store.RemoveChannelMember(ctx, fixture.channelID, fixture.guest)
			return change.MemberCount, err
		},
	)

	// Whichever ran first saw `before`; the other saw its predecessor's result.
	// Either way the final state is one added and one removed.
	if final := memberCount(t, pool, fixture.channelID); final != before {
		t.Fatalf("expected %d members committed, got %d", before, final)
	}
	valid := (added == before+1 && removed == before) || (removed == before-1 && added == before)
	if !valid {
		t.Fatalf("the two totals do not describe one sequence: add reported %d, remove reported %d, starting from %d",
			added, removed, before)
	}
}

// Two operators repeatedly adding the same person must not both claim to have
// added them. The lock makes the second one an idempotent no-op that reports
// the same total, rather than a second increment.
func TestPostgreSQL_ConcurrentAddsOfTheSamePersonAddOnce(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	before := memberCount(t, pool, fixture.channelID)
	first, second := runConcurrently(t, pool, fixture.channelID,
		func() (int, error) {
			change, err := store.AddChannelMembers(ctx, fixture.channelID, []string{fixture.owner})
			return change.MemberCount, err
		},
		func() (int, error) {
			change, err := store.AddChannelMembers(ctx, fixture.channelID, []string{fixture.owner})
			return change.MemberCount, err
		},
	)

	if first != before+1 || second != before+1 {
		t.Fatalf("both must report %d, got %d and %d", before+1, first, second)
	}
	if final := memberCount(t, pool, fixture.channelID); final != before+1 {
		t.Fatalf("the person must be added once, got %d members", final)
	}
}

// The lock is per channel, not global: a mutation on one channel must not have
// to wait for a transaction holding another channel's row.
//
// Proved by holding channel A's row in an open transaction and requiring a
// mutation on channel B to complete anyway. No timing assumption — if the lock
// were global, the second call would block until the timeout and fail.
func TestPostgreSQL_ChannelLockDoesNotSerializeDifferentChannels(t *testing.T) {
	pool := connectAdminTestDB(t)
	fixture := seedDirectory(t, pool)
	store := storage.NewPGXChannelDirectoryStore(pool)
	ctx := context.Background()

	var otherChannel string
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat.channels (workspace_id, slug, display_name, type, status, created_by)
		VALUES ($1::uuid, 'outro', 'Outro', 'public', 'active', $2::uuid)
		RETURNING id::text`, fixture.workspaceID, fixture.owner).Scan(&otherChannel); err != nil {
		t.Fatalf("insert second channel: %v", err)
	}

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx,
		`SELECT id FROM chat.channels WHERE id = $1::uuid FOR UPDATE`, fixture.channelID); err != nil {
		t.Fatalf("hold channel lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, addErr := store.AddChannelMembers(ctx, otherChannel, []string{fixture.owner})
		done <- addErr
	}()

	select {
	case addErr := <-done:
		if addErr != nil {
			t.Fatalf("a mutation on another channel failed: %v", addErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a mutation on a different channel blocked on another channel's lock: the lock is global, not per channel")
	}
}
