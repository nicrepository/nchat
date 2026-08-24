import { afterEach, describe, expect, it, vi } from "vitest";

import { jsonResponse } from "../test/harness";
import {
  CREDENTIAL_SETTING,
  OIDC_INTEGRATION,
  integrationsPayload,
  reportPayload,
  settingPayload,
} from "../test/integrationFixtures";
import { _resetCSRFToken } from "./client";
import {
  diagnoseIntegration,
  loadIntegrations,
  parseDiagnosticStatus,
  sendSMTPTestEmail,
} from "./integrationsApi";

afterEach(() => {
  vi.restoreAllMocks();
  _resetCSRFToken();
});

function stubFetch(body: unknown, status = 200) {
  // The parameters are declared but unread: the specs assert on the recorded
  // URL and init, so the stub has to have them in its signature.
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    void input;
    void init;
    return jsonResponse(body, status);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("integrationsApi", () => {
  it("reads the surface the API sends", async () => {
    stubFetch(integrationsPayload());
    const view = await loadIntegrations();

    expect(view.integrations).toHaveLength(3);
    const [oidc, smtp, turn] = view.integrations;
    expect(oidc.state).toBe("degraded");
    expect(oidc.stages).toContain("jwks");
    expect(smtp.actions[0].capability).toBe("admin.integrations.manage");
    expect(turn.diagnosable).toBe(false);
    expect(turn.diagnosticUnsupported).not.toBe("");
  });

  // The invariant, checked at the boundary rather than trusted: a credential
  // that arrived carrying a value is a server this console must not paper over
  // by rendering it.
  it("refuses a credential that arrives with a value", async () => {
    stubFetch(
      integrationsPayload([
        { ...OIDC_INTEGRATION, settings: [{ ...CREDENTIAL_SETTING, value: "leaked" }] },
      ]),
    );
    await expect(loadIntegrations()).rejects.toThrow(/credencial/i);
  });

  it("refuses a credential declared editable", async () => {
    stubFetch(
      integrationsPayload([
        { ...OIDC_INTEGRATION, settings: [{ ...CREDENTIAL_SETTING, editable: true }] },
      ]),
    );
    await expect(loadIntegrations()).rejects.toThrow(/credencial/i);
  });

  // Absent and zero are different answers: a check that did not run has no
  // latency, and rendering "0 ms" would claim one.
  it("keeps an absent latency absent", async () => {
    stubFetch(integrationsPayload([{ ...OIDC_INTEGRATION, latency_ms: undefined }]));
    const view = await loadIntegrations();
    expect(view.integrations[0].latencyMS).toBeNull();
  });

  it("refuses a payload that is not the contract", async () => {
    stubFetch({ data: { collected_at: 42, integrations: [] } });
    await expect(loadIntegrations()).rejects.toThrow(/collected_at/);
  });

  it("posts the diagnostic without a body and names only the integration", async () => {
    const fetchMock = stubFetch(reportPayload());
    const report = await diagnoseIntegration("oidc");

    expect(String(fetchMock.mock.calls[0][0])).toBe("/api/admin/integrations/oidc/diagnose");
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    expect(report.status).toBe("failed");
    expect(report.steps.map((step) => step.stage)).toEqual([
      "resolve",
      "connect",
      "tls",
      "credential",
    ]);
    // A stage that did not run carries no duration.
    expect(report.steps[3].latencyMS).toBeNull();
  });

  it("escapes the identifier it puts in the path", async () => {
    const fetchMock = stubFetch(reportPayload());
    await diagnoseIntegration("../../session");
    expect(String(fetchMock.mock.calls[0][0])).toBe(
      "/api/admin/integrations/..%2F..%2Fsession/diagnose",
    );
  });

  // There is no recipient argument and no recipient field, which is the whole
  // anti-relay control.
  it("sends the test message with no destination at all", async () => {
    const fetchMock = stubFetch(reportPayload({ integration: "smtp", status: "passed" }));
    const report = await sendSMTPTestEmail();

    expect(String(fetchMock.mock.calls[0][0])).toBe("/api/admin/integrations/smtp/test-email");
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.body).toBeUndefined();
    expect(report.integration).toBe("smtp");
  });

  it("degrades an unknown status into skipped, never into passed", () => {
    expect(parseDiagnosticStatus("passed")).toBe("passed");
    expect(parseDiagnosticStatus("brand-new")).toBe("skipped");
  });

  it("reads a setting that is hidden from this actor as hidden, not empty", async () => {
    stubFetch(
      integrationsPayload([{ ...OIDC_INTEGRATION, settings_visible: false, settings: [] }]),
    );
    const view = await loadIntegrations();
    expect(view.integrations[0].settingsVisible).toBe(false);
    expect(view.integrations[0].settings).toEqual([]);
  });

  it("carries the advanced flag through", async () => {
    stubFetch(
      integrationsPayload([
        { ...OIDC_INTEGRATION, settings: [settingPayload({ advanced: true })] },
      ]),
    );
    const view = await loadIntegrations();
    expect(view.integrations[0].settings[0].advanced).toBe(true);
  });
});
