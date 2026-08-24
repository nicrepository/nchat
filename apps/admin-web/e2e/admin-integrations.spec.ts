import { expect, test, type Page } from "@playwright/test";

/**
 * End-to-end coverage of the integrations screen (issue #582).
 *
 * The Admin API is stubbed at the network boundary, like every other console
 * suite: what is under test is the console's behaviour — that a credential
 * stays opaque across a reload, that a diagnostic runs only when asked and
 * renders stage by stage, that an integration the platform cannot check
 * explains itself, that the search opens the right section — and not the
 * server's authorization, which has its own suite in Go.
 *
 * The rule the earlier suites set holds here: no case may assert that the
 * frontend is what keeps an unauthorized operator out. The capability cases
 * below assert that the console *reports* what the server would refuse, which
 * is a different claim.
 */

const COLLECTED_AT = "2026-08-23T11:00:00.000Z";

function bootstrap(capabilities: string[]) {
  return {
    identity: {
      user_id: "11111111-1111-1111-1111-111111111111",
      email: "admin@example.test",
      display_name: "Admin E2E",
      avatar_url: "",
    },
    capabilities,
    environment: "STAGING",
    build: { service: "admin-service", version: "0.0.0", commit: "e2e" },
    session: {
      idle_expires_at: "2099-01-01T00:00:00Z",
      absolute_expires_at: "2099-01-01T08:00:00Z",
    },
    csrf_token: "csrf-e2e",
  };
}

const OIDC_FLAG = {
  key: "oidc.enabled",
  label: "Single sign-on habilitado",
  description: "Com false, todos os endpoints OIDC respondem 404.",
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
  advanced: false,
};

const OIDC_SECRET = {
  key: "secret.oidc_client_secret",
  label: "OIDC — client secret",
  description: "Credencial do client usada na troca de código por token.",
  category: "credentials",
  owner_service: "auth-service",
  class: "D",
  source: "sealed_secret",
  apply: "external",
  type: "string",
  nullable: false,
  editable: false,
  read_only_reason: "Credencial em Sealed Secret; a rotação segue o runbook.",
  sensitive: true,
  rollbackable: false,
  env_var: "OIDC_CLIENT_SECRET",
  observable: true,
  configured: true,
  advanced: true,
};

const OIDC = {
  id: "oidc",
  display_name: "Keycloak / OIDC",
  summary: "Single sign-on da plataforma. Indisponível, resta apenas o login local.",
  category: "identity",
  runbook_path: "docs/runbooks/task-auth-oidc-keycloak.md",
  health_service_id: "oidc",
  state: "degraded",
  enabled: true,
  observable: true,
  latency_ms: 120,
  checked_at: COLLECTED_AT,
  error_category: "invalid_configuration",
  detail: "O provedor respondeu com um issuer diferente do configurado.",
  diagnosable: true,
  stages: ["resolve", "connect", "tls", "discovery", "issuer", "jwks", "credential"],
  settings_visible: true,
  settings: [OIDC_FLAG, OIDC_SECRET],
  actions: [],
};

const SMTP = {
  id: "smtp",
  display_name: "SMTP",
  summary: "Relay de e-mail. Sem ele convites ficam na fila sem sair.",
  category: "messaging",
  runbook_path: "docs/runbooks/task-smtp-bruteforce-login-audit.md",
  health_service_id: "smtp",
  state: "disabled",
  enabled: false,
  observable: true,
  checked_at: COLLECTED_AT,
  diagnosable: true,
  stages: ["resolve", "connect", "tls", "credential", "ready"],
  settings_visible: true,
  settings: [],
  actions: [
    {
      id: "smtp.test_email",
      label: "Enviar e-mail de teste",
      description:
        "Entrega uma mensagem fixa pelo relay configurado, sempre para o endereço da sua própria conta administrativa.",
      capability: "admin.integrations.manage",
    },
  ],
};

const TURN = {
  id: "turn",
  display_name: "TURN / coturn",
  summary: "Relay de mídia para redes restritivas.",
  category: "realtime",
  runbook_path: "docs/runbooks/task-livekit-coturn-dev.md",
  health_service_id: "turn",
  state: "unknown",
  enabled: true,
  observable: false,
  checked_at: COLLECTED_AT,
  error_category: "not_observable",
  diagnosable: false,
  diagnostic_unsupported:
    "Nenhuma variável de ambiente da plataforma nomeia o servidor TURN, então não há alvo que este serviço possa verificar sem inventá-lo.",
  stages: [],
  settings_visible: true,
  settings: [],
  actions: [],
};

