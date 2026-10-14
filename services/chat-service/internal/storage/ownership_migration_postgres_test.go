package storage_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const ownership1043Migration = "000064_ownership_backfill_activation"
const ownership1043SHA = "1043000000000000000000000000000000000000"

func ownership1043Snapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var snapshot string
	err := pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
        'dm', (SELECT jsonb_agg(to_jsonb(m) ORDER BY conversation_id,user_id) FROM chat.dm_members m),
        'channel', (SELECT jsonb_agg(to_jsonb(m) ORDER BY channel_id,user_id) FROM chat.channel_members m),
        'audit', (SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM chat.ownership_audit a),
        'rollout', (SELECT to_jsonb(r) FROM chat.ownership_rollout r WHERE singleton))::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestOwnershipMigrationUpDownPostgreSQL(t *testing.T) {
	pool := ownershipPoolAt(t, true)
	for _, name := range []string{
		"000060_conversation_ownership_compatibility", "000061_conversation_ownership_guards",
		"000062_conversation_ownership_writers", "000063_conversation_ownership_outbox", ownership1043Migration,
	} {
		ownershipExec(t, pool, readChatMigration(t, name+".up.sql"))
	}
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipA, "owner")
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipB, "admin")
	// Expanded storage accepts the common contract; legacy role values stay intact.
	for _, role := range []string{"member", "admin", "owner"} {
		ownershipExec(t, pool, `UPDATE chat.dm_members SET ownership_role=$1 WHERE user_id=$2`, role, ownershipC)
		ownershipExec(t, pool, `UPDATE chat.channel_members SET ownership_role=$1 WHERE channel_id=$2 AND user_id=$3`, role, ownershipChannel, ownershipC)
	}
	var incompatible int
	if err := pool.QueryRow(t.Context(), `SELECT
        (SELECT count(*) FROM chat.dm_members WHERE role <> 'member') +
        (SELECT count(*) FROM chat.channel_members WHERE role NOT IN ('member','moderator'))`).Scan(&incompatible); err != nil || incompatible != 0 {
		t.Fatalf("legacy representation changed: %d, %v", incompatible, err)
	}
	before := ownership1043Snapshot(t, pool)
	ownershipExec(t, pool, readChatMigration(t, ownership1043Migration+".down.sql"))
	ownershipExec(t, pool, readChatMigration(t, ownership1043Migration+".up.sql"))
	if got := ownership1043Snapshot(t, pool); got != before {
		t.Fatal("up/down/up changed existing membership or audit data")
	}
	enableOwnership(t, pool)
	for _, name := range []string{ownership1043Migration, "000060_conversation_ownership_compatibility"} {
		ownership1043RejectDowngrade(t, pool, name)
	}
	// Disabling the flag cannot undo activation history or normalized role data.
	ownershipExec(t, pool, `UPDATE chat.ownership_rollout SET enabled=false`)
	ownership1043RejectDowngrade(t, pool, ownership1043Migration)
	ownershipExec(t, pool, `UPDATE chat.ownership_rollout SET legacy_retired_at=NULL,rollback_target_sha=NULL,retirement_evidence=NULL`)
	ownership1043RejectDowngrade(t, pool, ownership1043Migration)

}

func ownership1043RejectDowngrade(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	before := ownership1043Snapshot(t, pool)
	if _, err := pool.Exec(t.Context(), readChatMigration(t, name+".down.sql")); err == nil {
		t.Fatal("activated schema downgrade accepted")
	}
	ownershipExec(t, pool, "ROLLBACK")
	if ownership1043Snapshot(t, pool) != before {
		t.Fatal("rejected downgrade changed data")
	}
}

type ownershipBackfillCase struct {
	name, setup, groupOwner, channelOwner string
}

