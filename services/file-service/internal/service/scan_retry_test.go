package service_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

func TestScanRetryDelayIsBoundedDeterministicAndSpread(t *testing.T) {
	for _, attempt := range []int{-1, 0, 1, 2, 3, 8, 10, 32000} {
		a := service.RetryDelay(attempt, "claim-a")
		b := service.RetryDelay(attempt, "claim-b")
		if a < 10*time.Second || a > 2640*time.Second || b < 10*time.Second || b > 2640*time.Second {
			t.Fatalf("attempt=%d delays=%s,%s", attempt, a, b)
		}
		if a != service.RetryDelay(attempt, "claim-a") || a == b {
			t.Fatal("jitter not deterministic or spread")
		}
	}
}

type retryQueue struct {
	*fakeScanQueue
	job      service.ScanJob
	delay    time.Duration
	kind     string
	err      error
	calls    int
	deadline time.Time
}

func (q *retryQueue) RetryScan(ctx context.Context, j service.ScanJob, d time.Duration, k string) error {
	q.calls++
	q.deadline, _ = ctx.Deadline()
	q.job = j
	q.delay = d
	q.kind = k
	return q.err
}
func TestFailureSchedulesOnlyClaimedRetryAndCoolsFreshClaims(t *testing.T) {
	for _, writeErr := range []error{nil, service.ErrScanSuperseded, errors.New("database offline")} {
		f := newScanFixture(t)
		job := f.storeAttachment(t, []byte("synthetic retry"))
		q := &retryQueue{fakeScanQueue: f.queue, err: writeErr}
		f.scanner.err = errors.New("scanner unavailable")
		s := service.NewMalwareScanService(q, f.results, f.objects, f.keys, f.scanner, f.publisher, f.observer, testScanTimeout, discardLogger())
		if _, err := s.ProcessDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if q.calls != 1 || q.job.AttachmentID != job.AttachmentID || q.kind != "clamd_unavailable" || q.delay < 10*time.Second {
			t.Fatalf("retry=%+v", q)
		}
		if _, err := s.ProcessDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.scanner.callCount() != 1 {
			t.Fatal("cooldown allowed new scan")
		}
		if len(f.publisher.published()) != 0 {
			t.Fatal("failed scan published status")
		}
	}
}

// Channels order a completed read, loss of authority/cancellation, and late result.
type lateScan struct {
	started, release chan struct{}
	verdict          scanner.Verdict
}

func (s lateScan) Scan(_ context.Context, r io.Reader) (scanner.Verdict, error) {
	if _, err := io.Copy(io.Discard, r); err != nil {
		return scanner.VerdictInfected, err
	}
	close(s.started)
	<-s.release
	return s.verdict, nil
}

type authorityResults struct {
	approval  service.ScanApproval
	rejection service.ScanRejection
}

func (r *authorityResults) MarkScanClean(_ context.Context, a service.ScanApproval) (service.AttachmentLifecycleState, error) {
	r.approval = a
	return service.AttachmentLifecycleState{}, service.ErrScanSuperseded
}
func (r *authorityResults) MarkScanRejected(_ context.Context, a service.ScanRejection) (service.ScanRejectionOutcome, error) {
	r.rejection = a
	return service.ScanRejectionOutcome{}, service.ErrScanSuperseded
}

