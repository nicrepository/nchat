package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// The integration surface (issue #582), exercised against real local servers.
//
// What these specs are for is the security behaviour, not the happy path: a
// diagnostic that reaches a dependency is easy, and a diagnostic that refuses
// to reach the wrong one, refuses to say too much about what it found, and
// refuses to be pressed a hundred times is the thing worth asserting.

type integrationFixture struct {
	service    *service.IntegrationService
	health     *integrationHealthStub
	config     *integrationConfigStub
	authorizer *integrationAuthorizer
	audit      *integrationAudit
}

func newIntegrationFixture(t *testing.T, env map[string]string, options ...func(*integrationFixture)) *integrationFixture {
	t.Helper()
	fixture := &integrationFixture{
		health: &integrationHealthStub{snapshot: domain.HealthSnapshot{
			CollectedAt: time.Date(2026, 8, 23, 11, 0, 0, 0, time.UTC),
			Services: []domain.ServiceHealth{{
				Descriptor: mustHealthDescriptor(t, domain.HealthServiceSMTP),
				State:      domain.HealthDegraded, Enabled: true, Observable: true,
			}},
		}},
		config:     &integrationConfigStub{},
		authorizer: &integrationAuthorizer{},
		audit:      &integrationAudit{},
	}
	for _, apply := range options {
		apply(fixture)
	}
	fixture.service = service.NewIntegrationServiceWithEnv(
		fixture.health, fixture.config, fixture.authorizer, fixture.audit, nil, nil, envOf(env), fixedNow(),
	)
	return fixture
}

func mustHealthDescriptor(t *testing.T, id domain.HealthServiceID) domain.HealthServiceDescriptor {
	t.Helper()
	descriptor, ok := domain.LookupHealthService(id)
	if !ok {
		t.Fatalf("unknown health service %s", id)
	}
	return descriptor
}

// Opening the page must cost the platform one query and no outbound
// connection. The status comes from the snapshot the Health Center already
// collected.
func TestIntegrationListNeverForcesACollection(t *testing.T) {
	fixture := newIntegrationFixture(t, nil)

	view, err := fixture.service.List(context.Background(), integrationActor(domain.CapabilityIntegrationsRead))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(fixture.health.forced) != 1 || fixture.health.forced[0] {
		t.Fatalf("the listing must read the cached snapshot, got forced=%v", fixture.health.forced)
	}
	if len(view.Integrations) != len(domain.IntegrationRegistry()) {
		t.Fatalf("expected every declared integration, got %d", len(view.Integrations))
	}
}

// The configuration inventory is separately granted. An integrations-only
// operator gets the status and the diagnostic; the list of endpoints and
// credentials still requires admin.config.read.
func TestIntegrationListHidesTheInventoryWithoutConfigRead(t *testing.T) {
	fixture := newIntegrationFixture(t, nil)

	view, err := fixture.service.List(context.Background(), integrationActor(domain.CapabilityIntegrationsRead))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if fixture.config.calls != 0 {
		t.Fatal("the catalogue must not be read for an actor without admin.config.read")
	}
	for _, status := range view.Integrations {
		if status.SettingsVisible || len(status.Settings) != 0 {
			t.Fatalf("%s exposed settings without admin.config.read", status.Descriptor.ID)
		}
	}
}

func TestIntegrationListAttachesSettingsWithConfigRead(t *testing.T) {
	fixture := newIntegrationFixture(t, nil, func(f *integrationFixture) {
		f.config.view = service.ConfigCatalogView{Settings: []service.ConfigSetting{
			integrationSetting(t, "oidc.enabled", domain.TextValue("true")),
			integrationSetting(t, "secret.oidc_client_secret", domain.ConfigValue{}),
			integrationSetting(t, "oidc.provider_name", domain.TextValue("keycloak")),
		}}
	})

	view, err := fixture.service.List(context.Background(),
		integrationActor(domain.CapabilityIntegrationsRead, domain.CapabilityConfigRead))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	oidc := integrationByID(t, view, domain.IntegrationOIDC)
	if !oidc.SettingsVisible || len(oidc.Settings) != 3 {
		t.Fatalf("expected the three known OIDC settings, got %d", len(oidc.Settings))
	}
	// Common settings first, advanced last, so the console renders the
	// collapsed section without re-sorting.
	if oidc.Settings[0].Advanced || oidc.Settings[1].Advanced || !oidc.Settings[2].Advanced {
		t.Fatalf("advanced settings must sort last: %+v", oidc.Settings)
	}
}

func integrationSetting(t *testing.T, key domain.ConfigKey, value domain.ConfigValue) service.ConfigSetting {
	t.Helper()
	definition, ok := domain.LookupConfig(key)
	if !ok {
		t.Fatalf("unknown configuration key %s", key)
	}
	return service.ConfigSetting{Definition: definition, Value: value, Observable: true}
}

func integrationByID(t *testing.T, view service.IntegrationsView, id domain.IntegrationID) service.IntegrationStatus {
	t.Helper()
	for _, status := range view.Integrations {
		if status.Descriptor.ID == id {
			return status
		}
	}
	t.Fatalf("view has no %s", id)
	return service.IntegrationStatus{}
}

// An identifier the registry does not declare is refused before anything is
// resolved, and the refusal is recorded.
func TestDiagnoseRefusesAnUnknownIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t, nil)

	_, err := fixture.service.Diagnose(context.Background(), integrationActor(), "not-an-integration")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if event := fixture.audit.last(t); event.Result != domain.AuditResultDenied {
		t.Fatalf("expected the refusal to be recorded as denied, got %s", event.Result)
	}
}

// TURN and Link Scan are declared and cannot be checked from this pod. Asking
// is a conflict, not a silent success and not an invented result.
func TestDiagnoseRefusesAnIntegrationWithNoAdapter(t *testing.T) {
	fixture := newIntegrationFixture(t, nil)

	for _, id := range []domain.IntegrationID{domain.IntegrationTURN, domain.IntegrationLinkScan} {
		if _, err := fixture.service.Diagnose(context.Background(), integrationActor(), id); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("%s: expected conflict, got %v", id, err)
		}
	}
}

