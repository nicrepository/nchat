package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/linkfetch"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The preview worker (issue #807): what it stores for each fetch outcome, how
// a redirect is gated on the hop's own clearance, and that nothing about a
// link changes when a card fails.

type fakePreviewQueue struct {
	jobs      []storage.LinkPreviewJob
	targets   map[string]storage.LinkTargetState
	completed []storage.LinkPreviewResult
	failures  []struct {
		reason   string
		terminal bool
	}
	expired   []storage.LinkPreviewRow
	drained   []storage.LinkPreviewRow
	admitted  []string
	backlog   int
	failState string
	claimErr  error
	// rows is what the index serves as this workspace's previews: the row a
	// completion or failure stored, keyed by URL.
	rows map[string]storage.LinkPreviewRow
}

func (q *fakePreviewQueue) ClaimDueLinkPreviews(context.Context, int) ([]storage.LinkPreviewJob, error) {
	jobs := q.jobs
	q.jobs = nil
	return jobs, q.claimErr
}

func (q *fakePreviewQueue) CompleteLinkPreview(_ context.Context, claim storage.LinkPreviewJob, result storage.LinkPreviewResult) (storage.LinkPreviewRow, error) {
	q.completed = append(q.completed, result)
	row := storage.LinkPreviewRow{
		ID: claim.ID, WorkspaceID: "ws-1", CanonicalURL: "https://site.example/page",
		State: "ready", Title: result.Title, HasImage: len(result.ImageData) > 0,
	}
	q.store(row)
	return row, nil
}

func (q *fakePreviewQueue) store(row storage.LinkPreviewRow) {
	if q.rows == nil {
		q.rows = map[string]storage.LinkPreviewRow{}
	}
	q.rows[row.CanonicalURL] = row
}

func (q *fakePreviewQueue) FailLinkPreview(_ context.Context, claim storage.LinkPreviewJob, reason string, terminal bool) (storage.LinkPreviewRow, error) {
	id := claim.ID
	q.failures = append(q.failures, struct {
		reason   string
		terminal bool
	}{reason, terminal})
	state := "queued"
	if terminal {
		state = "failed"
	}
	if q.failState != "" {
		state = q.failState
	}
	row := storage.LinkPreviewRow{ID: id, WorkspaceID: "ws-1", CanonicalURL: "https://site.example/page", State: state}
	q.store(row)
	return row, nil
}

func (q *fakePreviewQueue) TerminalizeExpiredLinkPreviews(context.Context) ([]storage.LinkPreviewRow, error) {
	rows := q.expired
	q.expired = nil
	return rows, nil
}

func (q *fakePreviewQueue) DrainLinkPreviewsDisabled(context.Context) ([]storage.LinkPreviewRow, error) {
	rows := q.drained
	q.drained = nil
	return rows, nil
}

func (q *fakePreviewQueue) LinkPreviewBacklog(context.Context) (int, error) { return q.backlog, nil }

func (q *fakePreviewQueue) LoadLinkTargets(_ context.Context, urls []string) (map[string]storage.LinkTargetState, error) {
	out := map[string]storage.LinkTargetState{}
	for _, u := range urls {
		if t, ok := q.targets[u]; ok {
			out[u] = t
		}
	}
	return out, nil
}

func (q *fakePreviewQueue) AdmitLinkScans(_ context.Context, _ string, urls []string, _ storage.LinkScanCapacity) (storage.LinkScanAdmission, error) {
	q.admitted = append(q.admitted, urls...)
	return storage.LinkScanAdmission{Result: storage.AdmissionAllowed}, nil
}

// fakeFetcher answers a scripted document and image, and runs the hop policy
// the worker passed against a scripted redirect chain first.
type fakeFetcher struct {
	redirects []string
	document  string
	docErr    error
	image     []byte
	imageErr  error
	fetched   []string
}

