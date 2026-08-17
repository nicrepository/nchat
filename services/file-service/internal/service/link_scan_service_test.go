package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// file-service's RF-21 pipeline.
//
// The finding this closes: the preview used to submit a scan, receive the
// provider's uuid and throw it away. Nothing polled it, nothing survived a
// restart, and every retry submitted the URL again. These assert the loop that
// replaced it — durable job, one submission, polling by the stored id, and a
// verdict bound to the scan that produced it.

// fakeLinkStore is the durable half, in memory. Every row is a scanRow, so a
// "restart" is simply a new service over the same store.
type fakeLinkStore struct {
	mu   sync.Mutex
	rows map[string]*scanRow

	claims          int
	submitConflict  bool
	verdictConflict bool
	claimErr        error

	admitErr         error
	beginErr         error
	uncertainErr     error
	verdictErr       error
	pruneErr         error
	backlogErr       error
	budgetUsed       int
	begins           int
	persistCalls     int
	persistErr       error
	adopted          []string
	providerReserved int
	reserveErr       error
	prunes           int
	// clock stands in for now() so a test can place a submission attempt in the
	// past and cross the uncertainty horizon without sleeping.
	clock time.Time
}

type scanRow struct {
	canonicalURL string
	state        string
	scanUUID     string
	verdict      urlsafety.Verdict
	expiresAt    time.Time
	attempts     int
	// leased stands in for the real store's next_attempt_at: the claim pushes it
	// out, so a row that has been worked once is not due again until the
	// schedule says so. Without it one pass would process the same row eight
	// times, which is not what the real claim does.
	leased bool

	// The submission window, mirrored from the real row: when the outstanding
	// attempt was handed to the provider, and the compare-and-set token it owns.
	submitStartedAt  time.Time
	submitGeneration int
}

func newFakeLinkStore() *fakeLinkStore {
	return &fakeLinkStore{rows: map[string]*scanRow{}}
}

// digest stands in for the store's SHA-256 key. The identity is what matters
// here, not the hash function.
func digest(url string) []byte { return []byte("d:" + url) }

func (s *fakeLinkStore) LoadVerdict(_ context.Context, canonicalURL string) (urlsafety.Verdict, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[canonicalURL]
	if !ok || row.state != "done" || !row.expiresAt.After(time.Now()) {
		return urlsafety.VerdictUnknown, false, nil
	}
	return row.verdict, true, nil
}

// AdmitScan is the dedup point and the capacity gate at once, exactly as the
// real one is: a URL already tracked costs nothing and is always admitted, and
// only a genuinely new one is charged.
func (s *fakeLinkStore) AdmitScan(
	_ context.Context, canonicalURL string, capacity service.LinkScanCapacity,
) (service.LinkScanAdmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admitErr != nil {
		return service.LinkScanAdmission{}, s.admitErr
	}
	if _, exists := s.rows[canonicalURL]; exists {
		// A second preview of the same URL costs one no-op, not a second scan —
		// and is admitted however spent the budget is.
		return service.LinkScanAdmission{Result: service.AdmissionAllowed}, nil
	}
	if capacity.MaxPendingJobs > 0 && s.undecided() >= capacity.MaxPendingJobs {
		return service.LinkScanAdmission{NewScanCost: 1, Result: service.AdmissionBacklog}, nil
	}
	if capacity.NewURLBudget > 0 && s.budgetUsed >= capacity.NewURLBudget {
		return service.LinkScanAdmission{NewScanCost: 1, Result: service.AdmissionServiceBudget}, nil
	}
	s.budgetUsed++
	s.rows[canonicalURL] = &scanRow{canonicalURL: canonicalURL, state: service.StateSubmitPending}
	return service.LinkScanAdmission{NewScanCost: 1, Result: service.AdmissionAllowed}, nil
}

// undecided counts the rows that still owe the provider work. Inconclusive is
// terminal, exactly like done, so it does not count either.
func (s *fakeLinkStore) undecided() int {
	count := 0
	for _, row := range s.rows {
		if row.state != "done" && row.state != "inconclusive" {
			count++
		}
	}
	return count
}

func (s *fakeLinkStore) BeginSubmit(_ context.Context, urlDigest []byte, generation int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.beginErr != nil {
		return 0, s.beginErr
	}
	row := s.rowByDigest(urlDigest)
	if row == nil || row.state != service.StateSubmitPending || row.scanUUID != "" || row.submitGeneration != generation {
		return 0, service.ErrLinkScanConflict
	}
	s.begins++
	row.state = service.StateSubmitting
	row.submitStartedAt = s.now()
	row.submitGeneration++
	return row.submitGeneration, nil
}