// The budget is per administrator and per integration, and spending it is
// refused before any connection is opened.
func TestDiagnoseIsRateLimitedPerActorAndIntegration(t *testing.T) {
	limiter := &denyLimiter{}
	audit := &integrationAudit{}
	surface := service.NewIntegrationServiceWithEnv(
		&integrationHealthStub{}, &integrationConfigStub{}, &integrationAuthorizer{}, audit, limiter, nil, envOf(nil), fixedNow(),
	)

	_, err := surface.Diagnose(context.Background(), integrationActor(), domain.IntegrationOIDC)
	if !errors.Is(err, domain.ErrTooManyRequests) {
		t.Fatalf("expected the rate limit to refuse, got %v", err)
	}
	if len(limiter.keys) != 1 || !strings.Contains(limiter.keys[0], string(domain.IntegrationOIDC)) {
		t.Fatalf("the budget must be keyed by actor and integration, got %v", limiter.keys)
	}
	if !strings.HasPrefix(limiter.keys[0], integrationActor().UserID) {
		t.Fatalf("the budget must be keyed by the actor, got %v", limiter.keys)
	}
}

// A configuration this pod cannot see produces a report where every stage is
// skipped. It is never a failure of the dependency, and never a pass.
func TestDiagnoseReportsAnUnobservableTargetAsSkipped(t *testing.T) {
	fixture := newIntegrationFixture(t, map[string]string{"OIDC_ISSUER_URL": "   "})

	report, err := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationOIDC)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if report.Status != domain.DiagnosticSkipped {
		t.Fatalf("expected a skipped run, got %s", report.Status)
	}
	descriptor, _ := domain.LookupIntegration(domain.IntegrationOIDC)
	if len(report.Steps) != len(descriptor.Stages) {
		t.Fatalf("the report must describe every declared stage, got %d of %d", len(report.Steps), len(descriptor.Stages))
	}
	for _, step := range report.Steps {
		if step.Status != domain.DiagnosticSkipped {
			t.Fatalf("%s ran against an unobservable target", step.Stage)
		}
	}
}

// The OIDC happy path, end to end against a local provider.
func TestDiagnoseOIDCChecksDiscoveryIssuerAndKeys(t *testing.T) {
	provider := newOIDCProvider(t, func(issuer string) (string, string) { return issuer, issuer + "/keys" })
	fixture := newIntegrationFixture(t, map[string]string{"OIDC_ISSUER_URL": provider.URL})

	report, err := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationOIDC)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if report.Status != domain.DiagnosticPassed {
		t.Fatalf("expected the run to pass, got %s: %+v", report.Status, report.Steps)
	}
	for _, stage := range []domain.DiagnosticStage{domain.StageResolve, domain.StageConnect, domain.StageDiscovery, domain.StageIssuer, domain.StageJWKS} {
		if step := stepOf(t, report, stage); step.Status != domain.DiagnosticPassed {
			t.Fatalf("%s did not pass: %+v", stage, step)
		}
	}
	// Plain HTTP means no handshake happened, and the report says so rather
	// than claiming a TLS stage nobody ran.
	if step := stepOf(t, report, domain.StageTLS); step.Status != domain.DiagnosticSkipped {
		t.Fatalf("an http issuer must not report a TLS handshake: %+v", step)
	}
	// Verifying a client without performing a real authentication is not
	// something the protocol offers, and the report says that instead of
	// inventing a verdict.
	if step := stepOf(t, report, domain.StageCredential); step.Status != domain.DiagnosticSkipped {
		t.Fatalf("the client stage must be skipped, not guessed: %+v", step)
	}
}

// A provider that claims a different issuer would have every token it mints
// refused, and the diagnostic stops there rather than fetching keys from it.
func TestDiagnoseOIDCRefusesAnIssuerMismatch(t *testing.T) {
	provider := newOIDCProvider(t, func(issuer string) (string, string) {
		return "https://attacker.example.test", issuer + "/keys"
	})
	fixture := newIntegrationFixture(t, map[string]string{"OIDC_ISSUER_URL": provider.URL})

	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationOIDC)
	if step := stepOf(t, report, domain.StageIssuer); step.Status != domain.DiagnosticFailed {
		t.Fatalf("an issuer mismatch must fail: %+v", step)
	}
	if step := stepOf(t, report, domain.StageJWKS); step.Status != domain.DiagnosticSkipped {
		t.Fatalf("the key set must not be fetched from a provider that lies about its issuer: %+v", step)
	}
	if report.Status != domain.DiagnosticFailed {
		t.Fatalf("expected the run to fail, got %s", report.Status)
	}
}

// The jwks_uri comes from the provider's own response. Following it to another
// origin would let whatever answers the issuer nominate the next address this
// pod connects to, so it is refused and no request is made.
func TestDiagnoseOIDCRefusesAKeySetOutsideTheIssuerOrigin(t *testing.T) {
	var elsewhere int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere++
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()

	provider := newOIDCProvider(t, func(issuer string) (string, string) { return issuer, other.URL + "/keys" })
	fixture := newIntegrationFixture(t, map[string]string{"OIDC_ISSUER_URL": provider.URL})

	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationOIDC)
	if step := stepOf(t, report, domain.StageJWKS); step.Status != domain.DiagnosticFailed {
		t.Fatalf("a cross-origin key set must fail: %+v", step)
	}
	if elsewhere != 0 {
		t.Fatal("the diagnostic followed a provider-nominated address")
	}
}

// A redirect is not followed. A dependency that answers 302 does not get to
// choose a second destination, so reachability is reported and nothing more.
func TestDiagnoseStorageDoesNotFollowARedirect(t *testing.T) {
	var followed int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	filer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer filer.Close()

	fixture := newIntegrationFixture(t, map[string]string{"SEAWEEDFS_FILER_URL": filer.URL})
	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationStorage)

	if step := stepOf(t, report, domain.StageReady); step.Status != domain.DiagnosticWarning {
		t.Fatalf("an unfollowed redirect is a warning, not a pass: %+v", step)
	}
	if followed != 0 {
		t.Fatal("the diagnostic followed a redirect")
	}
}

