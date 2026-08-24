package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// The integration routes (issue #582), exercised through the real guard chain.
//
// What is under test here is the router and the contract: which capability each
// route declares, that a mutation passes CSRF, that an unknown integration is a
// 404 rather than a target, and that nothing a credential holds reaches the
// wire. The diagnostic pipeline itself has its own suite in internal/service.

// integrationStub stands in for the integration service.
type integrationStub struct {
	calls  []string
	actor  service.Actor
	id     domain.IntegrationID
	view   service.IntegrationsView
	report domain.DiagnosticReport
	err    error
}

func (i *integrationStub) List(_ context.Context, actor service.Actor) (service.IntegrationsView, error) {
	i.calls = append(i.calls, "list")
	i.actor = actor
	return i.view, i.err
}

func (i *integrationStub) Diagnose(_ context.Context, actor service.Actor, id domain.IntegrationID) (domain.DiagnosticReport, error) {
	i.calls = append(i.calls, "diagnose")
	i.actor, i.id = actor, id
	return i.report, i.err
}

func (i *integrationStub) SendTestEmail(_ context.Context, actor service.Actor) (domain.DiagnosticReport, error) {
	i.calls = append(i.calls, "test-email")
	i.actor = actor
	return i.report, i.err
}

func integrationsStub() *integrationStub {
	latency := int64(12)
	descriptor, _ := domain.LookupIntegration(domain.IntegrationOIDC)
	health, _ := domain.LookupHealthService(domain.HealthServiceOIDC)
	credential, _ := domain.LookupConfig("secret.oidc_client_secret")
	flag, _ := domain.LookupConfig("oidc.enabled")
	return &integrationStub{
		view: service.IntegrationsView{
			CollectedAt: time.Date(2026, 8, 23, 11, 0, 0, 0, time.UTC),
			Integrations: []service.IntegrationStatus{{
				Descriptor:      descriptor,
				Diagnosable:     true,
				SettingsVisible: true,
				Health: domain.ServiceHealth{
					Descriptor: health, State: domain.HealthDegraded, Enabled: true,
					Observable: true, LatencyMS: &latency, CheckedAt: time.Now().UTC(),
				},
				Settings: []service.IntegrationSetting{
					{Setting: service.ConfigSetting{Definition: flag, Value: domain.TextValue("true"), Observable: true}},
					{Setting: service.ConfigSetting{Definition: credential, Observable: true, Configured: true}, Advanced: true},
				},
			}},
		},
		report: domain.DiagnosticReport{
			Integration: domain.IntegrationOIDC,
			StartedAt:   time.Date(2026, 8, 23, 11, 5, 0, 0, time.UTC),
			Status:      domain.DiagnosticPassed,
			Summary:     "Todas as etapas verificadas concluíram com sucesso.",
			Steps: []domain.DiagnosticStep{
				{Stage: domain.StageResolve, Status: domain.DiagnosticPassed, LatencyMS: &latency},
				{Stage: domain.StageCredential, Status: domain.DiagnosticSkipped},
			},
		},
	}
}

// The listing names every integration the deployment has and whether each one
// is reachable, so it is guarded like a mutation, one capability weaker.
func TestIntegrationAPI_ListRequiresTheReadCapability(t *testing.T) {
	stub := integrationsStub()
	harness := newHarness(t, adminStore(domain.CapabilityUsersRead), withIntegrations(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminIntegrations, cookie, ""))

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 0 {
		t.Fatal("a refused request must not reach the service")
	}
	if !recordedDenial(harness.store, string(domain.CapabilityIntegrationsRead)) {
		t.Fatal("expected the denial to be recorded against the capability the route declares")
	}
}