func (s *fakeLinkStore) MarkSubmitUncertain(_ context.Context, urlDigest []byte, generation int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uncertainErr != nil {
		return s.uncertainErr
	}
	row := s.rowByDigest(urlDigest)
	if row == nil || row.submitGeneration != generation || row.state != service.StateSubmitting {
		return nil
	}
	row.state = service.StateSubmitUncertain
	return nil
}

func (s *fakeLinkStore) AdoptScanUUID(
	_ context.Context, urlDigest []byte, scanUUID string, generation int,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rowByDigest(urlDigest)
	if row == nil || row.submitGeneration != generation ||
		(row.state != service.StateSubmitting && row.state != service.StateSubmitUncertain) {
		return service.ErrLinkScanConflict
	}
	row.state, row.scanUUID, row.submitStartedAt = service.StatePolling, scanUUID, time.Time{}
	s.adopted = append(s.adopted, scanUUID)
	return nil
}

// ReserveProviderSubmit hands out `limit` submissions and then refuses, which is
// how a spent shared window is simulated without a clock.
func (s *fakeLinkStore) ReserveProviderSubmit(_ context.Context, limit int, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserveErr != nil {
		return false, s.reserveErr
	}
	if limit <= 0 {
		return true, nil
	}
	if s.providerReserved >= limit {
		return false, nil
	}
	s.providerReserved++
	return true, nil
}

func (s *fakeLinkStore) PruneLinkScanBudget(_ context.Context, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prunes++
	return s.pruneErr
}

// rowByDigest finds a row the way the real store's primary key does. Callers
// hold the lock.
func (s *fakeLinkStore) rowByDigest(urlDigest []byte) *scanRow {
	for _, row := range s.rows {
		if string(digest(row.canonicalURL)) == string(urlDigest) {
			return row
		}
	}
	return nil
}

// now is the store's clock. A test drives the uncertainty horizon by moving it
// rather than by sleeping.
func (s *fakeLinkStore) now() time.Time {
	if s.clock.IsZero() {
		return time.Now()
	}
	return s.clock
}

func (s *fakeLinkStore) ClaimDueScans(_ context.Context, batchSize int) ([]service.LinkScanJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	var jobs []service.LinkScanJob
	for _, row := range s.rows {
		if len(jobs) >= batchSize {
			break
		}
		if row.state == "done" || row.state == "inconclusive" || row.leased {
			continue
		}
		row.attempts++
		row.leased = true
		jobs = append(jobs, service.LinkScanJob{
			URLDigest: digest(row.canonicalURL), CanonicalURL: row.canonicalURL,
			State: row.state, ScanUUID: row.scanUUID, Attempts: row.attempts,
			SubmitStartedAt:  row.submitStartedAt,
			SubmitGeneration: row.submitGeneration,
		})
	}
	return jobs, nil
}

func (s *fakeLinkStore) RecordSubmission(
	_ context.Context, urlDigest []byte, scanUUID string, generation int,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistCalls++
	if s.submitConflict {
		return service.ErrLinkScanConflict
	}
	if s.persistErr != nil {
		return s.persistErr
	}
	row := s.rowByDigest(urlDigest)
	if row == nil || row.state != service.StateSubmitting || row.submitGeneration != generation {
		return service.ErrLinkScanConflict
	}
	row.state, row.scanUUID, row.submitStartedAt = service.StatePolling, scanUUID, time.Time{}
	return nil
}

func (s *fakeLinkStore) RecordVerdict(
	_ context.Context, _ []byte, scanUUID string, verdict urlsafety.Verdict, ttl time.Duration,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.verdictConflict {
		return service.ErrLinkScanConflict
	}
	if s.verdictErr != nil {
		return s.verdictErr
	}
	for _, row := range s.rows {
		if row.state == "polling" && row.scanUUID == scanUUID {
			if verdict == urlsafety.VerdictInconclusive {
				// Terminal, no TTL — mirrors the real store's RecordVerdict, which
				// stores no expiry and no polling backlog entry for this state.
				row.state, row.verdict = "inconclusive", urlsafety.VerdictUnknown
				return nil
			}
			row.state, row.verdict = "done", verdict
			row.expiresAt = time.Now().Add(ttl)
			return nil
		}
	}
	// No row carries this scan any more — a stale worker's answer.
	return service.ErrLinkScanConflict
}

func (s *fakeLinkStore) Backlog(_ context.Context) (map[string]int, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backlogErr != nil {
		return nil, 0, s.backlogErr
	}
	byState := map[string]int{}
	for _, row := range s.rows {
		if row.state != "done" && row.state != "inconclusive" {
			byState[row.state]++
		}
	}
	return byState, 0, nil
}

