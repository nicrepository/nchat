package urlsafety

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
)

// The pipeline collectors, asserted on the two things that actually go wrong
// with metrics: they fail to register (so an operator sees nothing), or they
// carry a label whose values a client controls (so one hostile sender can create
// unbounded series and take the scrape down).
//
// A URL, a hostname, a query, a message id, a scan uuid or a user id must never
// appear as a label value. The tests below assert that structurally — by
// checking the declared label *names* — rather than by inspecting samples, so a
// future call site that tried to pass one would have nowhere to put it.

func testMetrics(t *testing.T) *observability.Metrics {
	t.Helper()
	return observability.NewMetrics(observability.Config{
		ServiceName: "chat-service", MetricsEnabled: true,
	})
}

func TestPipelineMetricsRegisterOnAFreshRegistry(t *testing.T) {
	if metrics := NewPipelineMetrics(testMetrics(t), "chat-service"); metrics == nil {
		t.Fatal("the pipeline collectors did not register")
	}
}

// Registering the same collectors twice on one registry panics in
// client_golang. That must not be reachable: a service wires this once, and a
// second attempt has to be a nil result rather than a crashed process.
func TestPipelineMetricsDoNotPanicOnDuplicateRegistration(t *testing.T) {
	shared := testMetrics(t)
	if first := NewPipelineMetrics(shared, "chat-service"); first == nil {
		t.Fatal("the first registration failed")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("a duplicate registration panicked: %v", recovered)
		}
	}()
	if second := NewPipelineMetrics(shared, "chat-service"); second != nil {
		t.Fatal("a duplicate registration must not produce a second reporter")
	}
}

// A nil reporter is a supported wiring — observability is optional — and every
// method must tolerate it rather than making the caller check.
func TestNilPipelineMetricsIsUsable(t *testing.T) {
	var metrics *PipelineMetrics
	metrics.ObserveBacklog(map[string]int{StatePolling: 3}, time.Minute)
	metrics.ObserveOutbox(2, time.Minute)
	metrics.ObserveAttempt(OperationSubmit, AttemptSuccess)
	metrics.ObserveProviderDuration(OperationPoll, time.Now())
	metrics.ObserveRevalidations(1)
	metrics.ObserveAdmission(AdmissionAllowed, "ok")
	metrics.ObserveReconciliation(ReconcileAdopted)
}

// The names and label sets are the contract a dashboard is built against, so
// they are pinned here: renaming one silently breaks every panel.
func TestPipelineMetricNamesAndLabels(t *testing.T) {
	want := map[string][]string{
		"nchat_link_scan_pending":                    {"service", "state"},
		"nchat_link_scan_oldest_pending_age_seconds": {"service"},
		"nchat_link_scan_attempts_total":             {"operation", "result", "service"},
		"nchat_link_scan_provider_duration_seconds":  {"operation", "service"},
		"nchat_link_scan_revalidations_total":        {"reason", "service"},
		// The capacity signal an operator alerts on. `reason` says which ceiling
		// refused; the tenant that was refused is deliberately absent.
		"nchat_link_scan_admissions_total": {"reason", "result", "service"},
		// The uncertainty window, which is otherwise invisible: a submission
		// whose outcome was never written down looks exactly like a slow one.
		"nchat_link_scan_submit_reconciliation_total":             {"result", "service"},
		"nchat_message_publish_outbox_pending":                    {"service"},
		"nchat_message_publish_outbox_oldest_pending_age_seconds": {"service"},
	}

	exposition := scrapeAfterObserving(t)
	for name, wantLabels := range want {
		got, exported := exposition[name]
		if !exported {
			t.Fatalf("%s is not exported", name)
		}
		if strings.Join(got, ",") != strings.Join(wantLabels, ",") {
			t.Fatalf("%s labels = %v, want %v", name, got, wantLabels)
		}
	}
}

// The cardinality rule, stated as a test: no collector in this package may carry
// a label whose values come from a caller. One hostile sender choosing URLs
// would otherwise create unbounded series and take the scrape down with it.
func TestNoPipelineMetricCarriesAnUnboundedLabel(t *testing.T) {
	forbidden := map[string]struct{}{
		"url": {}, "canonical_url": {}, "host": {}, "hostname": {}, "query": {},
		"message_id": {}, "scan_uuid": {}, "user_id": {}, "sender_id": {},
		"channel_id": {}, "dm_id": {}, "conversation_id": {}, "workspace_id": {},
		"error": {}, "status_code": {},
		// Added with the capacity controls: the budget is keyed by workspace and
		// the reconciliation is about one scan, so these are precisely the two
		// values that would be tempting to attach and must not be.
		"workspace": {}, "tenant": {}, "scope_key": {}, "uuid": {},
	}

	for name, labels := range scrapeAfterObserving(t) {
		for _, label := range labels {
			if _, banned := forbidden[label]; banned {
				t.Fatalf("%s carries the unbounded label %q", name, label)
			}
		}
	}
}