// A diagnostic opens outbound connections and signs a credential. Reading the
// surface is not enough to cause that.
func TestIntegrationAPI_DiagnoseRequiresTheManageCapability(t *testing.T) {
	stub := integrationsStub()
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsRead), withIntegrations(stub))
	cookie, csrf := harness.establish(t)

	path := "/integrations/" + string(domain.IntegrationOIDC) + "/diagnose"
	response := harness.do(harness.authenticated(t, http.MethodPost, path, cookie, csrf))

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 0 {
		t.Fatal("a refused diagnostic must not reach the service")
	}
}

func TestIntegrationAPI_TestEmailRequiresTheManageCapability(t *testing.T) {
	stub := integrationsStub()
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsRead), withIntegrations(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodPost, RouteAdminIntegrationTestEmail, cookie, csrf))

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 0 {
		t.Fatal("a refused test message must not reach the service")
	}
}

// A forged cross-site request must be refused as a forgery, before the
// capability check, so the trail records real denials and not noise a hostile
// page can generate at will.
func TestIntegrationAPI_MutationsRefuseAForgedRequest(t *testing.T) {
	paths := []string{"/integrations/oidc/diagnose", RouteAdminIntegrationTestEmail}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			stub := integrationsStub()
			harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
			cookie, csrf := harness.establish(t)

			withoutToken := harness.authenticated(t, http.MethodPost, path, cookie, "")
			if response := harness.do(withoutToken); response.Code != http.StatusForbidden {
				t.Fatalf("a request with no CSRF token must be refused, got %d", response.Code)
			}
			foreign := harness.authenticated(t, http.MethodPost, path, cookie, csrf)
			foreign.Header.Set("Origin", "https://evil.example")
			if response := harness.do(foreign); response.Code != http.StatusForbidden {
				t.Fatalf("a request from a foreign origin must be refused, got %d", response.Code)
			}
			if len(stub.calls) != 0 {
				t.Fatal("a forged request must not reach the service")
			}
		})
	}
}

// An identifier the registry does not declare is a 404 and never a target.
func TestIntegrationAPI_DiagnoseRefusesAnUnknownIntegration(t *testing.T) {
	for _, id := range []string{"unknown", "OIDC", "postgres"} {
		t.Run(id, func(t *testing.T) {
			stub := integrationsStub()
			harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.authenticated(t, http.MethodPost, "/integrations/"+id+"/diagnose", cookie, csrf))
			if response.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatal("an unknown identifier must not reach the service")
			}
		})
	}
}

func TestIntegrationAPI_DiagnoseForwardsTheResolvedIdentifier(t *testing.T) {
	stub := integrationsStub()
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodPost, "/integrations/smtp/diagnose", cookie, csrf))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if stub.id != domain.IntegrationSMTP {
		t.Fatalf("expected the resolved identifier, got %q", stub.id)
	}
	if stub.actor.UserID != testUserID || stub.actor.Email != "admin@example.test" {
		t.Fatalf("the actor must come from the session, got %+v", stub.actor)
	}
	if stub.actor.CorrelationID == "" {
		t.Fatal("the correlation id must be minted by this service")
	}
}

// The rate limit is a refusal the client can act on: 429 with Retry-After, not
// a 400 that reads as "your request was wrong".
func TestIntegrationAPI_RateLimitAnswers429(t *testing.T) {
	stub := integrationsStub()
	stub.err = domain.ErrTooManyRequests
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodPost, "/integrations/oidc/diagnose", cookie, csrf))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d (%s)", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("a rate-limited answer must say when to try again")
	}
}

// An integration with no adapter answers 409: the platform cannot check it, and
// saying so is different from saying it does not exist and from inventing a
// result.
func TestIntegrationAPI_UnsupportedDiagnosticAnswers409(t *testing.T) {
	stub := integrationsStub()
	stub.err = domain.ErrConflict
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodPost, "/integrations/turn/diagnose", cookie, csrf))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%s)", response.Code, response.Body.String())
	}
}