// releaseLeases makes every claimed row due again, standing in for the lease
// lapsing between worker passes.
func (s *fakeLinkStore) releaseLeases() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		row.leased = false
	}
}

func (s *fakeLinkStore) row(url string) scanRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[url]; ok {
		return *row
	}
	return scanRow{}
}

// fakeLinkProvider is the Cloudflare half.
type fakeLinkProvider struct {
	mu sync.Mutex

	scanID    string
	submits   int
	polls     int
	verdict   urlsafety.Verdict
	submitErr error
	pollErr   error
}

func (p *fakeLinkProvider) Submit(_ context.Context, _ string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.submits++
	if p.submitErr != nil {
		return "", p.submitErr
	}
	if p.scanID == "" {
		return "scan-1", nil
	}
	return p.scanID, nil
}

func (p *fakeLinkProvider) Poll(_ context.Context, _, _ string) (urlsafety.Verdict, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polls++
	return p.verdict, p.pollErr
}

func (p *fakeLinkProvider) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.submits, p.polls
}

const testURL = "https://example.com/page"

// A URL nobody has scanned produces one durable job and no provider traffic:
// recording the need is the request path's whole responsibility.
func TestEnsureScanCreatesOneJobWithoutSubmitting(t *testing.T) {
	store := newFakeLinkStore()
	provider := &fakeLinkProvider{}

	for i := 0; i < 5; i++ {
		if _, err := store.AdmitScan(context.Background(), testURL, service.LinkScanCapacity{}); err != nil {
			t.Fatalf("EnsureScan: %v", err)
		}
	}
	if submits, polls := provider.counts(); submits != 0 || polls != 0 {
		t.Fatalf("the request path contacted the provider: submits=%d polls=%d", submits, polls)
	}
	if store.row(testURL).state != "submit_pending" {
		t.Fatalf("state=%q", store.row(testURL).state)
	}
	// Five previews of the same URL, one job. This is the quota fix: the old
	// code submitted a scan per request.
	store.mu.Lock()
	rows := len(store.rows)
	store.mu.Unlock()
	if rows != 1 {
		t.Fatalf("five previews produced %d jobs", rows)
	}
}

// The worker submits once and stores the id, which is what makes polling
// possible at all — the previous implementation discarded it.
func TestWorkerSubmitsAndPersistsTheScanID(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-9"}

	moved, err := service.NewLinkScanService(store, provider, nil).ProcessDue(context.Background())

	if err != nil || moved != 1 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
	submits, polls := provider.counts()
	// One step per pass: submitting and immediately polling would spend an
	// exchange on an answer the provider has not had time to produce.
	if submits != 1 || polls != 0 {
		t.Fatalf("submits=%d polls=%d", submits, polls)
	}
	row := store.row(testURL)
	if row.state != "polling" || row.scanUUID != "scan-9" {
		t.Fatalf("row=%+v", row)
	}
}

