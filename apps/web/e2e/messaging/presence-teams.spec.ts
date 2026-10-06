import { expect, test, type Page, type TestInfo } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  dropWebSocket,
  emitMessageCreated,
  emitPresence,
  installMessagingMocks,
  makeMessage,
  uniqueId,
  type MessagingScenario,
  type PresenceFixture,
} from "../helpers/messagingApi";
import {
  createPresenceSettingsServer,
  installPresenceSettingsServer,
  leaveTab,
  returnToTab,
  sentFrames,
  type PresenceSettingsServer,
} from "../helpers/presenceSettingsMock";

/**
 * Issue #798 — presence modelled on Teams, from the browser's side.
 *
 * The socket and the REST API are the existing mocks, so what these prove is
 * the client: that leaving a tab sends nothing that could mean "away", that real
 * activity is reported and throttled, that every server state reaches the
 * screen in words, and that the status menu talks to the server the way the
 * contract says — by keyboard, by mouse and by touch.
 *
 * What they deliberately do not prove is the server's aggregation: that an idle
 * tab loses to an active one, that a dropped socket waits out its grace, that
 * the last expired session is offline, that a call prevents away, that a
 * manual state expires on the server's clock. Those are decisions chat-service
 * makes from its own connection table and database, and asserting them through
 * a mocked socket would only assert the mock. They are covered where they live:
 * internal/ws/presence_grace_test.go, presence_compose_test.go,
 * internal/domain/presence_test.go and the PostgreSQL suites.
 */

const T0 = "2026-10-01T10:00:00.000Z";
const T1 = "2026-10-01T10:05:00.000Z";
const T2 = "2026-10-01T10:10:00.000Z";

async function openDM(page: Page, testInfo: TestInfo, settings?: PresenceSettingsServer) {
  const targetId = uniqueId(testInfo, "dm");
  const scenario = createScenario({
    kind: "dm",
    targetId,
    targetName: OTHER_USER_NAME,
    messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
  });
  await installMessagingMocks(page, scenario);
  if (settings) await installPresenceSettingsServer(page, settings);
  await page.goto(`/chat/dm/${targetId}`);
  await expect(page.getByTestId("chat-composer-input")).toBeVisible();
  return { scenario, targetId };
}

/**
 * Resolves when the page has its answer to a status write — after the server
 * has also told every session, as the real one does before answering.
 */
function writeAnswered(page: Page) {
  return page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/chat/presence/me") && response.request().method() === "PUT",
  );
}

function statusTrigger(page: Page) {
  return page.getByRole("button", { name: /alterar status/i });
}

function header(page: Page) {
  return page.getByTestId("chat-msg-header-presence");
}

function dmRow(page: Page) {
  return page.getByRole("option", { name: new RegExp(`Mensagem direta com ${OTHER_USER_NAME}`) });
}

async function pings(page: Page): Promise<number> {
  return (await sentFrames(page)).filter((frame) => frame["type"] === "ping").length;
}

async function announceSelf(page: Page, targetId: string, presence: Partial<PresenceFixture>) {
  await emitPresence(page, {
    kind: "dm",
    targetId,
    user: { user_id: CURRENT_USER_ID, state: "online", updated_at: T0, ...presence },
  });
}

