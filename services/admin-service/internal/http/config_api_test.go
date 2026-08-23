package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// configStub stands in for the configuration service. It records what it was
// asked and answers with whatever a spec set up: what is under test here is the
// router, the guards and the contract, not the pipeline — that has its own
// suite in internal/service.
type configStub struct {
	calls    []string
	request  service.ConfigChangeRequest
	actor    service.Actor
	view     service.ConfigCatalogView
	plan     service.ConfigPlan
	result   service.ConfigApplyResult
	versions []domain.ConfigVersion
	document domain.ConfigDocument
	limit    int
	rollback int64
	revision int
	reason   string
	err      error
}

func (c *configStub) Catalog(context.Context) (service.ConfigCatalogView, error) {
	c.calls = append(c.calls, "catalog")
	return c.view, c.err
}

func (c *configStub) Preview(_ context.Context, actor service.Actor, request service.ConfigChangeRequest) (service.ConfigPlan, error) {
	c.calls = append(c.calls, "preview")
	c.actor, c.request = actor, request
	return c.plan, c.err
}

func (c *configStub) Apply(_ context.Context, actor service.Actor, request service.ConfigChangeRequest) (service.ConfigApplyResult, error) {
	c.calls = append(c.calls, "apply")
	c.actor, c.request = actor, request
	return c.result, c.err
}

func (c *configStub) PreviewRollback(_ context.Context, actor service.Actor, versionID int64, revision int, reason string) (service.ConfigPlan, error) {
	c.calls = append(c.calls, "rollback-preview")
	c.actor, c.rollback, c.revision, c.reason = actor, versionID, revision, reason
	return c.plan, c.err
}

func (c *configStub) Versions(_ context.Context, document domain.ConfigDocument, limit int) ([]domain.ConfigVersion, error) {
	c.calls = append(c.calls, "versions")
	c.document, c.limit = document, limit
	return c.versions, c.err
}

func (c *configStub) Rollback(_ context.Context, actor service.Actor, versionID int64, revision int, _ string) (service.ConfigApplyResult, error) {
	c.calls = append(c.calls, "rollback")
	c.actor, c.rollback, c.revision = actor, versionID, revision
	return c.result, c.err
}

// configuredStub answers a catalog built from the real registry, so the
// contract specs run against the settings the platform actually declares.
func configuredStub() *configStub {
	settings := make([]service.ConfigSetting, 0, len(domain.ConfigCatalog()))
	for _, definition := range domain.ConfigCatalog() {
		setting := service.ConfigSetting{Definition: definition, Observable: true}
		switch {
		case definition.Sensitive:
			setting.Configured = true
		case definition.Editable:
			setting.Value = definition.Default
		default:
			setting.Value = domain.TextValue("observed")
		}
		settings = append(settings, setting)
	}
	return &configStub{view: service.ConfigCatalogView{
		Documents: []domain.ConfigDocumentState{{Document: domain.ConfigDocumentAuthPolicy, Revision: 3}},
		Settings:  settings,
	}}
}

func configBody(t *testing.T, body any) *strings.Reader {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	return strings.NewReader(string(encoded))
}

func (h *testHarness) configRequest(t *testing.T, method, path string, cookie *http.Cookie, csrf string, body any) *http.Request {
	t.Helper()
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, configBody(t, body))
		request.Header.Set("Content-Type", "application/json")
	}
	request.AddCookie(cookie)
	request.Header.Set("Origin", testOrigin)
	if csrf != "" {
		request.Header.Set(csrfHeaderName, csrf)
	}
	return request
}

func validChange() map[string]any {
	return map[string]any{
		"document":          string(domain.ConfigDocumentAuthPolicy),
		"expected_revision": 3,
		"changes":           map[string]any{string(domain.ConfigKeyDeviceMaxPerUser): 8},
	}
}

// The catalog names every integration, endpoint and credential the deployment
// has. It is guarded like a mutation, one capability weaker.
func TestConfigAPI_CatalogRequiresTheReadCapability(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilityUsersRead), withConfiguration(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodGet, RouteAdminConfig, cookie, "", nil))

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 0 {
		t.Fatal("a refused request must not reach the service")
	}
	if !recordedDenial(harness.store, string(domain.CapabilityConfigRead)) {
		t.Fatal("expected the denial to be recorded against the capability the route declares")
	}
}

