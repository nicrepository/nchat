import { expect, test, type Page } from "@playwright/test";

/**
 * Service Worker registration and lifecycle in a real browser (issue #747).
 *
 * What is covered here is the half of the issue a jsdom test cannot reach: that
 * /sw.js is actually served at the root, that the application bootstrap
 * registers it, that it reaches "activated", and that navigating the app again
 * does not produce a second registration.
 *
 * What is *not* covered here, and why: headless Chromium — the mode this suite
 * runs in, locally and in CI — reports Notification.permission as "denied" and
 * context.grantPermissions() does not change it, so registration.showNotification()
 * is refused and registration.getNotifications() is always empty. A push
 * delivered through CDP therefore has no observable effect to assert on. This
 * was confirmed against the real browser rather than assumed. Rendering a
 * notification and clicking one would need a headed browser (an X server on the
 * runner), which is a change to the CI harness and not to this issue. The push
 * payload contract, the fail-closed cases and the whole notificationclick
 * decision — focus, navigate, openWindow — are covered against the real API
 * shapes in src/notifications/serviceWorker.test.ts.
 */

interface WorkerState {
  scopes: string[];
  state: string | undefined;
}

function serviceWorkerState(page: Page): Promise<WorkerState> {
  return page.evaluate(async () => {
    const active = await navigator.serviceWorker.ready;
    const registrations = await navigator.serviceWorker.getRegistrations();
    return {
      scopes: registrations.map((registration) => registration.scope),
      state: active.active?.state,
    };
  });
}

test.describe("service worker: registro e ciclo de vida", () => {
  test("o bootstrap registra um worker ativo no escopo raiz", async ({ page, baseURL }) => {
    await page.goto("/login");

    await expect
      .poll(() => serviceWorkerState(page))
      .toEqual({
        scopes: [`${baseURL}/`],
        state: "activated",
      });
  });

  test("navegar de novo não cria um segundo registro", async ({ page, baseURL }) => {
    await page.goto("/login");
    await expect
      .poll(() => serviceWorkerState(page))
      .toEqual({
        scopes: [`${baseURL}/`],
        state: "activated",
      });

    await page.goto("/forgot-password");
    await page.reload();

    await expect
      .poll(() => serviceWorkerState(page))
      .toEqual({
        scopes: [`${baseURL}/`],
        state: "activated",
      });
  });

  test("a aplicação carrega normalmente com o worker registrado", async ({ page }) => {
    await page.goto("/login");

    await expect(page.getByRole("button", { name: "Entrar" })).toBeVisible();
    await expect.poll(() => serviceWorkerState(page)).toMatchObject({ state: "activated" });
  });
});