test.describe("presença estilo Teams (#798)", () => {
  test("trocar de aba não envia nada e não torna ninguém ausente", async ({ page }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);
    await announceSelf(page, targetId, { availability: "available" });
    await expect(statusTrigger(page)).toHaveAccessibleName("Status: Disponível. Alterar status");
    const before = await pings(page);

    await leaveTab(page);
    // Leaving is not activity, and it is not a statement about anything: no
    // frame at all, so the only way to away is the server's idle timeout.
    expect(await pings(page)).toBe(before);
    await expect(statusTrigger(page)).toHaveAccessibleName("Status: Disponível. Alterar status");

    // Coming back is a person returning, and is reported.
    await returnToTab(page);
    await expect.poll(() => pings(page)).toBe(before + 1);
  });

  test("ociosidade decidida pelo servidor vira Ausente, e atividade volta a Disponível", async ({
    page,
  }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);
    await announceSelf(page, targetId, { availability: "available" });

    // The server's idle timeout ran out across every session.
    await announceSelf(page, targetId, { state: "away", availability: "away", updated_at: T1 });
    await expect(statusTrigger(page)).toHaveAccessibleName(/Status: Ausente/);

    // A real interaction is reported at once; the server answers available.
    const before = await pings(page);
    await page.getByTestId("chat-composer-input").press("a");
    await expect.poll(() => pings(page)).toBe(before + 1);
    // A burst of typing is still one frame.
    await page.getByTestId("chat-composer-input").pressSequentially("bcdef");
    expect(await pings(page)).toBe(before + 1);

    await announceSelf(page, targetId, { availability: "available", updated_at: T2 });
    await expect(statusTrigger(page)).toHaveAccessibleName("Status: Disponível. Alterar status");
  });

  test("duas abas da mesma conta: a oculta não derruba a ativa", async ({ context }, testInfo) => {
    const hidden = await context.newPage();
    const active = await context.newPage();
    const { targetId } = await openDM(hidden, testInfo);
    await openDM(active, testInfo);

    await leaveTab(hidden);
    await active.getByTestId("chat-composer-input").press("a");
    await expect.poll(() => pings(active)).toBeGreaterThan(0);
    expect(await pings(hidden)).toBe(0);

    // The server aggregates both sessions and publishes one answer to both.
    for (const page of [hidden, active]) {
      await announceSelf(page, targetId, { availability: "available" });
      await expect(statusTrigger(page)).toHaveAccessibleName("Status: Disponível. Alterar status");
    }
  });

  test("queda curta da conexão não pisca Offline", async ({ page }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);
    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "online", availability: "available", updated_at: T0 },
    });
    await expect(dmRow(page).getByTestId("presence-dot")).toHaveAttribute(
      "data-presence",
      "online",
    );

    await dropWebSocket(page);
    // Nothing on screen claims a departure while the tab reconnects, and the
    // snapshot after the resubscribe confirms the person is still there.
    await expect(dmRow(page).getByTestId("presence-dot")).not.toHaveAttribute(
      "data-presence",
      "offline",
    );
    await expect(dmRow(page)).toHaveAccessibleName(
      `Mensagem direta com ${OTHER_USER_NAME}, Disponível`,
    );
  });

  test("a última sessão expirada é Offline, com visto por último fora da sidebar", async ({
    page,
  }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);
    // Half a minute inside the eighteenth, so a few milliseconds between this
    // process's clock and the browser's cannot change the minute shown.
    const seen = new Date(Date.now() - 18 * 60_000 - 30_000).toISOString();
    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "offline", availability: "offline", updated_at: seen },
    });

    await expect(dmRow(page)).toHaveAccessibleName(
      `Mensagem direta com ${OTHER_USER_NAME}, Offline`,
    );
    await expect(header(page)).toHaveText("Offline · visto há 18 min");
  });

  test("em chamada: Ocupado · Em chamada, e o fim recalcula", async ({ page }, testInfo) => {
    const { targetId } = await openDM(page, testInfo);
    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: {
        user_id: OTHER_USER_ID,
        state: "online",
        availability: "busy",
        activity: "in_call",
        updated_at: T1,
      },
    });
    await expect(header(page)).toHaveText("Ocupado · Em chamada");
    await expect(dmRow(page)).toHaveAccessibleName(
      `Mensagem direta com ${OTHER_USER_NAME}, Ocupado · Em chamada`,
    );
    await expect(dmRow(page).getByTestId("presence-dot")).toHaveAttribute(
      "title",
      "Ocupado · Em chamada",
    );

    // The call ended; the server recomputed from current facts.
    await emitPresence(page, {
      kind: "dm",
      targetId,
      user: { user_id: OTHER_USER_ID, state: "online", availability: "available", updated_at: T2 },
    });
    await expect(header(page)).toHaveText("Disponível");
  });

  test("Não perturbe escolhido numa aba chega à outra sessão", async ({ context }, testInfo) => {
    const server = createPresenceSettingsServer();
    const first = await context.newPage();
    const second = await context.newPage();
    const { targetId } = await openDM(first, testInfo, server);
    await openDM(second, testInfo, server);

    await statusTrigger(first).click();
    await first.getByRole("menuitemradio", { name: "Não perturbe" }).click();
    const answered = writeAnswered(first);
    await first.getByRole("menuitem", { name: "1 hora" }).click();
    await answered;
    expect(server.state).toBe("dnd");
    const put = server.requests.find((request) => request.method === "PUT");
    expect(Object.keys(put?.body ?? {}).sort()).toEqual(["expires_at", "state"]);
    const expiresIn = Date.parse(String(put?.body?.["expires_at"])) - Date.now();
    expect(expiresIn).toBeGreaterThan(55 * 60_000);
    expect(expiresIn).toBeLessThanOrEqual(60 * 60_000);

    // The server told both sessions before answering; it also publishes them.
    for (const page of [first, second]) {
      await announceSelf(page, targetId, { availability: "dnd", updated_at: T1 });
      await expect(statusTrigger(page)).toHaveAccessibleName(
        "Status: Não perturbe. Alterar status",
      );
    }
    await statusTrigger(second).click();
    await expect(second.getByRole("menuitemradio", { name: "Não perturbe" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
  });

  test("Aparecer offline mantém a sessão recebendo mensagens", async ({ page }, testInfo) => {
    const server = createPresenceSettingsServer();
    const { scenario, targetId } = await openDM(page, testInfo, server);
    await announceSelf(page, targetId, { availability: "available" });

    await statusTrigger(page).click();
    await page.getByRole("menuitemradio", { name: "Aparecer offline" }).click();
    const answered = writeAnswered(page);
    await page.getByRole("menuitem", { name: "Hoje" }).click();
    await answered;
    expect(server.state).toBe("appear_offline");
    await expect(statusTrigger(page)).toHaveAccessibleName(
      "Status: Aparecer offline. Alterar status",
    );

    // Everyone else is told "offline"; this session stays connected and keeps
    // receiving the conversation.
    await announceSelf(page, targetId, {
      state: "offline",
      availability: "offline",
      updated_at: T1,
    });
    await expect(statusTrigger(page)).toHaveAccessibleName(
      "Status: Aparecer offline. Alterar status",
    );
    await emitMessageCreated(page, scenario as MessagingScenario, {
      kind: "dm",
      targetId,
      message: makeMessage({ id: `${targetId}-hidden`, body_text: "chegou mesmo oculto" }),
    });
    await expect(page.getByText("chegou mesmo oculto")).toBeVisible();
  });

  // No hint is sent here: the real one comes from a server sweep, and a spec
  // that sent it would only prove the client obeys the mock. The page's own
  // timer reaches the end and asks the server, whose clock has also passed it.
  test("um status manual expirado volta ao automático sem ação", async ({ page }, testInfo) => {
    const HOUR = 60 * 60_000;
    const start = Date.now();
    await page.clock.install({ time: start });
    const server = createPresenceSettingsServer();
    let elapsed = 0;
    server.now = () => start + elapsed;
    server.state = "appear_offline";
    server.expiresAt = new Date(start + HOUR).toISOString();
    await openDM(page, testInfo, server);
    await expect(statusTrigger(page)).toHaveAccessibleName(
      "Status: Aparecer offline. Alterar status",
    );
    const reads = server.requests.length;

    elapsed = HOUR + 1_000;
    await page.clock.fastForward(elapsed);
    await expect(statusTrigger(page)).not.toHaveAccessibleName(/Aparecer offline/);
    expect(server.requests.slice(reads).map((request) => request.method)).toContain("GET");
    await statusTrigger(page).click();
    await expect(page.getByRole("menuitem", { name: "Redefinir status" })).toHaveCount(0);
    await expect(page.getByRole("menuitemradio", { name: "Aparecer offline" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  test("com o gate fechado o menu mostra o estado e não oferece mudança", async ({
    page,
  }, testInfo) => {
    const server = createPresenceSettingsServer();
    server.writable = false;
    server.state = "dnd";
    server.expiresAt = new Date(Date.now() + 60 * 60_000).toISOString();
    await openDM(page, testInfo, server);
    await expect(statusTrigger(page)).toHaveAccessibleName(/Status: Não perturbe/);
    await statusTrigger(page).click();
    await expect(page.getByRole("menuitemradio")).toHaveCount(0);
    await expect(page.getByText("Alterar o status não está disponível no momento.")).toBeVisible();
    expect(server.requests.every((request) => request.method === "GET")).toBe(true);
  });

  test("o menu de status é totalmente operável por teclado", async ({ page }, testInfo) => {
    const server = createPresenceSettingsServer();
    await openDM(page, testInfo, server);

    await statusTrigger(page).focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("menuitemradio", { name: "Disponível" })).toBeFocused();
    await page.keyboard.press("ArrowDown");
    await expect(page.getByRole("menuitemradio", { name: "Ocupado" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("menu", { name: "Status" })).toHaveCount(0);
    await expect(statusTrigger(page)).toBeFocused();

    await page.keyboard.press("Space");
    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("Enter");
    await expect(page.getByRole("menuitem", { name: "4 horas" })).toBeVisible();
    await page.keyboard.press("ArrowDown");
    const answered = writeAnswered(page);
    await page.keyboard.press("Enter");
    await answered;
    expect(server.state).toBe("busy");
    await expect(statusTrigger(page)).toBeFocused();
  });
});

test.describe("presença em tela de toque (#798)", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

  test("escolhe status e duração por toque, numa folha inferior", async ({ page }, testInfo) => {
    const server = createPresenceSettingsServer();
    await openDM(page, testInfo, server);

    // On a phone the sidebar is a drawer behind the "Conversas" toggle.
    await page.getByRole("button", { name: "Conversas" }).tap();
    const trigger = statusTrigger(page);
    await trigger.tap();
    const sheet = page.getByRole("menu", { name: "Status" });
    await expect(sheet).toBeVisible();
    const box = await sheet.boundingBox();
    expect(Math.round((box?.y ?? 0) + (box?.height ?? 0))).toBe(844);

    await page.getByRole("menuitemradio", { name: "Volto já" }).tap();
    const answered = writeAnswered(page);
    await page.getByRole("menuitem", { name: "1 hora" }).tap();
    await answered;
    expect(server.state).toBe("brb");
  });
});