// The invariant, asserted against the serialized response and the whole
// registry: no credential value is on the wire, and every credential still
// reports whether it is configured.
func TestConfigAPI_CatalogNeverSerializesACredential(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodGet, RouteAdminConfig, cookie, "", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}

	credentials := 0
	for _, setting := range decodeConfigSettings(t, response) {
		if sensitive, _ := setting["sensitive"].(bool); !sensitive {
			continue
		}
		credentials++
		assertCredentialPayload(t, setting)
	}
	if credentials == 0 {
		t.Fatal("expected the payload to inventory the platform credentials")
	}
	if !strings.Contains(response.Body.String(), `"revision":3`) {
		t.Fatal("expected the document revision to travel with the catalog")
	}
}

// assertCredentialPayload holds the one rule every sensitive setting obeys on
// the wire: a status, never a value, and no default to compare it against.
func assertCredentialPayload(t *testing.T, setting map[string]any) {
	t.Helper()
	if _, present := setting["value"]; present {
		t.Fatalf("%v serialized a value", setting["key"])
	}
	if _, present := setting["configured"]; !present {
		t.Fatalf("%v must report whether it is configured", setting["key"])
	}
	if _, present := setting["default"]; present {
		t.Fatalf("%v must not publish a default", setting["key"])
	}
}

func TestConfigAPI_ApplyRequiresTheManageCapability(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigApply, cookie, csrf, validChange()))

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 0 {
		t.Fatal("a refused mutation must not reach the service")
	}
}

// Preview reveals nothing the read capability does not already cover, so it is
// guarded by that one — and, being a POST, by CSRF and origin as well.
func TestConfigAPI_PreviewIsAllowedWithTheReadCapability(t *testing.T) {
	stub := configuredStub()
	stub.plan = service.ConfigPlan{Document: domain.ConfigDocumentAuthPolicy, Revision: 3, Apply: domain.ConfigApplyRuntime}
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigPreview, cookie, csrf, validChange()))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 1 || stub.calls[0] != "preview" {
		t.Fatalf("expected exactly one preview, got %v", stub.calls)
	}
}

// Every mutation on this surface passes the CSRF and origin guards. A forged
// cross-site request is refused as a forgery, before the capability check, so
// it does not fill the trail with somebody else's authorization failures.
func TestConfigAPI_MutationsRefuseAForgedRequest(t *testing.T) {
	for _, route := range []string{RouteAdminConfigPreview, RouteAdminConfigApply} {
		t.Run(route, func(t *testing.T) {
			stub := configuredStub()
			harness := newHarness(t, adminStore(domain.CapabilitySuperuser), withConfiguration(stub))
			cookie, csrf := harness.establish(t)

			missing := harness.configRequest(t, http.MethodPost, route, cookie, "", validChange())
			if code := harness.do(missing).Code; code != http.StatusForbidden {
				t.Fatalf("expected 403 without a CSRF token, got %d", code)
			}

			hostile := harness.configRequest(t, http.MethodPost, route, cookie, csrf, validChange())
			hostile.Header.Set("Origin", "https://evil.example")
			if code := harness.do(hostile).Code; code != http.StatusForbidden {
				t.Fatalf("expected 403 from a foreign origin, got %d", code)
			}

			if len(stub.calls) != 0 {
				t.Fatal("a forged request must not reach the service")
			}
		})
	}
}

func TestConfigAPI_RollbackRefusesAForgedRequest(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser), withConfiguration(stub))
	cookie, _ := harness.establish(t)

	request := harness.configRequest(t, http.MethodPost, "/config/versions/7/rollback", cookie, "",
		map[string]any{"expected_revision": 3})
	if code := harness.do(request).Code; code != http.StatusForbidden {
		t.Fatalf("expected 403 without a CSRF token, got %d", code)
	}
	if len(stub.calls) != 0 {
		t.Fatal("a forged rollback must not reach the service")
	}
}

// The body is decoded into a named struct with DisallowUnknownFields, so a
// field the API never agreed to accept cannot be bound onto anything.
func TestConfigAPI_ApplyRefusesUnknownBodyFields(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	body := validChange()
	body["actor_user_id"] = "99999999-9999-9999-9999-999999999999"
	body["capabilities"] = []string{"admin.superuser"}

	response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigApply, cookie, csrf, body))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 0 {
		t.Fatal("a body carrying unknown fields must not reach the service")
	}
}

