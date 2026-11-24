package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

type inspectionObserver struct {
	*scanObserver
	results []filepolicy.Result
}

type faultScanObjects struct {
	service.ObjectStore
	mode   string
	opens  int
	cancel context.CancelFunc
}

func (o *faultScanObjects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	o.opens++
	if o.mode == "av_open" && o.opens == 3 {
		return nil, errors.New("synthetic storage outage")
	}
	r, err := o.ObjectStore.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	if o.mode == "policy_canceled" {
		o.cancel()
	}
	if o.mode == "policy_read" || o.mode == "av_read" && o.opens == 3 {
		return &faultScanBody{Reader: io.MultiReader(io.LimitReader(r, int64(crypto.EnvelopeHeaderSize)), iotest.ErrReader(crypto.ErrCiphertext)), Closer: r}, nil
	}
	return r, nil
}

type faultScanBody struct {
	io.Reader
	io.Closer
}

func TestStructuralAndAVStorageFailuresNeverApprove(t *testing.T) {
	for mode, want := range map[string]string{
		"policy_read": "decrypt", "policy_canceled": "canceled", "av_open": "storage_read", "av_read": "decrypt",
	} {
		t.Run(mode, func(t *testing.T) {
			f := newScanFixture(t)
			job := f.storeAttachment(t, []byte("test"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			objects := &faultScanObjects{ObjectStore: f.objects, mode: mode, cancel: cancel}
			o := &diagnosticObserver{scanObserver: f.observer, phases: map[string]int{}}
			svc := service.NewMalwareScanService(f.queue, f.results, objects, f.keys, f.scanner, f.publisher, o, testScanTimeout, discardLogger())
			if _, err := svc.ProcessDue(ctx); err != nil {
				t.Fatal(err)
			}
			assertScanStaysPending(t, f, job, o, want)
		})
	}
}

type deadlineScanObjects struct {
	service.ObjectStore
	blockOpen bool
}

func (o deadlineScanObjects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if o.blockOpen {
		<-ctx.Done()
		return nil, errors.New("storage request interrupted")
	}
	r, err := o.ObjectStore.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	return &faultScanBody{Reader: io.MultiReader(io.LimitReader(r, int64(crypto.EnvelopeHeaderSize)), deadlineScanReader{ctx}), Closer: r}, nil
}

type deadlineScanReader struct{ ctx context.Context }

func (r deadlineScanReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

type scanDeadlineCase struct {
	name, want string
	jobTimeout time.Duration
	blockOpen  bool
	avCalls    int
}

func TestStructuralScanDeadlineClassification(t *testing.T) {
	for _, tc := range []scanDeadlineCase{
		{"structural read", "storage_read", testScanTimeout, false, 0},
		{"structural open", "storage_read", testScanTimeout, true, 0},
		{"parent deadline", "timeout", time.Second, false, 0},
		{"ClamAV deadline", "timeout", testScanTimeout, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, tc.run)
		})
	}
}

func (tc scanDeadlineCase) run(t *testing.T) {
	f := newScanFixture(t)
	job := f.storeAttachment(t, []byte("test"))
	var objects service.ObjectStore = deadlineScanObjects{ObjectStore: f.objects, blockOpen: tc.blockOpen}
	if tc.avCalls > 0 {
		objects = f.objects
		f.scanner.err = context.DeadlineExceeded
	}
	o := &diagnosticObserver{scanObserver: f.observer, phases: map[string]int{}}
	svc := service.NewMalwareScanService(f.queue, f.results, objects, f.keys, f.scanner, f.publisher, o, tc.jobTimeout, discardLogger())
	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertScanStaysPending(t, f, job, o, tc.want)
	if calls := f.scanner.callCount(); calls != tc.avCalls {
		t.Fatalf("AV calls=%d, want %d", calls, tc.avCalls)
	}
}

func assertScanStaysPending(t *testing.T, f *scanFixture, job service.ScanJob, o *diagnosticObserver, want string) {
	t.Helper()
	status, _ := f.queue.statusOf(job.AttachmentID)
	if status != domain.StatusPendingScan || len(f.publisher.changes) != 0 {
		t.Fatalf("failed scan status=%s published=%d", status, len(f.publisher.changes))
	}
	if len(o.failures) != 1 || o.failures[0] != want {
		t.Fatalf("failure categories=%v, want %s", o.failures, want)
	}
}

type boundedScanObjects struct {
	service.ObjectStore
	deadlines []time.Time
}

func (o *boundedScanObjects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	deadline, _ := ctx.Deadline()
	o.deadlines = append(o.deadlines, deadline)
	return o.ObjectStore.Open(ctx, key)
}

func TestStructuralDeadlineReachesStorageWithoutCancelingAV(t *testing.T) {
	f := newScanFixture(t)
	job := f.storeAttachment(t, []byte("two independently bounded passes"))
	objects := &boundedScanObjects{ObjectStore: f.objects}
	svc := service.NewMalwareScanService(f.queue, f.results, objects, f.keys, f.scanner, nil, nil, testScanTimeout, discardLogger())
	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(objects.deadlines) < 2 || !objects.deadlines[len(objects.deadlines)-1].After(objects.deadlines[0]) {
		t.Fatalf("storage deadlines: %v", objects.deadlines)
	}
	for _, deadline := range objects.deadlines[:len(objects.deadlines)-1] {
		if deadline.IsZero() || deadline.After(time.Now().Add(10*time.Second)) {
			t.Fatalf("unbounded structural storage: %v", deadline)
		}
	}
	if status, _ := f.queue.statusOf(job.AttachmentID); status != domain.StatusClean {
		t.Fatalf("structural cancellation leaked into AV: %s", status)
	}
}

func (o *inspectionObserver) ObserveFileInspection(r filepolicy.Result, _ time.Duration) {
	o.results = append(o.results, r)
}

func TestStructuralScanUsesRealPolicyBeforeClean(t *testing.T) {
	for _, tc := range []struct {
		name, filename string
		body           []byte
		code           filepolicy.Code
		status         domain.Status
	}{
		{"passive office", "document.docx", enterpriseCorpus(t, "document.docx"), filepolicy.Recognized, domain.StatusClean},
		{"zip as office", "document.docx", enterpriseCorpus(t, "bundle.zip"), filepolicy.Invalid, domain.StatusRejected},
		{"active office", "document.docx", workerOfficePackage(t), filepolicy.Active, domain.StatusRejected},
		{"macro enabled", "document.docm", enterpriseCorpus(t, "document.docx"), filepolicy.Active, domain.StatusRejected},
		{"opaque CAD", "drawing.dwg", []byte("opaque synthetic CAD"), filepolicy.Opaque, domain.StatusRejected},
		{"unknown", "data.unknown", []byte("synthetic"), filepolicy.Unsupported, domain.StatusRejected},
		{"invalid JSON", "data.json", []byte(`{"truncated":`), filepolicy.Invalid, domain.StatusRejected},
		{"inspection budget", "data.json", bytes.Repeat([]byte(" "), int(filepolicy.MaxTextBytes)+1), filepolicy.BudgetExceeded, domain.StatusRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScanFixture(t)
			job := f.storeAttachment(t, tc.body)
			f.queue.rows[0].job.Filename = tc.filename
			f.queue.rows[0].job.PolicyVersion = filepolicy.Version
			o := &inspectionObserver{scanObserver: f.observer}
			svc := service.NewMalwareScanService(f.queue, f.results, f.objects, f.keys, f.scanner, f.publisher, o, testScanTimeout, discardLogger())
			svc.SetFormatProfiles(filepolicy.Profiles{Data: true, Office: true, Archives: true})
			if _, err := svc.ProcessDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			status, _ := f.queue.statusOf(job.AttachmentID)
			if status != tc.status || len(o.results) != 1 || o.results[0].Code != tc.code {
				t.Fatalf("status=%s inspection=%+v", status, o.results)
			}
			assertStructuralAV(t, f, tc.body, tc.status)
		})
	}
}

func assertStructuralAV(t *testing.T, f *scanFixture, body []byte, status domain.Status) {
	t.Helper()
	if status != domain.StatusClean {
		if f.scanner.callCount() != 0 || f.observer.counted("policy_rejected") != 1 {
			t.Fatal("structural failure reached AV or was reported as malware")
		}
		return
	}
	if f.scanner.callCount() != 1 || !bytes.Equal(f.scanner.lastContent(), body) {
		t.Fatal("classification skipped AV or changed the original")
	}
}