// The verdict counter predates this file and is asserted alongside it, so the
// whole RF-21 metric surface has one place that describes it.
func TestVerdictCounterRegistersWithOneLabel(t *testing.T) {
	metrics := testMetrics(t)
	verdicts := NewMetrics(metrics)
	if verdicts == nil {
		t.Fatal("the verdict counter did not register")
	}
	verdicts.observe(resultSafe)

	labels, exported := scrape(t, metrics)["nchat_url_safety_checks_total"]
	if !exported {
		t.Fatal("nchat_url_safety_checks_total is not exported")
	}
	if strings.Join(labels, ",") != "result" {
		t.Fatalf("labels = %v, want [result]", labels)
	}
}

// scrapeAfterObserving registers the pipeline collectors, touches each one so it
// exports a sample, and returns the exposition's label names by metric.
func scrapeAfterObserving(t *testing.T) map[string][]string {
	t.Helper()
	metrics := testMetrics(t)
	reporter := NewPipelineMetrics(metrics, "chat-service")
	if reporter == nil {
		t.Fatal("registration failed")
	}
	reporter.ObserveBacklog(map[string]int{
		StateSubmitPending: 1, StateSubmitUncertain: 2, StatePolling: 3,
	}, 5*time.Second)
	reporter.ObserveOutbox(1, 5*time.Second)
	reporter.ObserveAttempt(OperationSubmit, AttemptSuccess)
	reporter.ObserveProviderDuration(OperationPoll, time.Now())
	reporter.ObserveRevalidations(1)
	reporter.ObserveAdmission(AdmissionRejected, "workspace_budget")
	reporter.ObserveReconciliation(ReconcileStale)
	return scrape(t, metrics)
}

// sampleLine matches one exposition sample: a name, optional labels, a value.
var sampleLine = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{([^}]*)\})? `)

// scrape reads the served exposition and returns each metric's label names.
//
// Reading what /metrics actually serves rather than the collector definitions:
// that is the surface an operator's Prometheus sees, and it is where a stray
// label would show up.
func scrape(t *testing.T, metrics *observability.Metrics) map[string][]string {
	t.Helper()
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", response.Code)
	}

	byName := map[string][]string{}
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		match := sampleLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		// Histograms export _bucket/_sum/_count; the base name is what a
		// dashboard queries, and "le" is the bucket boundary rather than a
		// dimension anybody chose.
		name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(
			match[1], "_bucket"), "_sum"), "_count")
		if _, done := byName[name]; done {
			continue
		}
		var labels []string
		for _, pair := range strings.Split(match[2], ",") {
			key, _, found := strings.Cut(pair, "=")
			if !found || key == "le" {
				continue
			}
			labels = append(labels, key)
		}
		sort.Strings(labels)
		byName[name] = labels
	}
	return byName
}

// The uncertain backlog has to be its own series.
//
// It is the number an operator alerts on after this round: submissions whose
// outcome nobody can recover, which a restart will not clear and deliberately
// must not clear. Folded into submit_pending it would be invisible — it would
// look like ordinary work waiting to start.
func TestTheUncertainBacklogIsReportedSeparately(t *testing.T) {
	metrics := testMetrics(t)
	reporter := NewPipelineMetrics(metrics, "chat-service")
	if reporter == nil {
		t.Fatal("registration failed")
	}
	reporter.ObserveBacklog(map[string]int{StateSubmitUncertain: 7}, time.Hour)

	exposition := scrapeRaw(t, metrics)
	if !strings.Contains(exposition, `state="submit_uncertain"} 7`) {
		t.Fatalf("the uncertain backlog is not reported: %s", exposition)
	}
	// Every known state is exported even at zero, so a dashboard shows "none"
	// rather than "no data" — which is indistinguishable from a broken scrape.
	for _, state := range []string{StateSubmitPending, StatePolling} {
		if !strings.Contains(exposition, `state="`+state+`"} 0`) {
			t.Fatalf("%s was not exported at zero", state)
		}
	}
	// And the age gauge is not per state: it answers "how long has the oldest
	// unresolved thing been waiting", which is what makes an hour-old uncertain
	// submission visible.
	if !strings.Contains(exposition, "nchat_link_scan_oldest_pending_age_seconds") {
		t.Fatal("the oldest-age gauge is missing")
	}
}

// scrapeRaw returns the exposition text.
func scrapeRaw(t *testing.T, metrics *observability.Metrics) string {
	t.Helper()
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", response.Code)
	}
	return response.Body.String()
}