func (f *fakeFetcher) FetchDocument(_ context.Context, target *url.URL, hop linkfetch.HopPolicy) (*url.URL, []byte, error) {
	f.fetched = append(f.fetched, target.String())
	for _, next := range f.redirects {
		parsed, _ := url.Parse(next)
		if err := hop(parsed); err != nil {
			return nil, nil, &hopRefusalForTest{cause: err}
		}
		target = parsed
	}
	if f.docErr != nil {
		return nil, nil, f.docErr
	}
	return target, []byte(f.document), nil
}

func (f *fakeFetcher) FetchImage(_ context.Context, target *url.URL, _ linkfetch.HopPolicy) ([]byte, error) {
	f.fetched = append(f.fetched, target.String())
	return f.image, f.imageErr
}

// hopRefusalForTest mirrors what the real fetcher returns for a vetoed hop:
// ErrRedirectRefused wrapping the caller's own reason.
type hopRefusalForTest struct{ cause error }

func (e *hopRefusalForTest) Error() string   { return "refused" }
func (e *hopRefusalForTest) Unwrap() []error { return []error{linkfetch.ErrRedirectRefused, e.cause} }

func previewJob() storage.LinkPreviewJob {
	return storage.LinkPreviewJob{ID: "p1", WorkspaceID: "ws-1", CanonicalURL: "https://site.example/page", Attempts: 1}
}

func pngFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Set(0, 0, color.NRGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png: %v", err)
	}
	return buf.Bytes()
}

func previewWorker(queue *fakePreviewQueue, fetcher *fakeFetcher) (*LinkPreviewService, *fakeLinkUpdatePublisher) {
	index := newFakeTargetIndex()
	index.targets = queue.targets
	index.refs = []storage.LinkReference{{MessageID: "m1", WorkspaceID: "ws-1", TargetType: storage.TargetChannel, TargetID: "c"}}
	if queue.rows == nil {
		queue.rows = map[string]storage.LinkPreviewRow{}
	}
	index.previews["ws-1"] = queue.rows
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)
	announcer.SetPreviewEnabled(true)
	worker := NewLinkPreviewService(queue, fetcher, nil)
	worker.SetAnnouncer(announcer)
	worker.SetEnabled(true)
	return worker, publisher
}

const ogDocument = `<html><head><meta property="og:title" content="Title"><meta property="og:description" content="Desc">` +
	`<meta property="og:site_name" content="Site"><meta property="og:image" content="https://cdn.site.example/og.png"></head><body></body></html>`

func TestPreviewWorkerStoresMetadataAndADerivedImage(t *testing.T) {
	queue := &fakePreviewQueue{jobs: []storage.LinkPreviewJob{previewJob()}, targets: map[string]storage.LinkTargetState{"https://site.example/page": target("safe", true)}}
	fetcher := &fakeFetcher{document: ogDocument, image: pngFixture(t)}
	worker, publisher := previewWorker(queue, fetcher)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if len(queue.completed) != 1 {
		t.Fatalf("completed = %+v failures = %+v", queue.completed, queue.failures)
	}
	got := queue.completed[0]
	if got.Title != "Title" || got.Description != "Desc" || got.SiteName != "Site" {
		t.Fatalf("metadata = %+v", got)
	}
	if got.ImageContentType != linkfetch.ThumbnailContentType || got.ImageWidth != 8 || len(got.ImageData) == 0 {
		t.Fatalf("image = %+v", got)
	}
	// The card was announced to the message naming the URL.
	if len(publisher.updates) != 1 || publisher.updates[0].link.Preview == nil {
		t.Fatalf("updates = %+v", publisher.updates)
	}
	if fetcher.fetched[1] != "https://cdn.site.example/og.png" {
		t.Fatalf("image fetch = %v", fetcher.fetched)
	}
}