func TestDiagnoseStorageReportsAFailingFiler(t *testing.T) {
	filer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("panic: internal path /data/volume/07 exploded"))
	}))
	defer filer.Close()

	fixture := newIntegrationFixture(t, map[string]string{"SEAWEEDFS_FILER_URL": filer.URL})
	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationStorage)

	step := stepOf(t, report, domain.StageReady)
	if step.Status != domain.DiagnosticFailed || step.Category != domain.HealthErrorDependencyUnavailable {
		t.Fatalf("a 500 must fail as an unavailable dependency: %+v", step)
	}
	// The remote body is never relayed, whatever it says about the cluster.
	if strings.Contains(step.Detail, "volume") || strings.Contains(step.Detail, "panic") {
		t.Fatalf("the dependency's response reached the report: %q", step.Detail)
	}
}

func TestDiagnoseClamAVReadsASanitizedVersion(t *testing.T) {
	daemon := startFakeClamd(t, &fakeClamd{version: "ClamAV 1.0.5/27000/Tue <script>alert(1)</script>"})
	fixture := newIntegrationFixture(t, map[string]string{"FILE_MALWARE_SCANNER_ADDRESS": daemon.address()})

	report, err := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationClamAV)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if report.Status != domain.DiagnosticPassed {
		t.Fatalf("expected the run to pass, got %s: %+v", report.Status, report.Steps)
	}
	if strings.ContainsAny(report.Version, "<>()") {
		t.Fatalf("the daemon's version reached the report unfiltered: %q", report.Version)
	}
	if !strings.HasPrefix(report.Version, "ClamAV 1.0.5") {
		t.Fatalf("the version an operator needs was lost: %q", report.Version)
	}
}

func TestDiagnoseClamAVRefusesAnUnexpectedReply(t *testing.T) {
	daemon := startFakeClamd(t, &fakeClamd{pong: "WHAT"})
	fixture := newIntegrationFixture(t, map[string]string{"FILE_MALWARE_SCANNER_ADDRESS": daemon.address()})

	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationClamAV)
	step := stepOf(t, report, domain.StageReady)
	if step.Status != domain.DiagnosticFailed || step.Category != domain.HealthErrorProtocolError {
		t.Fatalf("an uninterpretable reply must fail as a protocol error: %+v", step)
	}
}

// A relay configured without TLS is reported as a risk rather than as a pass,
// and a credential is never offered to it in clear text.
// A relay configured without TLS carries this platform's invitation and
// password-reset links in clear text. The whole run has to say so: a skipped
// TLS stage would leave DeriveDiagnosticStatus reporting DiagnosticPassed, and
// a green tick would tell an operator that configuration is fine.
//
// It is not a failure either — `none` is a mode the deployment is allowed to
// choose — so the run continues and every later stage still reports honestly.
func TestDiagnoseSMTPWarnsWhenTheRelayHasNoTLS(t *testing.T) {
	relay := startFakeSMTP(t, &fakeSMTPServer{})
	fixture := newIntegrationFixture(t, smtpEnv(relay, "none", "", ""))

	report, err := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationSMTP)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}

	step := stepOf(t, report, domain.StageTLS)
	if step.Status != domain.DiagnosticWarning {
		t.Fatalf("a plaintext relay must warn rather than pass or skip silently: %+v", step)
	}
	if step.LatencyMS != nil {
		t.Fatalf("no handshake was attempted, so no duration may be reported: %+v", step)
	}
	if step.Detail == "" {
		t.Fatalf("the warning must say what is wrong: %+v", step)
	}

	// The verdict of the *run*, which is what the review found wrong: a
	// warning on one stage has to pull the whole report to a warning.
	if report.Status != domain.DiagnosticWarning {
		t.Fatalf("expected the run to warn, got %s: %+v", report.Status, report.Steps)
	}
	if report.Summary == "" {
		t.Fatalf("a warning run must carry a summary an operator can act on")
	}

	// And it must not have become a failure. Missing TLS degrades the verdict;
	// it does not stop the diagnostic from finishing.
	expected := map[domain.DiagnosticStage]domain.DiagnosticStatus{
		domain.StageResolve: domain.DiagnosticPassed,
		domain.StageConnect: domain.DiagnosticPassed,
		// No username is configured in this fixture, so there is nothing to
		// authenticate with and the stage is honestly skipped.
		domain.StageCredential: domain.DiagnosticSkipped,
		domain.StageReady:      domain.DiagnosticPassed,
	}
	for stage, want := range expected {
		if got := stepOf(t, report, stage); got.Status != want {
			t.Fatalf("%s is %s, expected %s: %+v", stage, got.Status, want, got)
		}
	}
}

// STARTTLS against a certificate this pod does not trust fails. There is no
// setting that makes it pass, which is the property this asserts.
func TestDiagnoseSMTPFailsAnUntrustedSTARTTLS(t *testing.T) {
	relay := startFakeSMTP(t, &fakeSMTPServer{offerSTARTTLS: true})
	fixture := newIntegrationFixture(t, smtpEnv(relay, "starttls", "", ""))

	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationSMTP)
	step := stepOf(t, report, domain.StageTLS)
	if step.Status != domain.DiagnosticFailed {
		t.Fatalf("an unverifiable STARTTLS must fail: %+v", step)
	}
	for _, stage := range []domain.DiagnosticStage{domain.StageCredential, domain.StageReady} {
		if after := stepOf(t, report, stage); after.Status != domain.DiagnosticSkipped {
			t.Fatalf("%s ran after TLS failed: %+v", stage, after)
		}
	}
}

// net/smtp refuses PLAIN over an unencrypted connection and this code does not
// work around it: a relay that only takes a password in clear text is a
// finding, not a configuration to accommodate.
func TestDiagnoseSMTPNeverSendsACredentialInClearText(t *testing.T) {
	relay := startFakeSMTP(t, &fakeSMTPServer{})
	secret := "s3cr3t-relay-password"
	fixture := newIntegrationFixture(t, smtpEnv(relay, "none", "mailer", secret))

	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationSMTP)
	step := stepOf(t, report, domain.StageCredential)
	if step.Status != domain.DiagnosticFailed {
		t.Fatalf("authenticating over plain text must fail: %+v", step)
	}
	assertNoSecretLeaked(t, report, secret)
	envelope, body := relay.recorded()
	if strings.Contains(strings.Join(envelope, "\n")+body, secret) {
		t.Fatal("the password reached the wire over an unencrypted connection")
	}
}

