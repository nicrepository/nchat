package storage_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The deadline is a fact of the state machine, not a courtesy of the sweep
// (issue #807 CQ round 5). Proved at the SQL: a claim never takes an expired
// row whatever the sweep has reached, a normal outcome finds nothing to write
// once the deadline passed, and the sweep is then the only transition left.

const backlogPrefix = "https://targets.example/backlog-"

func backlogURL(i int) string { return fmt.Sprintf("%s%03d", backlogPrefix, i) }

// expireScan moves a target's deadline into the past directly in the
// database — the race a slow provider produces, without sleeping.
func (f linkTargetFixture) expireScan(t *testing.T, url string) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_scans SET deadline_at = now() - interval '1 second' WHERE canonical_url = $1`, url); err != nil {
		t.Fatalf("expire %s: %v", url, err)
	}
}

func (f linkTargetFixture) scanStatus(t *testing.T, url string) (status, reason string) {
	t.Helper()
	var why *string
	if err := f.pool.QueryRow(f.ctx, `SELECT status, terminal_reason FROM chat.link_scans WHERE canonical_url = $1`, url).Scan(&status, &why); err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if why != nil {
		reason = *why
	}
	return status, reason
}

func TestLinkScanDeadlineIsAStateMachineInvariantPostgreSQL(t *testing.T) {
	f := newLinkTargetFixture(t)

	t.Run("a backlog larger than one sweep pass leaves nothing claimable", func(t *testing.T) {
		f.reset(t)
		const expired = storage.ExportMaxTerminalizeBatch + 1
		for i := 0; i < expired; i++ {
			f.target(t, backlogURL(i), "pending", time.Minute) // deadline one minute ago
		}
		f.target(t, targetURLA, "pending", -time.Hour) // deadline in an hour: the one valid row

		swept, err := f.store.TerminalizeExpiredLinkScans(f.ctx)
		if err != nil || len(swept) != storage.ExportMaxTerminalizeBatch {
			t.Fatalf("first sweep = %d %v, want the batch size", len(swept), err)
		}
		// One expired row is left behind by the batch: the claim must not take it.
		jobs, err := f.store.ClaimDueLinkScans(f.ctx, expired+1)
		if err != nil || len(jobs) != 1 || jobs[0].CanonicalURL != targetURLA {
			t.Fatalf("claim = %+v %v, want only the valid target", jobs, err)
		}
		swept, err = f.store.TerminalizeExpiredLinkScans(f.ctx)
		if err != nil || len(swept) != 1 {
			t.Fatalf("second sweep = %d %v, want the leftover", len(swept), err)
		}
		if status, reason := f.scanStatus(t, swept[0]); status != "unknown" || reason != storage.TerminalReasonDeadline {
			t.Fatalf("leftover = %s/%s, want unknown/deadline", status, reason)
		}
	})

	t.Run("a normal outcome after the deadline finds nothing to write", func(t *testing.T) {
		for name, settle := range map[string]func(scanUUID string, generation int) error{
			"submission intent": func(_ string, generation int) error {
				_, err := f.store.BeginLinkScanSubmit(f.ctx, targetURLA, generation)
				return err
			},
			"scan id": func(_ string, generation int) error {
				return f.store.RecordLinkScanSubmission(f.ctx, targetURLA, "scan-late", generation)
			},
			"adopted scan id": func(_ string, generation int) error {
				return f.store.AdoptScanUUID(f.ctx, targetURLA, "scan-late", generation)
			},
			"safe": func(scanUUID string, _ int) error {
				return f.store.RecordLinkVerdict(f.ctx, storage.LinkVerdictWrite{CanonicalURL: targetURLA, ScanUUID: scanUUID, Verdict: urlsafety.VerdictSafe})
			},
			"malicious": func(scanUUID string, _ int) error {
				return f.store.RecordLinkVerdict(f.ctx, storage.LinkVerdictWrite{CanonicalURL: targetURLA, ScanUUID: scanUUID, Verdict: urlsafety.VerdictMalicious})
			},
			"inconclusive": func(scanUUID string, _ int) error {
				return f.store.RecordLinkVerdict(f.ctx, storage.LinkVerdictWrite{CanonicalURL: targetURLA, ScanUUID: scanUUID, Verdict: urlsafety.VerdictInconclusive})
			},
		} {
			t.Run(name, func(t *testing.T) {
				f.reset(t)
				f.target(t, targetURLA, "pending", -time.Hour)
				jobs, err := f.store.ClaimDueLinkScans(f.ctx, 1)
				if err != nil || len(jobs) != 1 {
					t.Fatalf("claim while valid = %+v %v", jobs, err)
				}
				// The verdict paths need a bound scan; bind it while still valid.
				scanUUID, generation := "", jobs[0].SubmitGeneration
				if name == "safe" || name == "malicious" || name == "inconclusive" {
					scanUUID = "scan-valid"
					if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_scans SET scan_uuid = $2 WHERE canonical_url = $1`, targetURLA, scanUUID); err != nil {
						t.Fatalf("bind scan: %v", err)
					}
				}
				// The provider is slow; the deadline passes in the database, and
				// nothing in Go checked the clock before the write.
				f.expireScan(t, targetURLA)
				if err := settle(scanUUID, generation); !errors.Is(err, storage.ErrLinkScanConflict) {
					t.Fatalf("%s after the deadline: %v, want the lost compare-and-set", name, err)
				}
				if status, _ := f.scanStatus(t, targetURLA); status != "pending" {
					t.Fatalf("status after the late outcome = %s, want still pending for the sweep", status)
				}
				if swept, err := f.store.TerminalizeExpiredLinkScans(f.ctx); err != nil || len(swept) != 1 {
					t.Fatalf("sweep = %v %v", swept, err)
				}
				if status, reason := f.scanStatus(t, targetURLA); status != "unknown" || reason != storage.TerminalReasonDeadline {
					t.Fatalf("after the sweep = %s/%s, want unknown/deadline", status, reason)
				}
			})
		}
	})

	t.Run("the same outcome inside the deadline is written", func(t *testing.T) {
		f.reset(t)
		f.target(t, targetURLA, "pending", -time.Hour)
		jobs, _ := f.store.ClaimDueLinkScans(f.ctx, 1)
		generation, err := f.store.BeginLinkScanSubmit(f.ctx, targetURLA, jobs[0].SubmitGeneration)
		if err != nil {
			t.Fatalf("BeginLinkScanSubmit: %v", err)
		}
		if err := f.store.RecordLinkScanSubmission(f.ctx, targetURLA, "scan-ok", generation); err != nil {
			t.Fatalf("RecordLinkScanSubmission: %v", err)
		}
		if err := f.store.RecordLinkVerdict(f.ctx, storage.LinkVerdictWrite{CanonicalURL: targetURLA, ScanUUID: "scan-ok", Verdict: urlsafety.VerdictSafe}); err != nil {
			t.Fatalf("RecordLinkVerdict: %v", err)
		}
		if status, _ := f.scanStatus(t, targetURLA); status != "safe" {
			t.Fatalf("status = %s, want safe", status)
		}
	})
}

