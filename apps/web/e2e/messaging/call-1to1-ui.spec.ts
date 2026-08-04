import { expect, test, type Locator, type Page, type TestInfo } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  createScenario,
  fillComposer,
  installMessagingMocks,
  uniqueId,
} from "../helpers/messagingApi";

type CallStatus = "ringing" | "active" | "ended";

interface CallFixture {
  callId: string;
  requestId: string;
  createdAt: string;
}

const browserErrors = new WeakMap<Page, string[]>();

test.describe.configure({ retries: 0 });

test.beforeEach(async ({ page }) => {
  const errors: string[] = [];
  browserErrors.set(page, errors);
  page.on("pageerror", (error) => errors.push(`pageerror: ${error.message}`));
  page.on("console", (message) => {
    if (message.type() !== "error") return;
    const expectedTokenFailure =
      message.text() ===
        "Failed to load resource: the server responded with a status of 503 (Service Unavailable)" &&
      message.location().url.includes("/api/media/media/livekit/token");
    const expectedSidebarFailure =
      message.text() ===
        "Failed to load resource: the server responded with a status of 503 (Service Unavailable)" &&
      message.location().url.includes("/api/chat/sidebar");
    if (!expectedTokenFailure && !expectedSidebarFailure) {
      errors.push(`console.error: ${message.text()}`);
    }
  });
});

test.afterEach(async ({ page }) => {
  expect(browserErrors.get(page)).toEqual([]);
});