func TestOwnershipBackfillTablePostgreSQL(t *testing.T) {
	for _, scenario := range []ownershipBackfillCase{
		{"creator", "", ownershipA, ownershipA},
		{"missing creator", `UPDATE chat.dm_conversations SET created_by='10430000-0000-4000-8000-000000000099'; UPDATE chat.channels SET created_by=NULL`, ownershipC, ownershipB},
		{"inactive creator", `UPDATE auth.users SET status='suspended' WHERE id='` + ownershipA + `'`, ownershipC, ownershipB},
		{"deleted creator", `UPDATE auth.users SET deleted_at=now() WHERE id='` + ownershipA + `'`, ownershipC, ownershipB},
		{"inactive membership", `UPDATE chat.dm_members SET status='left',left_at=now() WHERE user_id='` + ownershipA + `'; UPDATE chat.workspace_members SET status='suspended' WHERE user_id='` + ownershipA + `'`, ownershipC, ownershipB},
		{"group ignores admin", `UPDATE auth.users SET status='suspended' WHERE id='` + ownershipA + `'; UPDATE chat.dm_members SET ownership_role='admin' WHERE user_id='` + ownershipB + `'`, ownershipC, ownershipB},
		{"channel oldest fallback", `UPDATE auth.users SET status='suspended' WHERE id='` + ownershipA + `'; UPDATE chat.channel_members SET role='member'`, ownershipC, ownershipC},
		{"ties", `UPDATE auth.users SET status='suspended' WHERE id='` + ownershipA + `'; UPDATE chat.channel_members SET role='member'; UPDATE chat.dm_members SET joined_at='2020-01-01'; UPDATE chat.channel_members SET joined_at='2020-01-01'`, ownershipB, ownershipB},
		{"guest creator", `UPDATE chat.workspace_members SET role='guest' WHERE user_id='` + ownershipA + `'`, ownershipC, ownershipB},
		{"empty", `DELETE FROM chat.dm_members; DELETE FROM chat.channel_members`, "", ""},
		{"inactive workspace", `UPDATE chat.workspaces SET status='disabled'`, "", ""},
		{"cross workspace creator", `DELETE FROM chat.workspace_members WHERE user_id='` + ownershipA + `'; INSERT INTO chat.workspaces(id,name,slug) VALUES('10430000-0000-4000-8000-000000000001','Other','other-1043'); INSERT INTO chat.channels(workspace_id,slug,display_name,type,is_general) VALUES('10430000-0000-4000-8000-000000000001','general','General','public',true); INSERT INTO chat.workspace_members(workspace_id,user_id) VALUES('10430000-0000-4000-8000-000000000001','` + ownershipA + `')`, ownershipC, ownershipB},
		{"preserve guest owner", `UPDATE chat.dm_members SET ownership_role='owner' WHERE user_id='` + ownershipC + `'; UPDATE chat.channel_members SET ownership_role='owner' WHERE channel_id='` + ownershipChannel + `' AND user_id='` + ownershipC + `'; UPDATE chat.workspace_members SET role='guest' WHERE user_id='` + ownershipC + `'`, ownershipC, ownershipC},
	} {
		t.Run(scenario.name, func(t *testing.T) { ownership1043BackfillCase(t, scenario) })
	}
}

func ownership1043BackfillCase(t *testing.T, scenario ownershipBackfillCase) {
	t.Helper()
	pool := ownershipPool(t)
	ownershipExec(t, pool, `UPDATE chat.dm_members SET ownership_role=NULL; UPDATE chat.channel_members SET ownership_role=NULL`)
	if scenario.setup != "" {
		ownershipExec(t, pool, scenario.setup)
	}
	ownershipExec(t, pool, `SELECT chat.backfill_conversation_ownership()`)
	for kind, want := range map[string]string{"dm": scenario.groupOwner, "channel": scenario.channelOwner} {
		var got string
		err := pool.QueryRow(t.Context(), `SELECT COALESCE(string_agg(user_id::text,',' ORDER BY user_id),'') FROM chat.active_ownership_participants WHERE kind=$1 AND role='owner'`, kind).Scan(&got)
		if err != nil || got != want {
			t.Fatalf("%s owner=%s want=%s err=%v", kind, got, want, err)
		}
	}
	assertNoOrphans(t, pool)
	before := ownership1043Snapshot(t, pool)
	var changed int
	if err := pool.QueryRow(t.Context(), `SELECT chat.backfill_conversation_ownership()`).Scan(&changed); err != nil || changed != 0 {
		t.Fatalf("second backfill=%d err=%v", changed, err)
	}
	if ownership1043Snapshot(t, pool) != before {
		t.Fatal("second execution changed membership, audit or rollout")
	}
}