// The actor is built from the authenticated principal and never from the body,
// and it carries the capability set the session guard loaded on this request.
func TestConfigAPI_ActorAndCapabilitiesComeFromTheSession(t *testing.T) {
	stub := configuredStub()
	stub.result = service.ConfigApplyResult{
		Applied: true,
		State:   domain.ConfigDocumentState{Document: domain.ConfigDocumentAuthPolicy, Revision: 4},
		Version: domain.ConfigVersion{ID: 9, Document: domain.ConfigDocumentAuthPolicy, Revision: 4, AppliedAt: time.Now().UTC()},
	}
	harness := newHarness(t, adminStore(domain.CapabilityConfigManage), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigApply, cookie, csrf, validChange()))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}

	if stub.actor.UserID != testUserID {
		t.Fatalf("expected the session's principal, got %q", stub.actor.UserID)
	}
	if stub.actor.CorrelationID == "" {
		t.Fatal("expected the server-minted request id to travel with the actor")
	}
	if !stub.actor.Capabilities.Has(domain.CapabilityConfigManage) {
		t.Fatal("expected the live capability set to reach the service")
	}
	if stub.actor.Capabilities.Has(domain.CapabilitySuperuser) {
		t.Fatal("the actor must hold only what the principal was granted")
	}
	if stub.request.ExpectedRevision != 3 {
		t.Fatalf("expected the revision to be forwarded, got %d", stub.request.ExpectedRevision)
	}
}

// The values a change carries are raw JSON until the registry decides how to
// read them: the transport must not pre-decide a type.
func TestConfigAPI_ForwardsValuesUndecoded(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	body := validChange()
	body["changes"] = map[string]any{string(domain.ConfigKeyPasswordMinLength): "not-a-number"}
	if code := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigPreview, cookie, csrf, body)).Code; code != http.StatusOK {
		t.Fatalf("expected the transport to forward and the pipeline to judge, got %d", code)
	}
	if string(stub.request.Values[domain.ConfigKeyPasswordMinLength]) != `"not-a-number"` {
		t.Fatalf("expected the raw JSON to be forwarded, got %s", stub.request.Values[domain.ConfigKeyPasswordMinLength])
	}
}

func TestConfigAPI_RefusesMalformedEnvelopes(t *testing.T) {
	cases := map[string]map[string]any{
		"unknown document": {
			"document": "auth.unknown", "expected_revision": 3,
			"changes": map[string]any{string(domain.ConfigKeyDeviceMaxPerUser): 8},
		},
		"missing revision": {
			"document": string(domain.ConfigDocumentAuthPolicy),
			"changes":  map[string]any{string(domain.ConfigKeyDeviceMaxPerUser): 8},
		},
		"revision as text": {
			"document": string(domain.ConfigDocumentAuthPolicy), "expected_revision": "3",
			"changes": map[string]any{string(domain.ConfigKeyDeviceMaxPerUser): 8},
		},
		"empty change set": {
			"document": string(domain.ConfigDocumentAuthPolicy), "expected_revision": 3,
			"changes": map[string]any{},
		},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			stub := configuredStub()
			harness := newHarness(t, adminStore(domain.CapabilitySuperuser), withConfiguration(stub))
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigApply, cookie, csrf, body))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatal("a malformed envelope must not reach the service")
			}
		})
	}
}

// More entries than the platform declares is a body worth refusing before any
// of it is resolved.
func TestConfigAPI_RefusesAnOversizedChangeSet(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	changes := make(map[string]any, 64)
	for index := 0; index < 64; index++ {
		changes["auth.made.up"+string(rune('a'+index%26))+string(rune('a'+index/26))] = 1
	}
	body := validChange()
	body["changes"] = changes

	response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigApply, cookie, csrf, body))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 0 {
		t.Fatal("an oversized change set must not reach the service")
	}
}

// A lost race is a 409 in the platform's error envelope, never a merge and
// never a silent success. The console reloads and previews again from there.
func TestConfigAPI_ConflictIsRefusedInThePlatformEnvelope(t *testing.T) {
	stub := configuredStub()
	stub.err = domain.ErrConflict
	stub.result = service.ConfigApplyResult{Plan: service.ConfigPlan{
		Document: domain.ConfigDocumentAuthPolicy, Revision: 9, Stale: true,
	}}
	harness := newHarness(t, adminStore(domain.CapabilityConfigManage), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigApply, cookie, csrf, validChange()))

	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%s)", response.Code, response.Body.String())
	}
	var envelope struct {
		Data  any                    `json:"data"`
		Error *struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != "conflict" {
		t.Fatalf("expected a conflict envelope, got %s", response.Body.String())
	}
	if envelope.Data != nil {
		t.Fatalf("a refusal must carry no data, got %s", response.Body.String())
	}
}

