import { screen, waitFor, within } from "@testing-library/react";
import { Route, Routes } from "react-router";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { _resetCSRFToken } from "../api/client";
import {
  deferred,
  errorResponse,
  jsonResponse,
  renderWithSession,
  routedFetch,
} from "../test/harness";
import {
  OIDC_INTEGRATION,
  configCatalogPayload,
  integrationsPayload,
  reportPayload,
} from "../test/integrationFixtures";
import ConfigurationPage from "./ConfigurationPage";
import IntegrationsPage from "./IntegrationsPage";

const READER = ["admin.integrations.read", "admin.config.read"];
const OPERATOR = [...READER, "admin.integrations.manage"];

afterEach(() => {
  vi.restoreAllMocks();
  _resetCSRFToken();
});

type Route = { match: string; respond: () => Response | Promise<Response> };

function renderPage(capabilities: string[], routes: Route[] = defaultRoutes()) {
  const fetchMock = routedFetch(routes);
  vi.stubGlobal("fetch", fetchMock);
  renderWithSession(<IntegrationsPage />, capabilities);
  return fetchMock;
}

function defaultRoutes(): Route[] {
  return [
    {
      match: "/integrations/smtp/test-email",
      respond: () => jsonResponse(reportPayload({ integration: "smtp", status: "passed" })),
    },
    { match: "/diagnose", respond: () => jsonResponse(reportPayload()) },
    { match: "/integrations", respond: () => jsonResponse(integrationsPayload()) },
  ];
}

async function openCard(id: string) {
  const card = await screen.findByTestId(`integration-${id}`);
  await userEvent.click(within(card).getByRole("button", { name: "Abrir" }));
  return card;
}

