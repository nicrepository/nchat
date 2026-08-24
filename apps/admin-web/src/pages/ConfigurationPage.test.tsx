import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { _resetCSRFToken } from "../api/client";
import { errorResponse, jsonResponse, renderWithSession } from "../test/harness";
import ConfigurationPage from "./ConfigurationPage";

const MIN_LENGTH = {
  key: "auth.password.min_length",
  label: "Tamanho mínimo da senha",
  description: "Número mínimo de caracteres exigido.",
  category: "authentication",
  owner_service: "auth-service",
  class: "A",
  source: "database",
  apply: "runtime",
  type: "int",
  unit: "caracteres",
  min: 8,
  max: 128,
  nullable: false,
  default: 12,
  editable: true,
  sensitive: false,
  document: "auth.policy",
  manage_capability: "admin.config.manage",
  danger_note: "Abaixo de 12 caracteres a política fica mais fraca.",
  rollbackable: true,
  observable: true,
  value: 12,
};

const REQUIRE_SYMBOL = {
  ...MIN_LENGTH,
  key: "auth.password.require_symbol",
  label: "Exigir símbolo",
  type: "bool",
  unit: "",
  min: undefined,
  max: undefined,
  default: true,
  value: true,
};

const CREDENTIAL = {
  key: "secret.livekit_api_secret",
  label: "LiveKit — API secret",
  description: "Segredo usado para assinar tokens de sala.",
  category: "credentials",
  owner_service: "media-service",
  class: "D",
  source: "sealed_secret",
  apply: "external",
  type: "string",
  nullable: false,
  editable: false,
  read_only_reason: "Credencial em Sealed Secret; a rotação segue o runbook.",
  sensitive: true,
  rollbackable: false,
  env_var: "LIVEKIT_API_SECRET",
  observable: true,
  configured: true,
};

const DEPLOYMENT = {
  key: "oidc.enabled",
  label: "Single sign-on habilitado",
  description: "Com false, os endpoints OIDC respondem 404.",
  category: "integrations",
  owner_service: "auth-service",
  class: "C",
  source: "gitops",
  apply: "rollout",
  type: "string",
  nullable: false,
  editable: false,
  read_only_reason: "Definido no ConfigMap versionado em Git; alterar exige commit e rollout.",
  sensitive: false,
  rollbackable: false,
  env_var: "OIDC_ENABLED",
  observable: true,
  value: "true",
};

function catalogBody(settings: unknown[] = [MIN_LENGTH, REQUIRE_SYMBOL, CREDENTIAL, DEPLOYMENT]) {
  return { data: { documents: [{ key: "auth.policy", revision: 3 }], settings } };
}

function planBody(overrides: Record<string, unknown> = {}) {
  return {
    data: {
      plan: {
        document: "auth.policy",
        revision: 3,
        stale: false,
        superseded: false,
        changes: [
          {
            key: "auth.password.min_length",
            label: "Tamanho mínimo da senha",
            category: "authentication",
            owner_service: "auth-service",
            apply: "runtime",
            unit: "caracteres",
            dangerous: false,
            from: 12,
            to: 16,
          },
        ],
        dangerous: false,
        required_capability: "admin.config.manage",
        authorized: true,
        reason_required: false,
        warnings: [],
        errors: [],
        affected_services: ["auth-service"],
        apply: "runtime",
        ...overrides,
      },
    },
  };
}

const VERSION = {
  id: "7",
  document: "auth.policy",
  revision: 3,
  applied_at: "2026-08-20T12:00:00Z",
  actor_user_id: "11111111-1111-1111-1111-111111111111",
  actor_email: "admin@example.test",
  correlation_id: "req-1",
  reason: "endurecimento",
  reverts_revision: 0,
  rollbackable: true,
  changes: [
    {
      key: "auth.password.min_length",
      label: "Tamanho mínimo da senha",
      category: "authentication",
      owner_service: "auth-service",
      apply: "runtime",
      unit: "caracteres",
      dangerous: false,
      from: 8,
      to: 12,
    },
  ],
};