func TestConfigAPI_VersionsRefusesAnUnknownDocument(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodGet, RouteAdminConfigVersions+"?document=auth.unknown", cookie, "", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
	}

	response = harness.do(harness.configRequest(t, http.MethodGet, RouteAdminConfigVersions, cookie, "", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if stub.document != domain.ConfigDocumentAuthPolicy {
		t.Fatalf("expected the default document, got %q", stub.document)
	}
}

func TestConfigAPI_RollbackRefusesAMalformedVersion(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilityConfigManage), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	for _, path := range []string{"/config/versions/0/rollback", "/config/versions/abc/rollback", "/config/versions/-1/rollback"} {
		response := harness.do(harness.configRequest(t, http.MethodPost, path, cookie, csrf, map[string]any{"expected_revision": 3}))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d (%s)", path, response.Code, response.Body.String())
		}
	}
	if len(stub.calls) != 0 {
		t.Fatal("a malformed version id must not reach the service")
	}
}

func TestConfigAPI_RollbackForwardsTheVersionAndRevision(t *testing.T) {
	stub := configuredStub()
	stub.result = service.ConfigApplyResult{
		Applied: true,
		State:   domain.ConfigDocumentState{Document: domain.ConfigDocumentAuthPolicy, Revision: 5},
		Version: domain.ConfigVersion{ID: 11, Document: domain.ConfigDocumentAuthPolicy, Revision: 5, RevertsRevision: 3},
	}
	harness := newHarness(t, adminStore(domain.CapabilityConfigManage), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, "/config/versions/7/rollback", cookie, csrf,
		map[string]any{"expected_revision": 4, "reason": "revert"}))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if stub.rollback != 7 || stub.revision != 4 {
		t.Fatalf("expected version 7 at revision 4, got %d / %d", stub.rollback, stub.revision)
	}
}

// Every configuration route is one method. Anything else is a 405 with an
// Allow header, not a handler reached by an unexpected verb.
func TestConfigAPI_RoutesAcceptExactlyOneMethod(t *testing.T) {
	cases := []struct {
		path    string
		allowed string
		refused string
	}{
		{RouteAdminConfig, http.MethodGet, http.MethodPost},
		{RouteAdminConfigPreview, http.MethodPost, http.MethodGet},
		{RouteAdminConfigApply, http.MethodPost, http.MethodPatch},
		{RouteAdminConfigVersions, http.MethodGet, http.MethodDelete},
		{"/config/versions/7/rollback", http.MethodPost, http.MethodGet},
	}
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	for _, testCase := range cases {
		response := harness.do(harness.configRequest(t, testCase.refused, testCase.path, cookie, csrf, nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: expected 405, got %d", testCase.refused, testCase.path, response.Code)
		}
		if allow := response.Header().Get("Allow"); !strings.Contains(allow, testCase.allowed) {
			t.Fatalf("%s: expected Allow to name %s, got %q", testCase.path, testCase.allowed, allow)
		}
	}
}

// A pod without the configuration surface refuses these paths rather than
// serving one of them unguarded.
func TestConfigAPI_UnwiredSurfaceIsUnavailable(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser))
	cookie, csrf := harness.establish(t)

	for _, route := range []string{RouteAdminConfig, RouteAdminConfigVersions} {
		if code := harness.do(harness.configRequest(t, http.MethodGet, route, cookie, "", nil)).Code; code != http.StatusServiceUnavailable {
			t.Fatalf("%s: expected 503, got %d", route, code)
		}
	}
	for _, route := range []string{RouteAdminConfigPreview, RouteAdminConfigApply} {
		if code := harness.do(harness.configRequest(t, http.MethodPost, route, cookie, csrf, validChange())).Code; code != http.StatusServiceUnavailable {
			t.Fatalf("%s: expected 503, got %d", route, code)
		}
	}
}

// No cookie is no session, on a read and on a write alike.
func TestConfigAPI_RefusesRequestsWithoutASession(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser), withConfiguration(stub))

	request := httptest.NewRequest(http.MethodGet, RouteAdminConfig, nil)
	if code := harness.do(request).Code; code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
	if len(stub.calls) != 0 {
		t.Fatal("an unauthenticated request must not reach the service")
	}
}

