package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type ordinaryVerdictProvider struct {
	scanUUID string
	verdict  urlsafety.Verdict
}

func (p *ordinaryVerdictProvider) Submit(context.Context, string) (string, error) {
	return p.scanUUID, nil
}

func (p *ordinaryVerdictProvider) Poll(context.Context, string, string) (urlsafety.Verdict, error) {
	return p.verdict, nil
}

// The RF-21 worker lifecycle, against a real database.
//
// Everything here is a claim about SQL: a lease that another worker cannot take,
// a compare-and-set that a late answer loses, a promotion and its outbox event
// written in one statement, an expired verdict that stops being served. A fake
// store proves none of it — it would agree with whatever the Go around the query
// said, and the query is the thing under test.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations.
func TestLinkScanWorkerLifecyclePostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXMessageStore(pool)

	const (
		// The workspace the initial migration seeds, reused for the reason the
		// neighbouring tests reuse it: creating one carries invariants that would
		// be fixture noise here.
		workspace = "00000000-0000-0000-0000-000000000001"
		channel   = "e3000000-0000-4000-8000-000000000002"
		author    = "e3000000-0000-4000-8000-000000000003"
		mentioned = "e3000000-0000-4000-8000-000000000004"

		cleared = "e3000000-0000-4000-8000-00000000000a"
		refused = "e3000000-0000-4000-8000-00000000000b"

		goodURL = "https://worker.example/ok"
		badURL  = "https://worker.example/payload"

		// The binding between a verdict and the content it judged, stamped by the
		// service. The promotion refuses to publish on a mismatch, so every
		// association here carries the value its message carries.
		fingerprint = "fp-worker"
	)

	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.users (id, email, display_name)
		VALUES ($1, 'rf21-worker-author@e.test', 'Author'),
		       ($2, 'rf21-worker-mentioned@e.test', 'Mentioned')
		ON CONFLICT (id) DO NOTHING`, author, mentioned); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  VALUES ($1, $2, 'active'), ($1, $3, 'active') ON CONFLICT DO NOTHING`,
			[]any{workspace, author, mentioned}},
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		  VALUES ($2, $1, 'rf21-worker', 'RF21 worker', 'public', 'active')
		  ON CONFLICT (id) DO NOTHING`, []any{workspace, channel}},
		// A mention is only valid for somebody who is in the channel, so the
		// membership is part of the fixture rather than an assumption.
		{`INSERT INTO chat.channel_members (channel_id, user_id)
		  VALUES ($1, $2), ($1, $3) ON CONFLICT DO NOTHING`,
			[]any{channel, author, mentioned}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM chat.messages WHERE sender_id = $1`, author)
		_, _ = pool.Exec(background,
			`DELETE FROM chat.link_scans WHERE canonical_url = ANY($1::text[])`,
			[]string{goodURL, badURL})
		_, _ = pool.Exec(background,
			`DELETE FROM chat.link_scan_budget_usage WHERE scope_type = 'provider_submit'`)
	})

	// Every subtest states its own starting queue. The messages are left to the
	// cleanup above because several subtests build on the ones before them.
	resetQueue := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`DELETE FROM chat.link_scans WHERE canonical_url = ANY($1::text[])`,
			[]string{goodURL, badURL}); err != nil {
			t.Fatalf("reset queue: %v", err)
		}
	}

	// due makes a claimed row claimable again without waiting out its lease.
	due := func(t *testing.T, url string) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`UPDATE chat.link_scans SET next_attempt_at = NULL WHERE canonical_url = $1`,
			url); err != nil {
			t.Fatalf("make due: %v", err)
		}
	}

	claimOne := func(t *testing.T, url string) storage.LinkScanJob {
		t.Helper()
		jobs, err := store.ClaimDueLinkScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueLinkScans: %v", err)
		}
		for _, job := range jobs {
			if job.CanonicalURL == url {
				return job
			}
		}
		t.Fatalf("%s was not claimed; got %+v", url, jobs)
		return storage.LinkScanJob{}
	}

	// [§28] Submit-then-poll, end to end: the states a row moves through are the
	// states the worker branches on, and each one is read back from the database
	// rather than assumed.
	t.Run("a queued url is claimed, submitted and decided", func(t *testing.T) {
		resetQueue(t)
		if err := store.EnsureLinkScans(ctx, []string{goodURL, goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}

		byState, oldest, err := store.LinkScanBacklog(ctx)
		if err != nil {
			t.Fatalf("LinkScanBacklog: %v", err)
		}
		if byState[storage.SubmitPending] != 1 {
			t.Fatalf("backlog = %v, want one url awaiting submission", byState)
		}
		if oldest < 0 {
			t.Fatalf("oldest = %v, want a non-negative age", oldest)
		}

		job := claimOne(t, goodURL)
		if job.SubmitState() != storage.SubmitPending {
			t.Fatalf("state = %q, want %q", job.SubmitState(), storage.SubmitPending)
		}
		if job.Attempts != 1 {
			t.Fatalf("attempts = %d, want the claim to have counted itself", job.Attempts)
		}

		// The intent is recorded before the provider is called: this is the write
		// that makes a crash in the gap recoverable instead of resubmittable.
		generation, err := store.BeginLinkScanSubmit(ctx, goodURL, job.SubmitGeneration)
		if err != nil {
			t.Fatalf("BeginLinkScanSubmit: %v", err)
		}
		if generation != job.SubmitGeneration+1 {
			t.Fatalf("generation = %d, want %d", generation, job.SubmitGeneration+1)
		}

		byState, _, err = store.LinkScanBacklog(ctx)
		if err != nil {
			t.Fatalf("LinkScanBacklog: %v", err)
		}
		if byState[storage.SubmitUncertain] != 1 {
			t.Fatalf("backlog = %v, want the outstanding attempt reported on its own", byState)
		}

		if err := store.RecordLinkScanSubmission(ctx, goodURL, "scan-good", generation); err != nil {
			t.Fatalf("RecordLinkScanSubmission: %v", err)
		}

		byState, _, err = store.LinkScanBacklog(ctx)
		if err != nil {
			t.Fatalf("LinkScanBacklog: %v", err)
		}
		if byState[storage.SubmitPolling] != 1 || byState[storage.SubmitUncertain] != 0 {
			t.Fatalf("backlog = %v, want the confirmed id to leave the uncertain state", byState)
		}

		// Still pending, so still no verdict to serve. A URL under scan must not
		// read as cleared.
		verdicts, err := store.LoadLinkVerdicts(ctx, []string{goodURL})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		if len(verdicts) != 0 {
			t.Fatalf("a scan still running produced %v", verdicts)
		}

		if err := store.RecordLinkVerdict(ctx, goodURL, "scan-good", urlsafety.VerdictSafe); err != nil {
			t.Fatalf("RecordLinkVerdict: %v", err)
		}
		verdicts, err = store.LoadLinkVerdicts(ctx, []string{goodURL})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		if verdicts[goodURL] != urlsafety.VerdictSafe {
			t.Fatalf("verdict = %q, want %q", verdicts[goodURL], urlsafety.VerdictSafe)
		}

		// A decided row is claimed by nobody, which is what stops a cleared URL
		// from being re-scanned on every pass.
		due(t, goodURL)
		jobs, err := store.ClaimDueLinkScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueLinkScans: %v", err)
		}
		for _, claimed := range jobs {
			if claimed.CanonicalURL == goodURL {
				t.Fatal("a decided url was claimed again")
			}
		}
	})

	t.Run("an ordinary malicious poll converges an older active clearance", func(t *testing.T) {
		const (
			activeID  = "e3000000-0000-4000-8000-00000000001a"
			pendingID = "e3000000-0000-4000-8000-00000000001b"
		)
		resetQueue(t)
		if err := store.EnsureLinkScans(ctx, []string{badURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE chat.link_scans
			SET status = 'safe', scan_uuid = 'expired-safe',
			    decided_at = now() - interval '1 hour', next_attempt_at = NULL
			WHERE canonical_url = $1`, badURL); err != nil {
			t.Fatalf("seed expired clearance: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.messages
				(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
				 status, link_safety_state, link_safety_fingerprint,
				 link_safety_projection_version)
			VALUES
				($1, $3, $4, $5, 'user', $6, 'v2', 'active', 'safe', $7, 1),
				($2, $3, $4, $5, 'user', $6, 'v2', 'pending_link_scan', '', $7, 1)`,
			activeID, pendingID, workspace, channel, author, "veja "+badURL, fingerprint); err != nil {
			t.Fatalf("seed messages: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			VALUES ($1, $3, $4), ($2, $3, $4)`,
			activeID, pendingID, badURL, fingerprint); err != nil {
			t.Fatalf("seed associations: %v", err)
		}

		provider := &ordinaryVerdictProvider{
			scanUUID: "ordinary-malicious", verdict: urlsafety.VerdictMalicious,
		}
		worker := service.NewLinkScanService(store, provider, nil, nil)
		if moved, err := worker.ProcessDue(ctx); err != nil || moved != 1 {
			t.Fatalf("submit pass: moved=%d err=%v", moved, err)
		}
		due(t, badURL)
		if moved, err := worker.ProcessDue(ctx); err != nil || moved != 1 {
			t.Fatalf("poll pass: moved=%d err=%v", moved, err)
		}

		active, err := store.GetMessageByIDInWorkspace(ctx, workspace, activeID, author)
		if err != nil {
			t.Fatalf("read active message: %v", err)
		}
		if active.LinkSafety != domain.MessageLinkSafetyMalicious || active.BodyText != "" {
			t.Fatalf("active message=%q body=%q, want malicious body withheld",
				active.LinkSafety, active.BodyText)
		}
		var pendingStatus string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.messages WHERE id = $1`, pendingID,
		).Scan(&pendingStatus); err != nil {
			t.Fatalf("read pending message: %v", err)
		}
		if pendingStatus != "deleted" {
			t.Fatalf("pending message status=%q, want deleted", pendingStatus)
		}
	})

	// The RF-21 incident this branch fixes: Cloudflare answers HTTP 200,
	// task.status=finished, task.success=false, hasVerdicts=false. That is
	// VerdictInconclusive from the provider layer, and this proves the row it
	// produces is genuinely terminal against the real claim, backlog, reopen and
	// re-request queries — not just what a Go fake believes about them.
	t.Run("an inconclusive verdict is terminal, excluded from claims and backlog, and never reopened", func(t *testing.T) {
		resetQueue(t)
		if err := store.EnsureLinkScans(ctx, []string{goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}
		job := claimOne(t, goodURL)
		generation, err := store.BeginLinkScanSubmit(ctx, goodURL, job.SubmitGeneration)
		if err != nil {
			t.Fatalf("BeginLinkScanSubmit: %v", err)
		}
		if err := store.RecordLinkScanSubmission(ctx, goodURL, "scan-inconclusive", generation); err != nil {
			t.Fatalf("RecordLinkScanSubmission: %v", err)
		}
		if err := store.RecordLinkVerdict(ctx, goodURL, "scan-inconclusive", urlsafety.VerdictInconclusive); err != nil {
			t.Fatalf("RecordLinkVerdict(inconclusive): %v", err)
		}
		var status string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.link_scans WHERE canonical_url = $1`, goodURL).Scan(&status); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status != "inconclusive" {
			t.Fatalf("status = %q, want inconclusive", status)
		}

		// Never a usable verdict.
		verdicts, err := store.LoadLinkVerdicts(ctx, []string{goodURL})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		if len(verdicts) != 0 {
			t.Fatalf("an inconclusive scan produced a loadable verdict: %v", verdicts)
		}

		// Never claimed again, even once the row would otherwise look due.
		due(t, goodURL)
		jobs, err := store.ClaimDueLinkScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueLinkScans: %v", err)
		}
		for _, claimed := range jobs {
			if claimed.CanonicalURL == goodURL {
				t.Fatal("an inconclusive url was claimed again")
			}
		}

		// Never counted as backlog.
		byState, _, err := store.LinkScanBacklog(ctx)
		if err != nil {
			t.Fatalf("LinkScanBacklog: %v", err)
		}
		if n, present := byState["inconclusive"]; present && n > 0 {
			t.Fatalf("backlog counted an inconclusive row: %v", byState)
		}

		// A worker restart re-runs EnsureLinkScans for the same URL — this is the
		// dedup/admission path a fresh message naming the URL would go through.
		// It must not resubmit or reopen the terminal row.
		if err := store.EnsureLinkScans(ctx, []string{goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans (re-request): %v", err)
		}
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.link_scans WHERE canonical_url = $1`, goodURL).Scan(&status); err != nil {
			t.Fatalf("read status after re-request: %v", err)
		}
		if status != "inconclusive" {
			t.Fatalf("status = %q, a re-request reopened the row", status)
		}
		jobs, err = store.ClaimDueLinkScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueLinkScans after re-request: %v", err)
		}
		for _, claimed := range jobs {
			if claimed.CanonicalURL == goodURL {
				t.Fatal("a re-request submitted a fresh claim for an inconclusive url")
			}
		}

		// ReopenExpiredVerdicts is scoped to safe/malicious; even with a message
		// depending on it, an inconclusive row must not be swept back to pending.
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.messages
				(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
				 status, link_safety_fingerprint)
			VALUES ($1, $2, $3, $4, 'user', 'inconclusive body', 'v2', 'pending_link_scan', $5)
			ON CONFLICT (id) DO NOTHING`,
			refused, workspace, channel, author, fingerprint); err != nil {
			t.Fatalf("seed withheld message: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			refused, goodURL, fingerprint); err != nil {
			t.Fatalf("seed association: %v", err)
		}
		reopened, err := store.ReopenExpiredVerdicts(ctx)
		if err != nil {
			t.Fatalf("ReopenExpiredVerdicts: %v", err)
		}
		if reopened != 0 {
			t.Fatalf("reopened %d rows, want an inconclusive row never reopened", reopened)
		}
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.link_scans WHERE canonical_url = $1`, goodURL).Scan(&status); err != nil {
			t.Fatalf("read status after reopen sweep: %v", err)
		}
		if status != "inconclusive" {
			t.Fatalf("status = %q, the reopen sweep touched an inconclusive row", status)
		}

		// The message waiting on it resolves, and since issue #135 it resolves by
		// being *published* rather than refused.
		//
		// That is the policy change this whole issue is about. The provider's real
		// answer for this case was an operational refusal — the hostname had been
		// scanned too recently — which is evidence of nothing about the link, so
		// blocking a legitimate message on it was a product bug. The message goes
		// out; what stays revoked is this server's permission to fetch the URL,
		// which is carried by link_safety_state and by nothing else.
		summary, err := store.ResolveDecidedMessages(ctx)
		if err != nil {
			t.Fatalf("ResolveDecidedMessages: %v", err)
		}
		if summary.Published != 1 || summary.Blocked != 0 || summary.PublishedInconclusive != 1 {
			t.Fatalf("summary = %+v, want the withheld message published with an inconclusive marker", summary)
		}
		var msgStatus, linkSafety string
		if err := pool.QueryRow(ctx,
			`SELECT status, link_safety_state FROM chat.messages WHERE id = $1`, refused,
		).Scan(&msgStatus, &linkSafety); err != nil {
			t.Fatalf("read message status: %v", err)
		}
		if msgStatus != "active" {
			t.Fatalf("message status = %q, want active", msgStatus)
		}
		if linkSafety != "inconclusive" {
			t.Fatalf("link_safety_state = %q, want inconclusive", linkSafety)
		}
		// The event reaches the conversation, exactly as any other message's does.
		// A sender-only message.blocked here would be the old behaviour, and would
		// leave every other member without a message that was in fact published.
		var eventType, targetType, blockReason, targetID string
		if err := pool.QueryRow(ctx, `
			SELECT event_type, target_type, target_id::text, COALESCE(block_reason, '')
			FROM chat.message_publish_outbox WHERE message_id = $1`, refused,
		).Scan(&eventType, &targetType, &targetID, &blockReason); err != nil {
			t.Fatalf("read publish event: %v", err)
		}
		if eventType != storage.EventMessageCreated || targetType != storage.TargetChannel {
			t.Fatalf("event = %s/%s, want a creation addressed to the channel", eventType, targetType)
		}
		if targetID != channel {
			t.Fatalf("target_id = %q, want the channel the message was sent to", targetID)
		}
		if blockReason != "" {
			t.Fatalf("block_reason = %q, want none on a publication", blockReason)
		}

		if _, err := pool.Exec(ctx, `DELETE FROM chat.messages WHERE id = $1`, refused); err != nil {
			t.Fatalf("clean withheld message: %v", err)
		}
	})

	// The dedup point: two messages naming the same URL share one scan row.
	// Settled inconclusive, both must resolve to blocked from that single scan —
	// nothing here ever calls the provider a second time for the same URL.
	t.Run("two messages sharing a URL settle from one inconclusive scan", func(t *testing.T) {
		const (
			first  = "e3000000-0000-4000-8000-00000000000e"
			second = "e3000000-0000-4000-8000-00000000000f"
		)
		resetQueue(t)
		if _, err := pool.Exec(ctx, `DELETE FROM chat.messages WHERE id = ANY($1::uuid[])`,
			[]string{first, second}); err != nil {
			t.Fatalf("clean messages: %v", err)
		}

		// Both admissions name the same URL — this is the dedup point.
		if err := store.EnsureLinkScans(ctx, []string{goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans (first): %v", err)
		}
		if err := store.EnsureLinkScans(ctx, []string{goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans (second): %v", err)
		}
		var rows int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.link_scans WHERE canonical_url = $1`, goodURL).Scan(&rows); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		if rows != 1 {
			t.Fatalf("rows = %d, want exactly one scan for two admissions of the same url", rows)
		}

		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.messages
				(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
				 status, link_safety_fingerprint)
			VALUES ($1, $3, $4, $5, 'user', 'first',  'v2', 'pending_link_scan', $6),
			       ($2, $3, $4, $5, 'user', 'second', 'v2', 'pending_link_scan', $6)`,
			first, second, workspace, channel, author, fingerprint); err != nil {
			t.Fatalf("seed messages: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			VALUES ($1, $3, $4), ($2, $3, $4)`,
			first, second, goodURL, fingerprint); err != nil {
			t.Fatalf("seed associations: %v", err)
		}

		job := claimOne(t, goodURL)
		generation, err := store.BeginLinkScanSubmit(ctx, goodURL, job.SubmitGeneration)
		if err != nil {
			t.Fatalf("BeginLinkScanSubmit: %v", err)
		}
		if err := store.RecordLinkScanSubmission(ctx, goodURL, "scan-shared", generation); err != nil {
			t.Fatalf("RecordLinkScanSubmission: %v", err)
		}
		// One Submit, exactly: a second claim of the same url must find nothing.
		jobs, err := store.ClaimDueLinkScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueLinkScans: %v", err)
		}
		for _, j := range jobs {
			if j.CanonicalURL == goodURL {
				t.Fatal("the shared url was claimed a second time before it was decided")
			}
		}
		if err := store.RecordLinkVerdict(ctx, goodURL, "scan-shared", urlsafety.VerdictInconclusive); err != nil {
			t.Fatalf("RecordLinkVerdict(inconclusive): %v", err)
		}

		summary, err := store.ResolveDecidedMessages(ctx)
		if err != nil {
			t.Fatalf("ResolveDecidedMessages: %v", err)
		}
		if summary.Published != 2 || summary.Blocked != 0 || summary.PublishedInconclusive != 2 {
			t.Fatalf("summary = %+v, want both messages published from the one inconclusive scan", summary)
		}
		for _, id := range []string{first, second} {
			var status, linkSafety string
			if err := pool.QueryRow(ctx,
				`SELECT status, link_safety_state FROM chat.messages WHERE id = $1`, id,
			).Scan(&status, &linkSafety); err != nil {
				t.Fatalf("read status for %s: %v", id, err)
			}
			if status != "active" || linkSafety != "inconclusive" {
				t.Fatalf("message %s = %q/%q, want active/inconclusive", id, status, linkSafety)
			}
			var eventType, blockReason string
			if err := pool.QueryRow(ctx, `
				SELECT event_type, COALESCE(block_reason, '')
				FROM chat.message_publish_outbox WHERE message_id = $1`,
				id).Scan(&eventType, &blockReason); err != nil {
				t.Fatalf("read publish event for %s: %v", id, err)
			}
			if eventType != storage.EventMessageCreated {
				t.Fatalf("message %s event = %q, want message.created", id, eventType)
			}
			if blockReason != "" {
				t.Fatalf("message %s block_reason = %q, want none on a publication", id, blockReason)
			}
		}

		if _, err := pool.Exec(ctx, `DELETE FROM chat.messages WHERE id = ANY($1::uuid[])`,
			[]string{first, second}); err != nil {
			t.Fatalf("clean withheld messages: %v", err)
		}
	})

	// [§50] The compare-and-set, from the losing side. Each of these is a worker
	// whose lease expired while it was talking to the provider, arriving late with
	// an answer about a row that has moved on.
	t.Run("a superseded worker cannot overwrite the current attempt", func(t *testing.T) {
		resetQueue(t)
		if err := store.EnsureLinkScans(ctx, []string{goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}
		job := claimOne(t, goodURL)

		stale := job.SubmitGeneration
		generation, err := store.BeginLinkScanSubmit(ctx, goodURL, stale)
		if err != nil {
			t.Fatalf("BeginLinkScanSubmit: %v", err)
		}

		// The generation this worker expected is no longer the row's.
		if _, err := store.BeginLinkScanSubmit(ctx, goodURL, stale); !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("BeginLinkScanSubmit on a stale generation: %v, want ErrLinkScanConflict", err)
		}
		if err := store.RecordLinkScanSubmission(ctx, goodURL, "scan-stale", stale); !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("RecordLinkScanSubmission on a stale generation: %v, want ErrLinkScanConflict", err)
		}
		if err := store.AdoptScanUUID(ctx, goodURL, "scan-stale", stale); !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("AdoptScanUUID on a stale generation: %v, want ErrLinkScanConflict", err)
		}

		if err := store.RecordLinkScanSubmission(ctx, goodURL, "scan-current", generation); err != nil {
			t.Fatalf("RecordLinkScanSubmission: %v", err)
		}
		// A verdict is bound to the scan it came from, so an answer about a scan
		// this row no longer carries decides nothing.
		if err := store.RecordLinkVerdict(ctx, goodURL, "scan-stale", urlsafety.VerdictSafe); !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("RecordLinkVerdict for a superseded scan: %v, want ErrLinkScanConflict", err)
		}
		// And a second submission finds the id already bound.
		if err := store.RecordLinkScanSubmission(ctx, goodURL, "scan-second", generation); !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("a second submission: %v, want ErrLinkScanConflict", err)
		}
	})

	// A non-final verdict is refused before it reaches the database: the whole
	// point of the control is that a technical failure cannot read as a clearance.
	t.Run("only a final verdict may be stored", func(t *testing.T) {
		resetQueue(t)
		if err := store.EnsureLinkScans(ctx, []string{goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}
		err := store.RecordLinkVerdict(ctx, goodURL, "scan-any", urlsafety.VerdictUnknown)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("RecordLinkVerdict(unknown) = %v, want ErrInvalidInput", err)
		}
		var status string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.link_scans WHERE canonical_url = $1`, goodURL).Scan(&status); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status != "pending" {
			t.Fatalf("status = %q, want the row untouched", status)
		}
	})

	// The uncertain state is the one a restart must not resubmit. It leaves it by
	// adopting an id the provider's own search identified, never by submitting
	// again.
	t.Run("an uncertain attempt adopts the id the search recovered", func(t *testing.T) {
		resetQueue(t)
		if err := store.EnsureLinkScans(ctx, []string{goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}
		job := claimOne(t, goodURL)
		generation, err := store.BeginLinkScanSubmit(ctx, goodURL, job.SubmitGeneration)
		if err != nil {
			t.Fatalf("BeginLinkScanSubmit: %v", err)
		}

		// What the next pass sees after the crash: an attempt outstanding, no id.
		due(t, goodURL)
		recovered := claimOne(t, goodURL)
		if recovered.SubmitState() != storage.SubmitUncertain {
			t.Fatalf("state = %q, want %q", recovered.SubmitState(), storage.SubmitUncertain)
		}
		if recovered.SubmitStartedAt.IsZero() {
			t.Fatal("an outstanding attempt carried no start time")
		}

		if err := store.AdoptScanUUID(ctx, goodURL, "scan-recovered", generation); err != nil {
			t.Fatalf("AdoptScanUUID: %v", err)
		}
		due(t, goodURL)
		adopted := claimOne(t, goodURL)
		if adopted.SubmitState() != storage.SubmitPolling || adopted.ScanUUID != "scan-recovered" {
			t.Fatalf("job = %+v, want the recovered id adopted", adopted)
		}
	})

	// A claim is a lease. Two workers cannot hold one row, which is what stops one
	// URL from becoming two provider submissions.
	t.Run("a claimed url is not offered again until its lease expires", func(t *testing.T) {
		resetQueue(t)
		if err := store.EnsureLinkScans(ctx, []string{goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}
		claimOne(t, goodURL)

		jobs, err := store.ClaimDueLinkScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueLinkScans: %v", err)
		}
		for _, job := range jobs {
			if job.CanonicalURL == goodURL {
				t.Fatal("a leased url was handed to a second worker")
			}
		}

		// And a batch of nothing is not a query at all.
		if jobs, err := store.ClaimDueLinkScans(ctx, 0); err != nil || jobs != nil {
			t.Fatalf("ClaimDueLinkScans(0) = %v, %v, want no work", jobs, err)
		}
	})

	// [§31] A verdict is only usable while it is fresh, in both directions: it
	// stops being served, and the URL a withheld message waits on goes back in the
	// queue instead of stranding it.
	t.Run("an expired verdict is neither served nor left to strand a message", func(t *testing.T) {
		resetQueue(t)
		if err := store.EnsureLinkScans(ctx, []string{goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}
		expire := func(t *testing.T) {
			t.Helper()
			if _, err := pool.Exec(ctx, `
				UPDATE chat.link_scans
				   SET status = 'safe', decided_at = now() - ($2 * interval '1 second'), scan_uuid = NULL
				 WHERE canonical_url = $1`, goodURL, urlsafety.VerdictTTL.Seconds()+3600); err != nil {
				t.Fatalf("expire verdict: %v", err)
			}
		}

		expire(t)
		verdicts, err := store.LoadLinkVerdicts(ctx, []string{goodURL})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		if len(verdicts) != 0 {
			t.Fatalf("a lapsed clearance was served: %v", verdicts)
		}
		// An empty request is not a query.
		if verdicts, err := store.LoadLinkVerdicts(ctx, nil); err != nil || len(verdicts) != 0 {
			t.Fatalf("LoadLinkVerdicts(nil) = %v, %v", verdicts, err)
		}

		// Naming it again re-opens it, and only ever in this direction.
		if err := store.EnsureLinkScans(ctx, []string{goodURL}); err != nil {
			t.Fatalf("EnsureLinkScans: %v", err)
		}
		var status string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.link_scans WHERE canonical_url = $1`, goodURL).Scan(&status); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status != "pending" {
			t.Fatalf("status = %q, want a lapsed verdict re-queued", status)
		}
		// An empty set writes nothing.
		if err := store.EnsureLinkScans(ctx, nil); err != nil {
			t.Fatalf("EnsureLinkScans(nil): %v", err)
		}

		// Nothing is waiting on it, so the scheduled sweep leaves it alone: a
		// verdict nobody is blocked on is re-scanned on demand, not on a timer.
		expire(t)
		reopened, err := store.ReopenExpiredVerdicts(ctx)
		if err != nil {
			t.Fatalf("ReopenExpiredVerdicts: %v", err)
		}
		if reopened != 0 {
			t.Fatalf("reopened %d rows nobody was waiting on", reopened)
		}

		// With a withheld message depending on it, the same sweep must re-queue it
		// — otherwise the message is decided by nothing and waits forever.
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.messages
				(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
				 status, link_safety_fingerprint)
			VALUES ($1, $2, $3, $4, 'user', 'waiting', 'v2', 'pending_link_scan', $5)
			ON CONFLICT (id) DO NOTHING`,
			cleared, workspace, channel, author, fingerprint); err != nil {
			t.Fatalf("seed withheld message: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			cleared, goodURL, fingerprint); err != nil {
			t.Fatalf("seed association: %v", err)
		}
		reopened, err = store.ReopenExpiredVerdicts(ctx)
		if err != nil {
			t.Fatalf("ReopenExpiredVerdicts: %v", err)
		}
		if reopened != 1 {
			t.Fatalf("reopened %d rows, want the one a withheld message waits on", reopened)
		}
		// Idempotent: a row already pending is not matched, so a second pass and a
		// second replica cannot produce a second scan.
		if reopened, err = store.ReopenExpiredVerdicts(ctx); err != nil || reopened != 0 {
			t.Fatalf("a second sweep reopened %d rows (%v)", reopened, err)
		}

		if _, err := pool.Exec(ctx, `DELETE FROM chat.messages WHERE id = $1`, cleared); err != nil {
			t.Fatalf("clean withheld message: %v", err)
		}
	})

	// [§51] The promotion, and the event announcing it, in one statement. The two
	// outcomes go to different audiences, and a blocked message must never be
	// announced to the conversation it was never shown in.
	t.Run("decided messages are resolved and their events queued", func(t *testing.T) {
		resetQueue(t)
		if _, err := pool.Exec(ctx, `DELETE FROM chat.messages WHERE sender_id = $1`, author); err != nil {
			t.Fatalf("clean messages: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, decided_at)
			VALUES ($1, 'safe', now()), ($2, 'malicious', now())
			ON CONFLICT (canonical_url)
			DO UPDATE SET status = EXCLUDED.status, decided_at = now(), scan_uuid = NULL`,
			goodURL, badURL); err != nil {
			t.Fatalf("seed verdicts: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.messages
				(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
				 status, link_safety_fingerprint)
			VALUES ($1, $3, $4, $5, 'user', 'safe body',      'v2', 'pending_link_scan', $6),
			       ($2, $3, $4, $5, 'user', 'malicious body', 'v2', 'pending_link_scan', $6)`,
			cleared, refused, workspace, channel, author, fingerprint); err != nil {
			t.Fatalf("seed messages: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			VALUES ($1, $3, $5), ($2, $4, $5)`,
			cleared, refused, goodURL, badURL, fingerprint); err != nil {
			t.Fatalf("seed associations: %v", err)
		}
		// A mention parked at creation: a withheld message must produce no side
		// effect aimed at anyone else until it is publishable.
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.message_pending_mentions (message_id, user_id)
			VALUES ($1, $3), ($2, $3)`, cleared, refused, mentioned); err != nil {
			t.Fatalf("seed pending mentions: %v", err)
		}

		summary, err := store.ResolveDecidedMessages(ctx)
		if err != nil {
			t.Fatalf("ResolveDecidedMessages: %v", err)
		}
		if summary.Published != 1 || summary.Blocked != 1 || summary.Total() != 2 {
			t.Fatalf("summary = %+v, want one of each", summary)
		}

		var clearedStatus, refusedStatus string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.messages WHERE id = $1`, cleared).Scan(&clearedStatus); err != nil {
			t.Fatalf("read promoted status: %v", err)
		}
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.messages WHERE id = $1`, refused).Scan(&refusedStatus); err != nil {
			t.Fatalf("read blocked status: %v", err)
		}
		if clearedStatus != "active" || refusedStatus != "deleted" {
			t.Fatalf("statuses = %q / %q, want active / deleted", clearedStatus, refusedStatus)
		}

		// The audiences. A refusal addresses its author, not the channel.
		var eventType, targetType, targetID string
		if err := pool.QueryRow(ctx, `
			SELECT event_type, target_type, target_id::text
			FROM chat.message_publish_outbox WHERE message_id = $1`, refused,
		).Scan(&eventType, &targetType, &targetID); err != nil {
			t.Fatalf("read blocked event: %v", err)
		}
		if eventType != storage.EventMessageBlocked || targetType != storage.TargetSender || targetID != author {
			t.Fatalf("blocked event = %s/%s/%s, want a refusal addressed to its author",
				eventType, targetType, targetID)
		}
		if err := pool.QueryRow(ctx, `
			SELECT event_type, target_type, target_id::text
			FROM chat.message_publish_outbox WHERE message_id = $1`, cleared,
		).Scan(&eventType, &targetType, &targetID); err != nil {
			t.Fatalf("read published event: %v", err)
		}
		if eventType != storage.EventMessageCreated || targetType != "channel" || targetID != channel {
			t.Fatalf("published event = %s/%s/%s, want the channel told",
				eventType, targetType, targetID)
		}

		// The mention is released for the promoted message only.
		var released []string
		rows, err := pool.Query(ctx, `
			SELECT message_id::text FROM chat.notification_outbox
			WHERE recipient_user_id = $1 AND kind = 'mention'`, mentioned)
		if err != nil {
			t.Fatalf("read notifications: %v", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan notification: %v", err)
			}
			released = append(released, id)
		}
		rows.Close()
		if len(released) != 1 || released[0] != cleared {
			t.Fatalf("released mentions = %v, want only the promoted message's", released)
		}

		// Nothing is left withheld, so a second pass decides nothing. That is what
		// makes the promotion exactly-once under two replicas.
		if summary, err = store.ResolveDecidedMessages(ctx); err != nil || summary.Total() != 0 {
			t.Fatalf("a second pass resolved %+v (%v)", summary, err)
		}
	})

	// Regression: a mentioned user who was a channel member when the message was
	// sent, but left before the scan decided the message, must NOT have their
	// pending mention promoted. The hardened SQL revalidates membership at
	// promotion time, and this test is the evidence that it works.
	t.Run("a mention is not promoted when the recipient loses channel membership", func(t *testing.T) {
		const (
			lostMember    = "e3000000-0000-4000-8000-00000000000c"
			lostMemberMsg = "e3000000-0000-4000-8000-00000000000d"
		)
		resetQueue(t)
		if _, err := pool.Exec(ctx, `DELETE FROM chat.messages WHERE id = $1`, lostMemberMsg); err != nil {
			t.Fatalf("clean message: %v", err)
		}

		// Seed the user and make them a workspace + channel member.
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth.users (id, email, display_name)
			VALUES ($1, 'rf21-lost@e.test', 'Lost')
			ON CONFLICT (id) DO NOTHING`, lostMember); err != nil {
			t.Fatalf("seed lost member user: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.workspace_members (workspace_id, user_id, status)
			VALUES ($1, $2, 'active') ON CONFLICT DO NOTHING`, workspace, lostMember); err != nil {
			t.Fatalf("seed lost member workspace: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.channel_members (channel_id, user_id)
			VALUES ($1, $2) ON CONFLICT DO NOTHING`, channel, lostMember); err != nil {
			t.Fatalf("seed lost member channel: %v", err)
		}

		// A withheld message that mentions the soon-to-leave member.
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, decided_at)
			VALUES ($1, 'safe', now())
			ON CONFLICT (canonical_url)
			DO UPDATE SET status = 'safe', decided_at = now(), scan_uuid = NULL`,
			goodURL); err != nil {
			t.Fatalf("seed verdict: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.messages
				(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
				 status, link_safety_fingerprint)
			VALUES ($1, $2, $3, $4, 'user', 'mentioning lost member', 'v2',
			        'pending_link_scan', $5)`,
			lostMemberMsg, workspace, channel, author, fingerprint); err != nil {
			t.Fatalf("seed message: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			VALUES ($1, $2, $3)`, lostMemberMsg, goodURL, fingerprint); err != nil {
			t.Fatalf("seed association: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.message_pending_mentions (message_id, user_id)
			VALUES ($1, $2)`, lostMemberMsg, lostMember); err != nil {
			t.Fatalf("seed pending mention: %v", err)
		}

		// The member leaves the channel before the scan decides the message.
		if _, err := pool.Exec(ctx, `
			DELETE FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`,
			channel, lostMember); err != nil {
			t.Fatalf("remove channel membership: %v", err)
		}

		// Promotion resolves the message — published — but the mention must NOT
		// reach the notification outbox.
		summary, err := store.ResolveDecidedMessages(ctx)
		if err != nil {
			t.Fatalf("ResolveDecidedMessages: %v", err)
		}
		if summary.Published != 1 {
			t.Fatalf("summary = %+v, want the message published", summary)
		}

		var msgStatus string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.messages WHERE id = $1`, lostMemberMsg,
		).Scan(&msgStatus); err != nil {
			t.Fatalf("read message status: %v", err)
		}
		if msgStatus != "active" {
			t.Fatalf("message status = %q, want active", msgStatus)
		}

		// The outbox event exists — the message was promoted — but no mention
		// notification was emitted for the ex-member.
		var notificationCount int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM chat.notification_outbox
			WHERE message_id = $1 AND recipient_user_id = $2 AND kind = 'mention'`,
			lostMemberMsg, lostMember,
		).Scan(&notificationCount); err != nil {
			t.Fatalf("count notifications: %v", err)
		}
		if notificationCount != 0 {
			t.Fatalf("notification_outbox has %d rows for the ex-member, want 0", notificationCount)
		}

		// Clean up.
		if _, err := pool.Exec(ctx, `DELETE FROM chat.messages WHERE id = $1`, lostMemberMsg); err != nil {
			t.Fatalf("clean message: %v", err)
		}
	})

	// The dispatcher half: claim, deliver, retire. Depends on the events the
	// previous subtest wrote.
	t.Run("the dispatcher claims, delivers and retires events", func(t *testing.T) {
		pending, oldest, err := store.PublishOutboxBacklog(ctx)
		if err != nil {
			t.Fatalf("PublishOutboxBacklog: %v", err)
		}
		if pending != 2 || oldest < 0 {
			t.Fatalf("backlog = %d events, oldest %v, want the two just written", pending, oldest)
		}

		events, err := store.ClaimPublishEvents(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimPublishEvents: %v", err)
		}
		byMessage := map[string]storage.PublishEvent{}
		for _, event := range events {
			byMessage[event.MessageID] = event
		}
		if len(byMessage) != 2 {
			t.Fatalf("claimed %d events, want two", len(byMessage))
		}

		promotion := byMessage[cleared]
		if promotion.Blocked() {
			t.Fatal("a promotion reported itself as a refusal")
		}
		// The payload is read back from the committed row rather than carried in
		// the outbox, so what is announced is what every other reader sees.
		if promotion.Message.ID != cleared || promotion.Message.BodyText != "safe body" {
			t.Fatalf("promotion payload = %+v, want the promoted message", promotion.Message)
		}
		if promotion.Message.SenderDisplayName == "" {
			t.Fatal("the broadcast payload carried no sender display name")
		}
		if promotion.WorkspaceID != workspace {
			t.Fatalf("workspace = %q, want %q", promotion.WorkspaceID, workspace)
		}

		refusal := byMessage[refused]
		if !refusal.Blocked() {
			t.Fatal("a refusal did not report itself as one")
		}
		// Nothing about the content travels with a refusal.
		if refusal.Message.ID != "" || refusal.Message.BodyText != "" {
			t.Fatalf("a refusal carried a body: %+v", refusal.Message)
		}

		// A leased event is not handed to a second dispatcher.
		again, err := store.ClaimPublishEvents(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimPublishEvents: %v", err)
		}
		if len(again) != 0 {
			t.Fatalf("a leased event was claimed twice: %+v", again)
		}
		if events, err := store.ClaimPublishEvents(ctx, 0); err != nil || events != nil {
			t.Fatalf("ClaimPublishEvents(0) = %v, %v, want no work", events, err)
		}

		if err := store.MarkPublished(ctx, cleared); err != nil {
			t.Fatalf("MarkPublished: %v", err)
		}
		if err := store.CancelPublishEvent(ctx, refused); err != nil {
			t.Fatalf("CancelPublishEvent: %v", err)
		}

		var delivered, cancelled string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.message_publish_outbox WHERE message_id = $1`,
			cleared).Scan(&delivered); err != nil {
			t.Fatalf("read delivered: %v", err)
		}
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.message_publish_outbox WHERE message_id = $1`,
			refused).Scan(&cancelled); err != nil {
			t.Fatalf("read cancelled: %v", err)
		}
		// Two different operational facts, kept apart on purpose.
		if delivered != "delivered" || cancelled != "cancelled" {
			t.Fatalf("statuses = %q / %q, want delivered / cancelled", delivered, cancelled)
		}

		if pending, _, err = store.PublishOutboxBacklog(ctx); err != nil || pending != 0 {
			t.Fatalf("backlog = %d (%v), want the retired events gone", pending, err)
		}
	})

	// A message deleted between promotion and delivery has nothing left to
	// announce, ever. Without a terminal state its event stayed pending and was
	// retried forever.
	t.Run("an event whose message vanished is cancelled rather than retried", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
			UPDATE chat.message_publish_outbox
			   SET status = 'pending', next_attempt_at = NULL WHERE message_id = $1`,
			cleared); err != nil {
			t.Fatalf("requeue event: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE chat.messages SET status = 'deleted', deleted_at = now() WHERE id = $1`,
			cleared); err != nil {
			t.Fatalf("delete message: %v", err)
		}

		events, err := store.ClaimPublishEvents(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimPublishEvents: %v", err)
		}
		if len(events) != 1 || events[0].MessageID != cleared {
			t.Fatalf("claimed %+v, want the requeued event", events)
		}
		if !events[0].Cancelled {
			t.Fatal("an event for a vanished message was not marked cancellable")
		}
	})

	// The deployment-wide submission allowance. It lives in the database because
	// three replicas each allowing ten a minute is thirty a minute at the provider.
	t.Run("the provider allowance is enforced across callers", func(t *testing.T) {
		if _, err := pool.Exec(ctx,
			`DELETE FROM chat.link_scan_budget_usage WHERE scope_type = 'provider_submit'`); err != nil {
			t.Fatalf("reset provider budget: %v", err)
		}

		const window = time.Hour
		for attempt := 1; attempt <= 2; attempt++ {
			reserved, err := store.ReserveProviderSubmit(ctx, 2, window)
			if err != nil {
				t.Fatalf("ReserveProviderSubmit: %v", err)
			}
			if !reserved {
				t.Fatalf("submission %d of 2 was refused", attempt)
			}
		}
		reserved, err := store.ReserveProviderSubmit(ctx, 2, window)
		if err != nil {
			t.Fatalf("ReserveProviderSubmit: %v", err)
		}
		if reserved {
			t.Fatal("a third submission was allowed against an allowance of two")
		}

		// An unset limit is not a limit of zero: it disables the ceiling.
		if reserved, err = store.ReserveProviderSubmit(ctx, 0, window); err != nil || !reserved {
			t.Fatalf("ReserveProviderSubmit(0) = %v, %v, want the ceiling disabled", reserved, err)
		}

		// Retention: windows that can no longer be counted into are dropped, and a
		// current one is not.
		if _, err := pool.Exec(ctx, `
			UPDATE chat.link_scan_budget_usage
			   SET window_start = now() - interval '30 days'
			 WHERE scope_type = 'provider_submit'`); err != nil {
			t.Fatalf("age window: %v", err)
		}
		if err := store.PruneLinkScanBudget(ctx, 0); err != nil {
			t.Fatalf("PruneLinkScanBudget(0): %v", err)
		}
		if err := store.PruneLinkScanBudget(ctx, 24*time.Hour); err != nil {
			t.Fatalf("PruneLinkScanBudget: %v", err)
		}
		var remaining int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM chat.link_scan_budget_usage
			WHERE scope_type = 'provider_submit'`).Scan(&remaining); err != nil {
			t.Fatalf("count windows: %v", err)
		}
		if remaining != 0 {
			t.Fatalf("%d expired window(s) survived the prune", remaining)
		}
	})
}
