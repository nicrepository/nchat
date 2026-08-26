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
  test("toca apenas para incoming direto e para ao recusar", async ({ page }, testInfo) => {
    await instrumentIncomingCallRingtone(page);
    const call = await openCallConversation(page, testInfo, "E2E Participante");

    await emitCallEvent(page, callEvent(call, "ringing", 1));
    await expect.poll(() => incomingCallRingtoneCounts(page)).toEqual({ play: 1, pause: 0 });

    await page.getByRole("button", { name: "Recusar" }).click();
    await expectCommandCount(page, "call.decline", 1);
    await emitCallEvent(page, callEvent(call, "ended", 2));
    await expect.poll(() => incomingCallRingtoneCounts(page)).toEqual({ play: 1, pause: 1 });

    const outgoing = {
      callId: uniqueId(testInfo, "outgoing-call"),
      requestId: uniqueId(testInfo, "outgoing-request"),
      createdAt: call.createdAt,
    } satisfies CallFixture;
    const outgoingEvent = callEvent(outgoing, "ringing", 1);
    outgoingEvent.call.caller_id = CURRENT_USER_ID;
    outgoingEvent.call.callee_id = OTHER_USER_ID;
    await emitCallEvent(page, outgoingEvent);
    await waitForReactEffects(page);
    expect(await incomingCallRingtoneCounts(page)).toEqual({ play: 1, pause: 1 });

    const resourceEvent = callEvent(
      {
        callId: uniqueId(testInfo, "resource-call"),
        requestId: uniqueId(testInfo, "resource-request"),
        createdAt: call.createdAt,
      },
      "ringing",
      1,
    );
    resourceEvent.target_type = "channel";
    await emitCallEvent(page, resourceEvent);
    await waitForReactEffects(page);
    expect(await incomingCallRingtoneCounts(page)).toEqual({ play: 1, pause: 1 });
  });

  test("mantém uma única apresentação sonora entre duas abas main", async ({
    page,
    context,
  }, testInfo) => {
    const secondPage = await context.newPage();
    const targetId = uniqueId(testInfo, "two-main-tabs");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: "E2E Participante",
      messages: [],
    });
    await Promise.all([
      installMessagingMocks(page, scenario),
      installMessagingMocks(secondPage, scenario),
      instrumentIncomingCallRingtone(page),
      instrumentIncomingCallRingtone(secondPage),
    ]);
    await Promise.all([page.goto(`/chat/dm/${targetId}`), secondPage.goto(`/chat/dm/${targetId}`)]);
    await Promise.all([
      expect.poll(() => commandCount(page, "call.sync")).toBe(1),
      expect.poll(() => commandCount(secondPage, "call.sync")).toBe(1),
    ]);
    const call = {
      callId: uniqueId(testInfo, "two-main-tabs-call"),
      requestId: uniqueId(testInfo, "two-main-tabs-request"),
      createdAt: "2026-08-03T12:00:00.000Z",
    } satisfies CallFixture;

    await Promise.all([
      emitCallEvent(page, callEvent(call, "ringing", 1)),
      emitCallEvent(secondPage, callEvent(call, "ringing", 1)),
    ]);

    await Promise.all([
      expect(page.getByRole("dialog", { name: "Chamada recebida" })).toBeVisible(),
      expect(secondPage.getByRole("dialog", { name: "Chamada recebida" })).toBeVisible(),
    ]);
    await expect
      .poll(async () => {
        const [first, second] = await Promise.all([
          incomingCallRingtoneCounts(page),
          incomingCallRingtoneCounts(secondPage),
        ]);
        return first.play + second.play;
      })
      .toBe(1);
  });

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

    const unresolvedDialog = page.getByRole("dialog", { name: "Chamada recebida" });
    await expect(unresolvedDialog).toBeVisible();
    await expect(unresolvedDialog.getByRole("status")).toHaveText("Preparando chamada…");
    await expect(unresolvedDialog.getByRole("button", { name: "Atender" })).toHaveCount(0);
    await expect(unresolvedDialog.getByRole("button", { name: "Recusar" })).toHaveCount(0);
    await expect(unresolvedDialog.getByRole("button", { name: "Cancelar chamada" })).toHaveCount(0);

    sidebar.resolve();

    const dialog = page.getByRole("dialog", { name: "Chamada recebida" });
    await expect(dialog.getByText(participantName, { exact: true })).toBeVisible();
    const accept = dialog.getByRole("button", { name: "Atender com câmera" });
    await expect(dialog.getByRole("button", { name: "Recusar" })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Cancelar chamada" })).toHaveCount(0);
    expect(await commandCount(page, "call.sync")).toBe(1);
    await accept.click();
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

    const unresolvedDialog = page.getByRole("dialog", { name: "Chamada recebida" });
    await expect(unresolvedDialog.getByRole("alert")).toContainText(
      "Não foi possível preparar a chamada",
    );
    const retry = unresolvedDialog.getByRole("button", { name: "Tentar novamente" });
    await retry.click();
    await expect.poll(() => sidebarRequests).toBe(2);
    await expect(retry).toHaveCount(0);
    expect(sidebarRequests).toBe(2);

    retrySidebar.resolve();

    const dialog = page.getByRole("dialog", { name: "Chamada recebida" });
    await expect(dialog.getByText(participantName, { exact: true })).toBeVisible();
    const accept = dialog.getByRole("button", { name: "Atender com câmera" });
    await expect(dialog.getByRole("button", { name: "Recusar" })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Cancelar chamada" })).toHaveCount(0);
    expect(await commandCount(page, "call.sync")).toBe(1);
    await accept.click();
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

    const unresolvedDialog = page.getByLabel("Chamada com Participante");
    await expect(unresolvedDialog.getByText("Preparando chamada…")).toBeVisible();
    await expect(unresolvedDialog.getByRole("button", { name: "Ativar microfone" })).toBeVisible();
    await expect(unresolvedDialog.getByRole("button", { name: "Ativar câmera" })).toBeVisible();
    await expect(unresolvedDialog.getByRole("button", { name: "Encerrar chamada" })).toBeVisible();
    expect(tokenRequests).toBe(0);

    initialSidebar.resolve();
    const retry = unresolvedDialog.getByRole("button", { name: "Tentar novamente" });
    await expect(retry).toBeVisible();
    expect(sidebarRequests).toBe(1);
    await retry.click();
    await expect.poll(() => sidebarRequests).toBe(2);
    await expect(retry).toHaveCount(0);
    expect(sidebarRequests).toBe(2);
    expect(tokenRequests).toBe(0);

    retrySidebar.resolve();

    const dialog = page.getByLabel(`Chamada com ${participantName}`);
    await expect(dialog.getByRole("button", { name: "Encerrar chamada" })).toBeVisible();
    expect(sidebarRequests).toBe(2);
    // RF-23: an active call restored this way (never locally started or
    // accepted by this tab) must not request media on its own, however long
    // identity took to resolve — only the explicit activation click may.
    expect(tokenRequests).toBe(0);
    const activate = dialog.getByRole("button", { name: "Permitir câmera e microfone" });
    await expect(activate).toBeVisible();

    await activate.click();

    await expect.poll(() => tokenRequests).toBe(1);
    expect(await commandCount(page, "call.sync")).toBe(1);
  });

  test("integra dialog, foco, teclado, controles e encerramento no desktop", async ({
    page,
  }, testInfo) => {
    const participantName = "E2E Participante";
    const call = await openCallConversation(page, testInfo, participantName);

    await emitCallEvent(page, callEvent(call, "ringing", 1));

    const dialog = page.getByRole("dialog", { name: "Chamada recebida" });
    await expect(dialog).toBeVisible();
    await expect(dialog).toHaveAttribute("aria-modal", "false");

    const accept = dialog.getByRole("button", { name: "Atender com câmera" });
    await expect(accept).not.toBeFocused();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeVisible();

    await accept.click();
    await expectCommandCount(page, "call.accept", 1);
    expect(await commandCount(page, "call.accept")).toBe(1);

    await emitCallEvent(page, callEvent(call, "active", 2));
    const floating = page.getByTestId("floating-call-window");
    await expect(floating).toBeVisible();
    await expect(floating.getByText(participantName, { exact: true })).toBeVisible();

    const handle = floating.getByTestId("floating-call-handle");
    const beforeDrag = await floating.boundingBox();
    expect(beforeDrag).not.toBeNull();
    await handle.hover();
    await page.mouse.down();
    await page.mouse.move(beforeDrag!.x - 80, beforeDrag!.y - 60, { steps: 4 });
    await page.mouse.up();
    const afterDrag = await floating.boundingBox();
    expect(afterDrag).not.toBeNull();
    expect({ x: afterDrag!.x, y: afterDrag!.y }).not.toEqual({
      x: beforeDrag!.x,
      y: beforeDrag!.y,
    });

    await page.getByRole("link", { name: /^Meu perfil/ }).click();
    await expect(page).toHaveURL(/\/profile$/);
    await expect(floating).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(/\/chat\/dm\//);
    await expect(floating).toBeVisible();

    const microphone = floating.getByRole("button", { name: "Ativar microfone" });
    const camera = floating.getByRole("button", { name: "Ativar câmera" });
    const end = floating.getByRole("button", { name: "Encerrar chamada" });
    await expect(microphone).toBeVisible();
    await expect(camera).toBeVisible();
    await expect(end).toBeVisible();

    await end.focus();
    await expect(end).toBeFocused();
    await page.keyboard.press("Enter");
    await expectCommandCount(page, "call.end", 1);

    await emitCallEvent(page, callEvent(call, "ended", 3));
    await expect(floating).toHaveCount(0);
  });

  test("mantém fallback e controles utilizáveis em 320 px", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 320, height: 640 });
    const participantName = "Participante corporativo com nome deliberadamente longo";
    const call = await openCallConversation(page, testInfo, participantName);

    await emitCallEvent(page, callEvent(call, "active", 1));

    const dialog = page.getByTestId("floating-call-window");
    await expect(dialog).toBeVisible();
    await expectNoHorizontalScroll(page);

    const participant = dialog.getByText(participantName, { exact: true }).first();
    await expect(participant).toBeVisible();
    await expectInsideViewport(participant, page);
    const microphone = dialog.getByRole("button", { name: "Ativar microfone" });
    const camera = dialog.getByRole("button", { name: "Ativar câmera" });
    const end = dialog.getByRole("button", { name: "Encerrar chamada" });
    for (const control of [microphone, camera, end]) {
      await expect(control).toBeVisible();
      await expectInsideViewport(control, page);
    }

    await end.focus();
    await expect(end).toBeFocused();
    await page.keyboard.press("Enter");
    await expectCommandCount(page, "call.end", 1);
  });

  test("mantém palco, preview e ações acessíveis em landscape", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 640, height: 360 });
    const participantName = "E2E Participante";
    const call = await openCallConversation(page, testInfo, participantName);

    await emitCallEvent(page, callEvent(call, "active", 1));

    const dialog = page.getByTestId("floating-call-window");
    const header = dialog.locator("header");
    const stage = dialog.locator(".floating-call__stage");
    const microphone = dialog.getByRole("button", { name: "Ativar microfone" });
    const camera = dialog.getByRole("button", { name: "Ativar câmera" });
    const end = dialog.getByRole("button", { name: "Encerrar chamada" });

    await expect(dialog).toBeVisible();
    await expectNoHorizontalScroll(page);
    await expectInsideViewport(header, page);
    await expectInsideViewport(stage, page);
    for (const control of [microphone, camera, end]) {
      await expect(control).toBeVisible();
      await expectInsideViewport(control, page);
    }

    await end.press("Enter");
    await expectCommandCount(page, "call.end", 1);
  });

  // ── issue #673: icon call controls + persistent direct call bar ──────────

  test("cabeçalho da DM não exibe mais texto 'Áudio'/'Vídeo' — apenas icon buttons acessíveis", async ({
    page,
  }, testInfo) => {
    await openCallConversation(page, testInfo, "E2E Participante");

    await expect(page.getByRole("button", { name: "Iniciar chamada de áudio" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Iniciar chamada de vídeo" })).toBeVisible();
    await expect(page.getByText("Áudio", { exact: true })).toHaveCount(0);
    await expect(page.getByText("Vídeo", { exact: true })).toHaveCount(0);
  });

  test("nunca exibe a barra persistente enquanto a chamada está apenas ringing — IncomingCallPopup segue sendo a única superfície", async ({
    page,
  }, testInfo) => {
    const call = await openCallConversation(page, testInfo, "E2E Participante");

    await emitCallEvent(page, callEvent(call, "ringing", 1));

    await expect(page.getByRole("dialog", { name: "Chamada recebida" })).toBeVisible();
    await expect(page.getByTestId("active-direct-call-bar")).toHaveCount(0);
  });

  test("mantém apenas a superfície de ativação — nunca a barra persistente — antes da mídia conectar, e limpa tudo ao encerrar", async ({
    page,
  }, testInfo) => {
    const participantName = "E2E Participante";
    const call = await openCallConversation(page, testInfo, participantName);

    // Pushed directly as "active" (never locally accepted in this tab) —
    // the exact shape of a call restored by reload/reconnect/call.sync, same
    // as the RF-23 activation tests above.
    await emitCallEvent(page, callEvent(call, "active", 1));

    const floating = page.getByLabel(`Chamada com ${participantName}`);
    await expect(floating).toBeVisible();
    await expect(
      floating.getByRole("button", { name: "Permitir câmera e microfone" }),
    ).toBeVisible();
    // directPresentationCall (issue #673) requires a genuinely connected
    // local media session — this E2E project has no real LiveKit/media-
    // service to reach that state deterministically (see
    // call-floating-handoff.spec.ts's own file header for the same
    // constraint). This asserts the always-true half of the invariant that
    // IS deterministic here: an active-but-not-yet-connected call must never
    // show the persistent bar, only ringing→active is not enough on its own.
    await expect(page.getByTestId("active-direct-call-bar")).toHaveCount(0);

    await floating.getByRole("button", { name: "Encerrar chamada" }).click();
    await expectCommandCount(page, "call.end", 1);

    await emitCallEvent(page, callEvent(call, "ended", 2));

    await expect(floating).toHaveCount(0);
    await expect(page.getByTestId("active-direct-call-bar")).toHaveCount(0);
    // Cleanup: the header's own call actions return once the call is gone —
    // never left disabled/stale from the ended call.
    await expect(page.getByRole("button", { name: "Iniciar chamada de áudio" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Iniciar chamada de vídeo" })).toBeVisible();
  });
});

test.describe("permissões de mídia (RF-23)", () => {
  test("chamada de áudio solicita apenas o microfone e envia call.start após a permissão", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "perm-audio");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: "E2E Participante",
      messages: [],
    });
    await installMessagingMocks(page, scenario);
    await instrumentGetUserMedia(page, false);
    await stubMediaToken(page);

    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-shell")).toBeVisible();
    await expect.poll(() => commandCount(page, "call.sync")).toBe(1);

    await page.getByRole("button", { name: "Iniciar chamada de áudio" }).click();

    await expectCommandCount(page, "call.start", 1);
    expect(await getUserMediaCalls(page)).toEqual([{ audio: true, video: false }]);
  });

  test("chamada de vídeo solicita câmera e microfone antes de enviar call.start", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "perm-video");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: "E2E Participante",
      messages: [],
    });
    await installMessagingMocks(page, scenario);
    await instrumentGetUserMedia(page, false);
    await stubMediaToken(page);

    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-shell")).toBeVisible();
    await expect.poll(() => commandCount(page, "call.sync")).toBe(1);

    await page.getByRole("button", { name: "Iniciar chamada de vídeo" }).click();

    await expectCommandCount(page, "call.start", 1);
    expect(await getUserMediaCalls(page)).toEqual([{ audio: true, video: true }]);
  });

  test("permissão negada não cria a chamada e permite tentar novamente após liberar", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "perm-denied");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: "E2E Participante",
      messages: [],
    });
    await installMessagingMocks(page, scenario);
    await instrumentGetUserMedia(page, true);
    await stubMediaToken(page);

    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-shell")).toBeVisible();
    await expect.poll(() => commandCount(page, "call.sync")).toBe(1);

    const audioButton = page.getByRole("button", { name: "Iniciar chamada de áudio" });
    await audioButton.click();

    await expect(page.getByRole("alert")).toBeVisible();
    expect(await commandCount(page, "call.start")).toBe(0);
    await expect(page.getByRole("dialog", { name: /Chamada/ })).toHaveCount(0);
    await expect(audioButton).toBeEnabled();

    await allowGetUserMedia(page);
    await audioButton.click();

    await expectCommandCount(page, "call.start", 1);
  });

  test("chamada de áudio restaurada exige ativação explícita e solicita apenas o microfone", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "perm-restored-audio");
    const participantName = "E2E Participante";
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: participantName,
      messages: [],
    });
    await installMessagingMocks(page, scenario);
    await instrumentGetUserMedia(page, false);
    await stubMediaToken(page);

    await page.goto(`/chat/dm/${targetId}`);
    await expect.poll(() => commandCount(page, "call.sync")).toBe(1);
    const call = {
      callId: uniqueId(testInfo, "restored-audio-call"),
      requestId: uniqueId(testInfo, "restored-audio-request"),
      createdAt: "2026-08-03T12:00:00.000Z",
    } satisfies CallFixture;
    // Pushed directly as "active" (no local accept in this tab): the exact
    // shape of a call restored by reload, reconnect, or call.sync.
    await emitCallEvent(page, callEvent(call, "active", 1, "audio"));

    const dialog = page.getByLabel(`Chamada com ${participantName}`);
    await expect(dialog).toBeVisible();
    const activate = dialog.getByRole("button", { name: "Permitir microfone" });
    await expect(activate).toBeVisible();
    expect(await getUserMediaCalls(page)).toEqual([]);

    await activate.click();

    await expect.poll(() => getUserMediaCalls(page)).toEqual([{ audio: true, video: false }]);
  });

  test("nega a ativação de uma chamada restaurada e permite tentar novamente", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "perm-restored-denied");
    const participantName = "E2E Participante";
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: participantName,
      messages: [],
    });
    await installMessagingMocks(page, scenario);
    await instrumentGetUserMedia(page, true);
    await stubMediaToken(page);

    await page.goto(`/chat/dm/${targetId}`);
    await expect.poll(() => commandCount(page, "call.sync")).toBe(1);
    const call = {
      callId: uniqueId(testInfo, "restored-denied-call"),
      requestId: uniqueId(testInfo, "restored-denied-request"),
      createdAt: "2026-08-03T12:00:00.000Z",
    } satisfies CallFixture;
    await emitCallEvent(page, callEvent(call, "active", 1));

    const dialog = page.getByLabel(`Chamada com ${participantName}`);
    const activate = dialog.getByRole("button", { name: "Permitir câmera e microfone" });
    await activate.click();

    await expect(dialog.getByRole("alert")).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Encerrar chamada" })).toBeVisible();

    await allowGetUserMedia(page);
    await dialog.getByRole("button", { name: "Permitir câmera e microfone" }).click();

    await expect(dialog.getByRole("alert")).toHaveCount(0);
    await expect
      .poll(() => getUserMediaCalls(page))
      .toEqual([
        { audio: true, video: true },
        { audio: true, video: true },
      ]);
  });

  test("mantém Recusar habilitado durante o preflight de accept e nunca envia call.accept após a recusa", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "perm-decline-during-accept");
    const participantName = "E2E Participante";
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: participantName,
      messages: [],
    });
    await installMessagingMocks(page, scenario);
    await instrumentGetUserMediaHold(page);
    await stubMediaToken(page);

    await page.goto(`/chat/dm/${targetId}`);
    await expect.poll(() => commandCount(page, "call.sync")).toBe(1);
    const call = {
      callId: uniqueId(testInfo, "decline-during-accept-call"),
      requestId: uniqueId(testInfo, "decline-during-accept-request"),
      createdAt: "2026-08-03T12:00:00.000Z",
    } satisfies CallFixture;
    await emitCallEvent(page, callEvent(call, "ringing", 1));

    const dialog = page.getByRole("dialog", { name: "Chamada recebida" });
    const accept = dialog.getByRole("button", { name: "Atender com câmera" });
    const decline = dialog.getByRole("button", { name: "Recusar" });
    await accept.click();
    // Wait for accept()'s own getUserMedia preflight to actually be in
    // flight (held open, simulating the native prompt) before asserting.
    await expect.poll(() => getUserMediaCalls(page)).toEqual([{ audio: true, video: true }]);
    await expect(decline).toBeEnabled();

    await decline.click();

    await expectCommandCount(page, "call.decline", 1);
    expect(await commandCount(page, "call.accept")).toBe(0);

    // The held getUserMedia call resolves late, well after decline: it must
    // never let a stale accept preflight send call.accept or connect media.
    await resolveHeldGetUserMedia(page);
    await page.waitForFunction(
      () =>
        (window as unknown as { __e2eGetUserMediaSettled?: boolean }).__e2eGetUserMediaSettled ===
        true,
    );
    expect(await commandCount(page, "call.accept")).toBe(0);
  });
});