const INTEGRATIONS = {
  data: { collected_at: COLLECTED_AT, integrations: [OIDC, SMTP, TURN] },
};

const FAILED_REPORT = {
  data: {
    report: {
      integration: "oidc",
      started_at: "2026-08-23T11:05:00.000Z",
      status: "failed",
      summary: "Ao menos uma etapa falhou. As etapas seguintes não foram executadas.",
      steps: [
        { stage: "resolve", status: "passed", detail: "O nome resolve.", latency_ms: 3 },
        {
          stage: "connect",
          status: "passed",
          detail: "A dependência aceitou a conexão.",
          latency_ms: 8,
        },
        {
          stage: "tls",
          status: "failed",
          category: "tls_error",
          detail: "Não foi possível estabelecer TLS com a dependência.",
          latency_ms: 41,
        },
        {
          stage: "discovery",
          status: "skipped",
          detail: "Não executada porque uma etapa anterior falhou.",
        },
        {
          stage: "issuer",
          status: "skipped",
          detail: "Não executada porque uma etapa anterior falhou.",
        },
        {
          stage: "jwks",
          status: "skipped",
          detail: "Não executada porque uma etapa anterior falhou.",
        },
        {
          stage: "credential",
          status: "skipped",
          detail: "Não executada porque uma etapa anterior falhou.",
        },
      ],
    },
  },
};

const SENT_REPORT = {
  data: {
    report: {
      integration: "smtp",
      started_at: "2026-08-23T11:06:00.000Z",
      status: "passed",
      summary: "Todas as etapas verificadas concluíram com sucesso.",
      steps: [
        { stage: "resolve", status: "passed", latency_ms: 2 },
        { stage: "connect", status: "passed", latency_ms: 5 },
        { stage: "tls", status: "passed", latency_ms: 30 },
        { stage: "credential", status: "passed", latency_ms: 20 },
        { stage: "ready", status: "passed", latency_ms: 4 },
        {
          stage: "delivery",
          status: "passed",
          detail: "O relay aceitou a mensagem de teste.",
          latency_ms: 15,
        },
      ],
    },
  },
};

const CONFIG_CATALOG = {
  data: {
    documents: [{ key: "auth.policy", revision: 3 }],
    settings: [
      {
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
        rollbackable: true,
        observable: true,
        value: 12,
      },
      OIDC_FLAG,
      OIDC_SECRET,
    ],
  },
};

async function json(page: Page, pattern: string, body: unknown, status = 200) {
  await page.route(pattern, (route) =>
    route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) }),
  );
}

async function signedIn(page: Page, capabilities: string[] = ["admin.superuser"]) {
  await json(page, "**/api/admin/bootstrap", { data: bootstrap(capabilities) });
}

async function integrations(page: Page) {
  await json(page, "**/api/admin/integrations", INTEGRATIONS);
}