func TestPreviewWorkerKeepsTheCardWhenTheImageIsRejected(t *testing.T) {
	for name, fetcher := range map[string]*fakeFetcher{
		"not an image":  {document: ogDocument, image: []byte("<html>")},
		"fetch failed":  {document: ogDocument, imageErr: linkfetch.ErrURLNotAllowed},
		"sensitive url": {document: `<html><head><meta property="og:title" content="T"><meta property="og:image" content="https://cdn.site.example/img?token=abc"></head></html>`},
		"internal host": {document: `<html><head><meta property="og:title" content="T"><meta property="og:image" content="https://cdn.internal/img.png"></head></html>`},
	} {
		t.Run(name, func(t *testing.T) {
			queue := &fakePreviewQueue{jobs: []storage.LinkPreviewJob{previewJob()}, targets: map[string]storage.LinkTargetState{"https://site.example/page": target("safe", true)}}
			worker, _ := previewWorker(queue, fetcher)
			if _, err := worker.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			if len(queue.completed) != 1 || len(queue.completed[0].ImageData) != 0 {
				t.Fatalf("completed = %+v", queue.completed)
			}
		})
	}
}

func TestPreviewWorkerClassifiesEveryFetchOutcome(t *testing.T) {
	cases := map[string]struct {
		fetcher  *fakeFetcher
		reason   string
		terminal bool
	}{
		"no metadata":   {&fakeFetcher{document: "<html><head></head></html>"}, storage.PreviewFailureNoMetadata, true},
		"not html":      {&fakeFetcher{docErr: linkfetch.ErrUnsupportedContentType}, storage.PreviewFailureUnsupported, true},
		"ssrf blocked":  {&fakeFetcher{docErr: linkfetch.ErrURLNotAllowed}, storage.PreviewFailureBlocked, true},
		"invalid url":   {&fakeFetcher{docErr: linkfetch.ErrInvalidURL}, storage.PreviewFailureBlocked, true},
		"timeout":       {&fakeFetcher{docErr: linkfetch.ErrTimeout}, storage.PreviewFailureTimeout, false},
		"upstream":      {&fakeFetcher{docErr: linkfetch.ErrUpstream}, storage.PreviewFailureUpstream, false},
		"anything else": {&fakeFetcher{docErr: errors.New("boom")}, storage.PreviewFailureUpstream, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			queue := &fakePreviewQueue{jobs: []storage.LinkPreviewJob{previewJob()}, targets: map[string]storage.LinkTargetState{"https://site.example/page": target("safe", true)}}
			worker, _ := previewWorker(queue, tc.fetcher)
			if _, err := worker.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			if len(queue.failures) != 1 || queue.failures[0].reason != tc.reason || queue.failures[0].terminal != tc.terminal {
				t.Fatalf("failures = %+v, want %s/%v", queue.failures, tc.reason, tc.terminal)
			}
			if len(queue.completed) != 0 {
				t.Fatal("a failed fetch stored a preview")
			}
		})
	}
}

func TestPreviewWorkerFollowsARedirectOnlyOntoAClearedTarget(t *testing.T) {
	const hop = "https://www.site.example/page"
	cases := map[string]struct {
		hopStatus string
		reason    string
		terminal  bool
		admitted  int
		completed int
	}{
		"cleared":   {"safe", "", false, 0, 1},
		"pending":   {"pending", storage.PreviewFailureRedirectRefused, false, 1, 0},
		"absent":    {"", storage.PreviewFailureRedirectRefused, false, 1, 0},
		"unknown":   {"unknown", storage.PreviewFailureRedirectRefused, true, 0, 0},
		"malicious": {"malicious", storage.PreviewFailureRedirectRefused, true, 0, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			targets := map[string]storage.LinkTargetState{"https://site.example/page": target("safe", true)}
			if tc.hopStatus != "" {
				targets[hop] = target(tc.hopStatus, true)
			}
			queue := &fakePreviewQueue{jobs: []storage.LinkPreviewJob{previewJob()}, targets: targets}
			fetcher := &fakeFetcher{redirects: []string{hop}, document: ogDocument}
			worker, _ := previewWorker(queue, fetcher)
			if _, err := worker.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			if len(queue.completed) != tc.completed {
				t.Fatalf("completed = %+v", queue.completed)
			}
			if tc.completed == 0 && (len(queue.failures) != 1 || queue.failures[0].reason != tc.reason || queue.failures[0].terminal != tc.terminal) {
				t.Fatalf("failures = %+v", queue.failures)
			}
			// A hop nobody cleared is admitted for its own scan, once, charged to
			// the message's workspace.
			if len(queue.admitted) != tc.admitted {
				t.Fatalf("admitted = %v", queue.admitted)
			}
		})
	}
}