test.describe("chamada 1:1", () => {
  test("mantém o chat utilizável quando call.sync não encontra chamada", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "call-sync-empty");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: "E2E Participante",
      messages: [],
    });
    await installMessagingMocks(page, scenario);

    await page.goto(`/chat/dm/${targetId}`);
    await expect.poll(() => commandCount(page, "call.sync")).toBe(1);

    await expect(page.getByRole("dialog", { name: /Chamada/ })).toHaveCount(0);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await fillComposer(page, "chat continua utilizável");
  });

  test("aguarda a identidade antes de oferecer ações da chamada recebida", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "call-delayed-sidebar");
    const participantName = "E2E Participante";
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: participantName,
      messages: [],
    });
    const sidebar = deferredValue<void>();
    await installMessagingMocks(page, scenario);
    await page.route("**/api/chat/sidebar", async (route) => {
      await sidebar.promise;
      await route.fallback();
    });

    await page.goto("/chat/dm/" + targetId);
    await expect.poll(() => commandCount(page, "call.sync")).toBe(1);
    const call = {
      callId: uniqueId(testInfo, "delayed-call"),
      requestId: uniqueId(testInfo, "delayed-request"),
      createdAt: "2026-08-03T12:00:00.000Z",
    } satisfies CallFixture;
    await emitCallEvent(page, callEvent(call, "ringing", 1));

    const unresolvedDialog = page.getByRole("dialog", {
      name: "Chamada de vídeo com Participante",
    });
    await expect(unresolvedDialog).toBeVisible();
    await expect(unresolvedDialog.getByRole("status")).toHaveText("Preparando chamada…");
    await expect(unresolvedDialog.getByRole("button", { name: "Atender" })).toHaveCount(0);
    await expect(unresolvedDialog.getByRole("button", { name: "Recusar" })).toHaveCount(0);
    await expect(unresolvedDialog.getByRole("button", { name: "Cancelar chamada" })).toHaveCount(0);

    sidebar.resolve();

    const dialog = page.getByRole("dialog", {
      name: "Chamada de vídeo com " + participantName,
    });
    const accept = dialog.getByRole("button", { name: "Atender" });
    await expect(accept).toBeFocused();
    await expect(dialog.getByRole("button", { name: "Recusar" })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Cancelar chamada" })).toHaveCount(0);
    expect(await commandCount(page, "call.sync")).toBe(1);
    await page.keyboard.press("Enter");
    await expectCommandCount(page, "call.accept", 1);
  });

  test("recupera a identidade dentro do dialog após falha da sidebar", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "call-sidebar-retry");
    const participantName = "E2E Participante";
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: participantName,
      messages: [],
    });
    const initialSidebar = deferredValue<void>();
    const retrySidebar = deferredValue<void>();
    let sidebarRequests = 0;
    await installMessagingMocks(page, scenario);
    await page.route("**/api/chat/sidebar", async (route) => {
      sidebarRequests += 1;
      if (sidebarRequests === 1) {
        await initialSidebar.promise;
        await route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "sidebar_unavailable" } }),
        });
        return;
      }
      await retrySidebar.promise;
      await route.fallback();
    });

    await page.goto(`/chat/dm/${targetId}`);
    await expect.poll(() => commandCount(page, "call.sync")).toBe(1);
    const call = {
      callId: uniqueId(testInfo, "sidebar-retry-call"),
      requestId: uniqueId(testInfo, "sidebar-retry-request"),
      createdAt: "2026-08-03T12:00:00.000Z",
    } satisfies CallFixture;
    await emitCallEvent(page, callEvent(call, "ringing", 1));
    initialSidebar.resolve();

    const unresolvedDialog = page.getByRole("dialog", {
      name: "Chamada de vídeo com Participante",
    });
    await expect(unresolvedDialog.getByRole("alert")).toContainText(
      "Não foi possível preparar a chamada",
    );
    const retry = unresolvedDialog.getByRole("button", { name: "Tentar novamente" });
    await expect(retry).toBeFocused();
    await page.keyboard.press("Enter");
    await expect.poll(() => sidebarRequests).toBe(2);
    await expect(retry).toBeDisabled();
    await page.keyboard.press("Enter");
    expect(sidebarRequests).toBe(2);

    retrySidebar.resolve();

    const dialog = page.getByRole("dialog", {
      name: `Chamada de vídeo com ${participantName}`,
    });
    const accept = dialog.getByRole("button", { name: "Atender" });
    await expect(accept).toBeFocused();
    await expect(dialog.getByRole("button", { name: "Recusar" })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Cancelar chamada" })).toHaveCount(0);
    expect(await commandCount(page, "call.sync")).toBe(1);
    await page.keyboard.press("Enter");
    await expectCommandCount(page, "call.accept", 1);
  });

  test("aguarda a identidade antes de iniciar mídia para chamada ativa", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "active-delayed-sidebar");
    const participantName = "E2E Participante";
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: participantName,
      messages: [],
    });
    const initialSidebar = deferredValue<void>();
    const retrySidebar = deferredValue<void>();
    let sidebarRequests = 0;
    let tokenRequests = 0;
    await installMessagingMocks(page, scenario);
    await page.route("**/api/chat/sidebar", async (route) => {
      sidebarRequests += 1;
      if (sidebarRequests === 1) {
        await initialSidebar.promise;
        await route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "sidebar_unavailable" } }),
        });
        return;
      }
      await retrySidebar.promise;
      await route.fallback();
    });
    await page.route("**/api/media/media/livekit/token", (route) => {
      tokenRequests += 1;
      return route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "media_unavailable" } }),
      });
    });

    await page.goto(`/chat/dm/${targetId}`);
    await expect.poll(() => commandCount(page, "call.sync")).toBe(1);
    const call = {
      callId: uniqueId(testInfo, "active-delayed-call"),
      requestId: uniqueId(testInfo, "active-delayed-request"),
      createdAt: "2026-08-03T12:00:00.000Z",
    } satisfies CallFixture;
    await emitCallEvent(page, callEvent(call, "active", 1));

    const unresolvedDialog = page.getByRole("dialog", {
      name: "Chamada de vídeo com Participante",
    });
    await expect(unresolvedDialog.getByRole("status")).toHaveText("Preparando chamada…");
    await expect(unresolvedDialog.getByRole("button", { name: "Ativar microfone" })).toHaveCount(0);
    await expect(unresolvedDialog.getByRole("button", { name: "Ativar câmera" })).toHaveCount(0);
    await expect(unresolvedDialog.getByRole("button", { name: "Encerrar chamada" })).toHaveCount(0);
    expect(tokenRequests).toBe(0);

    initialSidebar.resolve();
    const retry = unresolvedDialog.getByRole("button", { name: "Tentar novamente" });
    await expect(retry).toBeFocused();
    await page.keyboard.press("Enter");
    await expect.poll(() => sidebarRequests).toBe(2);
    await expect(retry).toBeDisabled();
    await page.keyboard.press("Enter");
    expect(sidebarRequests).toBe(2);
    expect(tokenRequests).toBe(0);

    retrySidebar.resolve();

    const dialog = page.getByRole("dialog", {
      name: `Chamada de vídeo com ${participantName}`,
    });
    await expect(dialog.getByRole("button", { name: "Encerrar chamada" })).toBeFocused();
    await expect(dialog.getByRole("button", { name: "Ativar microfone" })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Ativar câmera" })).toBeVisible();
    await expect.poll(() => tokenRequests).toBe(1);
    expect(await commandCount(page, "call.sync")).toBe(1);
  });

  test("integra dialog, foco, teclado, controles e encerramento no desktop", async ({
    page,
  }, testInfo) => {
    const participantName = "E2E Participante";
    const call = await openCallConversation(page, testInfo, participantName);

    await emitCallEvent(page, callEvent(call, "ringing", 1));

    const dialog = page.getByRole("dialog", {
      name: `Chamada de vídeo com ${participantName}`,
    });
    await expect(dialog).toBeVisible();
    await expect(dialog).toHaveAttribute("open", "");
    expect(
      await dialog.evaluate(
        (element) => (element as HTMLDialogElement).open && element.matches(":modal"),
      ),
    ).toBe(true);

    const accept = dialog.getByRole("button", { name: "Atender" });
    await expect(accept).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeVisible();
    expect(await dialog.evaluate((element) => element.contains(document.activeElement))).toBe(true);

    await page.keyboard.press("Enter");
    await expectCommandCount(page, "call.accept", 1);
    expect(await commandCount(page, "call.accept")).toBe(1);

    await emitCallEvent(page, callEvent(call, "active", 2));
    await expect(dialog.getByText(`A câmera de ${participantName} está desligada`)).toBeVisible();
    await expect(dialog.getByText("Sua câmera está desligada")).toBeVisible();
    await expect(dialog.locator("video")).toHaveCount(0);

    const microphone = dialog.getByRole("button", { name: "Ativar microfone" });
    const camera = dialog.getByRole("button", { name: "Ativar câmera" });
    const end = dialog.getByRole("button", { name: "Encerrar chamada" });
    await expect(microphone).toBeVisible();
    await expect(camera).toBeVisible();
    await expect(end).toBeVisible();

    await focusByTab(page, end);
    await page.keyboard.press("Enter");
    await expectCommandCount(page, "call.end", 1);

    await emitCallEvent(page, callEvent(call, "ended", 3));
    await expect(dialog.getByText("Chamada encerrada")).toBeVisible();
    const close = dialog.getByRole("button", { name: "Fechar" });
    await expect(close).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(dialog).toHaveCount(0);
  });

  test("mantém fallback e controles utilizáveis em 320 px", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 320, height: 640 });
    const participantName = "Participante corporativo com nome deliberadamente longo";
    const call = await openCallConversation(page, testInfo, participantName);

    await emitCallEvent(page, callEvent(call, "active", 1));

    const dialog = page.getByRole("dialog", {
      name: `Chamada de vídeo com ${participantName}`,
    });
    await expect(dialog).toBeVisible();
    await expectFillsViewport(dialog, page);
    await expectNoHorizontalScroll(page);

    const participant = dialog.getByText(participantName, { exact: true }).first();
    await expect(participant).toBeVisible();
    await expectInsideViewport(participant, page);
    expect(
      await participant.evaluate((element) => {
        const style = getComputedStyle(element);
        return (
          element.scrollWidth > element.clientWidth &&
          style.overflow === "hidden" &&
          style.textOverflow === "ellipsis" &&
          style.whiteSpace === "nowrap"
        );
      }),
    ).toBe(true);
    await expect(dialog.getByText("Sua câmera está desligada")).toBeAttached();

    const controls = dialog.getByRole("contentinfo", { name: "Controles da chamada" });
    const preview = dialog.getByRole("complementary", { name: "Sua pré-visualização" });
    const microphone = dialog.getByRole("button", { name: "Ativar microfone" });
    const camera = dialog.getByRole("button", { name: "Ativar câmera" });
    const end = dialog.getByRole("button", { name: "Encerrar chamada" });
    await expect(controls).toBeVisible();
    await expect(preview).toBeVisible();
    for (const control of [microphone, camera, end]) {
      await expect(control).toBeVisible();
      await expectInsideViewport(control, page);
    }
    await expectNoOverlap(preview, controls);

    await focusByTab(page, end);
    await page.keyboard.press("Enter");
    await expectCommandCount(page, "call.end", 1);
  });

  test("mantém palco, preview e ações acessíveis em landscape", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 640, height: 360 });
    const participantName = "E2E Participante";
    const call = await openCallConversation(page, testInfo, participantName);

    await emitCallEvent(page, callEvent(call, "active", 1));

    const dialog = page.getByRole("dialog", {
      name: `Chamada de vídeo com ${participantName}`,
    });
    const header = dialog.locator("header");
    const stage = dialog.locator("main");
    const controls = dialog.getByRole("contentinfo", { name: "Controles da chamada" });
    const preview = dialog.getByRole("complementary", { name: "Sua pré-visualização" });
    const microphone = dialog.getByRole("button", { name: "Ativar microfone" });
    const camera = dialog.getByRole("button", { name: "Ativar câmera" });
    const end = dialog.getByRole("button", { name: "Encerrar chamada" });

    await expect(dialog).toBeVisible();
    await expectNoHorizontalScroll(page);
    await expectInsideViewport(header, page);
    await expectInsideViewport(stage, page);
    await expectInsideViewport(controls, page);
    for (const control of [microphone, camera, end]) {
      await expect(control).toBeVisible();
      await expectInsideViewport(control, page);
    }
    await expectNoOverlap(preview, end);
    expect(await end.evaluate((element) => getComputedStyle(element).flexDirection)).toBe("row");

    await focusByTab(page, end);
    await page.keyboard.press("Enter");
    await expectCommandCount(page, "call.end", 1);
  });
});