func ownership1043PSQL(t *testing.T, script string, variables ...string) error {
	t.Helper()
	_, err := exec.LookPath("psql")
	if err != nil {
		t.Fatal("psql is required to test the actual operator gates")
	}
	args := []string{"-X", "--no-password", "--dbname", os.Getenv("OWNERSHIP_TEST_DATABASE_URL")}
	for _, variable := range variables {
		args = append(args, "--set", variable)
	}
	args = append(args, "--file", filepath.Join("../../../../scripts/db/ownership", script))
	//nolint:gosec // Fixed executable, argv without a shell; callers use fixed scripts and the pool validates ownership_953_test.
	output, err := exec.CommandContext(t.Context(), "psql", args...).CombinedOutput()
	if err != nil {
		t.Logf("psql refused operation: %s", strings.TrimSpace(string(output)))
	}
	return err
}

func TestOwnershipOperationalActivationPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	valid := []string{"legacy_retired=true", "rollback_target_sha=" + ownership1043SHA, "retirement_evidence=test-1043"}
	t.Run("workspace lock timeout", func(t *testing.T) { ownership1043WorkspaceLock(t, pool) })
	t.Run("operator evidence", func(t *testing.T) { ownership1043Evidence(t, pool) })
	t.Run("legacy write and read", func(t *testing.T) { ownership1043Legacy(t, pool) })
	t.Run("preflight and atomicity", func(t *testing.T) { ownership1043Preflight(t, pool, valid) })
	t.Run("activation and compatible rollback", func(t *testing.T) { ownership1043Activate(t, pool, valid) })
	for _, scenario := range []struct {
		name, setup string
		accepted    bool
	}{
		{"missing rollout", `DELETE FROM chat.ownership_rollout`, false},
		{"inconsistent rollout", `UPDATE chat.ownership_rollout SET legacy_retired_at=now()`, false},
		{"previous activation without evidence", `UPDATE chat.ownership_rollout SET legacy_retired_at=now(), enabled=true`, false},
		{"empty conversations", `DELETE FROM chat.dm_members; DELETE FROM chat.channel_members`, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pool := ownershipPool(t)
			ownershipExec(t, pool, scenario.setup)
			before := ownership1043Snapshot(t, pool)
			err := ownership1043PSQL(t, "activate.sql", valid...)
			if (err == nil) != scenario.accepted {
				t.Fatalf("activation accepted=%v want=%v", err == nil, scenario.accepted)
			}
			if !scenario.accepted && ownership1043Snapshot(t, pool) != before {
				t.Fatal("rejected activation changed data")
			}
		})
	}
}

func ownership1043Evidence(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	before := ownership1043Snapshot(t, pool)
	for _, vars := range [][]string{
		nil,
		{"legacy_retired=true"},
		{"legacy_retired=true", "rollback_target_sha=" + ownership1043SHA},
		{"legacy_retired=false", "rollback_target_sha=" + ownership1043SHA, "retirement_evidence=test-1043"},
		{"legacy_retired=true", "rollback_target_sha=unknown", "retirement_evidence=test-1043"},
		{"legacy_retired=true", "rollback_target_sha=" + ownership1043SHA, "retirement_evidence=x'); DELETE FROM chat.dm_members; --"},
	} {
		if ownership1043PSQL(t, "activate.sql", vars...) == nil {
			t.Fatal("activation accepted missing/invalid operational evidence")
		}
		if ownership1043Snapshot(t, pool) != before {
			t.Fatal("rejected activation changed data")
		}
	}
}