// The test message goes to the administrator's own address and nowhere else.
// There is no recipient field, so there is nothing for a stolen session to aim.
func TestSendTestEmailDeliversOnlyToTheAuthenticatedAdministrator(t *testing.T) {
	relay := startFakeSMTP(t, &fakeSMTPServer{})
	fixture := newIntegrationFixture(t, smtpEnv(relay, "none", "", ""))
	actor := integrationActor(domain.CapabilityIntegrationsManage)

	report, err := fixture.service.SendTestEmail(context.Background(), actor)
	if err != nil {
		t.Fatalf("SendTestEmail: %v", err)
	}
	if step := stepOf(t, report, domain.StageDelivery); step.Status != domain.DiagnosticPassed {
		t.Fatalf("the relay accepted the message and the report must say so: %+v", step)
	}
	envelope, body := relay.recorded()
	joined := strings.Join(envelope, "\n")
	if !strings.Contains(joined, actor.Email) {
		t.Fatalf("the message was not addressed to the administrator: %v", envelope)
	}
	for _, line := range envelope {
		if strings.HasPrefix(strings.ToUpper(line), "RCPT") && !strings.Contains(line, actor.Email) {
			t.Fatalf("a second recipient reached the relay: %q", line)
		}
	}
	if !strings.Contains(body, "Auto-Submitted: auto-generated") {
		t.Fatalf("the test message must mark itself automated: %q", body)
	}
}

// An ordinary diagnostic never delivers anything, so the delivery stage does
// not appear in a report nobody asked to send.
func TestDiagnoseSMTPNeverDelivers(t *testing.T) {
	relay := startFakeSMTP(t, &fakeSMTPServer{})
	fixture := newIntegrationFixture(t, smtpEnv(relay, "none", "", ""))

	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationSMTP)
	for _, step := range report.Steps {
		if step.Stage == domain.StageDelivery {
			t.Fatal("a diagnostic reported a delivery it never performed")
		}
	}
	if envelope, _ := relay.recorded(); len(envelope) != 0 {
		t.Fatalf("a diagnostic sent mail: %v", envelope)
	}
}

func TestSendTestEmailRefusesAMalformedAdministrativeAddress(t *testing.T) {
	fixture := newIntegrationFixture(t, nil)
	for _, address := range []string{"", "   ", "operator", "operator@", "@example.test", "a@b", "op\r\nBcc: victim@x.test@example.test", "op erator@example.test"} {
		actor := integrationActor()
		actor.Email = address
		if _, err := fixture.service.SendTestEmail(context.Background(), actor); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("%q must be refused, got %v", address, err)
		}
	}
}

func TestSendTestEmailIsRateLimited(t *testing.T) {
	limiter := &denyLimiter{}
	surface := service.NewIntegrationServiceWithEnv(
		&integrationHealthStub{}, &integrationConfigStub{}, &integrationAuthorizer{}, &integrationAudit{}, nil, limiter, envOf(nil), fixedNow(),
	)
	if _, err := surface.SendTestEmail(context.Background(), integrationActor()); !errors.Is(err, domain.ErrTooManyRequests) {
		t.Fatalf("expected the rate limit to refuse, got %v", err)
	}
}

// The trail records what happened, and nothing about where or to whom.
func TestDiagnoseAuditsTheOutcomeWithoutTheTarget(t *testing.T) {
	filer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer filer.Close()
	fixture := newIntegrationFixture(t, map[string]string{"SEAWEEDFS_FILER_URL": filer.URL})

	if _, err := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationStorage); err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	event := fixture.audit.last(t)
	if event.Action != domain.AuditActionIntegrationDiagnose {
		t.Fatalf("unexpected action %q", event.Action)
	}
	if event.Resource != domain.AuditIntegrationResource(domain.IntegrationStorage) {
		t.Fatalf("unexpected resource %q", event.Resource)
	}
	if event.Metadata["outcome"] != string(domain.DiagnosticFailed) || event.Metadata["failed_stage"] != string(domain.StageReady) {
		t.Fatalf("the trail must name the outcome and the stage that failed: %v", event.Metadata)
	}
	for key, value := range event.Metadata {
		if strings.Contains(value, filer.URL) || strings.Contains(value, "127.0.0.1") {
			t.Fatalf("the trail recorded the target in %q: %q", key, value)
		}
	}
}

// The diagnostic belongs to one operator and one request, so navigating away
// cancels the outbound work rather than leaving it running.
func TestDiagnoseIsCancelledWithTheRequest(t *testing.T) {
	// The handler hangs until its own request context is cancelled, which is
	// what the client aborting produces. Nothing else releases it, so a run
	// that ignored cancellation would fail this spec by timing out.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	fixture := newIntegrationFixture(t, map[string]string{"SEAWEEDFS_FILER_URL": slow.URL})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	report, err := fixture.service.Diagnose(ctx, integrationActor(), domain.IntegrationStorage)
	if err != nil {
		t.Fatalf("a cancelled diagnostic is still a report, got %v", err)
	}
	if step := stepOf(t, report, domain.StageReady); step.Status != domain.DiagnosticFailed {
		t.Fatalf("a cancelled request must not report a ready dependency: %+v", step)
	}
}

// Two runs at once is the ceiling. A third is refused rather than queued: a
// queued diagnostic holds a request open behind work the operator cannot see.
func TestDiagnoseCapsConcurrentRuns(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	fixture := newIntegrationFixture(t, map[string]string{"SEAWEEDFS_FILER_URL": slow.URL})
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationStorage)
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(release)
			group.Wait()
			t.Fatal("the two permitted runs did not start")
		}
	}

	_, err := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationStorage)
	close(release)
	group.Wait()
	if !errors.Is(err, domain.ErrTooManyRequests) {
		t.Fatalf("a third concurrent run must be refused, got %v", err)
	}
}

