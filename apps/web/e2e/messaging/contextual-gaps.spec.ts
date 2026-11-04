import { expect, test, type Page } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  OTHER_CHANNEL_NAME,
  createScenario,
  fillComposer,
  installMessagingMocks,
  makeMessage,
} from "../helpers/messagingApi";

test.use({ screenshot: "only-on-failure" });

async function focusEvidence(page: Page, step: string) {
  await test.info().attach(step, {
    contentType: "application/json",
    body: JSON.stringify(
      await page.evaluate(() => ({
        url: location.href,
        historyLength: history.length,
        active: document.activeElement?.outerHTML,
      })),
    ),
  });
}

test("M-01: profile → Ctrl+K → Escape preserves the internal origin and native Forward", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await installMessagingMocks(
    page,
    createScenario({
      kind: "channel",
      targetId: "profile-origin",
      targetName: "Origem",
      messages: [],
    }),
  );
  await page.goto("/profile");
  const search = page.getByRole("button", { name: "Buscar no NChat", exact: true });
  await expect(search).toBeVisible();
  await search.focus();
  const initial = await page.evaluate(() => history.length);
  await page.keyboard.press("Control+k");
  await expect(page).toHaveURL(/\/chat\/search$/);
  await expect(page.getByRole("searchbox")).toBeFocused();
  await page.keyboard.press("Escape");
  await focusEvidence(page, "profile-first-escape");
  await expect(page).toHaveURL(/\/profile$/);
  await expect(page.getByTestId("global-search")).toHaveCount(0);
  await expect(search).toBeVisible();
  await expect(search).toBeFocused();
  expect(await page.evaluate(() => history.length)).toBe(initial + 1);
  await page.goForward();
  await expect(page.getByRole("searchbox")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/profile$/);
  await expect(search).toBeFocused();
  await search.click();
  await expect(page.getByRole("searchbox")).toBeFocused();
  await page.goBack();
  await expect(page).toHaveURL(/\/profile$/);
  await expect(search).toBeFocused();
  expect(await page.evaluate(() => history.length)).toBe(initial + 1);
});

test("R4: Home → search → Escape → native Forward → Escape at 1440x900", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await installMessagingMocks(
    page,
    createScenario({
      kind: "channel",
      targetId: "focus-origin",
      targetName: "Origem",
      messages: [],
    }),
  );
  await page.goto("/chat");
  const search = page.getByRole("button", { name: "Buscar no NChat", exact: true });
  await expect(search).toBeVisible();
  await expect(page.getByTestId("chat-nav-toggle")).toBeHidden();
  const initial = await page.evaluate(() => history.length);
  await focusEvidence(page, "home");
  await search.click();
  await expect(page).toHaveURL(/\/chat\/search$/);
  await expect(page.getByRole("searchbox")).toBeFocused();
  expect(await page.evaluate(() => history.length)).toBe(initial + 1);
  await focusEvidence(page, "search-first");
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/chat$/);
  await focusEvidence(page, "home-first-return");
  await expect(search).toBeFocused();
  await page.goForward();
  await expect(page).toHaveURL(/\/chat\/search$/);
  await expect(page.getByRole("searchbox")).toBeFocused();
  await focusEvidence(page, "search-native-forward");
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/chat$/);
  await expect(page.getByTestId("global-search")).toHaveCount(0);
  expect(await page.evaluate(() => history.length)).toBe(initial + 1);
  await focusEvidence(page, "home-second-return");
  await expect(search).toBeVisible();
  await expect(search).toBeFocused();
});

test("external local predecessor: search Escape stays inside NChat and opens the list", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("http://127.0.0.1:5173/controlled-origin", (route) =>
    route.fulfill({
      contentType: "text/html",
      body: '<a href="http://localhost:5173/chat/search">Abrir NChat</a>',
    }),
  );
  await installMessagingMocks(
    page,
    createScenario({
      kind: "channel",
      targetId: "external-origin",
      targetName: "Origem",
      messages: [],
    }),
  );
  await page.goto("http://127.0.0.1:5173/controlled-origin");
  await page.getByRole("link", { name: "Abrir NChat" }).click();
  await expect(page.getByRole("searchbox")).toBeFocused();
  const entries = await page.evaluate(() => history.length);
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL("http://localhost:5173/chat/search");
  await expect(page.getByTestId("chat-sidebar")).toBeVisible();
  await expect(page.getByTestId("chat-nav-toggle")).toBeFocused();
  expect(await page.evaluate(() => history.length)).toBe(entries);
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("chat-sidebar")).toBeHidden();
  await expect(page.getByTestId("chat-nav-toggle")).toBeFocused();
  await page.getByRole("searchbox").focus();
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("chat-sidebar")).toBeVisible();
  await expect(page).toHaveURL("http://localhost:5173/chat/search");
  expect(await page.evaluate(() => history.length)).toBe(entries);
});