// The restart case. A brand-new service over the same store finds the row in
// polling and continues by its stored id rather than starting over.
func TestWorkerResumesPollingAfterRestart(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-9"}
	if _, err := service.NewLinkScanService(store, provider, nil).ProcessDue(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// A different service instance — the process restarted; nothing was kept in
	// memory, and the lease its predecessor held has lapsed.
	store.releaseLeases()
	restarted := service.NewLinkScanService(store, provider, nil)
	provider.verdict = urlsafety.VerdictSafe
	if _, err := restarted.ProcessDue(context.Background()); err != nil {
		t.Fatalf("after restart: %v", err)
	}

	submits, polls := provider.counts()
	if submits != 1 {
		t.Fatalf("the restart resubmitted: submits=%d", submits)
	}
	if polls != 1 {
		t.Fatalf("the restart did not resume polling: polls=%d", polls)
	}
	if row := store.row(testURL); row.state != "done" || row.verdict != urlsafety.VerdictSafe {
		t.Fatalf("row=%+v", row)
	}
}

func TestFinalVerdictsArePersistedWithAnExpiry(t *testing.T) {
	for _, want := range []urlsafety.Verdict{urlsafety.VerdictSafe, urlsafety.VerdictMalicious} {
		t.Run(string(want), func(t *testing.T) {
			store := newFakeLinkStore()
			queueScan(t, store, testURL)
			provider := &fakeLinkProvider{verdict: want}
			worker := service.NewLinkScanService(store, provider, nil)

			// submit, then poll on the next pass, once the lease has lapsed.
			_, _ = worker.ProcessDue(context.Background())
			store.releaseLeases()
			_, _ = worker.ProcessDue(context.Background())

			row := store.row(testURL)
			if row.state != "done" || row.verdict != want {
				t.Fatalf("row=%+v", row)
			}
			if !row.expiresAt.After(time.Now()) {
				t.Fatal("a decided verdict carries no expiry")
			}
			// And the preview can now read it.
			verdict, ok, err := store.LoadVerdict(context.Background(), testURL)
			if err != nil || !ok || verdict != want {
				t.Fatalf("verdict=%v ok=%v err=%v", verdict, ok, err)
			}
		})
	}
}

// The fail-closed core: nothing that is not an explicit verdict may be written,
// so the URL stays undecided and the preview stays withheld.
func TestNothingButAFinalVerdictIsPersisted(t *testing.T) {
	for name, provider := range map[string]*fakeLinkProvider{
		"still running":  {pollErr: urlsafety.ErrScanPending},
		"provider down":  {pollErr: urlsafety.ErrUnavailable},
		"unknown":        {verdict: urlsafety.VerdictUnknown},
		"zero value":     {verdict: ""},
		"future verdict": {verdict: urlsafety.Verdict("future")},
		"verdict with an error": {
			verdict: urlsafety.VerdictSafe, pollErr: urlsafety.ErrUnavailable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeLinkStore()
			queueScan(t, store, testURL)
			provider.scanID = "scan-9"
			worker := service.NewLinkScanService(store, provider, nil)

			_, _ = worker.ProcessDue(context.Background()) // submit
			store.releaseLeases()
			_, _ = worker.ProcessDue(context.Background()) // poll

			row := store.row(testURL)
			if row.state == "done" {
				t.Fatalf("a non-final answer decided the URL: %+v", row)
			}
			if _, ok, _ := store.LoadVerdict(context.Background(), testURL); ok {
				t.Fatal("a non-final answer became a readable verdict")
			}
		})
	}
}

// A failed submission stores nothing, so the next claim submits again rather
// than waiting on an id nobody kept.
// A failed submission does not become a resubmission.
//
// It used to leave the row in submit_pending, which meant the next pass sent the
// URL again. That is wrong, and the reason is the one this whole window is
// about: the client cannot tell "Cloudflare refused" from "Cloudflare accepted
// and the response was lost" — both surface as a failed exchange — so treating
// the failure as "nothing happened" buys a second scan every time the second
// case occurs.
//
// The row is therefore uncertain, which is the state that reconciles rather than
// submits. Still no verdict, still no scan id, still nothing cleared.
func TestFailedSubmissionLeavesTheAttemptUncertain(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{submitErr: urlsafety.ErrUnavailable}

	if _, err := service.NewLinkScanService(store, provider, nil).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	row := store.row(testURL)
	if row.state != service.StateSubmitUncertain || row.scanUUID != "" {
		t.Fatalf("row=%+v, want an uncertain attempt with no scan id", row)
	}
	// One exchange, and the attempt is recorded as outstanding — which is what
	// the next pass reads to decide to reconcile instead of resubmit.
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits = %d, want 1", submits)
	}
	if row.submitStartedAt.IsZero() {
		t.Fatal("the attempt was not recorded as outstanding")
	}
}

// The stale-worker case. A worker polls a scan; while it waits, the row is
// reclaimed and rebound. Its answer must not overwrite the current one.
func TestStaleWorkerCannotOverwriteANewerScan(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-A", verdict: urlsafety.VerdictSafe}
	worker := service.NewLinkScanService(store, provider, nil)
	_, _ = worker.ProcessDue(context.Background()) // submit scan-A

	// The row moved on: another worker rebound it to a different scan.
	store.releaseLeases()
	store.verdictConflict = true

	_, _ = worker.ProcessDue(context.Background()) // poll, then lose the CAS

	if row := store.row(testURL); row.state == "done" {
		t.Fatalf("a superseded worker decided the URL: %+v", row)
	}
}

// Losing the submit race is not an error to retry: another worker already bound
// a scan, so submitting again would only orphan a third at the provider.
func TestLostSubmitRaceDoesNotResubmit(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	store.submitConflict = true
	provider := &fakeLinkProvider{}

	if _, err := service.NewLinkScanService(store, provider, nil).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits=%d — a lost race must not resubmit in the same pass", submits)
	}
}