async function instrumentGetUserMediaHold(page: Page) {
  // Holds every getUserMedia() call open until the test explicitly resolves
  // it (RF-23: simulates the native camera/microphone prompt staying open
  // indefinitely while the user clicks Recusar).
  await page.addInitScript(() => {
    const target = window as unknown as {
      __e2eGetUserMediaCalls: MediaStreamConstraints[];
      __e2eResolveGetUserMedia?: () => void;
      __e2eGetUserMediaSettled?: boolean;
    };
    target.__e2eGetUserMediaCalls = [];
    target.__e2eGetUserMediaSettled = false;
    const original = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
    navigator.mediaDevices.getUserMedia = (constraints?: MediaStreamConstraints) => {
      target.__e2eGetUserMediaCalls.push(constraints ?? {});
      return new Promise<MediaStream>((resolve, reject) => {
        target.__e2eResolveGetUserMedia = () => {
          original(constraints).then(
            (stream) => {
              target.__e2eGetUserMediaSettled = true;
              resolve(stream);
            },
            (error) => {
              target.__e2eGetUserMediaSettled = true;
              reject(error);
            },
          );
        };
      });
    };
  });
}

async function instrumentIncomingCallRingtone(page: Page) {
  await page.addInitScript(() => {
    const target = window as unknown as {
      __e2eIncomingCallRingtone: { play: number; pause: number };
    };
    target.__e2eIncomingCallRingtone = { play: 0, pause: 0 };
    const originalPlay = HTMLMediaElement.prototype.play;
    const originalPause = HTMLMediaElement.prototype.pause;
    HTMLMediaElement.prototype.play = function () {
      if (this.src.endsWith("/sounds/incoming-call.wav")) {
        target.__e2eIncomingCallRingtone.play += 1;
        return Promise.resolve();
      }
      return originalPlay.call(this);
    };
    HTMLMediaElement.prototype.pause = function () {
      if (this.src.endsWith("/sounds/incoming-call.wav")) {
        target.__e2eIncomingCallRingtone.pause += 1;
        return;
      }
      return originalPause.call(this);
    };
  });
}

