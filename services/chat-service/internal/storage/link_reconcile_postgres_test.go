package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Recovering from an inconclusive link, against a real database (issue #135).
//
// Everything here is a claim about SQL. The aggregation that decides whether a
// withheld message may be published, the one-way door out of 'inconclusive', the
// recompute that converges an already-delivered message, the attempt cap that
// stops the background pass looping, and the authorization on the reader-driven
// path are all predicates in queries — a fake store would agree with whatever the
// Go around them said, and the queries are what is under test.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations.
func TestLinkReconcilePostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXMessageStore(pool)

	const (
		workspace = "00000000-0000-0000-0000-000000000001"
		channel   = "e4000000-0000-4000-8000-000000000002"
		author    = "e4000000-0000-4000-8000-000000000003"
		outsider  = "e4000000-0000-4000-8000-000000000004"

		published = "e4000000-0000-4000-8000-00000000000a"
		withheld  = "e4000000-0000-4000-8000-00000000000b"

		urlA = "https://reconcile.example/a"
		urlB = "https://reconcile.example/b"

		fingerprint = "fp-reconcile"
	)
	allURLs := []string{urlA, urlB}

	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.users (id, email, display_name)
		VALUES ($1, 'rf21-recon-author@e.test', 'Author'),
		       ($2, 'rf21-recon-outsider@e.test', 'Outsider')
		ON CONFLICT (id) DO NOTHING`, author, outsider); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		// The outsider is an active workspace member but not a channel member, so
		// the authorization tests exercise the channel-visibility rule rather than
		// the workspace one — the weaker of the two, and therefore the one worth
		// proving.
		{`INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  VALUES ($1, $2, 'active'), ($1, $3, 'active') ON CONFLICT DO NOTHING`,
			[]any{workspace, author, outsider}},
		// DO UPDATE and not DO NOTHING: the private type is what the authorization
		// subtests actually assert, so a row left behind by an earlier run must not
		// be able to silently make them test a public channel instead.
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		  VALUES ($2, $1, 'rf21-recon', 'RF21 reconcile', 'private', 'active')
		  ON CONFLICT (id) DO UPDATE SET type = 'private', status = 'active'`,
			[]any{workspace, channel}},
		{`INSERT INTO chat.channel_members (channel_id, user_id)
		  VALUES ($1, $2) ON CONFLICT DO NOTHING`, []any{channel, author}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM chat.messages WHERE sender_id = $1`, author)
		_, _ = pool.Exec(background,
			`DELETE FROM chat.link_scans WHERE canonical_url = ANY($1::text[])`, allURLs)
	})

	// seedScan writes one decided link_scans row directly. Going through the
	// worker for every fixture would make these tests about the worker.
	seedScan := func(t *testing.T, url, status, scanUUID string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
			VALUES ($1, $2, NULLIF($3, ''), now())
			ON CONFLICT (canonical_url) DO UPDATE
			   SET status = EXCLUDED.status, scan_uuid = EXCLUDED.scan_uuid,
			       decided_at = EXCLUDED.decided_at, next_reconcile_at = NULL,
			       reconcile_attempts = 0`,
			url, status, scanUUID); err != nil {
			t.Fatalf("seed scan %s: %v", url, err)
		}
	}

	seedMessage := func(t *testing.T, id, status, linkSafety string, urls ...string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.messages
				(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
				 status, link_safety_state, link_safety_fingerprint)
			VALUES ($1, $2, $3, $4, 'user', 'body https://reconcile.example/a', 'v2', $5, $6, $7)
			ON CONFLICT (id) DO UPDATE
			   SET status = EXCLUDED.status, link_safety_state = EXCLUDED.link_safety_state`,
			id, workspace, channel, author, status, linkSafety, fingerprint); err != nil {
			t.Fatalf("seed message %s: %v", id, err)
		}
		for _, url := range urls {
			if _, err := pool.Exec(ctx, `
				INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
				VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				id, url, fingerprint); err != nil {
				t.Fatalf("seed association %s/%s: %v", id, url, err)
			}
		}
	}

	reset := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`DELETE FROM chat.messages WHERE id = ANY($1::uuid[])`,
			[]string{published, withheld}); err != nil {
			t.Fatalf("reset messages: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM chat.link_scans WHERE canonical_url = ANY($1::text[])`, allURLs); err != nil {
			t.Fatalf("reset scans: %v", err)
		}
		// The cross-service reconciliation lease (CQ-003) outlives the rows it is
		// about — it is keyed by digest and lives in files, deliberately not
		// cascaded from either verdict store — so a subtest that claimed a URL
		// would otherwise lock the next subtest out of it for a minute.
		if _, err := pool.Exec(ctx,
			`DELETE FROM files.link_reconcile_leases WHERE canonical_url = ANY($1::text[])`,
			allURLs); err != nil {
			t.Fatalf("reset reconcile leases: %v", err)
		}
	}

	// fresh is evidence the provider produced just now — the ordinary case. Age is
	// exercised explicitly by its own subtest below.
	fresh := func(verdict urlsafety.Verdict) urlsafety.ScanEvidence {
		return urlsafety.ScanEvidence{Verdict: verdict, ObservedAt: time.Now()}
	}

	messageState := func(t *testing.T, id string) (string, string) {
		t.Helper()
		var status, linkSafety string
		if err := pool.QueryRow(ctx,
			`SELECT status, link_safety_state FROM chat.messages WHERE id = $1`, id,
		).Scan(&status, &linkSafety); err != nil {
			t.Fatalf("read message %s: %v", id, err)
		}
		return status, linkSafety
	}

	// The aggregation rule that changed for this issue. A message with one
	// inconclusive link and one link still being scanned must stay withheld: the
	// pending one may yet come back malicious, and publishing early would deliver
	// a message that has to be withdrawn a moment later.
	//
	// Before #135 this could not happen — inconclusive meant "refuse", so deciding
	// early was harmless. It is precisely publishing on inconclusive that makes
	// waiting for every link mandatory.
	t.Run("a message is not published while any of its links may still be condemned", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "inconclusive", "scan-a")
		if _, err := pool.Exec(ctx,
			`INSERT INTO chat.link_scans (canonical_url) VALUES ($1)`, urlB); err != nil {
			t.Fatalf("seed pending scan: %v", err)
		}
		seedMessage(t, withheld, "pending_link_scan", "", urlA, urlB)

		summary, err := store.ResolveDecidedMessages(ctx)
		if err != nil {
			t.Fatalf("ResolveDecidedMessages: %v", err)
		}
		if summary.Total() != 0 {
			t.Fatalf("summary = %+v, want nothing decided while a link is still pending", summary)
		}
		if status, _ := messageState(t, withheld); status != "pending_link_scan" {
			t.Fatalf("status = %q, want the message still withheld", status)
		}

		// The pending link then comes back malicious: the message is refused, which
		// is the outcome publishing early would have got wrong.
		seedScan(t, urlB, "malicious", "scan-b")
		summary, err = store.ResolveDecidedMessages(ctx)
		if err != nil {
			t.Fatalf("ResolveDecidedMessages (after malicious): %v", err)
		}
		if summary.Blocked != 1 || summary.Published != 0 {
			t.Fatalf("summary = %+v, want the message blocked", summary)
		}
		status, linkSafety := messageState(t, withheld)
		if status != "deleted" || linkSafety != "malicious" {
			t.Fatalf("message = %q/%q, want deleted/malicious", status, linkSafety)
		}
	})

	// Precedence, with both terminal answers present at once: malicious wins over
	// inconclusive, always, and the message is refused rather than published with
	// a notice.
	t.Run("malicious wins over inconclusive", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "inconclusive", "scan-a")
		seedScan(t, urlB, "malicious", "scan-b")
		seedMessage(t, withheld, "pending_link_scan", "", urlA, urlB)

		summary, err := store.ResolveDecidedMessages(ctx)
		if err != nil {
			t.Fatalf("ResolveDecidedMessages: %v", err)
		}
		if summary.Blocked != 1 || summary.Published != 0 {
			t.Fatalf("summary = %+v, want blocked", summary)
		}
		status, linkSafety := messageState(t, withheld)
		if status != "deleted" || linkSafety != "malicious" {
			t.Fatalf("message = %q/%q, want deleted/malicious", status, linkSafety)
		}
	})

	// A message whose links are all cleared publishes with the 'safe' marker, not
	// with the empty one: "verified" and "nothing to say" are different facts, and
	// only the first authorises a server-side preview.
	t.Run("all safe publishes with a safe marker", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "safe", "scan-a")
		seedScan(t, urlB, "safe", "scan-b")
		seedMessage(t, withheld, "pending_link_scan", "", urlA, urlB)

		if _, err := store.ResolveDecidedMessages(ctx); err != nil {
			t.Fatalf("ResolveDecidedMessages: %v", err)
		}
		status, linkSafety := messageState(t, withheld)
		if status != "active" || linkSafety != "safe" {
			t.Fatalf("message = %q/%q, want active/safe", status, linkSafety)
		}
	})

	// The one-way door. Every clause of ReconcileLinkVerdict's predicate is a way
	// the wrong write could land, so each is exercised against the real statement.
	t.Run("only an inconclusive row bound to its own scan may be reconciled", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "inconclusive", "scan-a")

		// A verdict read from a different scan cannot be written here: it is a
		// statement about some other scan, and this row does not own it.
		if err := store.ReconcileLinkVerdict(ctx, urlA, "scan-somebody-else", fresh(urlsafety.VerdictSafe)); !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("a foreign scan id was accepted: %v", err)
		}
		// Inconclusive is not a verdict, and being able to write it here would make
		// "reconcile" a way to reset the recovery bookkeeping.
		if err := store.ReconcileLinkVerdict(ctx, urlA, "scan-a", fresh(urlsafety.VerdictInconclusive)); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("a non-final verdict was accepted: %v", err)
		}
		if err := store.ReconcileLinkVerdict(ctx, urlA, "scan-a", fresh(urlsafety.VerdictUnknown)); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("an unknown verdict was accepted: %v", err)
		}

		// The real transition.
		if err := store.ReconcileLinkVerdict(ctx, urlA, "scan-a", fresh(urlsafety.VerdictSafe)); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}
		verdicts, err := store.LoadLinkVerdicts(ctx, []string{urlA})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		// decided_at is stamped with now, not with the age of the report the verdict
		// came from — otherwise the clearance would be born already expired and the
		// row would be unresolvable forever.
		if verdicts[urlA] != urlsafety.VerdictSafe {
			t.Fatalf("verdicts = %v, want a fresh safe clearance", verdicts)
		}

		// And it is a one-way door: a decided row cannot be reconciled again, so a
		// second, later answer cannot overwrite the first.
		if err := store.ReconcileLinkVerdict(ctx, urlA, "scan-a", fresh(urlsafety.VerdictMalicious)); !errors.Is(err, storage.ErrLinkScanConflict) {
			t.Fatalf("an already-decided row was reconciled again: %v", err)
		}
	})

	// A published message converges when its link is finally cleared: the notice
	// goes away and the change is reported, once, for broadcasting.
	t.Run("a cleared link removes the notice from the messages carrying it", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "inconclusive", "scan-a")
		seedMessage(t, published, "active", "inconclusive", urlA)

		if err := store.ReconcileLinkVerdict(ctx, urlA, "scan-a", fresh(urlsafety.VerdictSafe)); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}
		changes, err := store.RefreshMessageLinkSafety(ctx, urlA)
		if err != nil {
			t.Fatalf("RefreshMessageLinkSafety: %v", err)
		}
		if len(changes) != 1 || changes[0].MessageID != published {
			t.Fatalf("changes = %+v, want exactly the published message", changes)
		}
		if changes[0].State != domain.MessageLinkSafetySafe {
			t.Fatalf("state = %q, want safe", changes[0].State)
		}
		// The audience is the conversation the message was delivered to — the same
		// routing its message.created used.
		if changes[0].TargetType != storage.TargetChannel || changes[0].TargetID != channel {
			t.Fatalf("target = %s/%s, want the channel", changes[0].TargetType, changes[0].TargetID)
		}
		if status, linkSafety := messageState(t, published); status != "active" || linkSafety != "safe" {
			t.Fatalf("message = %q/%q, want active/safe", status, linkSafety)
		}

		// Idempotent: a second pass changes nothing, so nothing is announced again.
		// Without this every background pass would be a broadcast.
		changes, err = store.RefreshMessageLinkSafety(ctx, urlA)
		if err != nil {
			t.Fatalf("RefreshMessageLinkSafety (repeat): %v", err)
		}
		if len(changes) != 0 {
			t.Fatalf("changes = %+v, want none on a repeat pass", changes)
		}
	})

	// The case that matters most: a message that was published because its link
	// was merely unverified, and which reconciliation later proves malicious. The
	// message stays where it is — it was delivered — and the marker withdraws its
	// links.
	t.Run("a condemned link withdraws the links of a message already delivered", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "inconclusive", "scan-a")
		seedMessage(t, published, "active", "inconclusive", urlA)

		if err := store.ReconcileLinkVerdict(ctx, urlA, "scan-a", fresh(urlsafety.VerdictMalicious)); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}
		changes, err := store.RefreshMessageLinkSafety(ctx, urlA)
		if err != nil {
			t.Fatalf("RefreshMessageLinkSafety: %v", err)
		}
		if len(changes) != 1 || changes[0].State != domain.MessageLinkSafetyMalicious {
			t.Fatalf("changes = %+v, want one malicious correction", changes)
		}
		// No second message.created: the message was published once and stays
		// published. Re-emitting it would duplicate it in every client's timeline
		// and re-fire its mentions.
		var events int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.message_publish_outbox WHERE message_id = $1`, published,
		).Scan(&events); err != nil {
			t.Fatalf("count publish events: %v", err)
		}
		if events != 0 {
			t.Fatalf("publish events = %d, want none — a correction is not a creation", events)
		}
		if status, linkSafety := messageState(t, published); status != "active" || linkSafety != "malicious" {
			t.Fatalf("message = %q/%q, want active/malicious", status, linkSafety)
		}
	})

	// Precedence again, this time in the recompute rather than the resolver: one
	// link cleared does not clear a message whose other link is still unverified.
	t.Run("the recompute folds every link of a message", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "inconclusive", "scan-a")
		seedScan(t, urlB, "inconclusive", "scan-b")
		seedMessage(t, published, "active", "inconclusive", urlA, urlB)

		if err := store.ReconcileLinkVerdict(ctx, urlA, "scan-a", fresh(urlsafety.VerdictSafe)); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}
		changes, err := store.RefreshMessageLinkSafety(ctx, urlA)
		if err != nil {
			t.Fatalf("RefreshMessageLinkSafety: %v", err)
		}
		if len(changes) != 0 {
			t.Fatalf("changes = %+v, want none while another link is still unverified", changes)
		}
		if _, linkSafety := messageState(t, published); linkSafety != "inconclusive" {
			t.Fatalf("link_safety_state = %q, want inconclusive", linkSafety)
		}
	})

	// An undecided link leaves the marker alone rather than folding in as "not
	// malicious". EnsureLinkScans legitimately returns a lapsed row to 'pending'
	// when somebody sends the same URL again, and during that window a message
	// that had been blocked must not quietly lose its block.
	t.Run("an undecided link never downgrades an existing marker", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "inconclusive", "scan-a")
		seedScan(t, urlB, "malicious", "scan-b")
		seedMessage(t, published, "active", "malicious", urlA, urlB)

		if _, err := pool.Exec(ctx, `
			UPDATE chat.link_scans SET status = 'pending', decided_at = NULL, scan_uuid = NULL
			 WHERE canonical_url = $1`, urlB); err != nil {
			t.Fatalf("reopen the condemned link: %v", err)
		}
		changes, err := store.RefreshMessageLinkSafety(ctx, urlA)
		if err != nil {
			t.Fatalf("RefreshMessageLinkSafety: %v", err)
		}
		if len(changes) != 0 {
			t.Fatalf("changes = %+v, want none while a link is undecided", changes)
		}
		if _, linkSafety := messageState(t, published); linkSafety != "malicious" {
			t.Fatalf("link_safety_state = %q, a re-queued link dropped a block", linkSafety)
		}
		message, err := store.GetMessageByIDInWorkspace(ctx, workspace, published, author)
		if err != nil {
			t.Fatalf("read blocked message: %v", err)
		}
		if message.BodyText != "" {
			t.Fatalf("body = %q, want it withheld until every link is decided", message.BodyText)
		}
	})

	// The background pass is bounded by a counter the claim consumes and nothing
	// refills, and it only looks at URLs a published message is actually showing a
	// notice for.
	t.Run("the background claim is bounded and scoped", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "inconclusive", "scan-a")
		// Nothing is waiting on urlB, so it must never be claimed: the answer would
		// be correct, useless, and paid for at the provider.
		seedScan(t, urlB, "inconclusive", "scan-b")
		seedMessage(t, published, "active", "inconclusive", urlA)

		seen := 0
		for attempt := 0; attempt < len(storage.ReconcileSchedule)+3; attempt++ {
			// The claim schedules the next attempt and takes the cross-service
			// lease; both are time-based, and expiring both is what lets this loop
			// exercise the attempt cap rather than the two cooldowns. Every real
			// schedule step is longer than ReconcileLeaseTTL, so in production the
			// lease is never what stops this service's own next attempt.
			if _, err := pool.Exec(ctx,
				`UPDATE chat.link_scans SET next_reconcile_at = NULL WHERE canonical_url = $1`,
				urlA); err != nil {
				t.Fatalf("make reconcile due: %v", err)
			}
			if _, err := pool.Exec(ctx,
				`UPDATE files.link_reconcile_leases SET leased_until = now() - interval '1 second'
				  WHERE canonical_url = $1`, urlA); err != nil {
				t.Fatalf("expire reconcile lease: %v", err)
			}
			scans, err := store.ClaimDueInconclusiveScans(ctx, 10)
			if err != nil {
				t.Fatalf("ClaimDueInconclusiveScans: %v", err)
			}
			for _, scan := range scans {
				if scan.CanonicalURL == urlB {
					t.Fatal("a url no published message is waiting on was claimed")
				}
				if scan.CanonicalURL == urlA {
					if scan.ScanUUID != "scan-a" {
						t.Fatalf("scan uuid = %q, want the one the row already owns", scan.ScanUUID)
					}
					seen++
				}
			}
		}
		if seen != len(storage.ReconcileSchedule) {
			t.Fatalf("claimed %d times, want exactly the %d automatic attempts",
				seen, len(storage.ReconcileSchedule))
		}
	})

	// The reader-driven path: a deployment-wide cooldown per URL, so a channel
	// full of people clicking one notice costs one provider search.
	t.Run("the manual claim is rate limited per url and does not spend the automatic budget", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "inconclusive", "scan-a")

		first, err := store.ClaimManualReconcile(ctx, []string{urlA})
		if err != nil {
			t.Fatalf("ClaimManualReconcile: %v", err)
		}
		if len(first) != 1 || first[0].ScanUUID != "scan-a" {
			t.Fatalf("claim = %+v, want the url with its own scan id", first)
		}
		second, err := store.ClaimManualReconcile(ctx, []string{urlA})
		if err != nil {
			t.Fatalf("ClaimManualReconcile (repeat): %v", err)
		}
		if len(second) != 0 {
			t.Fatalf("claim = %+v, want nothing inside the cooldown", second)
		}

		// A person asking is not a timer, so it must not consume the automatic
		// recovery budget — a few clicks would otherwise silence the schedule for
		// good.
		var attempts int
		if err := pool.QueryRow(ctx,
			`SELECT reconcile_attempts FROM chat.link_scans WHERE canonical_url = $1`, urlA,
		).Scan(&attempts); err != nil {
			t.Fatalf("read reconcile attempts: %v", err)
		}
		if attempts != 0 {
			t.Fatalf("reconcile_attempts = %d, want the automatic budget untouched", attempts)
		}

		// A row that is no longer inconclusive is never claimed at all.
		if err := store.ReconcileLinkVerdict(ctx, urlA, "scan-a", fresh(urlsafety.VerdictSafe)); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}
		decided, err := store.ClaimManualReconcile(ctx, []string{urlA})
		if err != nil {
			t.Fatalf("ClaimManualReconcile (decided): %v", err)
		}
		if len(decided) != 0 {
			t.Fatalf("claim = %+v, want nothing for a decided row", decided)
		}
	})

	// The whole of the manual endpoint's input validation: the client names a
	// message, and the URLs come from the database. Authorization is the ordinary
	// message-read rule, and every refusal looks the same.
	t.Run("the reader-driven lookup is authorized and derives its own urls", func(t *testing.T) {
		reset(t)
		seedScan(t, urlA, "inconclusive", "scan-a")
		seedMessage(t, published, "active", "inconclusive", urlA)

		urls, err := store.MessageInconclusiveURLs(ctx, workspace, author, published)
		if err != nil {
			t.Fatalf("MessageInconclusiveURLs: %v", err)
		}
		if len(urls) != 1 || urls[0] != urlA {
			t.Fatalf("urls = %v, want the url recorded for this message", urls)
		}

		// Every one of these is ErrNotFound, and that sameness is the point: a
		// caller must not be able to tell "you may not read it" from "there is no
		// such message" from "it has nothing to reconcile".
		for name, call := range map[string]func() ([]string, error){
			"a reader who cannot see the channel": func() ([]string, error) {
				return store.MessageInconclusiveURLs(ctx, workspace, outsider, published)
			},
			"a message that does not exist": func() ([]string, error) {
				return store.MessageInconclusiveURLs(ctx, workspace, author,
					"e4000000-0000-4000-8000-0000000000ff")
			},
			"another workspace": func() ([]string, error) {
				return store.MessageInconclusiveURLs(ctx,
					"00000000-0000-0000-0000-0000000000ff", author, published)
			},
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := call(); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
			})
		}

		// Once the link is decided there is nothing to reconcile, and that answers
		// the same way — otherwise the endpoint would be a per-message probe for
		// link-safety state.
		if err := store.ReconcileLinkVerdict(ctx, urlA, "scan-a", fresh(urlsafety.VerdictSafe)); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}
		if _, err := store.MessageInconclusiveURLs(ctx, workspace, author, published); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound once nothing is inconclusive", err)
		}

		// The state read-back applies the same authorization.
		state, updatedAt, err := store.MessageLinkSafety(ctx, workspace, author, published)
		if err != nil {
			t.Fatalf("MessageLinkSafety: %v", err)
		}
		if state != domain.MessageLinkSafetyInconclusive {
			// Still the stored marker: RefreshMessageLinkSafety has not run yet, and
			// the read is of the row rather than a re-derivation.
			t.Fatalf("state = %q, want the stored marker", state)
		}
		if updatedAt.IsZero() {
			t.Fatal("authoritative state has no update version")
		}
		if _, _, err := store.MessageLinkSafety(ctx, workspace, outsider, published); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound for a reader who cannot see it", err)
		}
	})
}

// Evidence age in the chat store (issue #135, CQ-001).
//
// A clearance adopted by reconciliation expires from the provider's own evidence
// time, never from adoption. chat's readers all apply `decided_at > now() -
// VerdictTTL`, so writing the evidence time into decided_at is what gives an
// adopted verdict exactly the lifetime it has left.
func TestLinkReconcileEvidenceAgePostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXMessageStore(pool)

	const url = "https://chat-aged.example/a"
	digest := urlsafety.URLDigest(url)

	seed := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`DELETE FROM chat.link_scans WHERE canonical_url = $1`, url); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
			VALUES ($1, 'inconclusive', 'scan-aged', now())`, url); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM chat.link_scans WHERE canonical_url = $1`, url)
		_, _ = pool.Exec(background, `DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest)
	})

	t.Run("a clearance is dated by the provider, not by adoption", func(t *testing.T) {
		seed(t)
		age := urlsafety.VerdictTTL / 3
		observed := time.Now().Add(-age)

		if err := store.ReconcileLinkVerdict(ctx, url, "scan-aged",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictSafe, ObservedAt: observed},
		); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}

		var decidedAgeSeconds float64
		if err := pool.QueryRow(ctx,
			`SELECT EXTRACT(EPOCH FROM (now() - decided_at))
			   FROM chat.link_scans WHERE canonical_url = $1`, url).Scan(&decidedAgeSeconds); err != nil {
			t.Fatalf("read decided_at: %v", err)
		}
		// Dated from the evidence: the row is already `age` old the moment it is
		// written, so the readers' freshness window gives it only the remainder.
		if decidedAgeSeconds < age.Seconds()-5 {
			t.Fatalf("decided_at is %.0fs old, want about %.0fs — the verdict was rejuvenated",
				decidedAgeSeconds, age.Seconds())
		}
		// And it is still usable, because the remainder is positive.
		verdicts, err := store.LoadLinkVerdicts(ctx, []string{url})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		if verdicts[url] != urlsafety.VerdictSafe {
			t.Fatalf("verdicts = %v, want a usable clearance while the remainder lasts", verdicts)
		}
	})

	t.Run("expired evidence writes no clearance", func(t *testing.T) {
		seed(t)
		observed := time.Now().Add(-urlsafety.VerdictTTL - time.Minute)

		err := store.ReconcileLinkVerdict(ctx, url, "scan-aged",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictSafe, ObservedAt: observed})

		if err == nil {
			t.Fatal("a clearance older than its lifetime was written")
		}
		var status string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM chat.link_scans WHERE canonical_url = $1`, url).Scan(&status); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status != "inconclusive" {
			t.Fatalf("status = %q, want the row left inconclusive", status)
		}
	})

	t.Run("undated evidence is refused", func(t *testing.T) {
		seed(t)

		err := store.ReconcileLinkVerdict(ctx, url, "scan-aged",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictSafe})

		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})

	// A condemnation is retained from adoption. Back-dating it would age the row
	// straight out of every reader's freshness window and *discard* the finding —
	// the opposite of conservative.
	t.Run("a condemnation is retained from adoption", func(t *testing.T) {
		seed(t)
		observed := time.Now().Add(-23 * time.Hour)

		if err := store.ReconcileLinkVerdict(ctx, url, "scan-aged",
			urlsafety.ScanEvidence{Verdict: urlsafety.VerdictMalicious, ObservedAt: observed},
		); err != nil {
			t.Fatalf("ReconcileLinkVerdict: %v", err)
		}

		verdicts, err := store.LoadLinkVerdicts(ctx, []string{url})
		if err != nil {
			t.Fatalf("LoadLinkVerdicts: %v", err)
		}
		if verdicts[url] != urlsafety.VerdictMalicious {
			t.Fatalf("verdicts = %v, an old condemnation was discarded", verdicts)
		}
	})
}