// The invariant, asserted against the serialized response: a credential
// attached to an integration reports whether it is configured and never what it
// is.
func TestIntegrationAPI_ListNeverSerializesACredential(t *testing.T) {
	stub := integrationsStub()
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsRead), withIntegrations(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminIntegrations, cookie, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}

	credentials := 0
	for _, integration := range decodeIntegrations(t, response) {
		credentials += assertIntegrationCredentials(t, integration)
	}
	if credentials == 0 {
		t.Fatal("expected the fixture to attach a credential to the integration")
	}
}

// assertIntegrationCredentials applies the one rule every sensitive setting
// obeys on the wire, and returns how many it checked.
//
// It reuses assertCredentialPayload rather than restating it: the integrations
// surface embeds the very same projection the configuration endpoint serves, so
// one assertion covering both is what keeps them from drifting.
func assertIntegrationCredentials(t *testing.T, integration map[string]any) int {
	t.Helper()
	settings, _ := integration["settings"].([]any)
	checked := 0
	for _, entry := range settings {
		setting, _ := entry.(map[string]any)
		if sensitive, _ := setting["sensitive"].(bool); !sensitive {
			continue
		}
		checked++
		assertCredentialPayload(t, setting)
	}
	return checked
}

// The payload carries the passive status, the declared plan and the reason an
// integration cannot be checked — everything the card renders without asking a
// second endpoint.
func TestIntegrationAPI_ListCarriesTheStatusAndThePlan(t *testing.T) {
	stub := integrationsStub()
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsRead), withIntegrations(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminIntegrations, cookie, ""))
	integrations := decodeIntegrations(t, response)
	if len(integrations) != 1 {
		t.Fatalf("expected one integration, got %d", len(integrations))
	}
	integration := integrations[0]
	if integration["state"] != string(domain.HealthDegraded) {
		t.Fatalf("expected the passive state on the card, got %v", integration["state"])
	}
	stages, _ := integration["stages"].([]any)
	if len(stages) == 0 {
		t.Fatal("the card must carry the declared diagnostic plan")
	}
	if diagnosable, _ := integration["diagnosable"].(bool); !diagnosable {
		t.Fatal("the OIDC card is diagnosable")
	}
}

// An integration with no measured round trip must not report zero milliseconds:
// a check that did not happen is not a fast one.
func TestIntegrationAPI_AbsentLatencyIsOmitted(t *testing.T) {
	stub := integrationsStub()
	stub.view.Integrations[0].Health.LatencyMS = nil
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsRead), withIntegrations(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminIntegrations, cookie, ""))
	if _, present := decodeIntegrations(t, response)[0]["latency_ms"]; present {
		t.Fatal("an unmeasured round trip must be absent, not zero")
	}
}

// A row with no collected health is unknown, never healthy and never blank.
func TestIntegrationAPI_MissingHealthIsUnknown(t *testing.T) {
	stub := integrationsStub()
	stub.view.Integrations[0].Health = domain.ServiceHealth{}
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsRead), withIntegrations(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminIntegrations, cookie, ""))
	if state := decodeIntegrations(t, response)[0]["state"]; state != string(domain.HealthUnknown) {
		t.Fatalf("an uncollected row must be unknown, got %v", state)
	}
}

// Both POSTs declare "request body: none". Silently executing an operation for
// a request that carries one would make the documented contract a suggestion.
//
// The rule is the absence of a *body*, not the absence of meaningful JSON, so
// `{}` is refused alongside `{"unexpected":true}` — and a single letter and a
// space are refused too, which is what proves the check is not a JSON decoder
// in disguise.
func TestIntegrationAPI_MutationsRefuseARequestBody(t *testing.T) {
	bodies := map[string]string{
		"json object":  `{"unexpected":true}`,
		"empty object": `{}`,
		"empty array":  `[]`,
		"empty string": `""`,
		"one letter":   "x",
		"whitespace":   " ",
		"newline":      "\n",
	}
	paths := []string{"/integrations/oidc/diagnose", RouteAdminIntegrationTestEmail}

	for _, path := range paths {
		for name, body := range bodies {
			t.Run(path+"/"+name, func(t *testing.T) {
				assertBodyRefused(t, path, body)
			})
		}
	}
}