// The LiveKit credential is proved with the smallest grant that can prove it,
// and the token is short lived and never leaves the request.
func TestDiagnoseLiveKitSignsAMinimalShortLivedToken(t *testing.T) {
	const apiKey, apiSecret = "devkey", "unit-test-livekit-secret-placeholder"
	var claims liveKitDiagnosticClaims
	var path string
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		claims = parseLiveKitToken(t, r.Header.Get("Authorization"), apiSecret)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rooms":[]}`))
	}))
	defer media.Close()

	fixture := newIntegrationFixture(t, map[string]string{
		"LIVEKIT_API_URL": media.URL, "LIVEKIT_API_KEY": apiKey, "LIVEKIT_API_SECRET": apiSecret,
	})
	report, err := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationLiveKit)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if report.Status != domain.DiagnosticPassed {
		t.Fatalf("expected the run to pass, got %s: %+v", report.Status, report.Steps)
	}
	if path != "/twirp/livekit.RoomService/ListRooms" {
		t.Fatalf("the diagnostic must list rooms and create none, called %q", path)
	}
	if !claims.Video.RoomList || claims.Video.RoomCreate || claims.Video.RoomJoin || claims.Video.RoomAdmin {
		t.Fatalf("the diagnostic token must grant only roomList: %+v", claims.Video)
	}
	if claims.Issuer != apiKey {
		t.Fatalf("the token must be issued by the configured key, got %q", claims.Issuer)
	}
	lifetime := claims.ExpiresAt.Sub(claims.NotBefore.Time)
	if lifetime > service.DiagnosticTokenTTLForTest+2*time.Second {
		t.Fatalf("the diagnostic token lives too long: %s", lifetime)
	}
	assertNoSecretLeaked(t, report, apiSecret)
}

func TestDiagnoseLiveKitReportsARefusedCredential(t *testing.T) {
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer media.Close()

	fixture := newIntegrationFixture(t, map[string]string{
		"LIVEKIT_API_URL": media.URL, "LIVEKIT_API_KEY": "devkey", "LIVEKIT_API_SECRET": "unit-test-livekit-secret-placeholder",
	})
	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationLiveKit)
	step := stepOf(t, report, domain.StageCredential)
	if step.Status != domain.DiagnosticFailed || step.Category != domain.HealthErrorAuthenticationFailed {
		t.Fatalf("a refused credential must be reported as such: %+v", step)
	}
	if after := stepOf(t, report, domain.StageReady); after.Status != domain.DiagnosticSkipped {
		t.Fatalf("the readiness stage must not run after the credential was refused: %+v", after)
	}
}

func TestDiagnoseLiveKitSkipsTheCredentialWithoutAKey(t *testing.T) {
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request may be made without a credential to sign it")
	}))
	defer media.Close()

	fixture := newIntegrationFixture(t, map[string]string{"LIVEKIT_API_URL": media.URL})
	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationLiveKit)
	if step := stepOf(t, report, domain.StageCredential); step.Status != domain.DiagnosticSkipped {
		t.Fatalf("an unobservable credential must skip the stage: %+v", step)
	}
}

// Helpers.

func smtpEnv(relay *fakeSMTPServer, tlsMode, username, password string) map[string]string {
	return map[string]string{
		"SMTP_HOST": relay.host(), "SMTP_PORT": relay.port(),
		"SMTP_TLS_MODE": tlsMode, "SMTP_FROM": "nchat@example.test", "SMTP_FROM_NAME": "NChat",
		"SMTP_USERNAME": username, "SMTP_PASSWORD": password,
	}
}

// newOIDCProvider serves a discovery document whose issuer and jwks_uri the
// caller decides, so a spec can model an honest provider and a lying one.
func newOIDCProvider(t *testing.T, describe func(base string) (issuer string, jwks string)) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			issuer, jwks := describe(server.URL)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": jwks})
		case "/keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"keys":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

type liveKitVideoGrant struct {
	RoomList   bool `json:"roomList"`
	RoomCreate bool `json:"roomCreate"`
	RoomJoin   bool `json:"roomJoin"`
	RoomAdmin  bool `json:"roomAdmin"`
}

type liveKitDiagnosticClaims struct {
	Video liveKitVideoGrant `json:"video"`
	jwt.RegisteredClaims
}

func parseLiveKitToken(t *testing.T, header, secret string) liveKitDiagnosticClaims {
	t.Helper()
	raw, found := strings.CutPrefix(header, "Bearer ")
	if !found {
		t.Fatalf("expected a bearer token, got %q", header)
	}
	var claims liveKitDiagnosticClaims
	// Claim validation is switched off and the lifetime asserted by the caller:
	// the token is deliberately valid for thirty seconds against the service's
	// injected clock, and the wall clock must not be what decides whether this
	// spec passes.
	token, err := jwt.ParseWithClaims(raw, &claims,
		func(*jwt.Token) (any, error) { return []byte(secret), nil },
		jwt.WithoutClaimsValidation())
	if err != nil || !token.Valid {
		t.Fatalf("the diagnostic token must be signed with the configured secret: %v", err)
	}
	return claims
}

// The address policy is not only a table: a diagnostic aimed at the metadata
// range refuses at the resolve stage and never opens a socket.
func TestDiagnoseRefusesAMetadataEndpointTarget(t *testing.T) {
	fixture := newIntegrationFixture(t, map[string]string{"SEAWEEDFS_FILER_URL": "http://169.254.169.254/latest/meta-data/"})

	report, err := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationStorage)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	step := stepOf(t, report, domain.StageResolve)
	if step.Status != domain.DiagnosticFailed || step.Category != domain.HealthErrorInvalidConfiguration {
		t.Fatalf("a metadata target must be refused as a configuration problem: %+v", step)
	}
	for _, later := range []domain.DiagnosticStage{domain.StageConnect, domain.StageTLS, domain.StageReady} {
		if after := stepOf(t, report, later); after.Status != domain.DiagnosticSkipped {
			t.Fatalf("%s ran against a refused address: %+v", later, after)
		}
	}
}