// decodeConfigList reads a named array of objects out of a payload, failing
// with the field that was wrong rather than panicking on a nil map three
// assertions later.
func decodeConfigList(t *testing.T, payload map[string]any, field string) []map[string]any {
	t.Helper()
	raw, ok := payload[field].([]any)
	if !ok {
		t.Fatalf("expected %s to be a list, got %v", field, payload[field])
	}
	entries := make([]map[string]any, 0, len(raw))
	for index, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("expected %s[%d] to be an object, got %v", field, index, item)
		}
		entries = append(entries, entry)
	}
	return entries
}

func decodeConfigSettings(t *testing.T, response *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	settings := decodeConfigList(t, decodeConfigData(t, response), "settings")
	if len(settings) == 0 {
		t.Fatal("expected settings in the payload")
	}
	return settings
}

func decodeConfigData(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (body %s)", err, response.Body.String())
	}
	return envelope.Data
}

func recordedDenial(store *stubStore, capability string) bool {
	for _, event := range store.audit {
		if event.Action == domain.AuditActionAuthorizationDeny && event.Metadata["capability"] == capability {
			return true
		}
	}
	return false
}

// The diff and the version payloads: labels, units and danger notes come from
// the registry, so the console never has to re-derive them.
// recordedVersion is a history entry a spec can assert against: a change the
// registry still declares, and one it no longer does.
func recordedVersion() domain.ConfigVersion {
	return domain.ConfigVersion{
		ID: 7, Document: domain.ConfigDocumentAuthPolicy, Revision: 4,
		AppliedAt:   time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		ActorUserID: testUserID, ActorEmail: "admin@example.test",
		CorrelationID: "req-7", Reason: "endurecimento", RevertsRevision: 3,
		Changes: []domain.ConfigChange{
			{Key: domain.ConfigKeyPasswordMinLength, From: domain.IntValue(12), To: domain.IntValue(8)},
			{Key: "auth.removed.setting", From: domain.IntValue(1), To: domain.IntValue(2)},
		},
	}
}

// readVersions performs the history request and returns the single version it
// answered with.
func readVersions(t *testing.T, stub *configStub, query string) map[string]any {
	t.Helper()
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodGet, RouteAdminConfigVersions+query, cookie, "", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	versions := decodeConfigList(t, decodeConfigData(t, response), "versions")
	if len(versions) != 1 {
		t.Fatalf("expected one version, got %v", versions)
	}
	return versions[0]
}

func TestConfigAPI_VersionPayloadNamesTheRevisionItReverted(t *testing.T) {
	stub := configuredStub()
	stub.versions = []domain.ConfigVersion{recordedVersion()}

	version := readVersions(t, stub, "?limit=10")

	if stub.limit != 10 {
		t.Fatalf("expected the requested limit to be forwarded, got %d", stub.limit)
	}
	if version["id"] != "7" {
		t.Fatalf("unexpected version id: %v", version["id"])
	}
	if version["reverts_revision"] != float64(3) {
		t.Fatalf("expected the reverted revision to be named, got %v", version["reverts_revision"])
	}
	// A version naming a key the registry no longer declares cannot be undone,
	// and the server is what says so.
	if version["rollbackable"] != false {
		t.Fatalf("expected the version to be unrollbackable: %v", version)
	}
}

// The diff and the version payloads: labels, units and danger notes come from
// the registry, so the console never has to re-derive them.
func TestConfigAPI_VersionPayloadCarriesTheTypedDiff(t *testing.T) {
	stub := configuredStub()
	stub.versions = []domain.ConfigVersion{recordedVersion()}

	changes := decodeConfigList(t, readVersions(t, stub, ""), "changes")

	if len(changes) != 2 {
		t.Fatalf("expected both recorded changes, got %v", changes)
	}
	known := changes[0]
	expectFields(t, known, map[string]any{
		"unit":          "caracteres",
		"owner_service": "auth-service",
		"dangerous":     true,
		"from":          float64(12),
		"to":            float64(8),
	})
	if known["label"] == "" || known["danger_note"] == "" {
		t.Fatalf("expected the registry's prose on the diff: %v", known)
	}
	// A removed key is still rendered, as itself: hiding a recorded change is
	// worse than showing one without its label.
	if changes[1]["label"] != "auth.removed.setting" {
		t.Fatalf("a removed key must still be rendered as itself: %v", changes[1])
	}
}

// expectFields asserts the payload fields a spec named, and names the one that
// disagreed instead of dumping the whole object.
func expectFields(t *testing.T, payload map[string]any, expected map[string]any) {
	t.Helper()
	for key, want := range expected {
		if payload[key] != want {
			t.Fatalf("%s = %v, expected %v", key, payload[key], want)
		}
	}
}