test.describe("Integrações", () => {
  test("opens the inventory without contacting any dependency", async ({ page }) => {
    const paths: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.pathname.startsWith("/api/admin/")) paths.push(url.pathname);
    });
    await signedIn(page);
    await integrations(page);
    await page.goto("/integrations");

    await expect(page.getByTestId("integration-oidc")).toBeVisible();
    await expect(page.getByTestId("integration-smtp")).toBeVisible();
    // No diagnostic ran. Opening the page is a read of the shared collection
    // and nothing else.
    expect(paths.some((path) => path.includes("/diagnose"))).toBe(false);
    expect(paths.some((path) => path.includes("test-email"))).toBe(false);
  });

  test("edits Keycloak without ever revealing the client secret", async ({ page }) => {
    await signedIn(page);
    await integrations(page);
    await page.goto("/integrations");

    const card = page.getByTestId("integration-oidc");
    await card.getByRole("button", { name: "Abrir" }).click();
    await card.getByText("Configuração avançada (1)").click();

    await expect(card.getByTestId("config-status-secret.oidc_client_secret")).toContainText(
      "Configurado",
    );
    // No field, no reveal, no replace: the platform has no endpoint behind any
    // of them, so the console offers none.
    await expect(card.getByTestId("config-secret.oidc_client_secret")).not.toContainText(
      /mostrar|revelar|substituir/i,
    );

    // The secret stays opaque across a reload, which is the property an
    // operator would test by hand.
    await page.reload();
    await page.getByTestId("integration-oidc").getByRole("button", { name: "Abrir" }).click();
    await page.getByText("Configuração avançada (1)").click();
    await expect(page.getByTestId("config-status-secret.oidc_client_secret")).toContainText(
      "Configurado",
    );
  });

  test("runs the Keycloak diagnostic and shows an actionable failure stage by stage", async ({
    page,
  }) => {
    await signedIn(page);
    await integrations(page);
    await json(page, "**/api/admin/integrations/oidc/diagnose", FAILED_REPORT);
    await page.goto("/integrations");

    const card = page.getByTestId("integration-oidc");
    await card.getByRole("button", { name: "Abrir" }).click();
    await expect(page.getByTestId("diagnostic-report")).toHaveCount(0);

    await page.getByTestId("diagnose-oidc").click();

    const report = page.getByTestId("diagnostic-report");
    await expect(report).toBeVisible();
    await expect(report.getByTestId("diagnostic-step-resolve")).toContainText("DNS");
    await expect(report.getByTestId("diagnostic-step-resolve")).toContainText("OK");
    await expect(report.getByTestId("diagnostic-step-tls")).toContainText("Falha");
    await expect(report.getByTestId("diagnostic-step-tls")).toContainText("Erro de TLS");
    // The stages after the failure are named as not executed, not as passing
    // and not as failing.
    await expect(report.getByTestId("diagnostic-step-jwks")).toContainText("Não executada");
    // Nothing remote reaches the screen: no status code, no body, no host.
    await expect(report).not.toContainText("500");
  });

  test("sends the SMTP test message only after an explicit confirmation", async ({ page }) => {
    await signedIn(page);
    await integrations(page);
    await json(page, "**/api/admin/integrations/smtp/test-email", SENT_REPORT);
    await page.goto("/integrations");

    const card = page.getByTestId("integration-smtp");
    await card.getByRole("button", { name: "Abrir" }).click();
    await page.getByTestId("action-smtp.test_email").click();

    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("própria conta administrativa");
    // There is no destination to type. That is the whole anti-relay control.
    await expect(dialog.getByRole("textbox")).toHaveCount(0);

    await dialog.getByRole("button", { name: "Enviar" }).click();
    await expect(page.getByTestId("diagnostic-step-delivery")).toContainText("OK");
  });

  test("explains an integration it cannot check instead of offering a button", async ({ page }) => {
    await signedIn(page);
    await integrations(page);
    await page.goto("/integrations");

    const card = page.getByTestId("integration-turn");
    await card.getByRole("button", { name: "Abrir" }).click();

    await expect(page.getByTestId("diagnose-turn")).toHaveCount(0);
    await expect(page.getByTestId("diagnostic-unsupported-turn")).toContainText(
      "Nenhuma variável de ambiente",
    );
  });

  test("reports the refusal a rate limit produces", async ({ page }) => {
    await signedIn(page);
    await integrations(page);
    await json(
      page,
      "**/api/admin/integrations/oidc/diagnose",
      { error: { code: "rate_limited", message: "too many requests" } },
      429,
    );
    await page.goto("/integrations");

    await page.getByTestId("integration-oidc").getByRole("button", { name: "Abrir" }).click();
    await page.getByTestId("diagnose-oidc").click();

    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByTestId("diagnostic-report")).toHaveCount(0);
  });

  test("hides the diagnostic from an operator who may only read", async ({ page }) => {
    await signedIn(page, ["admin.integrations.read", "admin.config.read"]);
    await integrations(page);
    await page.goto("/integrations");

    await page.getByTestId("integration-oidc").getByRole("button", { name: "Abrir" }).click();
    // Disabled rather than absent, with the missing permission named: the API
    // refuses it either way, and this is the console explaining why.
    await expect(page.getByTestId("diagnose-oidc")).toBeDisabled();
    await expect(page.getByText("admin.integrations.manage")).toBeVisible();
  });

  test("is operable by keyboard alone", async ({ page }) => {
    await signedIn(page);
    await integrations(page);
    await json(page, "**/api/admin/integrations/oidc/diagnose", FAILED_REPORT);
    await page.goto("/integrations");

    const toggle = page.getByTestId("integration-oidc").getByRole("button", { name: "Abrir" });
    await toggle.focus();
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await page.keyboard.press("Enter");
    await expect(
      page.getByTestId("integration-oidc").getByRole("button", { name: "Recolher" }),
    ).toHaveAttribute("aria-expanded", "true");

    await page.getByTestId("diagnose-oidc").focus();
    await page.keyboard.press("Enter");
    await expect(page.getByTestId("diagnostic-report")).toBeVisible();
  });

  test("stays usable at 200% zoom on a small viewport", async ({ page }) => {
    await page.setViewportSize({ width: 640, height: 800 });
    await signedIn(page);
    await integrations(page);
    await json(page, "**/api/admin/integrations/oidc/diagnose", FAILED_REPORT);
    await page.goto("/integrations");

    await page.getByTestId("integration-oidc").getByRole("button", { name: "Abrir" }).click();
    await page.getByTestId("diagnose-oidc").click();
    await expect(page.getByTestId("diagnostic-report")).toBeVisible();

    // The page may scroll down; it must never scroll sideways, or a result has
    // to be panned across to be read.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });
});