func TestPreviewWorkerDisabledDrainsWithoutFetching(t *testing.T) {
	queue := &fakePreviewQueue{
		jobs:    []storage.LinkPreviewJob{previewJob()},
		drained: []storage.LinkPreviewRow{{ID: "p2", WorkspaceID: "ws-1", CanonicalURL: "https://site.example/page", State: "failed"}},
		expired: []storage.LinkPreviewRow{{ID: "p3", WorkspaceID: "ws-1", CanonicalURL: "https://site.example/page", State: "failed"}},
		targets: map[string]storage.LinkTargetState{"https://site.example/page": target("safe", true)},
	}
	fetcher := &fakeFetcher{document: ogDocument}
	worker, publisher := previewWorker(queue, fetcher)
	worker.SetEnabled(false)

	moved, err := worker.ProcessDue(context.Background())
	if err != nil || moved != 0 {
		t.Fatalf("ProcessDue: %d %v", moved, err)
	}
	if len(fetcher.fetched) != 0 {
		t.Fatal("a disabled worker fetched")
	}
	// Both the deadline sweep and the disabled drain announced their rows.
	if len(publisher.updates) != 2 {
		t.Fatalf("updates = %+v", publisher.updates)
	}
	// Without a fetcher the worker cannot be enabled at all.
	if w := NewLinkPreviewService(queue, nil, nil); w.enabled {
		t.Fatal("no fetcher means drain only")
	}
	w := NewLinkPreviewService(queue, nil, nil)
	w.SetEnabled(true)
	if w.enabled {
		t.Fatal("SetEnabled(true) without a fetcher must stay off")
	}
}

func TestPreviewWorkerReportsClaimFailures(t *testing.T) {
	queue := &fakePreviewQueue{claimErr: errors.New("db down")}
	worker, _ := previewWorker(queue, &fakeFetcher{})
	if _, err := worker.ProcessDue(context.Background()); err == nil {
		t.Fatal("a claim failure must surface")
	}
}

// errPreviewQueue makes every persistence step fail, so the outcome logging
// and the "another worker owns the row" silence are both exercised.
type errPreviewQueue struct {
	fakePreviewQueue
	err error
}

func (q *errPreviewQueue) CompleteLinkPreview(context.Context, storage.LinkPreviewJob, storage.LinkPreviewResult) (storage.LinkPreviewRow, error) {
	return storage.LinkPreviewRow{}, q.err
}

func (q *errPreviewQueue) FailLinkPreview(context.Context, storage.LinkPreviewJob, string, bool) (storage.LinkPreviewRow, error) {
	return storage.LinkPreviewRow{}, q.err
}

func (q *errPreviewQueue) TerminalizeExpiredLinkPreviews(context.Context) ([]storage.LinkPreviewRow, error) {
	return nil, q.err
}

func (q *errPreviewQueue) AdmitLinkScans(context.Context, string, []string, storage.LinkScanCapacity) (storage.LinkScanAdmission, error) {
	return storage.LinkScanAdmission{}, q.err
}