func ownership1043Legacy(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	// Statements from incompatible N remain valid; N+1 reads the mapped state.
	ownershipExec(t, pool, `DELETE FROM chat.channel_members WHERE channel_id=$1 AND user_id=$2`, ownershipChannel, ownershipC)
	ownershipExec(t, pool, `INSERT INTO chat.channel_members(channel_id,user_id,role) VALUES($1,$2,'member')`, ownershipChannel, ownershipC)
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipC, "member")
	if _, err := pool.Exec(t.Context(), `UPDATE chat.dm_members SET role='owner' WHERE user_id=$1`, ownershipA); err == nil {
		t.Fatal("incompatible new legacy-column write accepted before activation")
	}
}

func ownership1043Preflight(t *testing.T, pool *pgxpool.Pool, valid []string) {
	t.Helper()
	// Activation must undo its own normalization when no automatic owner exists.
	ownershipExec(t, pool, `UPDATE chat.workspace_members SET role='guest'; UPDATE chat.dm_members SET ownership_role='member'; UPDATE chat.channel_members SET ownership_role=NULL`)
	before := ownership1043Snapshot(t, pool)
	if ownership1043PSQL(t, "preflight.sql") == nil || ownership1043PSQL(t, "activate.sql", valid...) == nil {
		t.Fatal("nonempty guest-only orphan accepted")
	}
	if ownership1043Snapshot(t, pool) != before {
		t.Fatal("failed preflight/activation left a partial backfill")
	}
	ownershipExec(t, pool, `UPDATE chat.workspace_members SET role='member'`)
	if err := ownership1043PSQL(t, "backfill.sql"); err != nil {
		t.Fatal(err)
	}
	if err := ownership1043PSQL(t, "preflight.sql"); err != nil {
		t.Fatal(err)
	}
}

func ownership1043Activate(t *testing.T, pool *pgxpool.Pool, valid []string) {
	t.Helper()
	if err := ownership1043PSQL(t, "activate.sql", valid...); err != nil {
		t.Fatal(err)
	}
	assertNoOrphans(t, pool)
	before := ownership1043Snapshot(t, pool)
	if err := ownership1043PSQL(t, "activate.sql", valid...); err != nil {
		t.Fatal(err)
	}
	if ownership1043Snapshot(t, pool) != before {
		t.Fatal("activation retry changed data or evidence")
	}
	if ownership1043PSQL(t, "activate.sql", "legacy_retired=true", "rollback_target_sha="+strings.Repeat("a", 40), "retirement_evidence=test-1043") == nil {
		t.Fatal("activation changed the recorded rollback target")
	}
	// The #953 rollback reader uses canonical role when the shadow is absent.
	ownershipExec(t, pool, `UPDATE chat.dm_members SET ownership_role=NULL WHERE user_id=$1`, ownershipA)
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipA, "owner")
}

func ownership1043WorkspaceLock(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	before := ownership1043Snapshot(t, pool)
	blocker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(t.Context()) }()
	if _, err := blocker.Exec(t.Context(), "LOCK TABLE chat.workspaces IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	_, err = conn.Exec(t.Context(), `BEGIN; SET LOCAL lock_timeout='50ms';
        SELECT chat.prepare_conversation_ownership_activation(true,'`+ownership1043SHA+`','test-1043'); COMMIT;`)
	if err == nil || !strings.Contains(err.Error(), "55P03") {
		t.Fatalf("expected workspace lock timeout, got %v", err)
	}
	if _, err := conn.Exec(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if ownership1043Snapshot(t, pool) != before {
		t.Fatal("lock timeout changed activation state")
	}
}