// A scheme outside the integration's policy is a configuration problem, and no
// stage runs.
func TestDiagnoseRefusesAnUnsupportedScheme(t *testing.T) {
	fixture := newIntegrationFixture(t, map[string]string{"SEAWEEDFS_FILER_URL": "file:///etc/passwd"})

	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationStorage)
	if report.Status != domain.DiagnosticSkipped {
		t.Fatalf("an unusable endpoint must produce a skipped run, got %s", report.Status)
	}
}

func TestDiagnoseReportsARefusedConnection(t *testing.T) {
	// A listener that is closed immediately leaves a port nothing is bound to,
	// which is the closest thing to a dependency that is down.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	fixture := newIntegrationFixture(t, map[string]string{"FILE_MALWARE_SCANNER_ADDRESS": address})
	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationClamAV)
	step := stepOf(t, report, domain.StageConnect)
	if step.Status != domain.DiagnosticFailed {
		t.Fatalf("a closed port must fail the connect stage: %+v", step)
	}
	if strings.Contains(step.Detail, address) {
		t.Fatalf("the address reached the report: %q", step.Detail)
	}
}

// TLS verification is on and there is no setting that turns it off, so a
// dependency presenting a certificate this pod does not trust fails the
// handshake stage rather than being reported as reachable.
func TestDiagnoseFailsAnUntrustedCertificate(t *testing.T) {
	filer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer filer.Close()

	fixture := newIntegrationFixture(t, map[string]string{"SEAWEEDFS_FILER_URL": filer.URL})
	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationStorage)

	step := stepOf(t, report, domain.StageTLS)
	if step.Status != domain.DiagnosticFailed || step.Category != domain.HealthErrorTLSError {
		t.Fatalf("an untrusted certificate must fail as a TLS error: %+v", step)
	}
	if after := stepOf(t, report, domain.StageReady); after.Status != domain.DiagnosticSkipped {
		t.Fatalf("no request may be made over a handshake that failed: %+v", after)
	}
}

// A server that accepts the connection and answers something that is not an
// SMTP greeting is not a relay, and the report says so instead of proceeding.
func TestDiagnoseSMTPRefusesANonRelayGreeting(t *testing.T) {
	relay := startFakeSMTP(t, &fakeSMTPServer{greeting: "HTTP/1.1 400 Bad Request\r\n\r\n"})
	fixture := newIntegrationFixture(t, smtpEnv(relay, "none", "", ""))

	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationSMTP)
	step := stepOf(t, report, domain.StageReady)
	if step.Status != domain.DiagnosticFailed || step.Category != domain.HealthErrorProtocolError {
		t.Fatalf("a server that is not a relay must fail as a protocol error: %+v", step)
	}
}

func TestSendTestEmailReportsARefusedEnvelope(t *testing.T) {
	relay := startFakeSMTP(t, &fakeSMTPServer{rejectRecipient: true})
	fixture := newIntegrationFixture(t, smtpEnv(relay, "none", "", ""))

	report, err := fixture.service.SendTestEmail(context.Background(), integrationActor())
	if err != nil {
		t.Fatalf("a refused envelope is a report, not an error: %v", err)
	}
	step := stepOf(t, report, domain.StageDelivery)
	if step.Status != domain.DiagnosticFailed {
		t.Fatalf("a refused recipient must fail the delivery stage: %+v", step)
	}
}

// A relay this pod cannot see is not a relay that is down. Without a host or a
// sender there is nothing to contact and nothing to write.
func TestDiagnoseSMTPWithoutAConfiguredRelayIsSkipped(t *testing.T) {
	for _, env := range []map[string]string{
		{"SMTP_FROM": "nchat@example.test"},
		{"SMTP_HOST": "relay.internal"},
	} {
		fixture := newIntegrationFixture(t, env)
		report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationSMTP)
		if report.Status != domain.DiagnosticSkipped {
			t.Fatalf("%v must produce a skipped run, got %s", env, report.Status)
		}
	}
}

