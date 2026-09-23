package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #807, against a real database: the deadline that ends every pending
// target, the per-target read the hydration makes, the fan-out index, and the
// workspace-scoped preview queue with its claim, its terminal outcomes, its
// revocation and the authorised image read. A fake store proves none of these
// — each is a claim about a statement.

type linkTargetFixture struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	store *storage.PGXMessageStore

	workspace, channel, member, outsider string
}

const (
	targetSeedWorkspace = "00000000-0000-0000-0000-000000000001"
	targetURLA          = "https://targets.example/a"
	targetURLB          = "https://targets.example/b"
	targetFingerprint   = "fp-807"
)

func newLinkTargetFixture(t *testing.T) linkTargetFixture {
	t.Helper()
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	f := linkTargetFixture{
		ctx: ctx, pool: pool, store: storage.NewPGXMessageStore(pool),
		workspace: targetSeedWorkspace,
		channel:   "e8070000-0000-4000-8000-000000000002",
		member:    "e8070000-0000-4000-8000-000000000003",
		outsider:  "e8070000-0000-4000-8000-000000000004",
	}
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO auth.users (id, email, display_name)
		  VALUES ($1, 'rf807-member@e.test', 'Member'), ($2, 'rf807-outsider@e.test', 'Outsider')
		  ON CONFLICT (id) DO NOTHING`, []any{f.member, f.outsider}},
		{`INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  VALUES ($1, $2, 'active') ON CONFLICT DO NOTHING`, []any{f.workspace, f.member}},
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		  VALUES ($2, $1, 'rf807-targets', 'RF807 targets', 'public', 'active')
		  ON CONFLICT (id) DO NOTHING`, []any{f.workspace, f.channel}},
		{`INSERT INTO chat.channel_members (channel_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			[]any{f.channel, f.member}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM chat.messages WHERE sender_id = $1`, f.member)
		_, _ = pool.Exec(background, `DELETE FROM chat.link_scans WHERE canonical_url LIKE 'https://targets.example/%'`)
	})
	return f
}

func (f linkTargetFixture) reset(t *testing.T) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `DELETE FROM chat.messages WHERE sender_id = $1`, f.member); err != nil {
		t.Fatalf("reset messages: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `DELETE FROM chat.link_scans WHERE canonical_url LIKE 'https://targets.example/%'`); err != nil {
		t.Fatalf("reset targets: %v", err)
	}
}

// target seeds one link_scans row in the given status, decided now when
// terminal, and with deadlineAgo shifting the deadline into the past.
func (f linkTargetFixture) target(t *testing.T, url, status string, deadlineAgo time.Duration) {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, `
		INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at, deadline_at)
		VALUES ($1, $2, CASE WHEN $2 = 'pending' THEN NULL ELSE 'scan-807' END,
		        CASE WHEN $2 = 'pending' THEN NULL ELSE now() END,
		        now() - ($3 * interval '1 second'))`, url, status, deadlineAgo.Seconds())
	if err != nil {
		t.Fatalf("seed target %s: %v", status, err)
	}
}