/**
 * A fetch stub that answers by route and can be given a different answer per
 * call, so a spec can describe a reload returning something new — which is what
 * the conflict and apply paths are about.
 */
function routes(handlers: Record<string, () => Response | Promise<Response>>) {
  // The init parameter is declared but unread: the stub answers by URL, and the
  // specs read the recorded bodies off the mock afterwards.
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    void init;
    const url = String(input);
    const match = Object.keys(handlers)
      .sort((a, b) => b.length - a.length)
      .find((route) => url.includes(route));
    if (match === undefined) throw new Error(`unstubbed request: ${url}`);
    return handlers[match]();
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function defaultRoutes(overrides: Record<string, () => Response | Promise<Response>> = {}) {
  return routes({
    // The rollback preview is its own route: the console names the version and
    // the server derives what reverting it would do.
    "/rollback/preview": () => jsonResponse(planBody()),
    "/config/versions": () => jsonResponse({ data: { versions: [VERSION] } }),
    "/config/preview": () => jsonResponse(planBody()),
    "/config/apply": () =>
      jsonResponse({
        data: {
          applied: true,
          document: "auth.policy",
          revision: 4,
          values: { "auth.password.min_length": 16 },
          plan: planBody().data.plan,
          version: { ...VERSION, id: "8", revision: 4 },
        },
      }),
    "/config": () => jsonResponse(catalogBody()),
    ...overrides,
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  _resetCSRFToken();
});

describe("ConfigurationPage", () => {
  it("renders each setting with its class, source and how it is applied", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.read", "admin.config.manage"]);

    const field = await screen.findByTestId("config-auth.password.min_length");
    expect(within(field).getByText("Classe A · runtime")).toBeInTheDocument();
    expect(within(field).getByText("Banco de dados")).toBeInTheDocument();
    expect(within(field).getByText("Aplica em runtime")).toBeInTheDocument();
    expect(within(field).getByLabelText("Tamanho mínimo da senha")).toHaveValue("12");
  });

  // The whole point of the credential rendering: a status, a rotation
  // procedure, and no control that would pretend to change it.
  it("shows a credential as configured, with no field and no value", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.read", "admin.config.manage"]);

    await screen.findByTestId("config-auth.password.min_length");
    await userEvent.click(screen.getByText(/Credenciais/));

    const field = screen.getByTestId("config-secret.livekit_api_secret");
    expect(within(field).getByTestId("config-status-secret.livekit_api_secret")).toHaveTextContent(
      "Configurado",
    );
    expect(within(field).queryByRole("textbox")).not.toBeInTheDocument();
    expect(within(field).queryByRole("checkbox")).not.toBeInTheDocument();
    expect(within(field).getAllByText(/Sealed Secret/).length).toBeGreaterThan(0);
    expect(field.textContent).not.toMatch(/mostrar|revelar|substituir/i);
  });

  it("shows a Git-managed setting as read-only with the reason", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.read"]);

    await screen.findByTestId("config-auth.password.min_length");
    await userEvent.click(screen.getByText(/Integrações/));

    const field = screen.getByTestId("config-oidc.enabled");
    expect(within(field).getByText("Exige rollout")).toBeInTheDocument();
    expect(within(field).getByText(/commit e rollout/)).toBeInTheDocument();
    expect(within(field).queryByRole("textbox")).not.toBeInTheDocument();
  });

  // Hiding the control is not the boundary; the API refuses the same request
  // either way. The screen still has to say why the field is inert.
  it("renders the editable settings read-only without the manage capability", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.read"]);

    await screen.findByTestId("config-auth.password.min_length");

    expect(screen.queryByRole("button", { name: "Revisar alterações" })).not.toBeInTheDocument();
    expect(screen.getAllByText("admin.config.manage").length).toBeGreaterThan(0);
    expect(
      screen.queryByLabelText("Tamanho mínimo da senha", { selector: "input" }),
    ).not.toBeInTheDocument();
  });

  it("validates locally before offering the review, and says so", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const input = await screen.findByLabelText("Tamanho mínimo da senha");
    await userEvent.clear(input);
    await userEvent.type(input, "2");

    expect(screen.getByTestId("config-error-auth.password.min_length")).toHaveTextContent(
      "O mínimo aceito é 8.",
    );
    expect(screen.getByRole("button", { name: "Revisar alterações" })).toBeDisabled();
  });

  it("reviews with the server's diff and only then applies", async () => {
    const fetchMock = defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const input = await screen.findByLabelText("Tamanho mínimo da senha");
    await userEvent.clear(input);
    await userEvent.type(input, "16");
    expect(screen.getByTestId("config-dirty")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Revisar alterações" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByTestId("config-diff")).toHaveTextContent("− 12 caracteres");
    expect(within(dialog).getByTestId("config-diff")).toHaveTextContent("+ 16 caracteres");

    // Reviewing writes nothing.
    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).includes("/config/apply")),
    ).toHaveLength(0);

    await userEvent.click(within(dialog).getByRole("button", { name: "Aplicar" }));

    await waitFor(() =>
      expect(screen.getByTestId("config-feedback")).toHaveTextContent("Revisão 4"),
    );
    const applied = fetchMock.mock.calls.find(([url]) => String(url).includes("/config/apply"));
    expect(JSON.parse(String(applied?.[1]?.body))).toMatchObject({
      document: "auth.policy",
      expected_revision: 3,
      changes: { "auth.password.min_length": 16 },
    });
  });

  // Only what actually changed travels, so the confirmation describes one
  // change rather than the whole form.
  it("sends only the fields that differ from the stored values", async () => {
    const fetchMock = defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const input = await screen.findByLabelText("Tamanho mínimo da senha");
    await userEvent.clear(input);
    await userEvent.type(input, "16");
    await userEvent.click(screen.getByRole("button", { name: "Revisar alterações" }));
    await screen.findByRole("dialog");

    const preview = fetchMock.mock.calls.find(([url]) => String(url).includes("/config/preview"));
    expect(JSON.parse(String(preview?.[1]?.body)).changes).toEqual({
      "auth.password.min_length": 16,
    });
  });

  it("blocks a dangerous change until a reason is given", async () => {
    defaultRoutes({
      "/config/preview": () =>
        jsonResponse(
          planBody({
            dangerous: true,
            reason_required: true,
            required_capability: "admin.superuser",
            warnings: ["Enfraquece a autenticação local."],
            changes: [
              {
                key: "auth.password.require_symbol",
                label: "Exigir símbolo",
                category: "authentication",
                owner_service: "auth-service",
                apply: "runtime",
                unit: "",
                dangerous: true,
                danger_note: "Desativar um requisito de complexidade enfraquece a autenticação.",
                from: true,
                to: false,
              },
            ],
          }),
        ),
    });
    renderWithSession(<ConfigurationPage />, ["admin.superuser"]);

    const checkbox = await screen.findByLabelText("Exigir símbolo");
    await userEvent.click(checkbox);
    await userEvent.click(screen.getByRole("button", { name: "Revisar alterações" }));

    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByTestId("config-danger-auth.password.require_symbol"),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Aplicar" })).toBeDisabled();

    await userEvent.type(within(dialog).getByLabelText(/Motivo/), "aprovado no SEC-77");
    expect(within(dialog).getByRole("button", { name: "Aplicar" })).toBeEnabled();
  });

  it("refuses to offer an apply the operator is not authorized for", async () => {
    defaultRoutes({
      "/config/preview": () =>
        jsonResponse(planBody({ authorized: false, required_capability: "admin.superuser" })),
    });
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const input = await screen.findByLabelText("Tamanho mínimo da senha");
    await userEvent.clear(input);
    await userEvent.type(input, "16");
    await userEvent.click(screen.getByRole("button", { name: "Revisar alterações" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByTestId("config-unauthorized")).toHaveTextContent("admin.superuser");
    expect(within(dialog).getByRole("button", { name: "Aplicar" })).toBeDisabled();
  });

  // A stale form is explained, not merged. The operator is told what happened
  // and what the current revision is.
  it("explains a version conflict reported by the preview", async () => {
    defaultRoutes({
      "/config/preview": () => jsonResponse(planBody({ stale: true, revision: 9 })),
    });
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const input = await screen.findByLabelText("Tamanho mínimo da senha");
    await userEvent.clear(input);
    await userEvent.type(input, "16");
    await userEvent.click(screen.getByRole("button", { name: "Revisar alterações" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByTestId("config-conflict")).toHaveTextContent("revisão atual: 9");
    expect(within(dialog).getByRole("button", { name: "Aplicar" })).toBeDisabled();
  });

  it("keeps the dialog open and shows the refusal when the apply loses the race", async () => {
    defaultRoutes({ "/config/apply": () => errorResponse(409, "conflict") });
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const input = await screen.findByLabelText("Tamanho mínimo da senha");
    await userEvent.clear(input);
    await userEvent.type(input, "16");
    await userEvent.click(screen.getByRole("button", { name: "Revisar alterações" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Aplicar" }));

    await waitFor(() => expect(screen.getByTestId("config-apply-error")).toBeInTheDocument());
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("lists the history and reverts a version through the same review", async () => {
    const fetchMock = defaultRoutes({
      "/config/versions/7/rollback": () =>
        jsonResponse({
          data: {
            applied: true,
            document: "auth.policy",
            revision: 4,
            values: {},
            plan: planBody().data.plan,
            version: { ...VERSION, id: "9", revision: 4, reverts_revision: 3 },
          },
        }),
    });
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const history = await screen.findByTestId("config-versions");
    expect(history).toHaveTextContent("Revisão 3");
    expect(history).toHaveTextContent("admin@example.test");

    await userEvent.click(within(history).getByRole("button", { name: "Reverter" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("button", { name: "Reverter" })).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Reverter" }));

    await waitFor(() =>
      expect(screen.getByTestId("config-feedback")).toHaveTextContent("Revisão 4"),
    );
    const rollback = fetchMock.mock.calls.find(([url]) => String(url).includes("/rollback"));
    expect(JSON.parse(String(rollback?.[1]?.body))).toMatchObject({ expected_revision: 3 });
  });

  it("does not offer a rollback the server marked unrepeatable", async () => {
    defaultRoutes({
      "/config/versions": () =>
        jsonResponse({ data: { versions: [{ ...VERSION, rollbackable: false }] } }),
    });
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const history = await screen.findByTestId("config-versions");
    expect(within(history).queryByRole("button", { name: "Reverter" })).not.toBeInTheDocument();
    expect(history).toHaveTextContent("não pode ser revertida");
  });

  it("discards drafts on request", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const input = await screen.findByLabelText("Tamanho mínimo da senha");
    await userEvent.clear(input);
    await userEvent.type(input, "16");
    expect(screen.getByTestId("config-dirty")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Descartar" }));

    expect(screen.queryByTestId("config-dirty")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Tamanho mínimo da senha")).toHaveValue("12");
  });

  it("reports a refused catalog as a permission problem and asks for nothing else", async () => {
    routes({ "/config": () => errorResponse(403, "forbidden") });
    renderWithSession(<ConfigurationPage />, ["admin.config.read"]);

    expect(await screen.findByRole("alert")).toHaveTextContent("permissão");
    expect(screen.queryByRole("button", { name: "Revisar alterações" })).not.toBeInTheDocument();
  });
});

describe("ConfigurationPage review snapshot", () => {
  // The confirm sends what the review froze. The hook owns that guarantee and
  // is tested directly in useConfigReview.test.ts; this is the end-to-end
  // evidence that the page wires it up.
  it("confirms against the revision the review was opened at", async () => {
    const fetchMock = defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const input = await screen.findByLabelText("Tamanho mínimo da senha");
    await userEvent.clear(input);
    await userEvent.type(input, "16");
    await userEvent.click(screen.getByRole("button", { name: "Revisar alterações" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Aplicar" }));
    await waitFor(() => expect(screen.getByTestId("config-feedback")).toBeInTheDocument());

    const preview = fetchMock.mock.calls.find(([url]) => String(url).includes("/config/preview"));
    const applied = fetchMock.mock.calls.find(([url]) => String(url).includes("/config/apply"));
    expect(JSON.parse(String(applied?.[1]?.body)).expected_revision).toBe(
      JSON.parse(String(preview?.[1]?.body)).expected_revision,
    );
    expect(JSON.parse(String(applied?.[1]?.body)).changes).toEqual({
      "auth.password.min_length": 16,
    });
  });

  // The form is held still while a review is open. Not the guarantee — the
  // snapshot is — but an operator should not be invited to edit behind a dialog
  // that will ignore what they type.
  it("holds the form still while a review is open", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const input = await screen.findByLabelText("Tamanho mínimo da senha");
    await userEvent.clear(input);
    await userEvent.type(input, "16");
    await userEvent.click(screen.getByRole("button", { name: "Revisar alterações" }));
    await screen.findByRole("dialog");

    expect(screen.getByLabelText("Tamanho mínimo da senha")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Revisar alterações" })).toBeDisabled();
  });
});

describe("ConfigurationPage rollback preview", () => {
  // The finding: the console used to rebuild the change set from the history it
  // was rendering. It now names the version and lets the server derive the
  // rest, which is what keeps the preview and the confirmed rollback in
  // agreement.
  it("previews a rollback by version, sending no historical values", async () => {
    const fetchMock = defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const history = await screen.findByTestId("config-versions");
    await userEvent.click(within(history).getByRole("button", { name: "Reverter" }));
    await screen.findByRole("dialog");

    const preview = fetchMock.mock.calls.find(([url]) => String(url).includes("/rollback/preview"));
    expect(String(preview?.[0])).toBe("/api/admin/config/versions/7/rollback/preview");
    expect(JSON.parse(String(preview?.[1]?.body))).toEqual({ expected_revision: 3 });
    // The generic preview endpoint is not used for a rollback at all.
    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/config/preview")),
    ).toHaveLength(0);
  });

  it("offers the confirmation when the version is still revertible", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const history = await screen.findByTestId("config-versions");
    await userEvent.click(within(history).getByRole("button", { name: "Reverter" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByTestId("config-diff")).toBeInTheDocument();
    expect(within(dialog).queryByTestId("config-superseded")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Reverter" })).toBeEnabled();
  });

  // v1: 10 -> 20, v2: 20 -> 30, current 30. Reverting v1 would discard v2, and
  // the operator learns that from the preview instead of from a 409.
  it("explains a superseded version and refuses to confirm it", async () => {
    const fetchMock = defaultRoutes({
      "/rollback/preview": () =>
        jsonResponse(
          planBody({
            superseded: true,
            // The plan names the version's own transition, not a diff against
            // the value somebody else has since written.
            changes: [{ ...planBody().data.plan.changes[0], from: 20, to: 10 }],
          }),
        ),
    });
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const history = await screen.findByTestId("config-versions");
    await userEvent.click(within(history).getByRole("button", { name: "Reverter" }));

    const dialog = await screen.findByRole("dialog");
    const alert = within(dialog).getByTestId("config-superseded");
    expect(alert).toHaveTextContent("não pode mais ser revertida");
    // The operator is told what to do, not given an internal reason.
    expect(alert.textContent).not.toMatch(/precondition|superseded|409/i);
    expect(within(dialog).getByTestId("config-diff")).toHaveTextContent("− 20 caracteres");
    expect(within(dialog).getByRole("button", { name: "Reverter" })).toBeDisabled();

    // Nothing was written, and pressing the disabled control changes that not
    // at all.
    await userEvent.click(within(dialog).getByRole("button", { name: "Reverter" }));
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes("/rollback"))).toHaveLength(
      1,
    );
  });

  // Superseded and stale are different problems with different remedies, so
  // they are different messages.
  it("distinguishes a superseded version from a stale revision", async () => {
    defaultRoutes({
      "/rollback/preview": () => jsonResponse(planBody({ stale: true, revision: 9 })),
    });
    renderWithSession(<ConfigurationPage />, ["admin.config.manage", "admin.config.read"]);

    const history = await screen.findByTestId("config-versions");
    await userEvent.click(within(history).getByRole("button", { name: "Reverter" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByTestId("config-conflict")).toBeInTheDocument();
    expect(within(dialog).queryByTestId("config-superseded")).not.toBeInTheDocument();
  });
});

/**
 * The configuration search (issue #582).
 *
 * The rule it exists to enforce is that no value is indexed, so the specs check
 * that a term matching only a value finds nothing — and that the metadata an
 * operator would actually type finds the field.
 */
describe("ConfigurationPage search", () => {
  const READER = ["admin.config.read"];

  it("counts what is declared before anything is typed", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, READER);

    await screen.findByTestId("config-auth.password.min_length");
    expect(screen.getByTestId("config-search-count")).toHaveTextContent(
      "4 configurações declaradas.",
    );
  });

  it("filters to the integration an operator is looking for", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, READER);
    await screen.findByTestId("config-auth.password.min_length");

    await userEvent.type(screen.getByLabelText("Buscar configuração"), "livekit");

    await waitFor(() =>
      expect(screen.queryByTestId("config-auth.password.min_length")).not.toBeInTheDocument(),
    );
    expect(screen.getByTestId("config-secret.livekit_api_secret")).toBeInTheDocument();
    expect(screen.getByTestId("config-search-count")).toHaveTextContent(
      "1 de 4 configurações correspondem.",
    );
  });

  // A match inside a collapsed section must not be hidden by the fold.
  it("opens the read-only sections while a term is active", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, READER);
    await screen.findByTestId("config-auth.password.min_length");

    await userEvent.type(screen.getByLabelText("Buscar configuração"), "livekit");
    await waitFor(() =>
      expect(screen.getByTestId("config-secret.livekit_api_secret")).toBeVisible(),
    );
    expect(screen.getByText(/Credenciais/).closest("details")).toHaveAttribute("open");
  });

  // The security property: a term that only occurs in a value finds nothing.
  it("does not index values", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, READER);
    await screen.findByTestId("config-auth.password.min_length");

    await userEvent.type(screen.getByLabelText("Buscar configuração"), "configurado");
    await waitFor(() =>
      expect(screen.getByTestId("config-search-count")).toHaveTextContent(
        "0 de 4 configurações correspondem.",
      ),
    );
  });

  it("seeds the term from the link that opened the page", async () => {
    defaultRoutes();
    renderWithSession(<ConfigurationPage />, READER, ["/configuration?q=LiveKit"]);

    await waitFor(() =>
      expect(screen.getByTestId("config-secret.livekit_api_secret")).toBeInTheDocument(),
    );
    expect(screen.getByLabelText("Buscar configuração")).toHaveValue("LiveKit");
    expect(screen.queryByTestId("config-auth.password.min_length")).not.toBeInTheDocument();
  });

  // A field edited before the search was typed is still an edit. Hiding it must
  // not drop it from the change set.
  it("keeps an edit hidden by the filter in the change set", async () => {
    const fetchMock = defaultRoutes();
    renderWithSession(<ConfigurationPage />, ["admin.config.read", "admin.config.manage"]);

    const input = await screen.findByLabelText("Tamanho mínimo da senha");
    await userEvent.clear(input);
    await userEvent.type(input, "16");

    await userEvent.type(screen.getByLabelText("Buscar configuração"), "livekit");
    await waitFor(() =>
      expect(screen.queryByTestId("config-auth.password.min_length")).not.toBeInTheDocument(),
    );

    await userEvent.click(screen.getByRole("button", { name: "Revisar alterações" }));
    await screen.findByRole("dialog");

    const preview = fetchMock.mock.calls.find((call) =>
      String(call[0]).includes("/config/preview"),
    );
    expect(preview).toBeDefined();
    const body = JSON.parse(String((preview?.[1] as RequestInit).body)) as {
      changes: Record<string, unknown>;
    };
    expect(body.changes).toEqual({ "auth.password.min_length": 16 });
  });
});