// A LiveKit URL with a trailing slash must still address the API, rather than
// producing a double slash the server answers with a 404.
func TestDiagnoseLiveKitNormalizesATrailingSlash(t *testing.T) {
	var path string
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"rooms":[]}`))
	}))
	defer media.Close()

	fixture := newIntegrationFixture(t, map[string]string{
		"LIVEKIT_API_URL": media.URL + "///", "LIVEKIT_API_KEY": "devkey",
		"LIVEKIT_API_SECRET": "unit-test-livekit-secret-placeholder",
	})
	if _, err := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationLiveKit); err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if path != "/twirp/livekit.RoomService/ListRooms" {
		t.Fatalf("expected the API path, got %q", path)
	}
}

// The discovery document is read under a byte cap and interpreted, not
// relayed. A provider that answers with something else fails the stage.
func TestDiagnoseOIDCRefusesAnUninterpretableDiscoveryDocument(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not a discovery document</html>"))
	}))
	defer provider.Close()

	fixture := newIntegrationFixture(t, map[string]string{"OIDC_ISSUER_URL": provider.URL})
	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationOIDC)
	step := stepOf(t, report, domain.StageDiscovery)
	if step.Status != domain.DiagnosticFailed || step.Category != domain.HealthErrorProtocolError {
		t.Fatalf("an uninterpretable document must fail as a protocol error: %+v", step)
	}
	if strings.Contains(step.Detail, "html") {
		t.Fatalf("the provider's response reached the report: %q", step.Detail)
	}
}

func TestDiagnoseOIDCReportsAMissingDiscoveryDocument(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer provider.Close()

	fixture := newIntegrationFixture(t, map[string]string{"OIDC_ISSUER_URL": provider.URL})
	report, _ := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationOIDC)
	if step := stepOf(t, report, domain.StageDiscovery); step.Status != domain.DiagnosticFailed {
		t.Fatalf("a missing discovery document must fail: %+v", step)
	}
}

// The console must never be told a dependency is healthy on the strength of an
// answer this build did not understand.
func TestReadinessFromStatusNeverPassesAnUnexpectedAnswer(t *testing.T) {
	cases := map[int]string{
		200: string(domain.DiagnosticPassed),
		204: string(domain.DiagnosticPassed),
		301: string(domain.DiagnosticWarning),
		400: string(domain.DiagnosticWarning),
		401: string(domain.DiagnosticFailed),
		403: string(domain.DiagnosticFailed),
		500: string(domain.DiagnosticFailed),
		503: string(domain.DiagnosticFailed),
	}
	for status, want := range cases {
		got, _ := service.ReadinessFromStatusForTest(status)
		if got != want {
			t.Fatalf("status %d judged %q, expected %q", status, got, want)
		}
	}
}

// A surface with no health collection and no catalogue is unavailable rather
// than an empty page that reads as "no integrations".
func TestIntegrationListPropagatesDependencyFailures(t *testing.T) {
	health := &integrationHealthStub{err: domain.ErrUnavailable}
	surface := service.NewIntegrationServiceWithEnv(health, &integrationConfigStub{}, &integrationAuthorizer{}, &integrationAudit{}, nil, nil, envOf(nil), fixedNow())
	if _, err := surface.List(context.Background(), integrationActor(domain.CapabilityIntegrationsRead)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected the health failure to propagate, got %v", err)
	}

	config := &integrationConfigStub{err: domain.ErrUnavailable}
	surface = service.NewIntegrationServiceWithEnv(&integrationHealthStub{}, config, &integrationAuthorizer{}, &integrationAudit{}, nil, nil, envOf(nil), fixedNow())
	_, err := surface.List(context.Background(), integrationActor(domain.CapabilityIntegrationsRead, domain.CapabilityConfigRead))
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected the catalogue failure to propagate, got %v", err)
	}

	var unwired *service.IntegrationService
	if _, err := unwired.List(context.Background(), integrationActor()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("an unwired surface must be unavailable, got %v", err)
	}
	if _, err := unwired.Diagnose(context.Background(), integrationActor(), domain.IntegrationOIDC); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("an unwired surface must refuse a diagnostic, got %v", err)
	}
	if _, err := unwired.SendTestEmail(context.Background(), integrationActor()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("an unwired surface must refuse a test message, got %v", err)
	}
}

func TestValidateTestRecipientAcceptsARealAddress(t *testing.T) {
	address, err := service.ValidateTestRecipientForTest("  operator@example.test  ")
	if err != nil {
		t.Fatalf("a real address must be accepted: %v", err)
	}
	if address != "operator@example.test" {
		t.Fatalf("the address must be trimmed, got %q", address)
	}
	if _, err := service.ValidateTestRecipientForTest(strings.Repeat("a", 250) + "@example.test"); err == nil {
		t.Fatal("an oversized address must be refused")
	}
}

// Re-authorization at the last safe point (CWE-367, issue #582).
//
// The middleware authorizes when the request arrives. An SMTP exchange then
// takes DNS, a TCP connect, a TLS handshake, an AUTH round trip and whatever
// the relay's response time happens to be — and in that interval a role can be
// revoked, a console session ended or a principal suspended. Nothing about the
// snapshot the middleware produced changes when any of that happens.
//
// The relay's NOOP hook is what makes these deterministic: it fires after AUTH
// and immediately before the envelope would be written, which is exactly the
// moment the second check has to notice.
func TestSendTestEmailRefusesWhenAuthorityIsRevokedDuringTheSession(t *testing.T) {
	cases := map[string]struct {
		revocation error
		reason     string
	}{
		"capability removed": {
			revocation: domain.ErrForbidden,
			reason:     "admin.integrations.manage was taken away mid-request",
		},
		"admin session revoked": {
			revocation: domain.ErrUnauthorized,
			reason:     "the console session was ended mid-request",
		},
		"principal suspended": {
			revocation: domain.ErrUnauthorized,
			reason:     "the administrative principal was suspended mid-request",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			assertRevocationStopsTheEnvelope(t, testCase.revocation, testCase.reason)
		})
	}
}

// assertRevocationStopsTheEnvelope runs one revocation scenario end to end and
// holds every claim the fix is for.
func assertRevocationStopsTheEnvelope(t *testing.T, revocation error, reason string) {
	t.Helper()
	authorizer := &integrationAuthorizer{}
	// The revocation lands while the SMTP session is in flight: NOOP fires
	// after AUTH and immediately before an envelope would be written.
	relay := startFakeSMTP(t, &fakeSMTPServer{onNoop: func() { authorizer.revoke(revocation) }})
	fixture := newIntegrationFixture(t, smtpEnv(relay, "none", "", ""), func(f *integrationFixture) {
		f.authorizer = authorizer
	})

	report, err := fixture.service.SendTestEmail(context.Background(), integrationActor())

	if !errors.Is(err, revocation) {
		t.Fatalf("%s: expected the operation to be refused, got %v", reason, err)
	}
	// No report leaks out of a refusal: the answer is an administrative error,
	// not a diagnostic result.
	if len(report.Steps) != 0 || report.Status != "" {
		t.Fatalf("a refused operation must not answer with a report: %+v", report)
	}
	assertNoEnvelopeReachedTheRelay(t, relay)
	assertRefusalWasAudited(t, fixture, err)
	// Both checks ran: one before the work started, one at the last safe point.
	// Without the second, the first would have passed and the message would
	// have gone out.
	assertReauthorizations(t, authorizer, 2)
}

// assertReauthorizations holds how many times the database was consulted, and
// that each read asked about the capability the route declares.
func assertReauthorizations(t *testing.T, authorizer *integrationAuthorizer, want int) {
	t.Helper()
	observed := authorizer.observed()
	if len(observed) != want {
		t.Fatalf("expected %d re-authorizations, got %d: %v", want, len(observed), observed)
	}
	for _, capability := range observed {
		if capability != domain.CapabilityIntegrationsManage {
			t.Fatalf("re-authorization asked about %s", capability)
		}
	}
}

// A revocation that is already visible when the request arrives is refused
// before anything is dialled, so the relay is never contacted at all.
func TestSendTestEmailRefusesBeforeContactingTheRelay(t *testing.T) {
	authorizer := &integrationAuthorizer{}
	authorizer.revoke(domain.ErrForbidden)
	relay := startFakeSMTP(t, &fakeSMTPServer{})
	fixture := newIntegrationFixture(t, smtpEnv(relay, "none", "", ""), func(f *integrationFixture) {
		f.authorizer = authorizer
	})

	if _, err := fixture.service.SendTestEmail(context.Background(), integrationActor()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected the operation to be refused, got %v", err)
	}
	assertNoEnvelopeReachedTheRelay(t, relay)
	if observed := authorizer.observed(); len(observed) != 1 {
		t.Fatalf("expected the run to stop at the first check, got %v", observed)
	}
}

// The other half of the contract: an administrator who is still authorized
// sends exactly one message, over one session, with one authentication.
func TestSendTestEmailStillDeliversForAnAuthorizedAdministrator(t *testing.T) {
	authorizer := &integrationAuthorizer{}
	relay := startFakeSMTP(t, &fakeSMTPServer{})
	fixture := newIntegrationFixture(t, smtpEnv(relay, "none", "", ""), func(f *integrationFixture) {
		f.authorizer = authorizer
	})
	actor := integrationActor(domain.CapabilityIntegrationsManage)

	report, err := fixture.service.SendTestEmail(context.Background(), actor)
	if err != nil {
		t.Fatalf("SendTestEmail: %v", err)
	}
	if step := stepOf(t, report, domain.StageDelivery); step.Status != domain.DiagnosticPassed {
		t.Fatalf("the relay accepted the message and the report must say so: %+v", step)
	}

	envelope, _ := relay.recorded()
	if countCommands(envelope, "MAIL") != 1 || countCommands(envelope, "RCPT") != 1 {
		t.Fatalf("expected exactly one envelope, got %v", envelope)
	}
	if observed := authorizer.observed(); len(observed) != 2 {
		t.Fatalf("expected exactly two re-authorizations, got %v", observed)
	}
	// The trail records the operation as a success, and only once.
	events := fixture.audit.all()
	if len(events) != 1 || events[0].Result != domain.AuditResultSuccess {
		t.Fatalf("expected one successful audit event, got %+v", events)
	}
}

// The shared entrypoint every diagnostic goes through re-derives authority
// before any adapter opens a connection. Asserting it once here rather than
// once per integration is deliberate: they all pass through Diagnose.
func TestDiagnoseRefusesWhenAuthorityIsAlreadyRevoked(t *testing.T) {
	for name, revocation := range map[string]error{
		"capability removed":  domain.ErrForbidden,
		"session revoked":     domain.ErrUnauthorized,
		"principal suspended": domain.ErrUnauthorized,
	} {
		t.Run(name, func(t *testing.T) {
			assertRevocationStopsTheDiagnostic(t, revocation)
		})
	}
}

// assertRevocationStopsTheDiagnostic proves the shared entrypoint refuses
// before any adapter opens a connection.
func assertRevocationStopsTheDiagnostic(t *testing.T, revocation error) {
	t.Helper()
	authorizer := &integrationAuthorizer{}
	authorizer.revoke(revocation)
	reached := false
	filer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer filer.Close()

	fixture := newIntegrationFixture(t, map[string]string{"SEAWEEDFS_FILER_URL": filer.URL},
		func(f *integrationFixture) { f.authorizer = authorizer })

	report, err := fixture.service.Diagnose(context.Background(), integrationActor(), domain.IntegrationStorage)
	if !errors.Is(err, revocation) {
		t.Fatalf("expected the diagnostic to be refused, got %v", err)
	}
	if len(report.Steps) != 0 {
		t.Fatalf("a refused diagnostic must not answer with a report: %+v", report)
	}
	if reached {
		t.Fatal("a refused diagnostic reached the dependency")
	}
	assertRefusalWasAudited(t, fixture, err)
	// The capability asked about is the route's own, not a guess.
	assertReauthorizations(t, authorizer, 1)
}

// A surface wired without an authorizer must refuse rather than fall back to
// the request-time snapshot.
func TestIntegrationSurfaceWithoutAnAuthorizerRefusesPrivilegedWork(t *testing.T) {
	surface := service.NewIntegrationServiceWithEnv(
		&integrationHealthStub{}, &integrationConfigStub{}, nil, &integrationAudit{},
		nil, nil, envOf(nil), fixedNow(),
	)
	if _, err := surface.Diagnose(context.Background(), integrationActor(), domain.IntegrationStorage); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	if _, err := surface.SendTestEmail(context.Background(), integrationActor()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}

// assertNoEnvelopeReachedTheRelay holds the property the whole fix exists for:
// the message was never handed over, in any part.
func assertNoEnvelopeReachedTheRelay(t *testing.T, relay *fakeSMTPServer) {
	t.Helper()
	envelope, body := relay.recorded()
	for _, line := range envelope {
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "MAIL") || strings.HasPrefix(upper, "RCPT") {
			t.Fatalf("an envelope reached the relay after authority was revoked: %q", line)
		}
	}
	if strings.TrimSpace(body) != "" {
		t.Fatalf("a message body reached the relay after authority was revoked: %q", body)
	}
}

// assertRefusalWasAudited holds the other half: a refusal is recorded as a
// denial and never as a completed operation.
func assertRefusalWasAudited(t *testing.T, fixture *integrationFixture, err error) {
	t.Helper()
	event := fixture.audit.last(t)
	if event.Result == domain.AuditResultSuccess {
		t.Fatalf("a refused operation was audited as a success: %+v", event)
	}
	if _, recorded := event.Metadata["outcome"]; recorded {
		t.Fatalf("a refused operation recorded an outcome: %+v", event.Metadata)
	}
	if err == nil {
		t.Fatal("this helper is only meaningful for a refusal")
	}
}

func countCommands(envelope []string, verb string) int {
	seen := 0
	for _, line := range envelope {
		if strings.HasPrefix(strings.ToUpper(line), verb) {
			seen++
		}
	}
	return seen
}