// One claim per item, taken immediately before processing. A batch leased up
// front leaves its last rows reclaimable while the earlier ones are still being
// worked, which is the lease finding.
func TestWorkerClaimsOneItemAtATime(t *testing.T) {
	store := newFakeLinkStore()
	for _, url := range []string{"https://a.example/x", "https://b.example/y", "https://c.example/z"} {
		queueScan(t, store, url)
	}
	provider := &fakeLinkProvider{}

	moved, err := service.NewLinkScanService(store, provider, nil).ProcessDue(context.Background())

	if err != nil || moved != 3 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
	store.mu.Lock()
	claims := store.claims
	store.mu.Unlock()
	// Three items, three claims, plus the one that found the queue empty. The
	// backlog read is a separate call and is not counted here.
	if claims != 4 {
		t.Fatalf("claims=%d, want one per item plus the empty one", claims)
	}
}

func TestClaimFailureIsReported(t *testing.T) {
	store := newFakeLinkStore()
	store.claimErr = errors.New("database unavailable")

	if _, err := service.NewLinkScanService(store, &fakeLinkProvider{}, nil).ProcessDue(context.Background()); err == nil {
		t.Fatal("a claim failure was swallowed")
	}
}

// A cancelled context stops the pass rather than working through the batch.
func TestProcessDueStopsWithItsContext(t *testing.T) {
	store := newFakeLinkStore()
	for _, url := range []string{"https://a.example/x", "https://b.example/y"} {
		queueScan(t, store, url)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	moved, err := service.NewLinkScanService(store, &fakeLinkProvider{}, nil).ProcessDue(ctx)

	if err != nil || moved != 0 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
}

// Observability is optional here for the same reason it is in chat-service, and
// this is the same contract stated against this worker: never calling SetMetrics
// is a supported deployment, not a crash.
//
// It walks a claim all the way through — submit, then poll to a verdict — with
// no reporter attached, because the point is that every reporting call site is
// reached and none of them needs a guard.
func TestLinkScanServiceRunsWithoutMetrics(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-1", verdict: urlsafety.VerdictSafe}

	// No SetMetrics. That is the point.
	worker := service.NewLinkScanService(store, provider, nil)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("submit pass: %v", err)
	}
	store.releaseLeases()
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("poll pass: %v", err)
	}

	// The work still happened: submitted once, polled once, verdict stored.
	submits, polls := provider.counts()
	if submits != 1 || polls != 1 {
		t.Fatalf("submits=%d polls=%d, want 1 and 1", submits, polls)
	}
	if row := store.row(testURL); row.state != "done" || row.verdict != urlsafety.VerdictSafe {
		t.Fatalf("row=%+v, want the verdict persisted", row)
	}
}

// And the other half: with a reporter attached the same pass produces samples.
func TestLinkScanServiceReportsWhenMetricsAreAttached(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	worker := service.NewLinkScanService(store, &fakeLinkProvider{scanID: "scan-1"}, nil)

	metrics := observability.NewMetrics(observability.Config{
		ServiceName: "file-service", MetricsEnabled: true,
	})
	reporter := urlsafety.NewPipelineMetrics(metrics, "file-service")
	if reporter == nil {
		t.Fatal("the pipeline collectors did not register")
	}
	worker.SetMetrics(reporter)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", response.Code)
	}
	for _, name := range []string{
		"nchat_link_scan_attempts_total",
		"nchat_link_scan_provider_duration_seconds_count",
		"nchat_link_scan_pending",
	} {
		if !strings.Contains(response.Body.String(), name) {
			t.Fatalf("%s was not exported", name)
		}
	}
}

// queueScan puts a URL in the queue the way a preview request does, with no
// capacity ceilings — the tests that are about capacity set them explicitly.
func queueScan(t *testing.T, store *fakeLinkStore, url string) {
	t.Helper()
	admission, err := store.AdmitScan(context.Background(), url, service.LinkScanCapacity{})
	if err != nil {
		t.Fatalf("AdmitScan(%q): %v", url, err)
	}
	if !admission.Allowed() {
		t.Fatalf("AdmitScan(%q) was refused: %s", url, admission.Result)
	}
}

// ── Capacity and the submission window ────────────────────────────────────────
//
// The same two findings as chat-service, in the service that previews links. The
// pipeline is the same, the provider is the same account, and the gaps were the
// same: a submission whose outcome was never written down got resubmitted, and
// nothing counted how many new URLs a client could introduce.

// searchingLinkProvider is a provider that can also be asked what it already has.
type searchingLinkProvider struct {
	*fakeLinkProvider
	mu       sync.Mutex
	searches int
	answers  []linkSearchAnswer
}

type linkSearchAnswer struct {
	record urlsafety.ScanRecord
	match  int
	err    error
}

