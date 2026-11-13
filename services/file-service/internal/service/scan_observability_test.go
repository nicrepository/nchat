package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

type diagnosticObserver struct {
	*scanObserver
	phases                          map[string]int
	commits, publications, failures []string
	waits                           []time.Duration
	finishes                        int
	snapshotErr                     error
	snapshots                       int
}

func (o *diagnosticObserver) ObserveScanPhase(p string, d time.Duration) {
	if d < 0 {
		panic("negative duration")
	}
	o.phases[p]++
}
func (o *diagnosticObserver) ObserveScanCommitted(r string, d time.Duration) {
	if d < 0 {
		panic("negative duration")
	}
	o.commits = append(o.commits, r)
}
func (o *diagnosticObserver) ObserveScanPublication(r string) {
	o.publications = append(o.publications, r)
}
func (o *diagnosticObserver) ObserveScanFailure(r string)              { o.failures = append(o.failures, r) }
func (o *diagnosticObserver) ScanStarted(d time.Duration)              { o.waits = append(o.waits, d) }
func (o *diagnosticObserver) ScanFinished(time.Duration)               { o.finishes++ }
func (o *diagnosticObserver) ObserveScanBackoff(time.Duration, string) {}
func (o *diagnosticObserver) ObserveScanQueueSnapshot(_ service.ScanQueueSnapshot, e error) {
	o.snapshots++
	o.snapshotErr = e
}

func TestScanTimingObservesOnlyAcknowledgedNewVerdicts(t *testing.T) {
	for _, kind := range []string{"clean", "rejected", "database", "repeat", "rescan", "lost_event", "unknown_finalized", "future_finalized", "storage"} {
		t.Run(kind, func(t *testing.T) {
			f := newScanFixture(t)
			f.storeAttachment(t, []byte("complete synthetic original"))
			f.queue.rows[0].job.QueuedAt = time.Now().Add(-time.Second)
			o := &diagnosticObserver{scanObserver: f.observer, phases: map[string]int{}}
			switch kind {
			case "rejected":
				f.scanner.verdict = scanner.VerdictInfected
			case "database":
				f.results.cleanErr = errors.New("database offline")
			case "repeat":
				f.results.cleanErr = service.ErrScanAlreadyRecorded
			case "rescan":
				f.results.statusUnchanged = true
			case "lost_event":
				f.publisher.err = errors.New("event lost")
			case "unknown_finalized":
				f.queue.rows[0].job.QueuedAt = time.Time{}
			case "future_finalized":
				f.queue.rows[0].job.QueuedAt = time.Now().Add(time.Hour)
			case "storage":
				f.objects.objects = map[string][]byte{}
			}
			s := service.NewMalwareScanService(f.queue, f.results, f.objects, f.keys, f.scanner, f.publisher, o, testScanTimeout, discardLogger())
			if _, err := s.ProcessDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := kind == "clean" || kind == "rejected" || kind == "lost_event"
			if (len(o.commits) == 1) != want {
				t.Fatalf("commits=%v", o.commits)
			}
			if o.finishes != 1 || len(o.waits) != 1 || o.phases["storage_open"] != 1 {
				t.Fatal("unbalanced attempt")
			}
			if kind == "unknown_finalized" && o.waits[0] >= 0 {
				t.Fatal("invented finalization timestamp")
			}
			if kind == "storage" {
				if len(o.phases) != 1 || len(o.publications) != 0 {
					t.Fatal("observed unexecuted phase")
				}
				return
			}
			for _, p := range []string{"format_validation", "scanner_stream", "persistence"} {
				if o.phases[p] != 1 {
					t.Fatalf("phase %s = %d", p, o.phases[p])
				}
			}
			if kind == "database" && (len(o.failures) != 1 || o.failures[0] != "database") {
				t.Fatal("lost failure category")
			}
			if kind == "lost_event" && (len(o.publications) != 1 || o.publications[0] != "error") {
				t.Fatal("lost publication failure")
			}
		})
	}
}

type snapshotQueue struct {
	*fakeScanQueue
	calls int
	err   error
}

func (q *snapshotQueue) ScanQueueSnapshot(context.Context) (service.ScanQueueSnapshot, error) {
	q.calls++
	return service.ScanQueueSnapshot{Depth: 3}, q.err
}
func TestSnapshotFailureDoesNotBlockScanningOrPublishZero(t *testing.T) {
	f := newScanFixture(t)
	j := f.storeAttachment(t, []byte("scan despite observability failure"))
	q := &snapshotQueue{fakeScanQueue: f.queue, err: errors.New("snapshot timeout")}
	o := &diagnosticObserver{scanObserver: f.observer, phases: map[string]int{}}
	o.SetScanQueueDepth(8)
	s := service.NewMalwareScanService(q, f.results, f.objects, f.keys, f.scanner, f.publisher, o, testScanTimeout, discardLogger())
	for i := 0; i < 3; i++ {
		if _, err := s.ProcessDue(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := f.queue.statusOf(j.AttachmentID)
	depth, _ := o.queueDepth()
	if state != domain.StatusClean || depth != 8 || q.calls != 1 || o.snapshots != 1 || o.snapshotErr == nil {
		t.Fatal("snapshot error changed processing or gauge")
	}
}