// assertBodyRefused sends one forbidden body and holds the two claims that
// matter: the request is refused, and the operation behind it never ran.
func assertBodyRefused(t *testing.T, path, body string) {
	t.Helper()
	stub := integrationsStub()
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.withBody(t, http.MethodPost, path, cookie, csrf, body))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
	}
	// No diagnostic slot spent, no connection opened, no message delivered.
	if len(stub.calls) != 0 {
		t.Fatalf("a request that breaks the contract reached the service: %v", stub.calls)
	}
}

// The check must not depend on Content-Length: a chunked request does not carry
// one, and a header is a claim rather than a fact. This request reports a length
// of -1 and still has to be refused.
func TestIntegrationAPI_RefusesABodyOfUnknownLength(t *testing.T) {
	for _, path := range []string{"/integrations/oidc/diagnose", RouteAdminIntegrationTestEmail} {
		t.Run(path, func(t *testing.T) {
			stub := integrationsStub()
			harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
			cookie, csrf := harness.establish(t)

			// io.NopCloser hides the concrete reader, so httptest cannot infer
			// a length and leaves ContentLength at -1.
			request := httptest.NewRequest(http.MethodPost, path, io.NopCloser(strings.NewReader("x")))
			request.AddCookie(cookie)
			request.Header.Set("Origin", testOrigin)
			request.Header.Set(csrfHeaderName, csrf)
			if request.ContentLength >= 0 {
				t.Fatalf("this spec needs an unknown length, got %d", request.ContentLength)
			}

			response := harness.do(request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatalf("a request that breaks the contract reached the service: %v", stub.calls)
			}
		})
	}
}

// The other half of the contract: a request that genuinely carries nothing is
// served exactly as before.
func TestIntegrationAPI_MutationsAcceptARequestWithNoBody(t *testing.T) {
	for path, call := range map[string]string{
		"/integrations/oidc/diagnose":  "diagnose",
		RouteAdminIntegrationTestEmail: "test-email",
	} {
		t.Run(path, func(t *testing.T) {
			stub := integrationsStub()
			harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.authenticated(t, http.MethodPost, path, cookie, csrf))

			if response.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 1 || stub.calls[0] != call {
				t.Fatalf("expected the operation to run once, got %v", stub.calls)
			}
		})
	}
}

// withBody builds an authenticated request carrying a raw body, so a spec can
// send something the endpoint's contract forbids.
func (h *testHarness) withBody(
	t *testing.T,
	method, path string,
	cookie *http.Cookie,
	csrf, body string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(cookie)
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		request.Header.Set(csrfHeaderName, csrf)
	}
	return request
}

func TestIntegrationAPI_RoutesAcceptExactlyOneMethod(t *testing.T) {
	cases := map[string][]string{
		RouteAdminIntegrations:         {http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut},
		"/integrations/oidc/diagnose":  {http.MethodGet, http.MethodPatch, http.MethodDelete, http.MethodPut},
		RouteAdminIntegrationTestEmail: {http.MethodGet, http.MethodPatch, http.MethodDelete, http.MethodPut},
	}
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser), withIntegrations(integrationsStub()))
	cookie, csrf := harness.establish(t)
	for path, methods := range cases {
		for _, method := range methods {
			response := harness.do(harness.authenticated(t, method, path, cookie, csrf))
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s: expected 405, got %d", method, path, response.Code)
			}
		}
	}
}