func TestPreviewWorkerSurvivesEveryStoreFailure(t *testing.T) {
	for name, err := range map[string]error{"conflict": storage.ErrLinkPreviewConflict, "outage": errors.New("db down")} {
		t.Run(name, func(t *testing.T) {
			queue := &errPreviewQueue{err: err}
			queue.jobs = []storage.LinkPreviewJob{previewJob(), {ID: "p2", WorkspaceID: "ws-1", CanonicalURL: "https://gone.example/x"}}
			queue.targets = map[string]storage.LinkTargetState{}
			fetcher := &fakeFetcher{redirects: []string{"https://hop.example/next"}, document: ogDocument}
			worker := NewLinkPreviewService(queue, fetcher, nil)
			worker.SetMetrics(urlsafety.NewPipelineMetrics(observability.NewMetrics(observability.Config{
				ServiceName: "chat-service", MetricsEnabled: true,
			}), "chat-service"))
			worker.SetScanCapacity(storage.LinkScanCapacity{})
			worker.SetEnabled(true)

			// A pending hop is admitted (and the admission failure logged), then
			// the refused fetch fails the row, which fails too; nothing panics.
			if moved, err := worker.ProcessDue(context.Background()); err != nil || moved != 2 {
				t.Fatalf("ProcessDue: %d %v", moved, err)
			}
			fetcher.redirects = nil
			queue.jobs = []storage.LinkPreviewJob{previewJob()}
			if _, err := worker.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			// A cancelled context silences every outcome.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			queue.jobs = []storage.LinkPreviewJob{previewJob()}
			if _, err := worker.ProcessDue(ctx); err != nil {
				t.Fatalf("ProcessDue cancelled: %v", err)
			}
		})
	}
}

func TestPreviewWorkerLoopRunsAPassAndStopsWithItsContext(t *testing.T) {
	queue := &fakePreviewQueue{claimErr: errors.New("db down")}
	worker, _ := previewWorker(queue, &fakeFetcher{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunLinkPreviewWorker(ctx, worker, time.Millisecond, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the preview loop did not stop with its context")
	}
	// A nil processor or a zero interval are both tolerated.
	RunLinkPreviewWorker(ctx, nil, 0, nil)
	logPreviewPass(context.Background(), slog.Default(), 3, nil)
}

// scrapeGauge reads one gauge line from the registry's /metrics.
func scrapeGauge(t *testing.T, metrics *observability.Metrics, name string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, name+"{") {
			return line
		}
	}
	return ""
}

// The backlog gauge follows the queue on the disabled pass too: the drain the
// sweep runs empties the queue, and the gauge must not keep reporting the work
// the last enabled pass saw.
func TestPreviewWorkerRefreshesTheBacklogGaugeWhenDisabled(t *testing.T) {
	metrics := observability.NewMetrics(observability.Config{ServiceName: "chat-service", MetricsEnabled: true})
	queue := &fakePreviewQueue{
		backlog: 7,
		targets: map[string]storage.LinkTargetState{"https://site.example/page": target("safe", true)},
	}
	worker, _ := previewWorker(queue, &fakeFetcher{document: ogDocument})
	worker.SetMetrics(urlsafety.NewPipelineMetrics(metrics, "chat-service"))

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if got := scrapeGauge(t, metrics, "nchat_link_preview_pending"); !strings.HasSuffix(got, " 7") {
		t.Fatalf("gauge after the enabled pass = %q, want 7", got)
	}

	// Switching the feature off drains the queue; the gauge follows it.
	worker.SetEnabled(false)
	queue.drained = []storage.LinkPreviewRow{{ID: "p2", WorkspaceID: "ws-1", CanonicalURL: "https://site.example/page", State: "failed"}}
	queue.backlog = 0
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue disabled: %v", err)
	}
	if got := scrapeGauge(t, metrics, "nchat_link_preview_pending"); !strings.HasSuffix(got, " 0") {
		t.Fatalf("gauge after the disabled pass = %q, want 0", got)
	}
}