func TestLinkPreviewDeadlineIsAStateMachineInvariantPostgreSQL(t *testing.T) {
	f := newLinkTargetFixture(t)
	queue := func(t *testing.T, url string) {
		t.Helper()
		if err := f.store.QueueLinkPreviews(f.ctx, f.workspace, []string{url}); err != nil {
			t.Fatalf("QueueLinkPreviews: %v", err)
		}
	}
	expirePreview := func(t *testing.T, id string) {
		t.Helper()
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_previews SET deadline_at = now() - interval '1 second' WHERE id = $1::uuid`, id); err != nil {
			t.Fatalf("expire preview: %v", err)
		}
	}
	previewState := func(t *testing.T, id string) (state, reason string) {
		t.Helper()
		var why *string
		if err := f.pool.QueryRow(f.ctx, `SELECT state, failure_reason FROM chat.link_previews WHERE id = $1::uuid`, id).Scan(&state, &why); err != nil {
			t.Fatalf("read preview: %v", err)
		}
		if why != nil {
			reason = *why
		}
		return state, reason
	}

	t.Run("a backlog larger than one sweep pass leaves nothing claimable", func(t *testing.T) {
		f.reset(t)
		const expired = storage.ExportMaxTerminalizeBatch + 1
		urls := make([]string, 0, expired)
		for i := 0; i < expired; i++ {
			f.target(t, backlogURL(i), "safe", 0)
			urls = append(urls, backlogURL(i))
		}
		f.target(t, targetURLA, "safe", 0)
		if err := f.store.QueueLinkPreviews(f.ctx, f.workspace, append(urls, targetURLA)); err != nil {
			t.Fatalf("queue: %v", err)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE chat.link_previews SET deadline_at = now() - interval '1 second'
			WHERE canonical_url LIKE $1`, backlogPrefix+"%"); err != nil {
			t.Fatalf("expire backlog: %v", err)
		}

		swept, err := f.store.TerminalizeExpiredLinkPreviews(f.ctx)
		if err != nil || len(swept) != storage.ExportMaxTerminalizeBatch {
			t.Fatalf("first sweep = %d %v, want the batch size", len(swept), err)
		}
		jobs, err := f.store.ClaimDueLinkPreviews(f.ctx, expired+1)
		if err != nil || len(jobs) != 1 || jobs[0].CanonicalURL != targetURLA {
			t.Fatalf("claim = %+v %v, want only the valid preview", jobs, err)
		}
		swept, err = f.store.TerminalizeExpiredLinkPreviews(f.ctx)
		if err != nil || len(swept) != 1 || swept[0].State != "failed" {
			t.Fatalf("second sweep = %+v %v, want the leftover failed", swept, err)
		}
		if _, reason := previewState(t, swept[0].ID); reason != storage.PreviewFailureDeadline {
			t.Fatalf("leftover reason = %s, want deadline", reason)
		}
	})

	t.Run("a normal outcome after the deadline finds nothing to write", func(t *testing.T) {
		for name, settle := range map[string]func(claim storage.LinkPreviewJob) error{
			"complete": func(claim storage.LinkPreviewJob) error {
				_, err := f.store.CompleteLinkPreview(f.ctx, claim, storage.LinkPreviewResult{Title: "late"})
				return err
			},
			"retry": func(claim storage.LinkPreviewJob) error {
				_, err := f.store.FailLinkPreview(f.ctx, claim, storage.PreviewFailureTimeout, false)
				return err
			},
			"terminal failure": func(claim storage.LinkPreviewJob) error {
				_, err := f.store.FailLinkPreview(f.ctx, claim, storage.PreviewFailureNoMetadata, true)
				return err
			},
		} {
			t.Run(name, func(t *testing.T) {
				f.reset(t)
				f.target(t, targetURLA, "safe", 0)
				queue(t, targetURLA)
				jobs, err := f.store.ClaimDueLinkPreviews(f.ctx, 1)
				if err != nil || len(jobs) != 1 {
					t.Fatalf("claim while valid = %+v %v", jobs, err)
				}
				// The fetch is slow; the deadline passes in the database while the
				// claim, still owned, is held in Go.
				expirePreview(t, jobs[0].ID)
				if err := settle(jobs[0]); !errors.Is(err, storage.ErrLinkPreviewConflict) {
					t.Fatalf("%s after the deadline: %v, want the lost compare-and-set", name, err)
				}
				if state, _ := previewState(t, jobs[0].ID); state != "fetching" {
					t.Fatalf("state after the late outcome = %s, want still fetching for the sweep", state)
				}
				swept, err := f.store.TerminalizeExpiredLinkPreviews(f.ctx)
				if err != nil || len(swept) != 1 || swept[0].State != "failed" {
					t.Fatalf("sweep = %+v %v", swept, err)
				}
				if state, reason := previewState(t, jobs[0].ID); state != "failed" || reason != storage.PreviewFailureDeadline {
					t.Fatalf("after the sweep = %s/%s, want failed/deadline", state, reason)
				}
			})
		}
	})
}