test("navigation preserves drafts, untouched unread, socket and an active signaling provider", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const scenario = createScenario({
    kind: "channel",
    targetId: "continuity",
    targetName: "Origem",
    messages: [makeMessage({ id: "continuity-message", body_text: "Mensagem de origem" })],
  });
  const unread = scenario.sidebarChannels.find(
    (channel) => channel.display_name === OTHER_CHANNEL_NAME,
  )!;
  unread.unread_count = 4;
  await installMessagingMocks(page, scenario);
  // No real media service: this verifies signaling/provider continuity only.
  await page.route("**/api/media/media/livekit/token", (route) =>
    route.fulfill({ status: 503, json: { error: { code: "media_unavailable" } } }),
  );
  await page.goto("/chat/channel/continuity");
  await expect(page.getByTestId("chat-msg-header")).toContainText("Origem");
  const lifecycle = () =>
    page.evaluate(() =>
      (
        window as unknown as {
          __e2eWebSocketLifecycle: () => { created: number; closed: number; active: number };
        }
      ).__e2eWebSocketLifecycle(),
    );
  const commands = () =>
    page.evaluate(
      () =>
        (
          window as unknown as {
            __e2eWebSocketMessages: () => Array<{ type: string }>;
          }
        )
          .__e2eWebSocketMessages()
          .filter((event) => event.type === "call.sync").length,
    );
  await expect.poll(commands).toBe(1);
  const socket = await lifecycle();
  await fillComposer(page, "Rascunho preservado na origem");
  const now = new Date().toISOString();
  await page.evaluate(
    ({ user, caller, now }) =>
      (
        window as unknown as {
          __e2eEmitWebSocketEvent: (event: unknown) => void;
        }
      ).__e2eEmitWebSocketEvent({
        type: "call.accepted",
        event_id: "continuity-call-1",
        target_type: "user",
        target_id: user,
        call: {
          call_id: "continuity-call",
          request_id: "continuity-request",
          caller_id: caller,
          callee_id: user,
          call_type: "audio",
          status: "active",
          version: 1,
          created_at: now,
          occurred_at: now,
          accepted_at: now,
          expires_at: new Date(Date.parse(now) + 300_000).toISOString(),
        },
      }),
    { user: CURRENT_USER_ID, caller: OTHER_USER_ID, now },
  );
  await expect(page.getByRole("button", { name: "Encerrar chamada", exact: true })).toBeVisible();
  await page.getByTestId("chat-nav-toggle").click();
  const untouched = page.getByRole("option", { name: new RegExp(OTHER_CHANNEL_NAME) });
  await expect(untouched.getByLabel("4 não lidas")).toBeVisible();
  await page.getByRole("option", { name: new RegExp(OTHER_USER_NAME) }).click();
  await expect(page).toHaveURL(/\/chat\/dm\//);
  await page.getByRole("button", { name: "Voltar", exact: true }).click();
  await expect(page).toHaveURL(/\/chat\/channel\/continuity$/);
  await expect(page.getByTestId("chat-composer-input")).toContainText(
    "Rascunho preservado na origem",
  );
  await expect(page.getByRole("button", { name: "Encerrar chamada", exact: true })).toBeVisible();
  expect(await lifecycle()).toEqual(socket);
  expect(await commands()).toBe(1);
  expect(scenario.requests.reads.filter((read) => read.targetId === unread.id)).toEqual([]);
  await page.getByTestId("chat-nav-toggle").click();
  await expect(untouched.getByLabel("4 não lidas")).toBeVisible();
});