// message seeds one published message naming the URLs, with the association
// bound to its fingerprint.
func (f linkTargetFixture) message(t *testing.T, body string, urls ...string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO chat.messages (id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
		                           status, link_safety_state, link_safety_fingerprint, link_safety_projection_version)
		VALUES ($1, $2, $3, $4, 'user', $5, 'v2', 'active', '', $6, 1)`,
		id, f.workspace, f.channel, f.member, body, targetFingerprint); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	for _, url := range urls {
		if _, err := f.pool.Exec(f.ctx, `
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint) VALUES ($1, $2, $3)`,
			id, url, targetFingerprint); err != nil {
			t.Fatalf("seed association: %v", err)
		}
	}
	return id
}

func TestLinkTargetConvergencePostgreSQL(t *testing.T) {
	f := newLinkTargetFixture(t)

	t.Run("admission gives every new target a deadline", func(t *testing.T) {
		f.reset(t)
		admission, err := f.store.AdmitLinkScans(f.ctx, f.workspace, []string{targetURLA}, storage.LinkScanCapacity{})
		if err != nil || !admission.Allowed() {
			t.Fatalf("AdmitLinkScans: %+v %v", admission, err)
		}
		var hasDeadline bool
		if err := f.pool.QueryRow(f.ctx, `SELECT deadline_at IS NOT NULL AND deadline_at > now()
			FROM chat.link_scans WHERE canonical_url = $1`, targetURLA).Scan(&hasDeadline); err != nil {
			t.Fatalf("read deadline: %v", err)
		}
		if !hasDeadline {
			t.Fatal("a pending target was created without a future deadline")
		}
	})

	t.Run("a pending target past its deadline becomes unknown and only then", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "pending", time.Minute)
		f.target(t, targetURLB, "pending", -time.Hour)

		urls, err := f.store.TerminalizeExpiredLinkScans(f.ctx)
		if err != nil {
			t.Fatalf("TerminalizeExpiredLinkScans: %v", err)
		}
		if len(urls) != 1 || urls[0] != targetURLA {
			t.Fatalf("terminalised %v, want only the expired target", urls)
		}
		targets, err := f.store.LoadLinkTargets(f.ctx, []string{targetURLA, targetURLB})
		if err != nil {
			t.Fatalf("LoadLinkTargets: %v", err)
		}
		if targets[targetURLA].Status != "unknown" || !targets[targetURLA].Fresh {
			t.Fatalf("expired target: %+v", targets[targetURLA])
		}
		if targets[targetURLB].Status != "pending" {
			t.Fatalf("a target inside its deadline moved: %+v", targets[targetURLB])
		}
		// Idempotent: nothing left to terminalise.
		if again, err := f.store.TerminalizeExpiredLinkScans(f.ctx); err != nil || len(again) != 0 {
			t.Fatalf("second sweep: %v %v", again, err)
		}
		var reason string
		if err := f.pool.QueryRow(f.ctx, `SELECT terminal_reason FROM chat.link_scans WHERE canonical_url = $1`,
			targetURLA).Scan(&reason); err != nil || reason != storage.TerminalReasonDeadline {
			t.Fatalf("terminal reason = %q (%v)", reason, err)
		}
	})

	t.Run("unknown is decided for the send path and never a clearance", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "unknown", 0)
		verdicts, err := f.store.LoadLinkVerdicts(f.ctx, []string{targetURLA})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		if verdicts[targetURLA] != urlsafety.VerdictInconclusive {
			t.Fatalf("unknown loaded as %q", verdicts[targetURLA])
		}
		// A fresh unknown costs nothing at admission and is not reopened.
		admission, err := f.store.AdmitLinkScans(f.ctx, f.workspace, []string{targetURLA}, storage.LinkScanCapacity{})
		if err != nil || !admission.Allowed() {
			t.Fatalf("AdmitLinkScans: %+v %v", admission, err)
		}
		targets, _ := f.store.LoadLinkTargets(f.ctx, []string{targetURLA})
		if targets[targetURLA].Status != "unknown" {
			t.Fatalf("a fresh unknown was reopened by admission: %+v", targets[targetURLA])
		}
	})

	t.Run("an expired unknown is reopened by the next reference, with a new deadline", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "unknown", 0)
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_scans SET decided_at = now() - interval '1 hour'
			WHERE canonical_url = $1`, targetURLA); err != nil {
			t.Fatalf("age target: %v", err)
		}
		if err := f.store.EnsureLinkScans(f.ctx, []string{targetURLA}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}
		var status, reason string
		var deadlineAhead bool
		if err := f.pool.QueryRow(f.ctx, `SELECT status, COALESCE(terminal_reason, ''), deadline_at > now()
			FROM chat.link_scans WHERE canonical_url = $1`, targetURLA).Scan(&status, &reason, &deadlineAhead); err != nil {
			t.Fatalf("read target: %v", err)
		}
		if status != "pending" || reason != "" || !deadlineAhead {
			t.Fatalf("reopened target: status=%q reason=%q deadlineAhead=%v", status, reason, deadlineAhead)
		}
	})

	t.Run("a policy terminal is a compare-and-set on pending", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "pending", -time.Hour)
		if err := f.store.RecordLinkTargetTerminal(f.ctx, targetURLA, storage.TerminalReasonSensitive); err != nil {
			t.Fatalf("RecordLinkTargetTerminal: %v", err)
		}
		if err := f.store.RecordLinkTargetTerminal(f.ctx, targetURLA, storage.TerminalReasonInternal); !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("a second terminal on a decided row must lose, got %v", err)
		}
	})

	t.Run("disabled safety converges every pending target at once", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "pending", -time.Hour)
		f.target(t, targetURLB, "pending", -time.Hour)
		urls, err := f.store.TerminalizePendingLinkScansDisabled(f.ctx)
		if err != nil || len(urls) != 2 {
			t.Fatalf("TerminalizePendingLinkScansDisabled: %v %v", urls, err)
		}
	})

	t.Run("a fan-out continuation is durable, leased, restarted by a newer announcement and bounded in retries", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "safe", 0)
		begun, err := f.store.BeginLinkFanout(f.ctx, targetURLA, "", storage.LinkFanoutKindTarget)
		if err != nil || begun.ID == "" || begun.ClaimID == "" {
			t.Fatalf("BeginLinkFanout: %+v %v, want a leased row with a claim id", begun, err)
		}
		// Leased by the pass that began it: nobody else claims it yet.
		if due, err := f.store.ClaimDueLinkFanouts(f.ctx, 10); err != nil || len(due) != 0 {
			t.Fatalf("a freshly begun fan-out was claimed: %+v %v", due, err)
		}
		cursor := uuid.NewString()
		if err := f.store.AdvanceLinkFanout(f.ctx, begun, cursor); err != nil {
			t.Fatalf("AdvanceLinkFanout: %v", err)
		}
		// Case C: the cursor survives the release, and the next claim resumes from it.
		due, err := f.store.ClaimDueLinkFanouts(f.ctx, 10)
		if err != nil || len(due) != 1 || due[0].ID != begun.ID || due[0].AfterMessageID != cursor || due[0].Attempts != 1 || due[0].ClaimID == begun.ClaimID {
			t.Fatalf("claim after advance = %+v %v, want the cursor under a new claim", due, err)
		}
		// Case D: one workspace's preview announcement is a different continuation.
		preview, err := f.store.BeginLinkFanout(f.ctx, targetURLA, f.workspace, storage.LinkFanoutKindPreview)
		if err != nil || preview.ID == begun.ID || preview.WorkspaceID != f.workspace {
			t.Fatalf("preview fan-out = %+v %v", preview, err)
		}
		// A newer announcement of the same key restarts the cursor.
		again, err := f.store.BeginLinkFanout(f.ctx, targetURLA, "", storage.LinkFanoutKindTarget)
		if err != nil || again.ID != begun.ID {
			t.Fatalf("restart = %+v %v, want the same row", again, err)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_fanouts SET lease_until = NULL`); err != nil {
			t.Fatalf("release: %v", err)
		}
		due, err = f.store.ClaimDueLinkFanouts(f.ctx, 10)
		if err != nil || len(due) != 2 {
			t.Fatalf("claims = %+v %v", due, err)
		}
		var previewClaim storage.LinkFanout
		for _, fanout := range due {
			if fanout.ID == begun.ID && (fanout.AfterMessageID != "" || fanout.Attempts != 1) {
				t.Fatalf("restarted fan-out = %+v, want cursor reset", fanout)
			}
			if fanout.ID == preview.ID {
				previewClaim = fanout
			}
		}
		// Case D: finishing the preview continuation leaves the target one alone.
		if err := f.store.FinishLinkFanout(f.ctx, previewClaim); err != nil {
			t.Fatalf("FinishLinkFanout: %v", err)
		}
		// Past the attempt ceiling with no progress, the continuation is dropped.
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_fanouts SET lease_until = NULL, attempts = 10`); err != nil {
			t.Fatalf("exhaust: %v", err)
		}
		if due, err := f.store.ClaimDueLinkFanouts(f.ctx, 10); err != nil || len(due) != 0 {
			t.Fatalf("an exhausted fan-out was claimed: %+v %v", due, err)
		}
		var remaining int
		if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM chat.link_fanouts`).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("fan-outs remaining = %d %v", remaining, err)
		}
	})

	t.Run("a fan-out claim is owned: a restart or a reclaim refuses the stale claimant", func(t *testing.T) {
		// Case A: a newer announcement restarts the row while A still holds it.
		// Case B: A's lease lapses and B reclaims. Either way A can neither
		// advance nor finish, and the current claimant can do both.
		for name, supersede := range map[string]func(t *testing.T, held storage.LinkFanout) storage.LinkFanout{
			"restart": func(t *testing.T, held storage.LinkFanout) storage.LinkFanout {
				t.Helper()
				again, err := f.store.BeginLinkFanout(f.ctx, targetURLA, "", storage.LinkFanoutKindTarget)
				if err != nil || again.ID != held.ID || again.ClaimID == held.ClaimID {
					t.Fatalf("restart = %+v %v, want the same row under a new claim", again, err)
				}
				return again
			},
			"reclaim": func(t *testing.T, held storage.LinkFanout) storage.LinkFanout {
				t.Helper()
				if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_fanouts SET lease_until = now() - interval '1 second' WHERE id = $1::uuid`, held.ID); err != nil {
					t.Fatalf("lapse: %v", err)
				}
				due, err := f.store.ClaimDueLinkFanouts(f.ctx, 10)
				if err != nil || len(due) != 1 || due[0].ID != held.ID || due[0].ClaimID == held.ClaimID {
					t.Fatalf("reclaim = %+v %v, want the same row under a new claim", due, err)
				}
				return due[0]
			},
		} {
			t.Run(name, func(t *testing.T) {
				f.reset(t)
				f.target(t, targetURLA, "safe", 0)
				stale, err := f.store.BeginLinkFanout(f.ctx, targetURLA, "", storage.LinkFanoutKindTarget)
				if err != nil {
					t.Fatalf("BeginLinkFanout: %v", err)
				}
				current := supersede(t, stale)
				if err := f.store.AdvanceLinkFanout(f.ctx, stale, uuid.NewString()); !errors.Is(err, storage.ErrLinkFanoutConflict) {
					t.Fatalf("stale advance: %v, want conflict", err)
				}
				if err := f.store.FinishLinkFanout(f.ctx, stale); !errors.Is(err, storage.ErrLinkFanoutConflict) {
					t.Fatalf("stale finish: %v, want conflict", err)
				}
				var after *string
				var claimID string
				if err := f.pool.QueryRow(f.ctx, `SELECT after_message_id::text, claim_id::text FROM chat.link_fanouts WHERE id = $1::uuid`, stale.ID).Scan(&after, &claimID); err != nil {
					t.Fatalf("the current continuation is gone: %v", err)
				}
				if after != nil || claimID != current.ClaimID {
					t.Fatalf("stale claimant touched the row: after=%v claim=%s", after, claimID)
				}
				cursor := uuid.NewString()
				if err := f.store.AdvanceLinkFanout(f.ctx, current, cursor); err != nil {
					t.Fatalf("current advance: %v", err)
				}
				// Advancing releases the claim as well: the same claim id is spent.
				if err := f.store.FinishLinkFanout(f.ctx, current); !errors.Is(err, storage.ErrLinkFanoutConflict) {
					t.Fatalf("a released claim finished the row: %v", err)
				}
				next, err := f.store.ClaimDueLinkFanouts(f.ctx, 10)
				if err != nil || len(next) != 1 || next[0].AfterMessageID != cursor {
					t.Fatalf("next claim = %+v %v, want the advanced cursor", next, err)
				}
				if err := f.store.FinishLinkFanout(f.ctx, next[0]); err != nil {
					t.Fatalf("current finish: %v", err)
				}
			})
		}
	})

	t.Run("fan-out lists only messages whose current body names the url", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "safe", 0)
		f.target(t, targetURLB, "safe", 0)
		current := f.message(t, "veja "+targetURLA, targetURLA)
		f.message(t, "veja "+targetURLB, targetURLB)
		stale := f.message(t, "editado", targetURLA)
		// An association left behind by an old body: its fingerprint no longer
		// matches the message's.
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.messages SET link_safety_fingerprint = 'fp-newer' WHERE id = $1`, stale); err != nil {
			t.Fatalf("stale association: %v", err)
		}

		refs, err := f.store.MessagesReferencingLink(f.ctx, targetURLA, "", "", 10)
		if err != nil {
			t.Fatalf("MessagesReferencingLink: %v", err)
		}
		if len(refs) != 1 || refs[0].MessageID != current || refs[0].TargetType != storage.TargetChannel || refs[0].TargetID != f.channel {
			t.Fatalf("references = %+v, want only the current message", refs)
		}
		if refs, err := f.store.MessagesReferencingLink(f.ctx, targetURLA, uuid.NewString(), "", 10); err != nil || len(refs) != 0 {
			t.Fatalf("another workspace saw the reference: %+v %v", refs, err)
		}
		workspaces, err := f.store.LinkWorkspacesReferencing(f.ctx, targetURLA)
		if err != nil || len(workspaces) != 1 || workspaces[0] != f.workspace {
			t.Fatalf("workspaces = %v (%v)", workspaces, err)
		}
		bodies, err := f.store.LoadMessageBodies(f.ctx, []string{current})
		if err != nil || bodies[current] != "veja "+targetURLA {
			t.Fatalf("bodies = %v (%v)", bodies, err)
		}
	})
}

func TestLinkPreviewQueuePostgreSQL(t *testing.T) {
	f := newLinkTargetFixture(t)

	queue := func(t *testing.T, url string) {
		t.Helper()
		if err := f.store.QueueLinkPreviews(f.ctx, f.workspace, []string{url}); err != nil {
			t.Fatalf("QueueLinkPreviews: %v", err)
		}
	}

	t.Run("only a safe target is claimed, and the claim leases it", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "safe", 0)
		f.target(t, targetURLB, "unknown", 0)
		queue(t, targetURLA)
		queue(t, targetURLB)

		jobs, err := f.store.ClaimDueLinkPreviews(f.ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueLinkPreviews: %v", err)
		}
		if len(jobs) != 1 || jobs[0].CanonicalURL != targetURLA || jobs[0].WorkspaceID != f.workspace || jobs[0].Attempts != 1 {
			t.Fatalf("jobs = %+v, want the safe target once", jobs)
		}
		if again, err := f.store.ClaimDueLinkPreviews(f.ctx, 10); err != nil || len(again) != 0 {
			t.Fatalf("a leased row was claimed again: %+v %v", again, err)
		}
	})

	t.Run("complete stores the card and serves the image to a member only", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "safe", 0)
		queue(t, targetURLA)
		jobs, _ := f.store.ClaimDueLinkPreviews(f.ctx, 1)
		row, err := f.store.CompleteLinkPreview(f.ctx, jobs[0], storage.LinkPreviewResult{
			SiteName: "Targets", Title: "A page", Description: "About a",
			ImageData: []byte{0xff, 0xd8, 0xff, 0xd9}, ImageContentType: "image/jpeg", ImageWidth: 2, ImageHeight: 2,
		})
		if err != nil {
			t.Fatalf("CompleteLinkPreview: %v", err)
		}
		if row.State != "ready" || row.Title != "A page" || !row.HasImage || row.ImageWidth != 2 {
			t.Fatalf("row = %+v", row)
		}
		previews, err := f.store.LoadLinkPreviews(f.ctx, f.workspace, []string{targetURLA})
		if err != nil || previews[targetURLA].ID != row.ID {
			t.Fatalf("LoadLinkPreviews: %v %v", previews, err)
		}
		if other, err := f.store.LoadLinkPreviews(f.ctx, uuid.NewString(), []string{targetURLA}); err != nil || len(other) != 0 {
			t.Fatalf("another workspace read the preview: %v %v", other, err)
		}
		image, err := f.store.LinkPreviewImage(f.ctx, f.workspace, f.member, row.ID)
		if err != nil || image.ContentType != "image/jpeg" || len(image.Data) != 4 {
			t.Fatalf("LinkPreviewImage: %+v %v", image, err)
		}
		if _, err := f.store.LinkPreviewImage(f.ctx, f.workspace, f.outsider, row.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("a non-member read the image: %v", err)
		}
		// A completed row cannot be completed again: the claim is gone.
		if _, err := f.store.CompleteLinkPreview(f.ctx, jobs[0], storage.LinkPreviewResult{}); !errors.Is(err, storage.ErrLinkPreviewConflict) {
			t.Fatalf("second completion: %v", err)
		}
		// Queueing again leaves a fresh ready row alone.
		queue(t, targetURLA)
		previews, _ = f.store.LoadLinkPreviews(f.ctx, f.workspace, []string{targetURLA})
		if previews[targetURLA].State != "ready" {
			t.Fatalf("a fresh ready preview was requeued: %+v", previews[targetURLA])
		}
	})

	t.Run("a condemned target loses its preview and its image", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "safe", 0)
		queue(t, targetURLA)
		jobs, _ := f.store.ClaimDueLinkPreviews(f.ctx, 1)
		row, _ := f.store.CompleteLinkPreview(f.ctx, jobs[0], storage.LinkPreviewResult{
			Title: "A page", ImageData: []byte{1, 2}, ImageContentType: "image/jpeg", ImageWidth: 1, ImageHeight: 1,
		})
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_scans SET status = 'malicious' WHERE canonical_url = $1`, targetURLA); err != nil {
			t.Fatalf("condemn: %v", err)
		}
		// The image read re-checks the target before the sweep reaches the row.
		if _, err := f.store.LinkPreviewImage(f.ctx, f.workspace, f.member, row.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("an image of a condemned target was served: %v", err)
		}
		revoked, err := f.store.RevokeLinkPreviews(f.ctx, targetURLA)
		if err != nil || len(revoked) != 1 || revoked[0].State != "failed" || revoked[0].HasImage {
			t.Fatalf("RevokeLinkPreviews: %+v %v", revoked, err)
		}
	})

	t.Run("failures retry until the ceiling, terminal reasons end at once", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "safe", 0)
		queue(t, targetURLA)
		jobs, _ := f.store.ClaimDueLinkPreviews(f.ctx, 1)
		row, err := f.store.FailLinkPreview(f.ctx, jobs[0], storage.PreviewFailureTimeout, false)
		if err != nil || row.State != "queued" {
			t.Fatalf("first transient failure: %+v %v", row, err)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_previews SET attempts = 2, lease_until = NULL, next_attempt_at = NULL WHERE id = $1::uuid`, row.ID); err != nil {
			t.Fatalf("stage ceiling: %v", err)
		}
		jobs, _ = f.store.ClaimDueLinkPreviews(f.ctx, 1) // the third and last attempt
		row, err = f.store.FailLinkPreview(f.ctx, jobs[0], storage.PreviewFailureTimeout, false)
		if err != nil || row.State != "failed" {
			t.Fatalf("failure at the ceiling: %+v %v", row, err)
		}

		queue(t, targetURLA) // a failed row is requeued
		jobs, _ = f.store.ClaimDueLinkPreviews(f.ctx, 1)
		row, err = f.store.FailLinkPreview(f.ctx, jobs[0], storage.PreviewFailureNoMetadata, true)
		if err != nil || row.State != "unsupported" {
			t.Fatalf("no metadata: %+v %v", row, err)
		}
	})

	t.Run("a claim is owned: a reclaimed lease refuses the stale worker's outcome", func(t *testing.T) {
		// Worker A claims, its lease expires, worker B reclaims. A's late
		// completion and A's late failure must both be refused as stale and
		// leave B's claim untouched; B's own outcome then lands.
		for name, settle := range map[string]func(claim storage.LinkPreviewJob) (storage.LinkPreviewRow, error){
			"complete": func(claim storage.LinkPreviewJob) (storage.LinkPreviewRow, error) {
				return f.store.CompleteLinkPreview(f.ctx, claim, storage.LinkPreviewResult{Title: "late"})
			},
			"fail": func(claim storage.LinkPreviewJob) (storage.LinkPreviewRow, error) {
				return f.store.FailLinkPreview(f.ctx, claim, storage.PreviewFailureTimeout, false)
			},
		} {
			t.Run(name, func(t *testing.T) {
				f.reset(t)
				f.target(t, targetURLA, "safe", 0)
				queue(t, targetURLA)
				first, _ := f.store.ClaimDueLinkPreviews(f.ctx, 1)
				if len(first) != 1 || first[0].ClaimID == "" {
					t.Fatalf("first claim = %+v, want a claim id", first)
				}
				if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_previews SET lease_until = now() - interval '1 second', next_attempt_at = now() - interval '1 second'
					WHERE id = $1::uuid`, first[0].ID); err != nil {
					t.Fatalf("expire lease: %v", err)
				}
				second, _ := f.store.ClaimDueLinkPreviews(f.ctx, 1)
				if len(second) != 1 || second[0].ID != first[0].ID || second[0].ClaimID == first[0].ClaimID {
					t.Fatalf("reclaim = %+v, want the same row under a new claim id", second)
				}
				if _, err := settle(first[0]); !errors.Is(err, storage.ErrLinkPreviewConflict) {
					t.Fatalf("the stale claim settled the row: %v", err)
				}
				var state string
				if err := f.pool.QueryRow(f.ctx, `SELECT state FROM chat.link_previews WHERE id = $1::uuid`, first[0].ID).Scan(&state); err != nil || state != "fetching" {
					t.Fatalf("state after the stale outcome = %q %v, want fetching", state, err)
				}
				row, err := f.store.CompleteLinkPreview(f.ctx, second[0], storage.LinkPreviewResult{Title: "current"})
				if err != nil || row.State != "ready" || row.Title != "current" {
					t.Fatalf("the current claim could not settle: %+v %v", row, err)
				}
				// Idempotency: neither claim can settle it again.
				if _, err := f.store.CompleteLinkPreview(f.ctx, second[0], storage.LinkPreviewResult{}); !errors.Is(err, storage.ErrLinkPreviewConflict) {
					t.Fatalf("a settled claim was settled twice: %v", err)
				}
			})
		}
	})

	t.Run("only a fresh safe verdict is claimed or served", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "safe", 0)
		queue(t, targetURLA)
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_scans SET decided_at = now() - ($2 * interval '1 second') - interval '1 minute'
			WHERE canonical_url = $1`, targetURLA, urlsafety.VerdictTTL.Seconds()); err != nil {
			t.Fatalf("age the clearance: %v", err)
		}
		if jobs, err := f.store.ClaimDueLinkPreviews(f.ctx, 10); err != nil || len(jobs) != 0 {
			t.Fatalf("an expired clearance was claimed: %+v %v", jobs, err)
		}
		// The clearance being rechecked (back to pending) is not a clearance either.
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_scans SET status = 'pending', decided_at = NULL WHERE canonical_url = $1`, targetURLA); err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if jobs, err := f.store.ClaimDueLinkPreviews(f.ctx, 10); err != nil || len(jobs) != 0 {
			t.Fatalf("a rechecked target was claimed: %+v %v", jobs, err)
		}
		// A new safe verdict admits the claim; the stored image is then served
		// only while that verdict stays fresh.
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_scans SET status = 'safe', decided_at = now() WHERE canonical_url = $1`, targetURLA); err != nil {
			t.Fatalf("clear: %v", err)
		}
		jobs, err := f.store.ClaimDueLinkPreviews(f.ctx, 10)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("a fresh clearance was not claimed: %+v %v", jobs, err)
		}
		row, err := f.store.CompleteLinkPreview(f.ctx, jobs[0], storage.LinkPreviewResult{
			Title: "A page", ImageData: []byte{1, 2}, ImageContentType: "image/jpeg", ImageWidth: 1, ImageHeight: 1,
		})
		if err != nil {
			t.Fatalf("CompleteLinkPreview: %v", err)
		}
		if _, err := f.store.LinkPreviewImage(f.ctx, f.workspace, f.member, row.ID); err != nil {
			t.Fatalf("image under a fresh clearance: %v", err)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_scans SET decided_at = now() - ($2 * interval '1 second') - interval '1 minute'
			WHERE canonical_url = $1`, targetURLA, urlsafety.VerdictTTL.Seconds()); err != nil {
			t.Fatalf("age the clearance: %v", err)
		}
		if _, err := f.store.LinkPreviewImage(f.ctx, f.workspace, f.member, row.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("an image was served on an expired clearance: %v", err)
		}
	})

	t.Run("the sweep ends a preview past its deadline and the drain ends every open one", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "safe", 0)
		f.target(t, targetURLB, "safe", 0)
		queue(t, targetURLA)
		queue(t, targetURLB)
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_previews SET deadline_at = now() - interval '1 minute'
			WHERE canonical_url = $1`, targetURLA); err != nil {
			t.Fatalf("expire: %v", err)
		}
		expired, err := f.store.TerminalizeExpiredLinkPreviews(f.ctx)
		if err != nil || len(expired) != 1 || expired[0].CanonicalURL != targetURLA || expired[0].State != "failed" {
			t.Fatalf("TerminalizeExpiredLinkPreviews: %+v %v", expired, err)
		}
		drained, err := f.store.DrainLinkPreviewsDisabled(f.ctx)
		if err != nil || len(drained) != 1 || drained[0].CanonicalURL != targetURLB {
			t.Fatalf("DrainLinkPreviewsDisabled: %+v %v", drained, err)
		}
		if pending, err := f.store.LinkPreviewBacklog(f.ctx); err != nil || pending != 0 {
			t.Fatalf("backlog = %d (%v)", pending, err)
		}
	})
}