func TestConfigAPI_AppliedResponseCarriesTheStoredValuesAndTheVersion(t *testing.T) {
	stub := configuredStub()
	stub.result = service.ConfigApplyResult{
		Applied: true,
		State: domain.ConfigDocumentState{
			Document: domain.ConfigDocumentAuthPolicy,
			Revision: 4,
			Values: map[domain.ConfigKey]domain.ConfigValue{
				domain.ConfigKeyDeviceMaxPerUser:       domain.IntValue(8),
				domain.ConfigKeyPasswordExpirationDays: domain.NullValue(domain.ConfigTypeInt),
			},
		},
		Version: domain.ConfigVersion{ID: 9, Document: domain.ConfigDocumentAuthPolicy, Revision: 4},
		Plan: service.ConfigPlan{
			Document: domain.ConfigDocumentAuthPolicy, Revision: 3, Apply: domain.ConfigApplyRuntime,
			RequiredCapability: domain.CapabilityConfigManage, Authorized: true,
			Changes: []domain.ConfigChange{
				{Key: domain.ConfigKeyDeviceMaxPerUser, From: domain.IntValue(5), To: domain.IntValue(8)},
			},
		},
	}
	harness := newHarness(t, adminStore(domain.CapabilityConfigManage), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigApply, cookie, csrf, validChange()))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}

	body := decodeConfigData(t, response)
	if body["applied"] != true || body["revision"].(float64) != 4 {
		t.Fatalf("unexpected result: %v", body)
	}
	values, _ := body["values"].(map[string]any)
	if values[string(domain.ConfigKeyDeviceMaxPerUser)].(float64) != 8 {
		t.Fatalf("expected the stored value, got %v", values)
	}
	if raw, present := values[string(domain.ConfigKeyPasswordExpirationDays)]; !present || raw != nil {
		t.Fatalf("an unset nullable value must be sent as null, got %v", values)
	}
	if _, present := body["version"]; !present {
		t.Fatal("an applied change must name the version it created")
	}
	plan, _ := body["plan"].(map[string]any)
	if plan["apply"] != string(domain.ConfigApplyRuntime) {
		t.Fatalf("expected the plan to say how the change was applied: %v", plan)
	}
}

// The idempotent case: nothing changed, so no version is named.
func TestConfigAPI_NoOpApplyReportsAppliedFalseWithoutAVersion(t *testing.T) {
	stub := configuredStub()
	stub.result = service.ConfigApplyResult{
		Applied: false,
		State:   domain.ConfigDocumentState{Document: domain.ConfigDocumentAuthPolicy, Revision: 3},
		Plan:    service.ConfigPlan{Document: domain.ConfigDocumentAuthPolicy, Revision: 3},
	}
	harness := newHarness(t, adminStore(domain.CapabilityConfigManage), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigApply, cookie, csrf, validChange()))

	body := decodeConfigData(t, response)
	if body["applied"] != false {
		t.Fatalf("expected a no-op, got %v", body)
	}
	if _, present := body["version"]; present {
		t.Fatal("a no-op must not name a version")
	}
}

// The plan on the preview response carries the validation failures, which is
// where the console reads its per-field messages from.
func TestConfigAPI_PreviewPayloadCarriesValidationFailures(t *testing.T) {
	stub := configuredStub()
	stub.plan = service.ConfigPlan{
		Document: domain.ConfigDocumentAuthPolicy, Revision: 3, Apply: domain.ConfigApplyRuntime,
		RequiredCapability: domain.CapabilitySuperuser, Dangerous: true, ReasonRequired: true,
		Warnings:         []string{"Enfraquece a autenticação local."},
		AffectedServices: []string{"auth-service"},
		Errors: []service.ConfigValidationError{
			{Key: domain.ConfigKeyPasswordMinLength, Message: "auth.password.min_length must be between 8 and 128"},
		},
	}
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigPreview, cookie, csrf, validChange()))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}

	plan, _ := decodeConfigData(t, response)["plan"].(map[string]any)
	failures, _ := plan["errors"].([]any)
	if len(failures) != 1 {
		t.Fatalf("expected the validation failure in the plan: %v", plan)
	}
	failure, _ := failures[0].(map[string]any)
	if failure["key"] != string(domain.ConfigKeyPasswordMinLength) {
		t.Fatalf("expected the failure to name its field: %v", failure)
	}
	if plan["required_capability"] != string(domain.CapabilitySuperuser) || plan["reason_required"] != true {
		t.Fatalf("expected the plan to state what the change demands: %v", plan)
	}
	if warnings, _ := plan["warnings"].([]any); len(warnings) != 1 {
		t.Fatalf("expected the warning to travel: %v", plan)
	}
}