test.describe("Busca de configurações", () => {
  test("finds an integration's settings and opens the right section", async ({ page }) => {
    await signedIn(page);
    await json(page, "**/api/admin/config", CONFIG_CATALOG);
    await json(page, "**/api/admin/config/versions*", { data: { versions: [] } });
    await page.goto("/configuration");

    await expect(page.getByTestId("config-auth.password.min_length")).toBeVisible();

    await page.getByLabel("Buscar configuração").fill("client secret");

    await expect(page.getByTestId("config-secret.oidc_client_secret")).toBeVisible();
    await expect(page.getByTestId("config-auth.password.min_length")).toHaveCount(0);
    await expect(page.getByTestId("config-search-count")).toContainText("1 de 3");
  });

  test("does not index values", async ({ page }) => {
    await signedIn(page);
    await json(page, "**/api/admin/config", CONFIG_CATALOG);
    await json(page, "**/api/admin/config/versions*", { data: { versions: [] } });
    await page.goto("/configuration");
    await expect(page.getByTestId("config-auth.password.min_length")).toBeVisible();

    // "configurado" is the *status* of the credential on screen. It is not
    // metadata, so it matches nothing: the index has labels and descriptions,
    // never values or anything derived from one.
    await page.getByLabel("Buscar configuração").fill("configurado");
    await expect(page.getByTestId("config-search-count")).toContainText("0 de 3");
  });

  // The link on an integration card has to land on the settings it names.
  //
  // It used to carry the display name — "Keycloak / OIDC" — which is
  // presentation: translated, tokenised on the slash, and containing a word
  // ("Keycloak") that appears in no configuration key. Following it produced an
  // empty result. The link now carries the integration id, the slug the keys are
  // namespaced with.
  //
  // The assertion is what the operator sees at the destination, not the URL:
  // asserting the URL is what let the bug through in the first place.
  test("follows an integration card into its real settings", async ({ page }) => {
    await signedIn(page);
    await integrations(page);
    await json(page, "**/api/admin/config", CONFIG_CATALOG);
    await json(page, "**/api/admin/config/versions*", { data: { versions: [] } });
    await page.goto("/integrations");

    await page.getByTestId("integration-oidc").getByRole("button", { name: "Abrir" }).click();
    await page.getByTestId("configure-oidc").click();

    await expect(page).toHaveURL(/\/configuration\?q=oidc$/);
    await expect(page.getByLabel("Buscar configuração")).toHaveValue("oidc");

    await expect(page.getByTestId("config-oidc.enabled")).toBeVisible();
    await expect(page.getByText("Single sign-on habilitado")).toBeVisible();
    await expect(page.getByTestId("config-search-count")).not.toContainText("0 de");
    // The unrelated policy field is gone, so the search really filtered.
    await expect(page.getByTestId("config-auth.password.min_length")).toHaveCount(0);
  });
});
