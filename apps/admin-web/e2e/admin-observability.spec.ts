import { expect, test, type Page } from "@playwright/test";

/**
 * End-to-end coverage of the Dashboard and the Health Center (issue #581).
 *
 * The Admin API is stubbed at the network boundary, like every other console
 * suite: what is under test is the console's behaviour — the states it keeps
 * apart, the filter, the diagnosis, the refresh — and not the server's
 * authorization, which has its own suite in Go.
 *
 * The rule the earlier suites set holds here: no case may assert that the
 * frontend is what keeps an unauthorized operator out. The 403 case below
 * asserts that the console *reports* a refusal the server made, which is a
 * different claim.
 */

const COLLECTED_AT = "2026-08-22T12:00:00.000Z";

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

const POSTGRES = {
  id: "postgres",
  display_name: "PostgreSQL",
  category: "data",
  impact: "Banco de dados da plataforma.",
  state: "healthy",
  enabled: true,
  observable: true,
  critical: true,
  latency_ms: 12,
  checked_at: COLLECTED_AT,
  runbook_path: "docs/runbooks/task-14-health-checks.md",
};

const LIVEKIT = {
  id: "livekit",
  display_name: "LiveKit",
  category: "realtime",
  impact: "Servidor de mídia das chamadas.",
  state: "unavailable",
  enabled: true,
  observable: true,
  critical: false,
  latency_ms: 3001,
  checked_at: COLLECTED_AT,
  error_category: "connection_timeout",
  detail: "A dependência não respondeu dentro do tempo limite do check.",
  config_key: "calls.livekit.enabled",
  runbook_path: "docs/runbooks/task-livekit-coturn-dev.md",
};

const SMTP = {
  id: "smtp",
  display_name: "SMTP",
  category: "messaging",
  impact: "Relay de e-mail.",
  state: "disabled",
  enabled: false,
  observable: true,
  critical: false,
  checked_at: COLLECTED_AT,
  runbook_path: "docs/runbooks/task-smtp-bruteforce-login-audit.md",
};

const TURN = {
  id: "turn",
  display_name: "TURN / coturn",
  category: "realtime",
  impact: "Relay de mídia para redes restritivas.",
  state: "unknown",
  enabled: true,
  observable: false,
  critical: false,
  checked_at: COLLECTED_AT,
  error_category: "not_observable",
  detail: "Este pod não recebe a configuração que nomeia o endpoint desta integração.",
  runbook_path: "docs/runbooks/task-livekit-coturn-dev.md",
};

const SNAPSHOT = {
  data: {
    collected_at: COLLECTED_AT,
    overall: "degraded",
    services: [LIVEKIT, TURN, POSTGRES, SMTP],
  },
};

const SUMMARY = {
  data: {
    summary: {
      collected_at: COLLECTED_AT,
      overall: "degraded",
      state_counts: { healthy: 1, degraded: 0, unavailable: 1, disabled: 1, unknown: 1 },
      metrics: [
        {
          key: "users.active_now",
          label: "Usuários ativos agora",
          definition: "Contas distintas com ao menos uma sessão de chat viva.",
          window: "instant",
          unit: "count",
          value: 3,
          available: true,
        },
        {
          key: "messages.last_24h",
          label: "Mensagens em 24 h",
          definition: "Mensagens criadas nas últimas 24 horas.",
          window: "last_24h",
          unit: "count",
          value: 431,
          available: true,
        },
      ],
      metrics_available: true,
      alerts: [
        {
          service_id: "livekit",
          severity: "warning",
          title: "LiveKit indisponível",
          impact: "Servidor de mídia das chamadas.",
          action: "Verifique se a dependência está de pé.",
          since: COLLECTED_AT,
          runbook_path: "docs/runbooks/task-livekit-coturn-dev.md",
          config_key: "calls.livekit.enabled",
        },
      ],
    },
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

async function observability(page: Page) {
  await json(page, "**/api/admin/overview", SUMMARY);
  await json(page, "**/api/admin/health/services", SNAPSHOT);
  await json(page, "**/api/admin/health/refresh", SNAPSHOT);
}

test.describe("Dashboard", () => {
  test("opens with the platform state, the alerts and the metrics", async ({ page }) => {
    await signedIn(page);
    await observability(page);
    await page.goto("/");

    const state = page.getByRole("region", { name: "Estado da plataforma" });
    await expect(state.getByRole("status")).toContainText("Degradado");

    const alerts = page.getByRole("region", { name: "Requer atenção" });
    await expect(alerts.getByText("LiveKit indisponível")).toBeVisible();
    await expect(alerts.getByText("Verifique se a dependência está de pé.")).toBeVisible();

    await expect(page.getByTestId("metric-users.active_now")).toContainText("3");
    await expect(page.getByTestId("metric-messages.last_24h")).toContainText("últimas 24 h");
  });

  test("loads the whole dashboard from one aggregate endpoint", async ({ page }) => {
    const paths = new Set<string>();
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.pathname.startsWith("/api/admin/")) paths.add(url.pathname);
    });
    await signedIn(page);
    await observability(page);
    await page.goto("/");
    await expect(page.getByRole("region", { name: "Estado da plataforma" })).toBeVisible();

    // The session handshake and one aggregate read. There is no endpoint per
    // card: a dozen metrics, the health rollup and the alerts all arrive
    // together. The count of *distinct* paths is the claim — the dev server
    // mounts under StrictMode, so the same path can legitimately be fetched
    // twice.
    expect([...paths].sort()).toEqual(["/api/admin/bootstrap", "/api/admin/overview"]);
  });

  test("leads from an alert into the matching diagnosis", async ({ page }) => {
    await signedIn(page);
    await observability(page);
    await page.goto("/");

    await page.getByRole("link", { name: "Ver diagnóstico" }).click();

    await expect(page.getByRole("heading", { name: "Health Center" })).toBeVisible();
    // The row the alert pointed at is already open.
    await expect(page.locator("#health-detail-livekit")).toContainText("Tempo limite de conexão");
  });
});