func TestConfigAPI_CatalogFailureIsNotServedAsAnEmptyCatalogue(t *testing.T) {
	stub := configuredStub()
	stub.err = domain.ErrUnavailable
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, _ := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodGet, RouteAdminConfig, cookie, "", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d (%s)", response.Code, response.Body.String())
	}
}

func TestConfigAPI_VersionsRefusesAMalformedLimit(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, _ := harness.establish(t)

	for _, query := range []string{"?limit=abc", "?limit=0", "?limit=-3"} {
		response := harness.do(harness.configRequest(t, http.MethodGet, RouteAdminConfigVersions+query, cookie, "", nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", query, response.Code)
		}
	}
	if len(stub.calls) != 0 {
		t.Fatal("a malformed limit must not reach the service")
	}
}

func TestConfigAPI_RollbackRefusesAMalformedBody(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilityConfigManage), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	for _, body := range []map[string]any{
		{"expected_revision": "4"},
		{"expected_revision": 0},
		{"reason": "sem revisao"},
		{"expected_revision": 4, "changes": map[string]any{}},
	} {
		response := harness.do(harness.configRequest(t, http.MethodPost, "/config/versions/7/rollback", cookie, csrf, body))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%v: expected 400, got %d (%s)", body, response.Code, response.Body.String())
		}
	}
	if len(stub.calls) != 0 {
		t.Fatal("a malformed rollback must not reach the service")
	}
}

func TestConfigAPI_RollbackSurfacesAMissingVersionAsNotFound(t *testing.T) {
	stub := configuredStub()
	stub.err = domain.ErrNotFound
	harness := newHarness(t, adminStore(domain.CapabilityConfigManage), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, "/config/versions/99/rollback", cookie, csrf,
		map[string]any{"expected_revision": 4}))

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", response.Code, response.Body.String())
	}
}

// A rollback the platform can no longer perform is a conflict the console can
// act on, never a 500 and never a silent success.
func TestConfigAPI_SupersededRollbackIsAConflict(t *testing.T) {
	stub := configuredStub()
	stub.err = domain.ErrConflict
	harness := newHarness(t, adminStore(domain.CapabilityConfigManage), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, "/config/versions/7/rollback", cookie, csrf,
		map[string]any{"expected_revision": 4, "reason": "reverter"}))

	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%s)", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "internal") {
		t.Fatalf("a superseded rollback must not be reported as an internal error: %s", response.Body.String())
	}
}

// The plan tells the console *why* it cannot proceed, so a superseded version
// can be shown as no longer revertible instead of failing on click.
func TestConfigAPI_PlanPublishesSupersededSeparatelyFromStale(t *testing.T) {
	stub := configuredStub()
	stub.plan = service.ConfigPlan{
		Document: domain.ConfigDocumentAuthPolicy, Revision: 5, Apply: domain.ConfigApplyRuntime,
		Stale: false, Superseded: true, Authorized: true,
		RequiredCapability: domain.CapabilityConfigManage,
	}
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, RouteAdminConfigPreview, cookie, csrf, validChange()))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}

	plan, _ := decodeConfigData(t, response)["plan"].(map[string]any)
	if plan["superseded"] != true {
		t.Fatalf("expected superseded in the plan: %v", plan)
	}
	if plan["stale"] != false {
		t.Fatalf("superseded and stale are different facts: %v", plan)
	}
}

// The rollback preview is its own route: the client names a version and the
// revision it holds, and nothing else. Values, preconditions and eligibility
// are derived server-side.
func TestConfigAPI_RollbackPreviewSendsOnlyTheVersionAndRevision(t *testing.T) {
	stub := configuredStub()
	stub.plan = service.ConfigPlan{
		Document: domain.ConfigDocumentAuthPolicy, Revision: 4, Apply: domain.ConfigApplyRuntime,
		Authorized: true, RequiredCapability: domain.CapabilityConfigManage,
	}
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, "/config/versions/7/rollback/preview",
		cookie, csrf, map[string]any{"expected_revision": 4}))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 1 || stub.calls[0] != "rollback-preview" {
		t.Fatalf("expected exactly one rollback preview, got %v", stub.calls)
	}
	if stub.rollback != 7 || stub.revision != 4 {
		t.Fatalf("expected version 7 at revision 4, got %d / %d", stub.rollback, stub.revision)
	}
}

