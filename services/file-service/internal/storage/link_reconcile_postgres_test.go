package storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// The one deliberate way out of 'inconclusive', against a real database
// (issue #135).
//
// Migration 000008 made an inconclusive row immutable by UPDATE, which was right
// for a rolling deployment and wrong for recovery: a scan that came back empty
// could never become previewable again, even after Cloudflare had a verdict for
// it. 000009 opens exactly one door.
//
// The tests below are almost entirely about the door staying narrow. The guard
// lives in a trigger precisely because the thing it defends against — an
// origin/develop worker whose claim predicate is `state <> 'done'` — is code this
// database cannot see, so proving it in Go alone would prove nothing.
//
// Opt-in like its neighbour: needs FILE_TEST_DATABASE_URL against a _test
// database carrying the real migrations.
func TestLinkScanReconcilePostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXLinkScanStore(pool)

	const url = "https://reconcile-files.example/inconclusive"
	digest := sha256.Sum256([]byte(url))

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM files.link_scans WHERE url_digest = $1`, digest[:])
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest[:])
	})

	// seed writes the row the way the worker leaves it after a terminal scan with
	// no usable verdict: state inconclusive, uuid preserved, attempt history kept,
	// nothing scheduled.
	seed := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`DELETE FROM files.link_scans WHERE url_digest = $1`, digest[:]); err != nil {
			t.Fatalf("reset row: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM files.link_fetch_denylist WHERE url_digest = $1`, digest[:]); err != nil {
			t.Fatalf("reset denylist: %v", err)
		}
		// The cross-service lease (CQ-003) is not cascaded from the verdict row, so
		// a subtest that claimed this URL would lock the next one out for a minute.
		if _, err := pool.Exec(ctx,
			`DELETE FROM files.link_reconcile_leases WHERE url_digest = $1`, digest[:]); err != nil {
			t.Fatalf("reset reconcile lease: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, attempts, created_at, updated_at)
			VALUES ($1, $2, 'inconclusive', 'scan-terminal', 7,
			        now() - interval '1 day', now() - interval '1 day')`,
			digest[:], url); err != nil {
			t.Fatalf("seed inconclusive row: %v", err)
		}
	}

	// fresh is evidence the provider produced just now — the ordinary case. Age is
	// exercised explicitly by its own subtest.
	fresh := func(verdict urlsafety.Verdict) urlsafety.ScanEvidence {
		return urlsafety.ScanEvidence{Verdict: verdict, ObservedAt: time.Now()}
	}

	row := func(t *testing.T) (state, scanUUID string, attempts, reconcileAttempts int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT state, COALESCE(scan_uuid, ''), attempts, reconcile_attempts
			FROM files.link_scans WHERE url_digest = $1`, digest[:],
		).Scan(&state, &scanUUID, &attempts, &reconcileAttempts); err != nil {
			t.Fatalf("read row: %v", err)
		}
		return
	}

	// The door itself. This is the transition 000009 exists to allow, and the only
	// one — it restores previewability for a URL whose verdict finally arrived.
	t.Run("explicit reconciliation moves an inconclusive row to done", func(t *testing.T) {
		seed(t)

		if err := store.ReconcileVerdict(ctx, digest[:], "scan-terminal", fresh(urlsafety.VerdictSafe)); err != nil {
			t.Fatalf("ReconcileVerdict: %v", err)
		}
		state, scanUUID, attempts, _ := row(t)
		if state != "done" || scanUUID != "scan-terminal" || attempts != 7 {
			t.Fatalf("row = %q/%q/%d, want done with its uuid and history intact",
				state, scanUUID, attempts)
		}
		// And the preview gate can now see it, which is the whole point: the
		// clearance keeps only the lifetime left from the provider evidence, so it
		// is not refreshed merely because this service adopted it later.
		verdict, ok, err := store.LoadVerdict(ctx, url)
		if err != nil {
			t.Fatalf("LoadVerdict: %v", err)
		}
		if !ok || verdict != urlsafety.VerdictSafe {
			t.Fatalf("verdict = %q ok=%v, want a live safe clearance", verdict, ok)
		}

		// One-way: a decided row cannot be reconciled again, so a later answer
		// cannot overwrite the first.
		if err := store.ReconcileVerdict(ctx, digest[:], "scan-terminal", fresh(urlsafety.VerdictMalicious)); !errors.Is(err, service.ErrLinkScanConflict) {
			t.Fatalf("a decided row was reconciled again: %v", err)
		}
	})

	// The other direction, which is the one that costs a user something: a link
	// that was only unverified turns out to be condemned.
	t.Run("reconciliation may also condemn", func(t *testing.T) {
		seed(t)

		if err := store.ReconcileVerdict(ctx, digest[:], "scan-terminal", fresh(urlsafety.VerdictMalicious)); err != nil {
			t.Fatalf("ReconcileVerdict: %v", err)
		}
		verdict, ok, err := store.LoadVerdict(ctx, url)
		if err != nil {
			t.Fatalf("LoadVerdict: %v", err)
		}
		if !ok || verdict != urlsafety.VerdictMalicious {
			t.Fatalf("verdict = %q ok=%v, want malicious", verdict, ok)
		}
		var denied bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1)`,
			digest[:]).Scan(&denied); err != nil {
			t.Fatalf("read denylist: %v", err)
		}
		if !denied {
			t.Fatal("malicious reconciliation did not publish the global denial")
		}
	})

	// A verdict is a statement about one scan. A row may only adopt one it owns.
	t.Run("a verdict from another scan is refused", func(t *testing.T) {
		seed(t)

		if err := store.ReconcileVerdict(ctx, digest[:], "scan-somebody-else", fresh(urlsafety.VerdictMalicious)); !errors.Is(err, service.ErrLinkScanConflict) {
			t.Fatalf("a foreign scan id was accepted: %v", err)
		}
		if state, _, _, _ := row(t); state != "inconclusive" {
			t.Fatalf("state = %q, want the row untouched", state)
		}
		var denied bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM files.link_fetch_denylist WHERE url_digest = $1)`,
			digest[:]).Scan(&denied); err != nil {
			t.Fatalf("read denylist after failed reconciliation CAS: %v", err)
		}
		if denied {
			t.Fatal("a failed reconciliation CAS published a global denial")
		}
		// Inconclusive is not a verdict, and writing it here would make "reconcile"
		// a way to reset the recovery bookkeeping.
		if err := store.ReconcileVerdict(ctx, digest[:], "scan-terminal", fresh(urlsafety.VerdictInconclusive)); err == nil {
			t.Fatal("a non-final verdict was accepted")
		}
	})

	// The rolling-deployment guarantee, restated against the *new* trigger. This
	// is the regression 000009 could most easily have introduced: its bookkeeping
	// branch leaves the state alone, and the legacy claim also leaves the state
	// alone, so a branch that merely checked "the state did not change" would have
	// handed the row straight back to an origin/develop worker.
	t.Run("the origin develop claim still cannot claim or mutate an inconclusive row", func(t *testing.T) {
		seed(t)

		tag, err := pool.Exec(ctx, `
			WITH due AS (
				SELECT ls.url_digest
				FROM files.link_scans ls
				WHERE ls.state <> 'done'
				  AND (ls.next_attempt_at IS NULL OR ls.next_attempt_at <= now())
				  AND (ls.lease_until IS NULL OR ls.lease_until <= now())
				ORDER BY ls.next_attempt_at NULLS FIRST, ls.created_at
				LIMIT 10
				FOR UPDATE SKIP LOCKED
			)
			UPDATE files.link_scans ls
			   SET attempts = LEAST(ls.attempts + 1, 32767),
			       lease_until = now() + interval '60 seconds',
			       next_attempt_at = now() + interval '60 seconds',
			       updated_at = now()
			  FROM due
			 WHERE ls.url_digest = due.url_digest`)
		if err != nil {
			t.Fatalf("legacy claim: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatal("origin/develop claimed an inconclusive row")
		}
		state, scanUUID, attempts, _ := row(t)
		if state != "inconclusive" || scanUUID != "scan-terminal" || attempts != 7 {
			t.Fatalf("legacy claim mutated the row: %q/%q/%d", state, scanUUID, attempts)
		}
	})

	// Every reopening path, refused one at a time. Each of these is a state a
	// worker could try to write; none of them may reach the row.
	t.Run("no update reopens an inconclusive row", func(t *testing.T) {
		for _, state := range []string{"submit_pending", "submitting", "submit_uncertain", "polling"} {
			t.Run(state, func(t *testing.T) {
				seed(t)
				tag, err := pool.Exec(ctx, `
					UPDATE files.link_scans
					   SET state = $2, scan_uuid = NULL, attempts = 0,
					       next_attempt_at = NULL, lease_until = NULL, updated_at = now()
					 WHERE url_digest = $1`, digest[:], state)
				if err != nil {
					t.Fatalf("attempt to reopen: %v", err)
				}
				if tag.RowsAffected() != 0 {
					t.Fatalf("an inconclusive row was reopened as %q", state)
				}
				gotState, scanUUID, attempts, _ := row(t)
				if gotState != "inconclusive" || scanUUID != "scan-terminal" || attempts != 7 {
					t.Fatalf("row mutated: %q/%q/%d", gotState, scanUUID, attempts)
				}
			})
		}

		// Nor by the narrower moves a determined caller might try: swapping the
		// scan uuid, laundering the attempt history, or backdating the row to look
		// untouched.
		for name, statement := range map[string]string{
			"replace the scan uuid": `UPDATE files.link_scans SET scan_uuid = 'other' WHERE url_digest = $1`,
			"launder the attempts":  `UPDATE files.link_scans SET attempts = 0 WHERE url_digest = $1`,
			"backdate the row":      `UPDATE files.link_scans SET updated_at = now() - interval '30 days' WHERE url_digest = $1`,
			"schedule an attempt":   `UPDATE files.link_scans SET next_attempt_at = now() WHERE url_digest = $1`,
			"take a lease":          `UPDATE files.link_scans SET lease_until = now() + interval '1 minute' WHERE url_digest = $1`,
			"invent a verdict":      `UPDATE files.link_scans SET verdict = 'safe' WHERE url_digest = $1`,
		} {
			t.Run(name, func(t *testing.T) {
				seed(t)
				tag, err := pool.Exec(ctx, statement, digest[:])
				if err != nil {
					t.Fatalf("attempt: %v", err)
				}
				if tag.RowsAffected() != 0 {
					t.Fatalf("an inconclusive row accepted %q", name)
				}
			})
		}

		// 'done' is only reachable with a complete, attributable verdict. A bare
		// state change is not one.
		seed(t)
		tag, err := pool.Exec(ctx,
			`UPDATE files.link_scans SET state = 'done' WHERE url_digest = $1`, digest[:])
		if err != nil {
			t.Fatalf("attempt to close without a verdict: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatal("an inconclusive row was closed without a verdict")
		}
	})

	// The recovery budget: consumed by the claim whether or not the provider then
	// answers, never refilled, and scheduled so a pass cannot spin.
	t.Run("the reconciliation claim is bounded and never refilled", func(t *testing.T) {
		seed(t)

		claims := 0
		for attempt := 0; attempt < len(storage.ReconcileSchedule)+3; attempt++ {
			jobs, err := store.ClaimDueReconciliations(ctx, 10)
			if err != nil {
				t.Fatalf("ClaimDueReconciliations: %v", err)
			}
			for _, job := range jobs {
				if job.ScanUUID != "scan-terminal" {
					t.Fatalf("scan uuid = %q, want the one the row already owns", job.ScanUUID)
				}
				claims++
			}
			// The claim schedules the next attempt and takes the cross-service lease
			// (CQ-003); both are time-based, so both are moved into the past to let
			// the loop exercise the attempt cap rather than either cooldown. Every
			// real schedule step is longer than ReconcileLeaseTTL, so the lease is
			// never what stops this service's own next attempt in production.
			if _, err := pool.Exec(ctx, `
				UPDATE files.link_scans
				   SET next_reconcile_at = now() - interval '1 second'
				 WHERE url_digest = $1 AND state = 'inconclusive'`, digest[:]); err != nil {
				t.Fatalf("advance the schedule: %v", err)
			}
			if _, err := pool.Exec(ctx, `
				UPDATE files.link_reconcile_leases
				   SET leased_until = now() - interval '1 second'
				 WHERE url_digest = $1`, digest[:]); err != nil {
				t.Fatalf("expire the reconcile lease: %v", err)
			}
		}
		if claims != len(storage.ReconcileSchedule) {
			t.Fatalf("claimed %d times, want exactly the %d automatic attempts",
				claims, len(storage.ReconcileSchedule))
		}

		// And the counter is not something a later statement can wind back.
		if _, err := pool.Exec(ctx,
			`UPDATE files.link_scans SET reconcile_attempts = 0 WHERE url_digest = $1`,
			digest[:]); err != nil {
			t.Fatalf("attempt to refill: %v", err)
		}
		if _, _, _, reconcileAttempts := row(t); reconcileAttempts != len(storage.ReconcileSchedule) {
			t.Fatalf("reconcile_attempts = %d, the recovery budget was refilled", reconcileAttempts)
		}
	})

	// Even after the door opens, an inconclusive row is never work the pipeline
	// reports as outstanding or charges capacity for.
	t.Run("an inconclusive row is still outside the queue and the backlog", func(t *testing.T) {
		seed(t)

		jobs, err := store.ClaimDueScans(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDueScans: %v", err)
		}
		for _, job := range jobs {
			if job.CanonicalURL == url {
				t.Fatalf("an inconclusive row was claimed for scanning: %+v", job)
			}
		}
		byState, _, err := store.Backlog(ctx)
		if err != nil {
			t.Fatalf("Backlog: %v", err)
		}
		if _, present := byState["inconclusive"]; present {
			t.Fatalf("backlog counted an inconclusive row: %v", byState)
		}
		// A preview request for the same URL admits free and does not reopen it —
		// AdmitScan only reopens a 'done' row past its expiry.
		admission, err := store.AdmitScan(ctx, url, service.LinkScanCapacity{
			NewURLBudget: 1, BudgetWindow: time.Minute, MaxPendingJobs: 1,
		})
		if err != nil {
			t.Fatalf("AdmitScan: %v", err)
		}
		if !admission.Allowed() || admission.NewScanCost != 0 {
			t.Fatalf("admission = %+v, want a free admission that starts nothing", admission)
		}
		if state, _, _, _ := row(t); state != "inconclusive" {
			t.Fatalf("state = %q, a preview request reopened the row", state)
		}
	})
}

// The per-column allowlist on the reconciliation transition (issue #135, CQ-006).
//
// 000009 guarded the `inconclusive -> done` exit with three conditions and left
// every other column free to move in the same UPDATE, so a statement that
// satisfied them could rewrite the row's identity on the way through. 000011
// decides per column instead.
//
// Each case below is one protected column, mutated alongside an otherwise
// perfectly legitimate transition. The legitimate transition on its own must
// still pass, and the legacy claim must still match nothing — a guard that
// refuses everything is not a fix.
func TestLinkScanReconcileColumnAllowlistPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)

	const url = "https://allowlist.example/a"
	digest := sha256.Sum256([]byte(url))
	other := sha256.Sum256([]byte("https://allowlist.example/other"))

	seed := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`DELETE FROM files.link_scans WHERE url_digest = ANY($1::bytea[])`,
			[][]byte{digest[:], other[:]}); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO files.link_scans
			    (url_digest, canonical_url, state, scan_uuid, attempts,
			     submit_generation, created_at, updated_at)
			VALUES ($1, $2, 'inconclusive', 'scan-guarded', 5, 3,
			        now() - interval '2 days', now() - interval '2 days')`,
			digest[:], url); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM files.link_scans WHERE url_digest = ANY($1::bytea[])`,
			[][]byte{digest[:], other[:]})
	})

	// The legitimate transition. Asserted first, so the refusals below are known to
	// be about the extra column and not about the guard refusing everything.
	t.Run("the legitimate transition passes", func(t *testing.T) {
		seed(t)
		tag, err := pool.Exec(ctx, `
			UPDATE files.link_scans
			   SET state = 'done', verdict = 'safe',
			       verdict_expires_at = now() + interval '10 minutes',
			       lease_until = NULL, next_attempt_at = NULL, next_reconcile_at = NULL,
			       updated_at = now()
			 WHERE url_digest = $1`, digest[:])
		if err != nil {
			t.Fatalf("legitimate transition: %v", err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatal("the legitimate reconciliation transition was refused")
		}
	})

	// One protected column per case, each set to a different value alongside an
	// otherwise valid `-> done`.
	// The digest is inlined as a bytea literal rather than passed as a parameter:
	// only one case needs it, and pgx refuses a statement carrying an argument its
	// SQL never references.
	otherLiteral := `'\x` + hex.EncodeToString(other[:]) + `'::bytea`
	for name, mutation := range map[string]string{
		"url_digest":                "url_digest = " + otherLiteral,
		"canonical_url":             "canonical_url = 'https://elsewhere.example/x'",
		"scan_uuid":                 "scan_uuid = 'scan-substituted'",
		"attempts":                  "attempts = 0",
		"submit_generation":         "submit_generation = 99",
		"submit_attempt_started_at": "submit_attempt_started_at = now()",
		"created_at":                "created_at = now()",
		"reconcile_attempts":        "reconcile_attempts = -1",
	} {
		t.Run("refuses a transition that also rewrites "+name, func(t *testing.T) {
			seed(t)
			tag, err := pool.Exec(ctx, `
				UPDATE files.link_scans
				   SET state = 'done', verdict = 'safe',
				       verdict_expires_at = now() + interval '10 minutes',
				       updated_at = now(),
				       `+mutation+`
				 WHERE url_digest = $1`, digest[:])
			if err != nil {
				// A negative smallint violates no constraint, so an error here would be
				// a surprise; report it rather than treating it as a pass.
				t.Fatalf("attempt: %v", err)
			}
			if tag.RowsAffected() != 0 {
				t.Fatalf("a transition rewriting %s was allowed", name)
			}
			// And the row is exactly as it was.
			var state, scanUUID, canonical string
			var attempts, generation int
			if err := pool.QueryRow(ctx, `
				SELECT state, COALESCE(scan_uuid, ''), canonical_url, attempts, submit_generation
				  FROM files.link_scans WHERE url_digest = $1`, digest[:],
			).Scan(&state, &scanUUID, &canonical, &attempts, &generation); err != nil {
				t.Fatalf("read row: %v", err)
			}
			if state != "inconclusive" || scanUUID != "scan-guarded" ||
				canonical != url || attempts != 5 || generation != 3 {
				t.Fatalf("row mutated: %q/%q/%q/%d/%d",
					state, scanUUID, canonical, attempts, generation)
			}
		})
	}

	// 'done' still requires a real, attributable verdict.
	t.Run("refuses a transition with no verdict", func(t *testing.T) {
		seed(t)
		tag, err := pool.Exec(ctx,
			`UPDATE files.link_scans SET state = 'done', updated_at = now() WHERE url_digest = $1`,
			digest[:])
		if err != nil {
			t.Fatalf("attempt: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatal("an inconclusive row was closed without a verdict")
		}
	})

	// The rolling-deployment guarantee, restated against the narrower trigger.
	t.Run("the origin develop claim still matches nothing", func(t *testing.T) {
		seed(t)
		tag, err := pool.Exec(ctx, `
			WITH due AS (
				SELECT ls.url_digest
				FROM files.link_scans ls
				WHERE ls.state <> 'done'
				  AND (ls.next_attempt_at IS NULL OR ls.next_attempt_at <= now())
				  AND (ls.lease_until IS NULL OR ls.lease_until <= now())
				ORDER BY ls.next_attempt_at NULLS FIRST, ls.created_at
				LIMIT 10
				FOR UPDATE SKIP LOCKED
			)
			UPDATE files.link_scans ls
			   SET attempts = LEAST(ls.attempts + 1, 32767),
			       lease_until = now() + interval '60 seconds',
			       next_attempt_at = now() + interval '60 seconds',
			       updated_at = now()
			  FROM due
			 WHERE ls.url_digest = due.url_digest`)
		if err != nil {
			t.Fatalf("legacy claim: %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatal("origin/develop claimed an inconclusive row")
		}
	})

	// Reconciliation bookkeeping still works — the narrower guard must not have
	// frozen the backoff it exists to permit.
	t.Run("reconciliation bookkeeping still passes", func(t *testing.T) {
		seed(t)
		tag, err := pool.Exec(ctx, `
			UPDATE files.link_scans
			   SET reconcile_attempts = reconcile_attempts + 1,
			       next_reconcile_at = now() + interval '5 minutes',
			       updated_at = now()
			 WHERE url_digest = $1`, digest[:])
		if err != nil {
			t.Fatalf("bookkeeping: %v", err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatal("reconciliation bookkeeping was refused")
		}
	})
}