// A deployment without the surface wired answers 503 rather than serving a
// diagnostic without its guards.
func TestIntegrationAPI_UnwiredSurfaceIsUnavailable(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser))
	cookie, csrf := harness.establish(t)

	for _, request := range []*http.Request{
		harness.authenticated(t, http.MethodGet, RouteAdminIntegrations, cookie, ""),
		harness.authenticated(t, http.MethodPost, "/integrations/oidc/diagnose", cookie, csrf),
		harness.authenticated(t, http.MethodPost, RouteAdminIntegrationTestEmail, cookie, csrf),
	} {
		if response := harness.do(request); response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: expected 503, got %d", request.Method, request.URL.Path, response.Code)
		}
	}
}

func TestIntegrationAPI_RefusesRequestsWithoutASession(t *testing.T) {
	stub := integrationsStub()
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser), withIntegrations(stub))
	requests := map[string]string{
		RouteAdminIntegrations:         http.MethodGet,
		"/integrations/oidc/diagnose":  http.MethodPost,
		RouteAdminIntegrationTestEmail: http.MethodPost,
	}
	for path, method := range requests {
		request := httptest.NewRequest(method, path, nil)
		if response := harness.do(request); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: expected 401, got %d", method, path, response.Code)
		}
	}
	if len(stub.calls) != 0 {
		t.Fatal("an unauthenticated request must not reach the service")
	}
}

// The report reaches the console as stages, with the sanitized category and no
// remote text. A failed run is a 200 carrying the diagnosis, not a 502.
func TestIntegrationAPI_DiagnosticReportIsStaged(t *testing.T) {
	stub := integrationsStub()
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodPost, "/integrations/oidc/diagnose", cookie, csrf))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Report struct {
				Status string `json:"status"`
				Steps  []struct {
					Stage     string `json:"stage"`
					Status    string `json:"status"`
					LatencyMS *int64 `json:"latency_ms"`
				} `json:"steps"`
			} `json:"report"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Data.Report.Status != string(domain.DiagnosticPassed) || len(envelope.Data.Report.Steps) != 2 {
		t.Fatalf("unexpected report: %+v", envelope.Data.Report)
	}
	// A stage that did not run carries no latency: it was not measured, and
	// zero would read as instantaneous.
	if envelope.Data.Report.Steps[1].LatencyMS != nil {
		t.Fatal("a skipped stage must not report a duration")
	}
}

func decodeIntegrations(t *testing.T, response *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var envelope struct {
		Data struct {
			Integrations []map[string]any `json:"integrations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return envelope.Data.Integrations
}

// The test message takes no body and no destination. The handler forwards the
// actor the session guard built, and nothing else.
func TestIntegrationAPI_TestEmailTakesNoDestination(t *testing.T) {
	stub := integrationsStub()
	stub.report.Integration = domain.IntegrationSMTP
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodPost, RouteAdminIntegrationTestEmail, cookie, csrf))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 1 || stub.calls[0] != "test-email" {
		t.Fatalf("unexpected calls %v", stub.calls)
	}
	if stub.actor.Email != "admin@example.test" {
		t.Fatalf("the destination must be the authenticated administrator, got %q", stub.actor.Email)
	}
	if !strings.Contains(response.Body.String(), `"integration":"smtp"`) {
		t.Fatalf("the report must name the integration: %s", response.Body.String())
	}
}

// An administrative account with no usable address is a 400 the operator can
// act on, not a 500.
func TestIntegrationAPI_TestEmailRefusesAMalformedAdministrativeAddress(t *testing.T) {
	stub := integrationsStub()
	stub.err = domain.ErrInvalidInput
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodPost, RouteAdminIntegrationTestEmail, cookie, csrf))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
	}
}

