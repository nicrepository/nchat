import { expect, test, type Page } from "@playwright/test";

import {
  allowsServiceUnavailable,
  captureBrowserErrors,
  expectNoUnexpectedBrowserErrors,
} from "./helpers/browserErrors";

/**
 * GET /api/notifications/push/config through the real app (issue #902).
 *
 * What a jsdom test cannot show: that the page really asks the
 * notification-service on its own, at the URL the gateway routes, and that the
 * three answers it can get — a key, an explicit null, an outage — end up as three
 * different things on the screen. Whether each status maps to the right snapshot
 * is src/notifications/webPushReconciler.test.ts; whether the service answers
 * 503 instead of 404 is the Go router tests.
 *
 * Notification.permission is pinned to "default": headless Chromium reports
 * "denied", which would hide every other message behind the "blocked" one. Nothing
 * here prompts, and nothing may subscribe — each test asserts it.
 */

const CONFIG_PATH = "/api/notifications/push/config";
const SUBSCRIPTIONS_GLOB = "**/api/notifications/push/subscriptions**";

async function signedInProfilePage(page: Page) {
  await page.addInitScript(() => {
    sessionStorage.setItem("nchat_at", "e2e-at");
    Object.defineProperty(Notification, "permission", { get: () => "default" });
  });
  const json = (body: unknown) => ({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
  await page.route("**/api/auth/me", (route) =>
    route.fulfill(json({ data: { id: "e2e-user", display_name: "E2E User" } })),
  );
  await page.route("**/api/chat/sidebar", (route) =>
    route.fulfill(
      json({
        data: {
          current_user_id: "e2e-user",
          workspace: { id: "e2e-workspace", name: "E2E", slug: "e2e" },
          channels: [],
          dm_conversations: [],
        },
      }),
    ),
  );
  await page.route("**/api/chat/channel-categories", (route) =>
    route.fulfill(json({ data: { groups: [] } })),
  );
}

/**
 * Answers the config request and counts what the page sends. `reached(n)` settles
 * once the n-th config request has been answered, so a test waits on an event
 * rather than on time.
 */
async function stubPushConfig(page: Page, answer: { status: number; body: unknown }) {
  const seen = { configRequests: 0, subscriptionRequests: 0 };
  const waiting: Array<{ n: number; resolve: () => void }> = [];
  const reached = (n: number) =>
    new Promise<void>((resolve) => {
      if (seen.configRequests >= n) resolve();
      else waiting.push({ n, resolve });
    });
  await page.route(`**${CONFIG_PATH}`, async (route) => {
    seen.configRequests += 1;
    waiting.filter((w) => seen.configRequests >= w.n).forEach((w) => w.resolve());
    await route.fulfill({
      status: answer.status,
      contentType: "application/json",
      body: JSON.stringify(answer.body),
    });
  });
  await page.route(SUBSCRIPTIONS_GLOB, async (route) => {
    seen.subscriptionRequests += 1;
    await route.abort();
  });
  await page.route("**/__e2e-barrier", (route) => route.fulfill({ status: 204 }));
  return { seen, reached };
}

/**
 * "Nothing else asks" without waiting for it: run the page's clock a minute ahead
 * (every timer it could have scheduled fires), then send a request of our own and
 * wait for it. Requests are handled in the order they were issued, so anything
 * the page sent before the barrier has been counted when it answers.
 */
async function letPageTimersRun(page: Page) {
  await page.clock.fastForward(60_000);
  await page.evaluate(() => fetch("/__e2e-barrier"));
}

/** A second interaction, past the coalescing window: the only thing that may ask again. */
async function focusAgain(page: Page) {
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
}

/**
 * A registration that is not active yet makes the first pass stop before it asks
 * for the config, and the next one is held back by the reconciler's coalescing
 * window. Activating the worker on an earlier page load is the deterministic way
 * past that: the load that follows asks straight away.
 */
async function activateServiceWorker(page: Page) {
  await page.goto("/login");
  await page.evaluate(() => navigator.serviceWorker.ready);
}

test.describe("push config: o que a tela diz para cada resposta", () => {
  test("com a chave pública: aceita a configuração e não assina nada sozinho", async ({ page }) => {
    captureBrowserErrors(page);
    await activateServiceWorker(page);
    await page.clock.install();
    await signedInProfilePage(page);
    const { seen, reached } = await stubPushConfig(page, {
      status: 200,
      body: { data: { vapid_public_key: "BE2E-public-key" } },
    });

    await page.goto("/profile/notifications");
    await reached(1);
    await expect(page.getByText(/Ative notificações do navegador/)).toBeVisible();

    await letPageTimersRun(page);
    expect(seen.configRequests).toBe(1);

    await focusAgain(page);
    await reached(2);
    await letPageTimersRun(page);
    expect(seen.configRequests).toBe(2);
    expect(seen.subscriptionRequests).toBe(0);
    expectNoUnexpectedBrowserErrors(page);
  });

  test("com chave nula: informa que o ambiente não entrega push, sem assinar", async ({ page }) => {
    captureBrowserErrors(page);
    await activateServiceWorker(page);
    await page.clock.install();
    await signedInProfilePage(page);
    const { seen, reached } = await stubPushConfig(page, {
      status: 200,
      body: { data: { vapid_public_key: null } },
    });

    await page.goto("/profile/notifications");
    await reached(1);
    await expect(page.getByText(/ainda não estão disponíveis neste ambiente/)).toBeVisible();

    // One pass on load; no timer keeps asking.
    await letPageTimersRun(page);
    expect(seen.configRequests).toBe(1);

    await focusAgain(page);
    await reached(2);
    await letPageTimersRun(page);
    expect(seen.configRequests).toBe(2);
    expect(seen.subscriptionRequests).toBe(0);
    await expect(page.getByText(/ainda não estão disponíveis neste ambiente/)).toBeVisible();
    expectNoUnexpectedBrowserErrors(page);
  });

  test("com 503: é um erro de backend recuperável, não um ambiente sem push", async ({ page }) => {
    captureBrowserErrors(page, [allowsServiceUnavailable(CONFIG_PATH)]);
    await activateServiceWorker(page);
    await page.clock.install();
    await signedInProfilePage(page);
    const { seen, reached } = await stubPushConfig(page, {
      status: 503,
      body: { error: { code: "push_delivery_unavailable", message: "unavailable" } },
    });

    await page.goto("/profile/notifications");
    await reached(1);
    await expect(
      page.getByText(/Não foi possível conectar ao serviço de notificações/),
    ).toBeVisible();
    await expect(page.getByText(/ainda não estão disponíveis neste ambiente/)).toHaveCount(0);

    // The failure is not retried on a timer; only the next interaction asks again.
    await letPageTimersRun(page);
    expect(seen.configRequests).toBe(1);

    await focusAgain(page);
    await reached(2);
    await letPageTimersRun(page);
    expect(seen.configRequests).toBe(2);
    expect(seen.subscriptionRequests).toBe(0);
    await expect(page.getByText(/ainda não estão disponíveis neste ambiente/)).toHaveCount(0);
    expectNoUnexpectedBrowserErrors(page);
  });
});
