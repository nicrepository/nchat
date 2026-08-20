import { expect, test, type Page } from "@playwright/test";

/**
 * End-to-end coverage of the console shell.
 *
 * The Admin API is stubbed at the network boundary, exactly as the chat app's
 * specs stub theirs: what is under test here is the console's own behaviour —
 * the gate, the navigation, the environment indicator, the deep link and the
 * sign-out — not the server's authorization, which has its own suite in Go.
 *
 * The one thing these specs must never do is assert that the frontend is what
 * keeps an unauthorized user out. Every case below that ends in a refusal ends
 * there because the API refused.
 */

const BOOTSTRAP = {
  identity: {
    user_id: "11111111-1111-1111-1111-111111111111",
    email: "admin@example.test",
    display_name: "Admin E2E",
    avatar_url: "",
  },
  capabilities: ["admin.audit.read", "admin.config.read"],
  environment: "STAGING",
  build: { service: "admin-service", version: "0.0.0", commit: "e2e" },
  session: {
    idle_expires_at: "2099-01-01T00:00:00Z",
    absolute_expires_at: "2099-01-01T08:00:00Z",
  },
  csrf_token: "csrf-e2e",
};

async function stubBootstrap(page: Page, body: unknown, status = 200) {
  await page.route("**/api/admin/bootstrap", (route) =>
    route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) }),
  );
}

async function stubAudit(page: Page, status = 200) {
  await page.route("**/api/admin/audit/events*", (route) =>
    route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(
        status === 200
          ? { data: { events: [] } }
          : { error: { code: "forbidden", message: "forbidden" } },
      ),
    }),
  );
}

test("um visitante anônimo recebe a tela de acesso, não o console", async ({ page }) => {
  await stubBootstrap(page, { error: { code: "unauthorized", message: "unauthorized" } }, 401);

  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Console administrativo" })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Seções administrativas" })).toHaveCount(0);
});

test("login estabelece a sessão administrativa e monta o shell", async ({ page }) => {
  await stubBootstrap(page, { error: { code: "unauthorized", message: "unauthorized" } }, 401);
  await page.route("**/api/auth/login", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ access_token: "at-e2e", token_type: "Bearer", expires_in: 900 }),
    }),
  );
  await page.route("**/api/admin/session", (route) =>
    route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({ data: BOOTSTRAP }),
    }),
  );

  await page.goto("/");
  await page.getByLabel("E-mail").fill("admin@example.test");
  await page.getByLabel("Senha").fill("correct-horse");
  await page.getByRole("button", { name: "Entrar" }).click();

  await expect(page.getByRole("heading", { name: "Visão geral" })).toBeVisible();
  // The proof of identity is never persisted by the console.
  expect(await page.evaluate(() => sessionStorage.length + localStorage.length)).toBe(0);
});

test("o shell navega entre as seções implementadas", async ({ page }) => {
  await stubBootstrap(page, { data: BOOTSTRAP });
  await stubAudit(page);

  await page.goto("/");
  await page.getByRole("link", { name: "Auditoria" }).click();

  await expect(page.getByRole("heading", { name: "Auditoria" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Auditoria" })).toHaveAttribute(
    "aria-current",
    "page",
  );
});

test("deep link direto respeita a mesma proteção", async ({ page }) => {
  await stubBootstrap(page, { error: { code: "unauthorized", message: "unauthorized" } }, 401);

  await page.goto("/audit");

  await expect(page.getByRole("heading", { name: "Console administrativo" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Auditoria" })).toHaveCount(0);
});

test("deep link funciona com sessão válida", async ({ page }) => {
  await stubBootstrap(page, { data: BOOTSTRAP });
  await stubAudit(page);

  await page.goto("/audit");

  await expect(page.getByRole("heading", { name: "Auditoria" })).toBeVisible();
});

test("usuário comum autenticado recebe recusa explícita", async ({ page }) => {
  await stubBootstrap(page, { error: { code: "forbidden", message: "forbidden" } }, 403);

  await page.goto("/");

  await expect(page.getByRole("alert")).toContainText("não possui acesso administrativo");
  await expect(page.getByRole("navigation", { name: "Seções administrativas" })).toHaveCount(0);
});

test("admin parcial não vê nem alcança a seção que não pode ler", async ({ page }) => {
  await stubBootstrap(page, {
    data: { ...BOOTSTRAP, capabilities: ["admin.config.read"] },
  });
  await stubAudit(page, 403);

  await page.goto("/");
  const nav = page.getByRole("navigation", { name: "Seções administrativas" });
  await expect(nav.getByRole("link", { name: "Auditoria" })).toHaveCount(0);

  // And the section refuses even when reached by URL, because the API refuses.
  await page.goto("/audit");
  await expect(page.getByRole("alert")).toContainText("Você não tem permissão");
});

test("o ambiente exibido vem do backend, não do hostname", async ({ page }) => {
  await stubBootstrap(page, { data: { ...BOOTSTRAP, environment: "PRODUCTION" } });

  await page.goto("/");

  await expect(page.getByTestId("admin-environment")).toHaveText(/PRODUCTION/);
  await expect(page.getByTestId("admin-environment")).toHaveAttribute(
    "data-environment",
    "PRODUCTION",
  );
});

test("seções não implementadas aparecem como indisponíveis e não são clicáveis", async ({
  page,
}) => {
  await stubBootstrap(page, { data: { ...BOOTSTRAP, capabilities: ["admin.superuser"] } });

  await page.goto("/");
  const nav = page.getByRole("navigation", { name: "Seções administrativas" });

  await expect(nav.getByText("Health Center")).toHaveCount(1);
  await expect(nav.getByRole("link", { name: "Health Center" })).toHaveCount(0);
});

test("logout encerra a sessão e volta para a tela de acesso", async ({ page }) => {
  await stubBootstrap(page, { data: BOOTSTRAP });
  await page.route("**/api/admin/session", (route) => route.fulfill({ status: 204, body: "" }));

  await page.goto("/");
  await page.getByRole("button", { name: "Sair" }).click();

  await expect(page.getByRole("heading", { name: "Console administrativo" })).toBeVisible();
});