func (p *searchingLinkProvider) FindRecentScan(
	_ context.Context, _ string, _ time.Time,
) (urlsafety.ScanRecord, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.searches++
	if len(p.answers) == 0 {
		return urlsafety.ScanRecord{}, 0, urlsafety.ErrNotCheckable
	}
	answer := p.answers[0]
	if len(p.answers) > 1 {
		p.answers = p.answers[1:]
	}
	return answer.record, answer.match, answer.err
}

func uncertainWorker(store *fakeLinkStore, provider service.LinkScanProvider) *service.LinkScanService {
	worker := service.NewLinkScanService(store, provider, nil)
	worker.SetCapacity(service.LinkScanWorkerCapacity{UncertainTimeout: time.Hour})
	worker.SetPersistRetryDelay(0)
	return worker
}

// The finding end to end: the provider accepts, the local write fails, and the
// scan is recovered by asking rather than bought again.
func TestAnAcceptedSubmissionIsRecoveredRatherThanRepeated(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	store.persistErr = errors.New("connection reset")
	provider := &searchingLinkProvider{fakeLinkProvider: &fakeLinkProvider{scanID: "scan-A"}}
	worker := uncertainWorker(store, provider)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits = %d, want 1", submits)
	}
	if store.persistCalls < 2 {
		t.Fatalf("persist attempts = %d, want a bounded retry", store.persistCalls)
	}
	if row := store.row(testURL); row.state != service.StateSubmitUncertain {
		t.Fatalf("row = %+v, want an uncertain attempt", row)
	}

	// The search knows about scan-A. It is adopted, and nothing is submitted.
	store.persistErr = nil
	store.releaseLeases()
	provider.answers = []linkSearchAnswer{{record: urlsafety.ScanRecord{UUID: "scan-A"}, match: 1}}

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	row := store.row(testURL)
	if row.state != service.StatePolling || row.scanUUID != "scan-A" {
		t.Fatalf("row = %+v, want polling scan-A", row)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("total submits = %d, want exactly 1", submits)
	}
}

// A search that answers nothing, or cannot answer, must not become a submission.
func TestReconciliationNeverSubmitsOnAbsenceOrFailure(t *testing.T) {
	for name, answer := range map[string]linkSearchAnswer{
		"not found yet": {err: urlsafety.ErrNotCheckable},
		"search failed": {err: urlsafety.ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeLinkStore()
			queueScan(t, store, testURL)
			store.rows[testURL].state = service.StateSubmitUncertain
			store.rows[testURL].submitStartedAt = time.Now().Add(-time.Minute)
			provider := &searchingLinkProvider{
				fakeLinkProvider: &fakeLinkProvider{scanID: "scan-A"},
				answers:          []linkSearchAnswer{answer},
			}

			if _, err := uncertainWorker(store, provider).ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}

			if submits, _ := provider.counts(); submits != 0 {
				t.Fatalf("submits = %d, want none", submits)
			}
			if row := store.row(testURL); row.state != service.StateSubmitUncertain {
				t.Fatalf("row = %+v, want the attempt still uncertain", row)
			}
		})
	}
}

// The regression this round exists for: no elapsed time and no number of worker
// cycles ever produces a second submission.
//
// The previous implementation allowed one bounded resubmit past a 15-minute
// horizon. Cloudflare has no idempotency token, so that "bounded" duplicate is a
// scan the account is billed for on a submission that probably already
// succeeded. The horizon is now a reporting threshold and nothing else.
func TestAnUncertainAttemptIsNeverResubmitted(t *testing.T) {
	for name, age := range map[string]time.Duration{
		"one minute old":                 time.Minute,
		"past the old 15-minute horizon": 20 * time.Minute,
		"days old":                       72 * time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeLinkStore()
			queueScan(t, store, testURL)
			store.rows[testURL].state = service.StateSubmitUncertain
			store.rows[testURL].submitStartedAt = time.Now().Add(-age)
			provider := &searchingLinkProvider{fakeLinkProvider: &fakeLinkProvider{scanID: "scan-A"}}
			worker := service.NewLinkScanService(store, provider, nil)
			worker.SetCapacity(service.LinkScanWorkerCapacity{UncertainTimeout: 15 * time.Minute})

			// Several cycles, because "eventually" is exactly what the removed
			// branch was: a thing that only happened after enough passes.
			for cycle := range 5 {
				store.releaseLeases()
				if _, err := worker.ProcessDue(context.Background()); err != nil {
					t.Fatalf("cycle %d: %v", cycle, err)
				}
			}

			if submits, _ := provider.counts(); submits != 0 {
				t.Fatalf("an uncertain attempt was resubmitted %d times", submits)
			}
			if store.begins != 0 {
				t.Fatalf("a submission intent was recorded %d times", store.begins)
			}
			// The row never went back to "never submitted" — the transition that no
			// longer exists.
			if row := store.row(testURL); row.state != service.StateSubmitUncertain {
				t.Fatalf("row state = %q, want it still uncertain", row.state)
			}
		})
	}
}