async function openCallConversation(page: Page, testInfo: TestInfo, participantName: string) {
  const targetId = uniqueId(testInfo, "call-dm");
  const callId = uniqueId(testInfo, "call");
  const scenario = createScenario({
    kind: "dm",
    targetId,
    targetName: participantName,
    messages: [],
  });
  await installMessagingMocks(page, scenario);
  await page.route("**/api/media/media/livekit/token", (route) =>
    route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "media_unavailable" } }),
    }),
  );
  await page.goto(`/chat/dm/${targetId}`);
  await expect(page.getByTestId("chat-shell")).toBeVisible();
  await expect.poll(() => commandCount(page, "call.sync")).toBe(1);
  return {
    callId,
    requestId: uniqueId(testInfo, "request"),
    createdAt: "2026-08-03T12:00:00.000Z",
  } satisfies CallFixture;
}

function deferredValue<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function callEvent(call: CallFixture, status: CallStatus, version: number) {
  const eventType =
    status === "ringing" ? "call.ringing" : status === "active" ? "call.accepted" : "call.ended";
  return {
    type: eventType,
    event_id: `${call.callId}-${version}`,
    target_type: "user",
    target_id: CURRENT_USER_ID,
    call: {
      call_id: call.callId,
      request_id: call.requestId,
      caller_id: OTHER_USER_ID,
      callee_id: CURRENT_USER_ID,
      call_type: "video",
      status,
      version,
      created_at: call.createdAt,
      occurred_at: call.createdAt,
      expires_at: "2026-08-03T12:05:00.000Z",
      ...(status === "active" ? { accepted_at: call.createdAt } : {}),
      ...(status === "ended" ? { ended_at: call.createdAt } : {}),
    },
  };
}

