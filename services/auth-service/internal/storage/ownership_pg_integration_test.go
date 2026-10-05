package storage_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// The actual status writer must roll back account status and session revocation
// when ownership cannot pass to an eligible participant.
func TestOwnershipAccountInvalidationPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	applyChatMigrations(t, pool)
	a := insertActiveUser(t, pool, "ownership-a@example.test")
	b := insertActiveUser(t, pool, "ownership-b@example.test")
	const ws = "00000000-0000-0000-0000-000000000001"
	const conversation = "95300000-0000-4000-8000-000000000009"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO chat.workspace_members(workspace_id,user_id) VALUES($1,$2),($1,$3)`, ws, a, b)
	exec(`INSERT INTO chat.dm_conversations(id,workspace_id,type,created_by) VALUES($1,$2,'group',$3)`, conversation, ws, a)
	exec(`INSERT INTO chat.dm_members(conversation_id,user_id) VALUES($1,$2),($1,$3)`, conversation, a, b)
	exec(ownershipActivationSQL(t))
	store := storage.NewPGXUserStore(pool)
	if _, err := store.UpdateUserStatus(t.Context(), a, "suspended"); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := pool.QueryRow(t.Context(), `SELECT user_id::text FROM chat.active_ownership_participants WHERE kind='dm' AND conversation_id=$1 AND role='owner'`, conversation).Scan(&owner); err != nil || owner != b {
		t.Fatalf("successor=%s err=%v", owner, err)
	}
	if _, err := store.UpdateUserStatus(t.Context(), a, "active"); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE chat.workspace_members SET role='guest' WHERE user_id=$1`, a)
	exec(`SELECT chat.assign_ownership('dm',$1,$2,'member',$3,'manual')`, conversation, a, b)
	exec(`INSERT INTO auth.user_sessions(user_id,refresh_token_hash,idle_expires_at) VALUES($1,'ownership-session-953',now()+interval '1 hour')`, b)
	if _, err := store.UpdateUserStatus(t.Context(), b, "suspended"); !errors.Is(err, domain.ErrConversationOwnershipConflict) {
		t.Fatalf("missing ownership conflict: %v", err)
	}
	var status string
	var revoked bool
	if err := pool.QueryRow(t.Context(), `SELECT u.status,s.revoked_at IS NOT NULL FROM auth.users u JOIN auth.user_sessions s ON s.user_id=u.id WHERE u.id=$1 AND s.refresh_token_hash='ownership-session-953'`, b).Scan(&status, &revoked); err != nil || status != "active" || revoked {
		t.Fatalf("partial invalidation status=%s revoked=%v error=%v", status, revoked, err)
	}
}

// Parse the operator transaction with fixed test evidence.
func ownershipActivationSQL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../../../scripts/db/ownership/activate.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	_, transaction, found := strings.Cut(sql, "BEGIN;")
	if !found {
		t.Fatal("activation transaction missing")
	}
	transaction = strings.NewReplacer(
		":'legacy_retired'", "'true'",
		":'rollback_target_sha'", "'1043000000000000000000000000000000000000'",
		":'retirement_evidence'", "'test-1043'",
		"\\gset", ";",
	).Replace(transaction)
	return "BEGIN;" + transaction
}