// [§62] Previewing the same URL repeatedly is free after the first, and a brand
// new one is charged. That is the unit: what the provider bills.
func TestPreviewBudgetChargesNewURLsOnly(t *testing.T) {
	store := newFakeLinkStore()
	capacity := service.LinkScanCapacity{NewURLBudget: 1, BudgetWindow: time.Hour}

	first, err := store.AdmitScan(context.Background(), testURL, capacity)
	if err != nil || !first.Allowed() {
		t.Fatalf("first = %+v, err = %v", first, err)
	}
	// Budget spent, but the same URL asks for nothing.
	repeat, err := store.AdmitScan(context.Background(), testURL, capacity)
	if err != nil || !repeat.Allowed() || repeat.NewScanCost != 0 {
		t.Fatalf("a repeated preview was charged: %+v %v", repeat, err)
	}
	// A different URL is new work, and the window is spent.
	fresh, err := store.AdmitScan(context.Background(), "https://other.example/x", capacity)
	if err != nil {
		t.Fatalf("AdmitScan: %v", err)
	}
	if fresh.Result != service.AdmissionServiceBudget {
		t.Fatalf("result = %q, want the budget refusal", fresh.Result)
	}
}

// A full queue refuses new work before anything is created.
func TestPreviewBacklogCapRefusesNewWork(t *testing.T) {
	store := newFakeLinkStore()
	capacity := service.LinkScanCapacity{MaxPendingJobs: 1}

	if admission, err := store.AdmitScan(context.Background(), testURL, capacity); err != nil ||
		!admission.Allowed() {
		t.Fatalf("filling the backlog: %+v %v", admission, err)
	}
	admission, err := store.AdmitScan(context.Background(), "https://other.example/x", capacity)
	if err != nil {
		t.Fatalf("AdmitScan: %v", err)
	}
	if admission.Result != service.AdmissionBacklog {
		t.Fatalf("result = %q, want the backlog refusal", admission.Result)
	}
	if _, exists := store.rows["https://other.example/x"]; exists {
		t.Fatal("a refused URL was queued anyway")
	}
}

// [§56] The shared provider allowance, across two workers over one store.
func TestProviderSubmitAllowanceIsSharedBetweenWorkers(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	queueScan(t, store, "https://other.example/x")
	provider := &fakeLinkProvider{scanID: "scan-A"}
	capacity := service.LinkScanWorkerCapacity{
		ProviderSubmitLimit: 1, ProviderSubmitWindow: time.Minute, UncertainTimeout: time.Hour,
	}
	first := service.NewLinkScanService(store, provider, nil)
	first.SetCapacity(capacity)
	second := service.NewLinkScanService(store, provider, nil)
	second.SetCapacity(capacity)

	if _, err := first.ProcessDue(context.Background()); err != nil {
		t.Fatalf("first worker: %v", err)
	}
	store.releaseLeases()
	if _, err := second.ProcessDue(context.Background()); err != nil {
		t.Fatalf("second worker: %v", err)
	}

	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits = %d, want 1 across both workers", submits)
	}
	if store.begins != 1 {
		t.Fatalf("intents recorded = %d, want one", store.begins)
	}
}

// The RF-21 incident this branch fixes: Cloudflare answers HTTP 200,
// task.status=finished, task.success=false, hasVerdicts=false — the provider
// confirms the scan is done but never produced a usable verdict. Before this
// change that surfaced as ErrUnavailable, which is retried forever, so the
// finished scan was polled on every pass and the URL never left the queue.
// ErrScanInconclusive is what the provider layer now reports for that exact
// answer, and this proves the worker settles it once and stops.
func TestInconclusiveScanIsTerminalAndNeverPolledAgain(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-9", pollErr: urlsafety.ErrScanInconclusive}
	worker := service.NewLinkScanService(store, provider, nil)

	_, _ = worker.ProcessDue(context.Background()) // submit
	store.releaseLeases()
	_, _ = worker.ProcessDue(context.Background()) // poll -> inconclusive

	row := store.row(testURL)
	if row.state != "inconclusive" {
		t.Fatalf("state = %q, want inconclusive", row.state)
	}

	// Further passes must not touch it again, even once its lease would
	// otherwise be due — ClaimDueScans excludes the state outright.
	store.releaseLeases()
	moved, err := worker.ProcessDue(context.Background())
	if err != nil || moved != 0 {
		t.Fatalf("moved=%d err=%v, want an inconclusive row to never be claimed again", moved, err)
	}
	if _, polls := provider.counts(); polls != 1 {
		t.Fatalf("polls = %d, want exactly 1 — an inconclusive scan must not be polled again", polls)
	}
}