async function emitCallEvent(page: Page, event: ReturnType<typeof callEvent>) {
  await page.waitForFunction(
    () =>
      typeof (window as unknown as { __e2eEmitWebSocketEvent?: (value: unknown) => void })
        .__e2eEmitWebSocketEvent === "function",
  );
  await page.evaluate((value) => {
    (
      window as unknown as { __e2eEmitWebSocketEvent: (event: typeof value) => void }
    ).__e2eEmitWebSocketEvent(value);
  }, event);
}

async function commandCount(page: Page, type: string) {
  return page.evaluate((commandType) => {
    const messages = (
      window as unknown as {
        __e2eWebSocketMessages?: () => Array<Record<string, unknown>>;
      }
    ).__e2eWebSocketMessages?.();
    return messages?.filter((message) => message["type"] === commandType).length ?? 0;
  }, type);
}

async function expectCommandCount(page: Page, type: string, count: number) {
  await expect.poll(() => commandCount(page, type)).toBe(count);
}

async function focusByTab(page: Page, target: Locator) {
  for (let stop = 0; stop < 12; stop += 1) {
    if (await target.evaluate((element) => element === document.activeElement)) return;
    await page.keyboard.press("Tab");
  }
  await expect(target).toBeFocused();
}

async function expectFillsViewport(locator: Locator, page: Page) {
  const box = await locator.boundingBox();
  const viewport = page.viewportSize();
  expect(box).not.toBeNull();
  expect(viewport).not.toBeNull();
  expect(box!.x).toBeCloseTo(0, 0);
  expect(box!.y).toBeCloseTo(0, 0);
  expect(box!.width).toBeCloseTo(viewport!.width, 0);
  expect(box!.height).toBeCloseTo(viewport!.height, 0);
}

async function expectInsideViewport(locator: Locator, page: Page) {
  const box = await locator.boundingBox();
  const viewport = page.viewportSize();
  expect(box).not.toBeNull();
  expect(viewport).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(viewport!.width);
  expect(box!.y + box!.height).toBeLessThanOrEqual(viewport!.height);
}

async function expectNoHorizontalScroll(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
}

async function expectNoOverlap(first: Locator, second: Locator) {
  const firstBox = await first.boundingBox();
  const secondBox = await second.boundingBox();
  expect(firstBox).not.toBeNull();
  expect(secondBox).not.toBeNull();
  const overlapWidth = Math.max(
    0,
    Math.min(firstBox!.x + firstBox!.width, secondBox!.x + secondBox!.width) -
      Math.max(firstBox!.x, secondBox!.x),
  );
  const overlapHeight = Math.max(
    0,
    Math.min(firstBox!.y + firstBox!.height, secondBox!.y + secondBox!.height) -
      Math.max(firstBox!.y, secondBox!.y),
  );
  expect(overlapWidth * overlapHeight).toBe(0);
}
