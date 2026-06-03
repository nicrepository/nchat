import { expect, test, type Page } from "@playwright/test";

const TEST_EMAIL = "e2e-user@example.test";
const TEST_PASSWORD = "test-password";

const MOCK_TOKENS = {
  access_token: "e2e-at",
  refresh_token: "e2e-rt",
  token_type: "Bearer",
  expires_in: 3600,
};

async function mockLoginSuccess(page: Page) {
  await page.route("**/api/auth/login", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ...MOCK_TOKENS,
        user: {
          id: "e2e-user",
          email: TEST_EMAIL,
          display_name: "E2E User",
          must_change_password: false,
        },
      }),
    }),
  );
}

async function mockLoginFailure(page: Page) {
  await page.route("**/api/auth/login", (route) =>
    route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({
        error: { code: "invalid_credentials", message: "Invalid credentials" },
      }),
    }),
  );
}

async function mockRefreshApi(page: Page) {
  await page.route("**/api/auth/refresh", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(MOCK_TOKENS),
    }),
  );
}

async function mockForgotPasswordApi(page: Page) {
  await page.route("**/api/auth/password/forgot", (route) =>
    route.fulfill({ status: 204 }),
  );
}

async function performLogin(page: Page) {
  await mockLoginSuccess(page);
  await mockRefreshApi(page);
  await page.goto("/login");
  await page.getByLabel("E-mail corporativo").fill(TEST_EMAIL);
  await page.getByLabel("Senha").fill(TEST_PASSWORD);
  await page.getByRole("button", { name: /^Entrar$/i }).click();
  await expect(page).toHaveURL("/");
}

test.describe("auth", () => {
  test("unauthenticated visit to protected route redirects to /login", async ({ page }) => {
    await page.goto("/");
    await expect(page).toHaveURL("/login");
  });

  test("valid credentials log in and show home page", async ({ page }) => {
    await performLogin(page);
    await expect(page.getByRole("heading", { name: "NChat" })).toBeVisible();
  });

  test("session persists after page reload", async ({ page }) => {
    await performLogin(page);
    await page.reload();
    await expect(page).toHaveURL("/");
    await expect(page.getByRole("heading", { name: "NChat" })).toBeVisible();
  });

  // Simulates logout by clearing session tokens (no logout UI button exists yet in HomePage).
  test("clearing session tokens blocks access to protected route", async ({ page }) => {
    await performLogin(page);
    await page.evaluate(() => {
      sessionStorage.removeItem("nchat_at");
      sessionStorage.removeItem("nchat_rt");
    });
    await page.goto("/");
    await expect(page).toHaveURL("/login");
  });

  test("invalid credentials show error and keep user on /login", async ({ page }) => {
    await mockLoginFailure(page);
    await page.goto("/login");
    await page.getByLabel("E-mail corporativo").fill("wrong@example.com");
    await page.getByLabel("Senha").fill("wrongpassword");
    await page.getByRole("button", { name: /^Entrar$/i }).click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("alert")).toContainText(/e-mail ou senha inválidos/i);
    await expect(page).toHaveURL("/login");
  });

  test("forgot password form shows success message after submission", async ({ page }) => {
    await mockForgotPasswordApi(page);
    await page.goto("/forgot-password");
    await page.getByLabel("E-mail corporativo").fill("user@nic-labs.com");
    await page.getByRole("button", { name: /enviar instruções/i }).click();
    await expect(page.getByRole("status")).toBeVisible();
    await expect(page.getByRole("status")).toContainText(/instruções/i);
    await expect(page.getByRole("link", { name: /voltar para entrar/i })).toBeVisible();
  });

  test("SSO button is visible but disabled (smoke test)", async ({ page }) => {
    await page.goto("/login");
    const ssoButton = page.getByRole("button", { name: /sso/i });
    await expect(ssoButton).toBeVisible();
    await expect(ssoButton).toBeDisabled();
  });
});