func TestLateVerdictsCarryAuthorityAndPublishNothing(t *testing.T) {
	for _, verdict := range []scanner.Verdict{scanner.VerdictClean, scanner.VerdictInfected} {
		f := newScanFixture(t)
		f.storeAttachment(t, []byte("synthetic late result"))
		f.queue.rows[0].job.ClaimToken = "claim-a"
		slow := lateScan{make(chan struct{}), make(chan struct{}), verdict}
		results := &authorityResults{}
		s := service.NewMalwareScanService(f.queue, results, f.objects, f.keys, slow, f.publisher, f.observer, testScanTimeout, discardLogger())
		done := make(chan struct{})
		go func() { defer close(done); _, _ = s.ProcessDue(context.Background()) }()
		<-slow.started
		// A different claim now owns the row; the result seam returns superseded,
		// whose PostgreSQL enforcement is exercised by the real role integration suite.
		close(slow.release)
		<-done
		token := results.approval.ClaimToken
		if verdict == scanner.VerdictInfected {
			token = results.rejection.ClaimToken
		}
		if token != "claim-a" || len(f.publisher.published()) != 0 || f.observer.counted("superseded") != 1 {
			t.Fatal("late result lost authority or published status")
		}
	}
}
func TestScannerReturningCleanAfterCancellationCannotApprove(t *testing.T) {
	f := newScanFixture(t)
	f.storeAttachment(t, []byte("synthetic canceled result"))
	slow := lateScan{make(chan struct{}), make(chan struct{}), scanner.VerdictClean}
	s := service.NewMalwareScanService(f.queue, f.results, f.objects, f.keys, slow, f.publisher, f.observer, testScanTimeout, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = s.ProcessDue(ctx) }()
	<-slow.started
	cancel()
	close(slow.release)
	<-done
	clean, rejected := f.results.verdictCounts()
	if clean != 0 || rejected != 0 || len(f.publisher.published()) != 0 {
		t.Fatal("canceled scanner produced verdict")
	}
}
func TestQueueDepthIsRateLimitedAcrossDrainPasses(t *testing.T) {
	f := newScanFixture(t)
	f.storeAttachment(t, []byte("first"))
	f.storeAttachment(t, []byte("second"))
	for i := 0; i < 2; i++ {
		if _, err := f.service.ProcessDue(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, updates := f.observer.queueDepth(); updates != 1 {
		t.Fatalf("full queue counts=%d", updates)
	}
}

type deadlineApproval struct {
	*fakeResults
	deadline time.Time
}

func (r *deadlineApproval) MarkScanClean(ctx context.Context, _ service.ScanApproval) (service.AttachmentLifecycleState, error) {
	r.deadline, _ = ctx.Deadline()
	return service.AttachmentLifecycleState{}, service.ErrScanConflict
}
func TestResultFailureRetrySharesTheWriteDeadline(t *testing.T) {
	f := newScanFixture(t)
	f.storeAttachment(t, []byte("synthetic failed result write"))
	q := &retryQueue{fakeScanQueue: f.queue}
	r := &deadlineApproval{fakeResults: f.results}
	s := service.NewMalwareScanService(q, r, f.objects, f.keys, f.scanner, f.publisher, f.observer, testScanTimeout, discardLogger())
	if _, err := s.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.deadline.IsZero() || q.deadline != r.deadline {
		t.Fatal("retry extended the result-write shutdown budget")
	}
}

type retryReasonObserver struct {
	*scanObserver
	reason   string
	backoffs int
}

func (*retryReasonObserver) ScanStarted(time.Duration)  {}
func (*retryReasonObserver) ScanFinished(time.Duration) {}
func (o *retryReasonObserver) ObserveScanBackoff(_ time.Duration, reason string) {
	o.reason = reason
	o.backoffs++
}

func TestResultRetryClassifiesPolicySeparatelyFromDatabase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cause  error
		reason string
	}{
		{"inactive policy", service.ErrSignaturePolicyInactive, "signature_policy"},
		{"invalid policy", service.ErrSignaturePolicyInvalid, "signature_policy"},
		{"database failure", errors.New("private arbitrary driver message"), "database"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScanFixture(t)
			job := f.storeAttachment(t, []byte("synthetic policy retry"))
			f.results.cleanErr = tc.cause
			q := &retryQueue{fakeScanQueue: f.queue}
			o := &retryReasonObserver{scanObserver: f.observer}
			s := service.NewMalwareScanService(q, f.results, f.objects, f.keys, f.scanner, f.publisher, o, testScanTimeout, discardLogger())
			if _, err := s.ProcessDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if q.calls != 1 || q.kind != tc.reason || o.reason != tc.reason || o.backoffs != 1 {
				t.Fatalf("retry reason=%s backoff=%s count=%d", q.kind, o.reason, o.backoffs)
			}
			if status, _ := f.queue.statusOf(job.AttachmentID); status != domain.StatusPendingScan {
				t.Fatalf("status=%s", status)
			}
			if len(f.publisher.published()) != 0 || o.counted("clean") != 0 {
				t.Fatal("failed result approved or published success")
			}
		})
	}
}