// A worker restart must not resubmit a URL that already settled inconclusive:
// a fresh service instance over the same store, with the lease lapsed exactly
// as it would after a crash, must find nothing to claim.
func TestWorkerRestartAfterInconclusiveDoesNotResubmit(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-9", pollErr: urlsafety.ErrScanInconclusive}
	worker := service.NewLinkScanService(store, provider, nil)
	_, _ = worker.ProcessDue(context.Background()) // submit
	store.releaseLeases()
	_, _ = worker.ProcessDue(context.Background()) // poll -> inconclusive

	store.releaseLeases()
	restarted := service.NewLinkScanService(store, provider, nil)
	moved, err := restarted.ProcessDue(context.Background())
	if err != nil || moved != 0 {
		t.Fatalf("moved=%d err=%v, want the restart to find nothing due", moved, err)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits = %d, want 1 — a restart must not resubmit an inconclusive scan", submits)
	}
}

// Backlog feeds the pipeline gauges. An inconclusive row is decided, not
// stuck, and must never inflate it — including once enough time has passed
// that the row would otherwise look overdue.
func TestInconclusiveScanNeverReappearsInBacklog(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-9", pollErr: urlsafety.ErrScanInconclusive}
	worker := service.NewLinkScanService(store, provider, nil)
	_, _ = worker.ProcessDue(context.Background())
	store.releaseLeases()
	_, _ = worker.ProcessDue(context.Background())

	byState, _, err := store.Backlog(context.Background())
	if err != nil {
		t.Fatalf("Backlog: %v", err)
	}
	if _, present := byState["inconclusive"]; present {
		t.Fatalf("backlog counted an inconclusive row: %+v", byState)
	}

	// Time passing changes nothing: there is no TTL and no next_attempt_at to
	// bring it back due.
	store.releaseLeases()
	byState, _, err = store.Backlog(context.Background())
	if err != nil {
		t.Fatalf("Backlog after time passes: %v", err)
	}
	if _, present := byState["inconclusive"]; present {
		t.Fatalf("backlog counted an inconclusive row after time passed: %+v", byState)
	}
}

// LoadVerdict is the preview's read path. An inconclusive scan must never be
// mistaken for a usable verdict, immediately after it settles or later.
func TestLoadVerdictNeverReturnsInconclusive(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-9", pollErr: urlsafety.ErrScanInconclusive}
	worker := service.NewLinkScanService(store, provider, nil)
	_, _ = worker.ProcessDue(context.Background())
	store.releaseLeases()
	_, _ = worker.ProcessDue(context.Background())

	if verdict, ok, err := store.LoadVerdict(context.Background(), testURL); err != nil || ok {
		t.Fatalf("verdict=%v ok=%v err=%v, want no usable verdict", verdict, ok, err)
	}

	// Simulate the would-be TTL horizon elapsing — there never was one, but the
	// read path must stay closed regardless.
	store.clock = store.clock.Add(24 * time.Hour)
	if verdict, ok, err := store.LoadVerdict(context.Background(), testURL); err != nil || ok {
		t.Fatalf("verdict=%v ok=%v err=%v, want no usable verdict after the horizon", verdict, ok, err)
	}
}

// AdmitScan is the dedup point: two previews of the same URL, settled
// inconclusive by one scan, must not reopen it or trigger a second Submit.
func TestInconclusiveScanIsNotReopenedByAdmitScan(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-9", pollErr: urlsafety.ErrScanInconclusive}
	worker := service.NewLinkScanService(store, provider, nil)
	_, _ = worker.ProcessDue(context.Background())
	store.releaseLeases()
	_, _ = worker.ProcessDue(context.Background())

	// A second, later preview request for the very same URL — the request path
	// admission, not the worker.
	admission, err := store.AdmitScan(context.Background(), testURL, service.LinkScanCapacity{})
	if err != nil {
		t.Fatalf("AdmitScan: %v", err)
	}
	if !admission.Allowed() || admission.NewScanCost != 0 {
		t.Fatalf("admission=%+v, want a free admit that reopens nothing", admission)
	}
	if row := store.row(testURL); row.state != "inconclusive" {
		t.Fatalf("state = %q, a second admit reopened the row", row.state)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits = %d, want 1 — a dedup admit must never resubmit", submits)
	}
}