test.describe("Health Center", () => {
  test("lists every dependency and keeps the states apart", async ({ page }) => {
    await signedIn(page);
    await observability(page);
    await page.goto("/health");

    await expect(page.getByTestId("health-row-postgres")).toContainText("Saudável");
    await expect(page.getByTestId("health-row-livekit")).toContainText("Indisponível");
    await expect(page.getByTestId("health-row-smtp")).toContainText("Desabilitado");
    await expect(page.getByTestId("health-row-turn")).toContainText("Desconhecido");

    // A check that never ran reports no latency at all rather than 0 ms.
    await expect(page.getByTestId("health-row-smtp")).toContainText("—");
    await expect(page.getByTestId("health-row-postgres")).toContainText("12 ms");
  });

  test("filters down to the degraded and unavailable dependencies", async ({ page }) => {
    await signedIn(page);
    await observability(page);
    await page.goto("/health");

    await page.getByLabel("Filtrar por estado").selectOption("unavailable");

    await expect(page.getByTestId("health-row-livekit")).toBeVisible();
    await expect(page.getByTestId("health-row-postgres")).toHaveCount(0);
    await expect(page.getByTestId("health-row-smtp")).toHaveCount(0);
  });

  test("opens a diagnosis without revealing any endpoint", async ({ page }) => {
    await signedIn(page);
    await observability(page);
    await page.goto("/health");

    await page.getByTestId("health-row-livekit").getByRole("button", { name: "Detalhes" }).click();

    const detail = page.locator("#health-detail-livekit");
    await expect(detail).toContainText("Tempo limite de conexão");
    await expect(detail).toContainText("docs/runbooks/task-livekit-coturn-dev.md");
    // The diagnosis says what is wrong and what to do, and names no host, port
    // or credential — because the API never sent one.
    await expect(detail).not.toContainText("livekit.");
    await expect(detail).not.toContainText("7880");
  });

  test("an unreachable integration does not break the page", async ({ page }) => {
    await signedIn(page);
    await observability(page);
    await page.goto("/health");

    // Every other row is still rendered alongside the failing one.
    await expect(page.getByRole("row")).toHaveCount(5);
    await expect(page.getByTestId("health-row-postgres")).toContainText("Saudável");
  });

  test("refreshes on demand and reports the new collection", async ({ page }) => {
    await signedIn(page);
    await json(page, "**/api/admin/health/services", SNAPSHOT);
    await json(page, "**/api/admin/health/refresh", {
      data: {
        collected_at: "2026-08-22T12:07:00.000Z",
        overall: "healthy",
        services: [POSTGRES],
      },
    });
    await page.goto("/health");

    await page.getByRole("button", { name: "Atualizar agora" }).click();

    await expect(page.getByTestId("health-row-livekit")).toHaveCount(0);
    await expect(page.getByTestId("health-row-postgres")).toBeVisible();
  });

  test("reports a refusal the server made, and stays usable", async ({ page }) => {
    // The console never decides authorization. What it must do is render the
    // refusal as a permission problem rather than as an outage.
    await signedIn(page, ["admin.audit.read"]);
    await json(page, "**/api/admin/health/services", { error: { code: "forbidden" } }, 403);
    await page.goto("/health");

    await expect(page.getByRole("alert")).toContainText("Você não tem permissão para esta seção.");
    await expect(page.getByRole("table")).toHaveCount(0);
    // The legend is static and still explains the model.
    await expect(page.getByRole("region", { name: "O que cada estado significa" })).toBeVisible();
  });

  test("hides the operational dashboard for a session without the capability", async ({ page }) => {
    await signedIn(page, ["admin.audit.read"]);
    await page.goto("/");

    await expect(page.getByText("não tem a permissão")).toBeVisible();
    await expect(page.getByRole("region", { name: "Esta sessão" })).toBeVisible();
  });
});