async function incomingCallRingtoneCounts(page: Page) {
  return page.evaluate(
    () =>
      (window as unknown as { __e2eIncomingCallRingtone: { play: number; pause: number } })
        .__e2eIncomingCallRingtone,
  );
}

async function waitForReactEffects(page: Page) {
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
}

async function resolveHeldGetUserMedia(page: Page) {
  await page.evaluate(() => {
    (window as unknown as { __e2eResolveGetUserMedia?: () => void }).__e2eResolveGetUserMedia?.();
  });
}

async function instrumentGetUserMedia(page: Page, initialDeny: boolean) {
  await page.addInitScript((deny) => {
    const target = window as unknown as {
      __e2eGetUserMediaCalls: MediaStreamConstraints[];
      __e2eDenyGetUserMedia: boolean;
    };
    target.__e2eGetUserMediaCalls = [];
    target.__e2eDenyGetUserMedia = deny;
    const original = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
    navigator.mediaDevices.getUserMedia = (constraints?: MediaStreamConstraints) => {
      target.__e2eGetUserMediaCalls.push(constraints ?? {});
      if (target.__e2eDenyGetUserMedia) {
        return Promise.reject(new DOMException("denied by e2e", "NotAllowedError"));
      }
      return original(constraints);
    };
  }, initialDeny);
}

async function getUserMediaCalls(page: Page): Promise<MediaStreamConstraints[]> {
  return page.evaluate(
    () =>
      (window as unknown as { __e2eGetUserMediaCalls?: MediaStreamConstraints[] })
        .__e2eGetUserMediaCalls ?? [],
  );
}

async function allowGetUserMedia(page: Page) {
  await page.evaluate(() => {
    (window as unknown as { __e2eDenyGetUserMedia: boolean }).__e2eDenyGetUserMedia = false;
  });
}

async function stubMediaToken(page: Page) {
  await page.route("**/api/media/media/livekit/token", (route) =>
    route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "media_unavailable" } }),
    }),
  );
}

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
  await stubMediaToken(page);
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

function callEvent(
  call: CallFixture,
  status: CallStatus,
  version: number,
  callType: "audio" | "video" = "video",
) {
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
      call_type: callType,
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
