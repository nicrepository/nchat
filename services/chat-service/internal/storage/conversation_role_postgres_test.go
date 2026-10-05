package storage_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func roleEffects(t *testing.T, pool *pgxpool.Pool) (audit, outbox int) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM chat.ownership_audit),(SELECT count(*) FROM chat.ownership_outbox)`).Scan(&audit, &outbox); err != nil {
		t.Fatal(err)
	}
	return
}

func TestOwnershipConversationRoleMatrixPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		t.Run(kind, func(t *testing.T) {
			pool := ownershipPool(t)
			enableOwnership(t, pool)
			store := storage.NewPGXOwnershipStore(pool)
			input := roleInput(kind, domain.ConversationOwner)
			if kind == "channel" {
				input.Scope.ConversationID = ownershipChannel
			}
			ownershipExec(t, pool, `SELECT chat.assign_ownership($1,$2,$3,'owner',$4,'manual')`, kind, input.Scope.ConversationID, ownershipC, ownershipA)
			roles := []domain.ConversationRole{domain.ConversationOwner, domain.ConversationAdmin, domain.ConversationMember}
			for _, actor := range roles {
				for _, before := range roles {
					for _, after := range roles {
						ownershipExec(t, pool, `SELECT chat.assign_ownership($1,$2,$3,$4,$5,'manual')`, kind, input.Scope.ConversationID, ownershipA, string(actor), ownershipC)
						ownershipExec(t, pool, `SELECT chat.assign_ownership($1,$2,$3,$4,$5,'manual')`, kind, input.Scope.ConversationID, ownershipB, string(before), ownershipC)
						audit, outbox := roleEffects(t, pool)
						input.Role = after
						result, err := store.Mutate(t.Context(), input)
						final := before
						if actor == domain.ConversationOwner {
							if err != nil || result.Role != after {
								t.Fatalf("%s %s->%s: result=%+v err=%v", actor, before, after, result, err)
							}
							final = after
						} else if !errors.Is(err, domain.ErrForbidden) {
							t.Fatalf("%s admitted: %v", actor, err)
						}
						assertOwnershipRole(t, pool, kind, input.Scope.ConversationID, ownershipB, string(final))
						nextAudit, nextOutbox := roleEffects(t, pool)
						writes := 0
						if actor == domain.ConversationOwner && before != after {
							writes = 1
						}
						if nextAudit != audit+writes || nextOutbox != outbox+writes {
							t.Fatalf("unexpected effects %s %s->%s: audit %d->%d outbox %d->%d", actor, before, after, audit, nextAudit, outbox, nextOutbox)
						}
					}
				}
			}
			assertNoOrphans(t, pool)
		})
	}
}

func TestOwnershipConversationRoleBoundaryPostgreSQL(t *testing.T) {
	for _, scenario := range []string{"last-owner", "sole-owner", "cross-workspace", "actor-removed", "target-removed", "workspace-admin", "actor-demoted", "actor-suspended", "target-suspended", "archived", "public", "rollout-disabled"} {
		t.Run(scenario, func(t *testing.T) {
			pool := ownershipPool(t)
			enableOwnership(t, pool)
			input := roleInput("dm", domain.ConversationAdmin)
			want := domain.ErrNotFound
			switch scenario {
			case "last-owner":
				input.TargetUserID = ownershipA
				want = domain.ErrOwnershipConflict
			case "sole-owner":
				ownershipExec(t, pool, `DELETE FROM chat.dm_members WHERE user_id<>$1`, ownershipA)
				input.TargetUserID = ownershipA
				want = domain.ErrOwnershipConflict
			case "cross-workspace":
				input.Scope.WorkspaceID = "00000000-0000-0000-0000-000000000002"
			case "actor-removed":
				ownershipExec(t, pool, `UPDATE chat.dm_members SET status='left',left_at=now() WHERE user_id=$1`, ownershipA)
			case "target-removed":
				ownershipExec(t, pool, `UPDATE chat.dm_members SET status='left',left_at=now() WHERE user_id=$1`, ownershipB)
			case "workspace-admin":
				ownershipExec(t, pool, `UPDATE chat.workspace_members SET role='admin' WHERE user_id=$1`, ownershipC)
				input.Scope.ActorID = ownershipC
				want = domain.ErrForbidden
			case "actor-demoted":
				ownershipExec(t, pool, `SELECT chat.assign_ownership('dm',$1,$2,'owner',$3,'manual')`, ownershipDM, ownershipC, ownershipA)
				ownershipExec(t, pool, `SELECT chat.assign_ownership('dm',$1,$2,'member',$3,'manual')`, ownershipDM, ownershipA, ownershipC)
				want = domain.ErrForbidden
			case "actor-suspended":
				ownershipExec(t, pool, `UPDATE auth.users SET status='suspended' WHERE id=$1`, ownershipA)
			case "target-suspended":
				ownershipExec(t, pool, `UPDATE auth.users SET status='suspended' WHERE id=$1`, ownershipB)
			case "archived":
				ownershipExec(t, pool, `UPDATE chat.dm_conversations SET status='archived' WHERE id=$1`, ownershipDM)
			case "public":
				input.Scope.Kind = "channel"
				input.Scope.ConversationID = ownershipChannel
				ownershipExec(t, pool, `UPDATE chat.channels SET type='public' WHERE id=$1`, ownershipChannel)
			case "rollout-disabled":
				ownershipExec(t, pool, `UPDATE chat.ownership_rollout SET enabled=false WHERE singleton`)
				want = domain.ErrForbidden
			}
			audit, outbox := roleEffects(t, pool)
			_, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), input)
			if !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			nextAudit, nextOutbox := roleEffects(t, pool)
			if audit != nextAudit || outbox != nextOutbox {
				t.Fatal("denial wrote effects")
			}
		})
	}
}

func TestOwnershipConversationRoleRollbackPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	// Fail after assign_ownership has changed the membership and written audit.
	ownershipExec(t, pool, `CREATE FUNCTION chat.fail_role_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$;
 CREATE TRIGGER fail_role_audit AFTER INSERT ON chat.ownership_audit FOR EACH ROW EXECUTE FUNCTION chat.fail_role_audit()`)
	audit, outbox := roleEffects(t, pool)
	_, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), roleInput("dm", domain.ConversationAdmin))
	if err == nil {
		t.Fatal("injected failure succeeded")
	}
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipB, "member")
	nextAudit, nextOutbox := roleEffects(t, pool)
	if audit != nextAudit || outbox != nextOutbox {
		t.Fatal("failed transaction retained effects")
	}
}

func TestOwnershipConversationRoleRacesPostgreSQL(t *testing.T) {
	for _, scenario := range []string{"mutual-demotion", "promote-remove", "same-target-promotion"} {
		t.Run(scenario, func(t *testing.T) {
			pool := ownershipPool(t)
			enableOwnership(t, pool)
			first := roleInput("dm", domain.ConversationOwner)
			second := first
			switch scenario {
			case "mutual-demotion":
				ownershipExec(t, pool, `SELECT chat.assign_ownership('dm',$1,$2,'owner',$3,'manual')`, ownershipDM, ownershipB, ownershipA)
				first.Role = domain.ConversationMember
				second.Scope.ActorID = ownershipB
				second.TargetUserID = ownershipA
				second.Role = domain.ConversationMember
			case "promote-remove":
				second.Operation = "remove"
			}
			store := storage.NewPGXOwnershipStore(pool)
			results := make(chan error, 2)
			start := make(chan struct{})
			for _, input := range []storage.OwnershipMutation{first, second} {
				go func() { <-start; _, err := store.Mutate(t.Context(), input); results <- err }()
			}
			close(start)
			successes := 0
			for range 2 {
				err := <-results
				if err == nil {
					successes++
				} else if !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrNotFound) {
					t.Fatal(err)
				}
			}
			assertNoOrphans(t, pool)
			if scenario == "mutual-demotion" && successes != 1 {
				t.Fatalf("successful demotions=%d", successes)
			}
			if scenario == "same-target-promotion" {
				if successes != 2 {
					t.Fatalf("promotions=%d", successes)
				}
				var count int
				if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_audit WHERE target_user_id=$1 AND reason='manual'`, ownershipB).Scan(&count); err != nil || count != 1 {
					t.Fatalf("audit=%d err=%v", count, err)
				}
			}
			if scenario == "promote-remove" {
				var status string
				if err := pool.QueryRow(t.Context(), `SELECT status FROM chat.dm_members WHERE user_id=$1`, ownershipB).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status == "left" {
					_, err := store.Mutate(t.Context(), first)
					if !errors.Is(err, domain.ErrNotFound) {
						t.Fatalf("removed membership recreated: %v", err)
					}
				} else {
					assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipB, "owner")
				}
			}
		})
	}
}