// The card carries the actions the registry declares, with the capability each
// one requires, so the console can disable what it must not offer — while the
// API still refuses it either way.
func TestIntegrationAPI_ListCarriesDeclaredActions(t *testing.T) {
	stub := integrationsStub()
	smtp, _ := domain.LookupIntegration(domain.IntegrationSMTP)
	stub.view.Integrations = append(stub.view.Integrations, service.IntegrationStatus{
		Descriptor: smtp, Diagnosable: true, SettingsVisible: true,
	})
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsRead), withIntegrations(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminIntegrations, cookie, ""))
	integrations := decodeIntegrations(t, response)
	actions, _ := integrations[1]["actions"].([]any)
	if len(actions) != 1 {
		t.Fatalf("expected the SMTP test message, got %v", actions)
	}
	action, _ := actions[0].(map[string]any)
	if action["id"] != string(domain.IntegrationActionSMTPTestEmail) {
		t.Fatalf("unexpected action %v", action)
	}
	if action["capability"] != string(domain.CapabilityIntegrationsManage) {
		t.Fatalf("the action must declare the capability it requires, got %v", action["capability"])
	}
}

// An integration the platform cannot check says so, with the reason, instead of
// offering a button that would invent a result.
func TestIntegrationAPI_ListExplainsAnUnsupportedDiagnostic(t *testing.T) {
	stub := integrationsStub()
	turn, _ := domain.LookupIntegration(domain.IntegrationTURN)
	stub.view.Integrations = []service.IntegrationStatus{{Descriptor: turn}}
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsRead), withIntegrations(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminIntegrations, cookie, ""))
	integration := decodeIntegrations(t, response)[0]
	if diagnosable, _ := integration["diagnosable"].(bool); diagnosable {
		t.Fatal("TURN must not be offered as diagnosable")
	}
	reason, _ := integration["diagnostic_unsupported"].(string)
	if strings.TrimSpace(reason) == "" {
		t.Fatal("an integration with no diagnostic must explain why")
	}
}

// An operator without admin.config.read gets the status without the inventory,
// and the payload says which of the two it is.
func TestIntegrationAPI_ListDistinguishesHiddenSettingsFromNone(t *testing.T) {
	stub := integrationsStub()
	stub.view.Integrations[0].SettingsVisible = false
	stub.view.Integrations[0].Settings = nil
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsRead), withIntegrations(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminIntegrations, cookie, ""))
	integration := decodeIntegrations(t, response)[0]
	if visible, _ := integration["settings_visible"].(bool); visible {
		t.Fatal("the payload must say the inventory is hidden rather than empty")
	}
}

// A revocation that lands mid-request reaches the client as the same coarse
// answer the middleware would have given: 401 to prove identity again, 403 for
// an identity that is known and still not allowed. Neither body says which of
// the several possible revocations applied.
func TestIntegrationAPI_RevocationDuringTheRequestIsRefused(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
	}{
		"capability removed":  {domain.ErrForbidden, http.StatusForbidden},
		"session revoked":     {domain.ErrUnauthorized, http.StatusUnauthorized},
		"principal suspended": {domain.ErrUnauthorized, http.StatusUnauthorized},
	}
	paths := []string{"/integrations/oidc/diagnose", RouteAdminIntegrationTestEmail}

	for _, path := range paths {
		for name, testCase := range cases {
			t.Run(path+"/"+name, func(t *testing.T) {
				assertRevocationAnswer(t, path, testCase.err, testCase.status)
			})
		}
	}
}

// assertRevocationAnswer holds the client-facing half: the right coarse status,
// and a body that says nothing about why.
func assertRevocationAnswer(t *testing.T, path string, revocation error, status int) {
	t.Helper()
	stub := integrationsStub()
	stub.err = revocation
	harness := newHarness(t, adminStore(domain.CapabilityIntegrationsManage), withIntegrations(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodPost, path, cookie, csrf))

	if response.Code != status {
		t.Fatalf("expected %d, got %d (%s)", status, response.Code, response.Body.String())
	}
	// No session id, no principal id, no role, no SQL.
	body := response.Body.String()
	for _, leak := range []string{testUserID, testAdminSession, testAuthSessionID, "admin.integrations", "SELECT"} {
		if strings.Contains(body, leak) {
			t.Fatalf("the refusal leaked %q: %s", leak, body)
		}
	}
}