describe("IntegrationsPage", () => {
  it("lists every integration without contacting any of them", async () => {
    const fetchMock = renderPage(READER);
    await screen.findByTestId("integration-oidc");

    expect(screen.getByTestId("integration-smtp")).toBeInTheDocument();
    expect(screen.getByTestId("integration-turn")).toBeInTheDocument();
    // Opening the page is one read of the shared collection. Nothing here runs
    // a diagnostic, and nothing may.
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0][0])).toContain("/integrations");
  });

  it("shows a loading state before the surface arrives", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    );
    renderWithSession(<IntegrationsPage />, READER);
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("reports a permission failure as one, with no retry offered", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => errorResponse(403)),
    );
    renderWithSession(<IntegrationsPage />, READER);
    expect(await screen.findByRole("alert")).toHaveTextContent(/permissão/i);
  });

  it("never runs a diagnostic on render, only when asked", async () => {
    const fetchMock = renderPage(OPERATOR);
    await openCard("oidc");
    expect(fetchMock).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByTestId("diagnose-oidc"));
    await screen.findByTestId("diagnostic-report");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  // The whole point of the staged result: an operator learns what failed and
  // what was never reached, instead of "Erro 500".
  it("renders the diagnostic stage by stage, including what did not run", async () => {
    renderPage(OPERATOR);
    await openCard("oidc");
    await userEvent.click(screen.getByTestId("diagnose-oidc"));

    const report = await screen.findByTestId("diagnostic-report");
    expect(within(report).getByTestId("diagnostic-step-resolve")).toHaveTextContent("DNS");
    expect(within(report).getByTestId("diagnostic-step-tls")).toHaveTextContent("Falha");
    expect(within(report).getByTestId("diagnostic-step-tls")).toHaveTextContent("Erro de TLS");
    expect(within(report).getByTestId("diagnostic-step-credential")).toHaveTextContent(
      "Não executada",
    );
    // A stage with no measurement shows an em dash, never 0 ms.
    expect(within(report).getByTestId("diagnostic-step-credential")).toHaveTextContent("—");
  });

  it("disables the diagnostic without the manage capability and says which one is missing", async () => {
    renderPage(READER);
    await openCard("oidc");

    expect(screen.getByTestId("diagnose-oidc")).toBeDisabled();
    expect(screen.getByText(/admin\.integrations\.manage/)).toBeInTheDocument();
  });

  it("explains an integration it cannot check instead of offering a button", async () => {
    renderPage(OPERATOR);
    await openCard("turn");

    expect(screen.queryByTestId("diagnose-turn")).not.toBeInTheDocument();
    expect(screen.getByTestId("diagnostic-unsupported-turn")).toHaveTextContent(
      /nenhuma variável/i,
    );
  });

  // A credential is a status and never a value, and no reload changes that.
  it("shows a credential as configured and never as a value", async () => {
    renderPage(READER);
    const card = await openCard("oidc");

    expect(within(card).getByTestId("config-status-secret.oidc_client_secret")).toHaveTextContent(
      "Configurado",
    );
    expect(card.textContent).not.toContain("client-secret-value");
    expect(within(card).queryByLabelText(/client secret/i)).not.toBeInTheDocument();
  });

  it("folds the advanced settings away by default", async () => {
    renderPage(READER);
    const card = await openCard("oidc");

    const advanced = within(card).getByText(/Configuração avançada/);
    expect(advanced.closest("details")).not.toHaveAttribute("open");
  });

  it("says the inventory is hidden rather than empty without admin.config.read", async () => {
    const fetchMock = routedFetch([
      {
        match: "/integrations",
        respond: () =>
          jsonResponse(
            integrationsPayload([{ ...OIDC_INTEGRATION, settings_visible: false, settings: [] }]),
          ),
      },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<IntegrationsPage />, ["admin.integrations.read"]);
    await openCard("oidc");

    expect(screen.getByText(/admin\.config\.read/)).toBeInTheDocument();
  });

  // The test message leaves the platform, so it is confirmed first and the
  // dialog states the destination rather than asking for one.
  it("confirms the test message and offers no destination field", async () => {
    const fetchMock = renderPage(OPERATOR);
    await openCard("smtp");
    await userEvent.click(screen.getByTestId("action-smtp.test_email"));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/própria conta administrativa/);
    expect(within(dialog).queryByRole("textbox")).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    await userEvent.click(within(dialog).getByRole("button", { name: "Enviar" }));
    await screen.findByTestId("diagnostic-report");
    const sent = fetchMock.mock.calls.map((call) => String(call[0]));
    expect(sent).toContain("/api/admin/integrations/smtp/test-email");
  });

  it("cancels the confirmation without sending anything", async () => {
    const fetchMock = renderPage(OPERATOR);
    await openCard("smtp");
    await userEvent.click(screen.getByTestId("action-smtp.test_email"));
    await userEvent.click(screen.getByRole("button", { name: "Cancelar" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("surfaces a rate-limited diagnostic as an actionable message", async () => {
    renderPage(OPERATOR, [
      { match: "/diagnose", respond: () => errorResponse(429, "rate_limited") },
      { match: "/integrations", respond: () => jsonResponse(integrationsPayload()) },
    ]);
    await openCard("oidc");
    await userEvent.click(screen.getByTestId("diagnose-oidc"));

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByTestId("diagnostic-report")).not.toBeInTheDocument();
  });

  it("keeps the button disabled while a run is in flight", async () => {
    const pending = deferred<Response>();
    renderPage(OPERATOR, [
      { match: "/diagnose", respond: () => pending.promise },
      { match: "/integrations", respond: () => jsonResponse(integrationsPayload()) },
    ]);
    await openCard("oidc");
    await userEvent.click(screen.getByTestId("diagnose-oidc"));

    expect(screen.getByTestId("diagnose-oidc")).toBeDisabled();
    pending.resolve(jsonResponse(reportPayload()));
    await screen.findByTestId("diagnostic-report");
  });

  // A result must never appear under a different integration's heading: it
  // would read as that one's diagnosis.
  it("clears the result when another card is opened", async () => {
    renderPage(OPERATOR);
    await openCard("oidc");
    await userEvent.click(screen.getByTestId("diagnose-oidc"));
    await screen.findByTestId("diagnostic-report");

    await openCard("smtp");
    expect(screen.queryByTestId("diagnostic-report")).not.toBeInTheDocument();
  });

  // The regression the generation guard in useDiagnosticRun exists for.
  //
  // A report carries no integration id, so a late result from an abandoned run
  // would be painted under whichever card is open — reading as that
  // integration's own diagnosis. This walks the exact sequence: start on
  // Keycloak, switch to SMTP, let Keycloak answer late, then run SMTP.
  it("never shows one integration's diagnostic under another", async () => {
    const keycloak = deferred<Response>();
    const smtp = deferred<Response>();
    renderPage(OPERATOR, [
      { match: "/integrations/oidc/diagnose", respond: () => keycloak.promise },
      { match: "/integrations/smtp/diagnose", respond: () => smtp.promise },
      { match: "/integrations", respond: () => jsonResponse(integrationsPayload()) },
    ]);

    await openCard("oidc");
    await userEvent.click(screen.getByTestId("diagnose-oidc"));
    expect(screen.getByTestId("diagnose-oidc")).toBeDisabled();

    // The operator gives up waiting and opens another integration.
    await openCard("smtp");
    expect(screen.queryByTestId("diagnostic-report")).not.toBeInTheDocument();
    // The abandoned run must not still hold the slot: SMTP is startable at once.
    expect(screen.getByTestId("diagnose-smtp")).toBeEnabled();

    // Keycloak answers late, into a card nobody is looking at.
    keycloak.resolve(
      jsonResponse(reportPayload({ integration: "oidc", summary: "DIAGNOSTICO DO KEYCLOAK" })),
    );
    await waitFor(() => expect(screen.getByTestId("diagnose-smtp")).toBeEnabled());
    expect(screen.queryByTestId("diagnostic-report")).not.toBeInTheDocument();
    expect(screen.queryByText("DIAGNOSTICO DO KEYCLOAK")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    await userEvent.click(screen.getByTestId("diagnose-smtp"));
    smtp.resolve(
      jsonResponse(
        reportPayload({ integration: "smtp", status: "passed", summary: "DIAGNOSTICO DO SMTP" }),
      ),
    );

    const report = await screen.findByTestId("diagnostic-report");
    expect(report).toHaveTextContent("DIAGNOSTICO DO SMTP");
    expect(report).not.toHaveTextContent("DIAGNOSTICO DO KEYCLOAK");
    // And it is inside the SMTP card, not merely somewhere on the page.
    expect(
      within(screen.getByTestId("integration-smtp")).getByTestId("diagnostic-report"),
    ).toBeInTheDocument();
  });

  // The deep link has to land on the settings it names.
  //
  // It used to carry the display name — "Keycloak / OIDC" — which is
  // presentation: it is translated, the slash is tokenised by the search, and
  // the word "Keycloak" appears in no configuration key. An operator following
  // it arrived at an empty result. The link now carries the integration id,
  // which is the slug the keys are namespaced with.
  //
  // This walks the whole path rather than asserting a URL, because a URL
  // assertion is what let the bug through: it passed while the destination
  // showed nothing.
  it("opens the configuration screen on settings that actually exist", async () => {
    const fetchMock = routedFetch([
      { match: "/config/versions", respond: () => jsonResponse({ data: { versions: [] } }) },
      { match: "/config", respond: () => jsonResponse(configCatalogPayload()) },
      { match: "/integrations", respond: () => jsonResponse(integrationsPayload()) },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(
      <Routes>
        <Route path="/integrations" element={<IntegrationsPage />} />
        <Route path="/configuration" element={<ConfigurationPage />} />
      </Routes>,
      READER,
      ["/integrations"],
    );

    await openCard("oidc");
    await userEvent.click(screen.getByTestId("configure-oidc"));

    // A stable identifier, not the translated name.
    const search = await screen.findByLabelText("Buscar configuração");
    expect(search).toHaveValue("oidc");

    // And the destination is not empty: a real OIDC field from the catalogue
    // is on screen. This is the assertion the operator's experience depends on.
    expect(await screen.findByTestId("config-oidc.enabled")).toBeInTheDocument();
    expect(screen.getByText("Single sign-on habilitado")).toBeInTheDocument();
    expect(screen.getByTestId("config-search-count")).not.toHaveTextContent("0 de");
    // The unrelated authentication policy field is filtered out, so the search
    // really ran rather than the page simply rendering everything.
    expect(screen.queryByTestId("config-auth.password.min_length")).not.toBeInTheDocument();
  });

  it("says how old the collection is", async () => {
    renderPage(READER);
    await screen.findByTestId("integration-oidc");
    expect(screen.getByText(/Última coleta:/)).toBeInTheDocument();
  });
});
