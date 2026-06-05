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
  await page.route("**/api/auth/password/forgot", (route) => route.fulfill({ status: 204 }));
}

async function mockOIDCExchangeSuccess(page: Page) {
  await page.route("**/api/auth/oidc/keycloak/exchange", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ...MOCK_TOKENS,
        user: {
          id: "sso-user",
          email: "sso@example.test",
          display_name: "SSO User",
          must_change_password: false,
        },
      }),
    }),
  );
}

async function mockOIDCExchangeFailure(page: Page) {
  await page.route("**/api/auth/oidc/keycloak/exchange", (route) =>
    route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({
        error: { code: "invalid_oidc_callback", message: "expired or reused code" },
      }),
    }),
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

  test("SSO link is visible and points to backend Keycloak login endpoint", async ({ page }) => {
    await page.goto("/login");
    const ssoLink = page.getByRole("link", { name: /entrar com keycloak/i });
    await expect(ssoLink).toBeVisible();
    await expect(ssoLink).toHaveAttribute("href", "/api/auth/oidc/keycloak/login");
  });

  test("OIDC callback with missing code shows generic SSO error and link to /login", async ({
    page,
  }) => {
    await page.goto("/oidc-callback");
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("alert")).toContainText(
      /não foi possível concluir o login com sso/i,
    );
    await expect(page.getByRole("link", { name: /voltar ao login/i })).toBeVisible();
  });

  test("OIDC callback successfully exchanges code, stores session, and navigates home", async ({
    page,
  }) => {
    await mockOIDCExchangeSuccess(page);
    await mockRefreshApi(page);
    await page.goto("/oidc-callback?code=opaque-test-code");
    await expect(page).toHaveURL("/");
    await expect(page.getByRole("heading", { name: "NChat" })).toBeVisible();
  });

  test("OIDC callback handles exchange failure with generic SSO error", async ({ page }) => {
    await mockOIDCExchangeFailure(page);
    await page.goto("/oidc-callback?code=expired-code");
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("alert")).toContainText(
      /não foi possível concluir o login com sso/i,
    );
    await expect(page.getByRole("alert").textContent()).resolves.not.toMatch(
      /expired-code|expired or reused/i,
    );
  });

  test("OIDC callback posts exchange request exactly once", async ({ page }) => {
    let exchangeCount = 0;
    await page.route("**/api/auth/oidc/keycloak/exchange", (route) => {
      exchangeCount++;
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          ...MOCK_TOKENS,
          user: {
            id: "sso-user",
            email: "sso@example.test",
            display_name: "SSO User",
            must_change_password: false,
          },
        }),
      });
    });
    await mockRefreshApi(page);
    await page.goto("/oidc-callback?code=opaque-test-code");
    await expect(page).toHaveURL("/");
    expect(exchangeCount).toBe(1);
  });

  test("OIDC callback removes code from URL on success before navigating home", async ({
    page,
  }) => {
    await mockOIDCExchangeSuccess(page);
    await mockRefreshApi(page);
    await page.goto("/oidc-callback?code=opaque-test-code");
    await expect(page).toHaveURL("/");
    // Code must never appear in the final URL
    expect(page.url()).not.toContain("code=");
  });

  test("login page redirects already-authenticated user to /", async ({ page }) => {
    await performLogin(page);
    await page.goto("/login");
    await expect(page).toHaveURL("/");
    await expect(page.getByRole("heading", { name: "NChat" })).toBeVisible();
  });

  test("public route /forgot-password is accessible when unauthenticated", async ({ page }) => {
    await mockForgotPasswordApi(page);
    await page.goto("/forgot-password");
    await expect(page).toHaveURL("/forgot-password");
    await expect(page.getByLabel(/e-mail corporativo/i)).toBeVisible();
  });

  test("public route /reset-password is accessible when unauthenticated", async ({ page }) => {
    await page.goto("/reset-password");
    await expect(page).toHaveURL("/reset-password");
    await expect(page).not.toHaveURL("/login");
  });

  test("public route /accept-invite is accessible when unauthenticated", async ({ page }) => {
    await page.goto("/accept-invite");
    await expect(page).toHaveURL("/accept-invite");
    await expect(page).not.toHaveURL("/login");
  });
});