// Reading a plan is a read. The preview must not require the capability that
// writes, or an auditor could not see why a rollback is unavailable.
func TestConfigAPI_RollbackPreviewRequiresOnlyTheReadCapability(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, "/config/versions/7/rollback/preview",
		cookie, csrf, map[string]any{"expected_revision": 4}))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}

	// A principal with neither capability is still refused.
	denied := newHarness(t, adminStore(domain.CapabilityUsersRead), withConfiguration(configuredStub()))
	deniedCookie, deniedCSRF := denied.establish(t)
	if code := denied.do(denied.configRequest(t, http.MethodPost, "/config/versions/7/rollback/preview",
		deniedCookie, deniedCSRF, map[string]any{"expected_revision": 4})).Code; code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", code)
	}
}

// The client cannot smuggle the values to restore, the preconditions or the
// verdict: the body names one field, and anything else is refused before the
// service is reached.
func TestConfigAPI_RollbackPreviewRefusesClientSuppliedRollbackData(t *testing.T) {
	bodies := []map[string]any{
		{"expected_revision": 4, "changes": map[string]any{"auth.device.max_per_user": 5}},
		{"expected_revision": 4, "preconditions": map[string]any{"auth.device.max_per_user": 20}},
		{"expected_revision": 4, "superseded": false},
		{"expected_revision": 4, "from": 30, "to": 10},
	}
	for _, body := range bodies {
		stub := configuredStub()
		harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
		cookie, csrf := harness.establish(t)

		response := harness.do(harness.configRequest(t, http.MethodPost, "/config/versions/7/rollback/preview",
			cookie, csrf, body))

		if response.Code != http.StatusBadRequest {
			t.Fatalf("%v: expected 400, got %d (%s)", body, response.Code, response.Body.String())
		}
		if len(stub.calls) != 0 {
			t.Fatalf("%v: a refused body must not reach the service", body)
		}
	}
}

func TestConfigAPI_RollbackPreviewIsGuardedLikeEveryOtherMutation(t *testing.T) {
	stub := configuredStub()
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	missing := harness.configRequest(t, http.MethodPost, "/config/versions/7/rollback/preview", cookie, "",
		map[string]any{"expected_revision": 4})
	if code := harness.do(missing).Code; code != http.StatusForbidden {
		t.Fatalf("expected 403 without a CSRF token, got %d", code)
	}

	hostile := harness.configRequest(t, http.MethodPost, "/config/versions/7/rollback/preview", cookie, csrf,
		map[string]any{"expected_revision": 4})
	hostile.Header.Set("Origin", "https://evil.example")
	if code := harness.do(hostile).Code; code != http.StatusForbidden {
		t.Fatalf("expected 403 from a foreign origin, got %d", code)
	}

	if code := harness.do(harness.configRequest(t, http.MethodGet, "/config/versions/7/rollback/preview",
		cookie, "", nil)).Code; code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET, got %d", code)
	}
	if len(stub.calls) != 0 {
		t.Fatal("a forged or misrouted request must not reach the service")
	}
}

// A superseded verdict travels on the wire, because the console renders it.
func TestConfigAPI_RollbackPreviewPublishesSuperseded(t *testing.T) {
	stub := configuredStub()
	stub.plan = service.ConfigPlan{
		Document: domain.ConfigDocumentAuthPolicy, Revision: 5, Apply: domain.ConfigApplyRuntime,
		Superseded: true, Authorized: true, RequiredCapability: domain.CapabilityConfigManage,
		Changes: []domain.ConfigChange{
			{Key: domain.ConfigKeyDeviceMaxPerUser, From: domain.IntValue(20), To: domain.IntValue(5)},
		},
	}
	harness := newHarness(t, adminStore(domain.CapabilityConfigRead), withConfiguration(stub))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.configRequest(t, http.MethodPost, "/config/versions/7/rollback/preview",
		cookie, csrf, map[string]any{"expected_revision": 5}))

	plan, _ := decodeConfigData(t, response)["plan"].(map[string]any)
	if plan["superseded"] != true {
		t.Fatalf("expected superseded on the wire: %v", plan)
	}
	// The diff names the version's own transition, so the operator sees which
	// change is being refused rather than a diff against the current value.
	changes := decodeConfigList(t, plan, "changes")
	if len(changes) != 1 || changes[0]["from"] != float64(20) {
		t.Fatalf("expected the version's transition in the plan: %v", changes)
	}
}
