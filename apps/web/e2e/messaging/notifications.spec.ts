import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";

import {
  GROUP_DM_ID,
  GROUP_DM_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  emitMessageCreated,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  messagesFor,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Instrumentation local a este spec, no mesmo estilo de
 * instrumentIncomingCallRingtone() em call-1to1-ui.spec.ts: substitui a API
 * nativa via addInitScript e expõe o estado em window.__e2e* para os testes
 * lerem depois.
 *
 * `document.hasFocus` também é sobrescrito aqui porque notifyOrChime()
 * (useChatSidebar.ts) só tenta a notificação nativa quando a aba é reportada
 * como sem foco — e no Chromium headless do Playwright a própria página
 * normalmente reporta foco true, o que impediria o branch de notificação de
 * ser exercitado.
 */
async function installNotificationMock(page: Page) {
  await page.addInitScript(() => {
    interface CapturedNotification {
      title: string;
      body?: string;
      tag?: string;
      icon?: string;
      onclick: (() => void) | null;
    }
    const target = window as unknown as {
      __e2eNotificationPermission: NotificationPermission;
      __e2eNotifications: CapturedNotification[];
      __e2eWindowFocused: boolean;
      __e2eTriggerNotificationClick: (index: number) => void;
    };
    target.__e2eNotificationPermission = "default";
    target.__e2eNotifications = [];
    target.__e2eWindowFocused = true;
    target.__e2eTriggerNotificationClick = (index: number) => {
      target.__e2eNotifications[index]?.onclick?.();
    };

    class MockNotification {
      static get permission(): NotificationPermission {
        return target.__e2eNotificationPermission;
      }
      static requestPermission() {
        return Promise.resolve(target.__e2eNotificationPermission);
      }
      constructor(title: string, options: NotificationOptions = {}) {
        const record: CapturedNotification = {
          title,
          body: options.body,
          tag: options.tag,
          icon: options.icon,
          onclick: null,
        };
        target.__e2eNotifications.push(record);
        Object.defineProperty(this, "onclick", {
          get: () => record.onclick,
          set: (handler: (() => void) | null) => {
            record.onclick = handler;
          },
        });
      }
      close() {
        // No-op: nothing in production code asserts on close() being called.
      }
    }

    Object.defineProperty(window, "Notification", {
      configurable: true,
      writable: true,
      value: MockNotification,
    });
    document.hasFocus = () => target.__e2eWindowFocused;
  });
}

async function setNotificationPermission(page: Page, permission: NotificationPermission) {
  await page.evaluate((value) => {
    (
      window as unknown as { __e2eNotificationPermission: NotificationPermission }
    ).__e2eNotificationPermission = value;
  }, permission);
}

async function setWindowFocused(page: Page, focused: boolean) {
  await page.evaluate((value) => {
    (window as unknown as { __e2eWindowFocused: boolean }).__e2eWindowFocused = value;
  }, focused);
}

async function capturedNotifications(page: Page) {
  return page.evaluate(
    () =>
      (
        window as unknown as {
          __e2eNotifications: Array<{ title: string; body?: string; tag?: string }>;
        }
      ).__e2eNotifications,
  );
}

async function triggerNotificationClick(page: Page, index = 0) {
  await page.evaluate(
    (i) =>
      (
        window as unknown as { __e2eTriggerNotificationClick: (index: number) => void }
      ).__e2eTriggerNotificationClick(i),
    index,
  );
}

/** Mirrors instrumentIncomingCallRingtone() in call-1to1-ui.spec.ts, targeting the message chime instead of the ringtone. */
async function instrumentMessageSound(page: Page) {
  await page.addInitScript(() => {
    const target = window as unknown as { __e2eMessageSound: { play: number } };
    target.__e2eMessageSound = { play: 0 };
    const originalPlay = HTMLMediaElement.prototype.play;
    HTMLMediaElement.prototype.play = function (this: HTMLMediaElement) {
      if (this.src.endsWith("/sounds/message-received.wav")) {
        target.__e2eMessageSound.play += 1;
        return Promise.resolve();
      }
      return originalPlay.call(this);
    };
  });
}

async function messageSoundPlayCount(page: Page) {
  return page.evaluate(
    () => (window as unknown as { __e2eMessageSound: { play: number } }).__e2eMessageSound.play,
  );
}

/**
 * Objetivo: badge de não lidas, notificação nativa do navegador (concedida e
 * negada), som de fallback e clique na notificação levando a navegação,
 * scroll e foco corretos — RF de notificações (Subtask C).
 *
 * Todos os cenários usam o grupo padrão (GROUP_DM_ID) como alvo da mensagem
 * recebida, com uma conversa DM diferente aberta como "ativa", reproduzindo o
 * mesmo padrão já validado em message-interactions.spec.ts ("atualiza unread
 * de grupo não selecionado e limpa ao abrir"): o grupo já existe por padrão
 * no fixture do sidebar e por isso já está inscrito via WebSocket assim que a
 * página carrega, sem precisar de setup extra.
 */
test.describe("notificações — badge, notificação nativa, som e navegação", () => {
  test("mensagem nova atualiza o badge de não lidas do grupo e limpa ao abrir a conversa", async ({
    page,
  }, testInfo) => {
    const activeTargetId = uniqueId(testInfo, "badge-active-dm");
    const scenario = createScenario({
      kind: "dm",
      targetId: activeTargetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${activeTargetId}-msg`, body_text: "conversa ativa" })],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${activeTargetId}`);

    const groupOption = page
      .getByRole("region", { name: "Grupos" })
      .getByRole("option", { name: `Grupo ${GROUP_DM_NAME}` });
    await expect(groupOption.getByLabel("1 não lidas")).toHaveCount(0);

    const incoming = makeMessage({
      id: `${GROUP_DM_ID}-badge-incoming`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "mensagem nova para o badge",
    });
    await emitMessageCreated(page, scenario, {
      kind: "dm",
      targetId: GROUP_DM_ID,
      message: incoming,
    });

    await expect(groupOption.getByLabel("1 não lidas")).toBeVisible();

    await groupOption.click();
    await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
    await expect(groupOption.getByLabel("1 não lidas")).toHaveCount(0);
  });

  test("com permissão concedida e aba em segundo plano, exibe notificação nativa com o conteúdo da mensagem", async ({
    page,
  }, testInfo) => {
    const activeTargetId = uniqueId(testInfo, "notif-granted-active-dm");
    const scenario = createScenario({
      kind: "dm",
      targetId: activeTargetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${activeTargetId}-msg`, body_text: "conversa ativa" })],
    });
    await installMessagingMocks(page, scenario);
    await installNotificationMock(page);
    await page.goto(`/chat/dm/${activeTargetId}`);
    await setNotificationPermission(page, "granted");
    await setWindowFocused(page, false);

    const incoming = makeMessage({
      id: `${GROUP_DM_ID}-notif-granted`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "conteúdo relevante da notificação",
    });
    await emitMessageCreated(page, scenario, {
      kind: "dm",
      targetId: GROUP_DM_ID,
      message: incoming,
    });

    await expect.poll(() => capturedNotifications(page)).toHaveLength(1);
    const [notification] = await capturedNotifications(page);
    expect(notification.title).toBe(OTHER_USER_NAME);
    expect(notification.body).toContain("conteúdo relevante da notificação");
    expect(notification.tag).toBe(`nchat-message-dm-${GROUP_DM_ID}`);
  });

  test("com permissão bloqueada, nenhuma notificação nativa é criada e o som de fallback dispara", async ({
    page,
  }, testInfo) => {
    const activeTargetId = uniqueId(testInfo, "notif-denied-active-dm");
    const scenario = createScenario({
      kind: "dm",
      targetId: activeTargetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${activeTargetId}-msg`, body_text: "conversa ativa" })],
    });
    await installMessagingMocks(page, scenario);
    await installNotificationMock(page);
    await instrumentMessageSound(page);
    await page.goto(`/chat/dm/${activeTargetId}`);
    await setNotificationPermission(page, "denied");
    await setWindowFocused(page, false);

    const groupOption = page
      .getByRole("region", { name: "Grupos" })
      .getByRole("option", { name: `Grupo ${GROUP_DM_NAME}` });

    const incoming = makeMessage({
      id: `${GROUP_DM_ID}-notif-denied`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "mensagem com permissão bloqueada",
    });
    await emitMessageCreated(page, scenario, {
      kind: "dm",
      targetId: GROUP_DM_ID,
      message: incoming,
    });

    // Confirma que o evento chegou de fato (via badge) antes de afirmar a
    // ausência de notificação — sem isso, um falso positivo seria possível
    // caso o próprio evento nunca tivesse chegado.
    await expect(groupOption.getByLabel("1 não lidas")).toBeVisible();

    expect(await capturedNotifications(page)).toHaveLength(0);
    await expect.poll(() => messageSoundPlayCount(page)).toBeGreaterThan(0);
  });

  test("clicar na notificação navega até a conversa, rola para o fim e mantém o composer utilizável", async ({
    page,
  }, testInfo) => {
    const activeTargetId = uniqueId(testInfo, "notif-click-active-dm");
    const scenario = createScenario({
      kind: "dm",
      targetId: activeTargetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${activeTargetId}-msg`, body_text: "conversa ativa" })],
    });
    // Semeia o grupo (alvo da notificação) com histórico longo, para provar
    // que a conversa realmente rola até o fim ao ser aberta via notificação,
    // e não apenas por já estar vazia.
    messagesFor(scenario, "dm", GROUP_DM_ID).push(
      ...Array.from({ length: 40 }, (_, index) =>
        makeMessage({
          id: `${GROUP_DM_ID}-history-${index}`,
          body_text: `Histórico ${index} do grupo.`,
        }),
      ),
    );
    await installMessagingMocks(page, scenario);
    await installNotificationMock(page);
    await page.goto(`/chat/dm/${activeTargetId}`);
    await setNotificationPermission(page, "granted");
    await setWindowFocused(page, false);

    const incoming = makeMessage({
      id: `${GROUP_DM_ID}-notif-click`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "clique para navegar até o grupo",
    });
    await emitMessageCreated(page, scenario, {
      kind: "dm",
      targetId: GROUP_DM_ID,
      message: incoming,
    });
    await expect.poll(() => capturedNotifications(page)).toHaveLength(1);

    // A Notification API nativa não é clicável pelo Playwright — o teste
    // invoca o handler capturado, exatamente como o navegador faria ao
    // processar o clique do usuário na notificação do sistema.
    await triggerNotificationClick(page);

    await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);

    const input = page.getByTestId("chat-composer-input");
    await expect(input).toBeFocused();

    const list = page.locator(".chat-msg-area__list");
    await expect
      .poll(async () =>
        list.evaluate((element) =>
          Math.abs(element.scrollHeight - element.clientHeight - element.scrollTop),
        ),
      )
      .toBeLessThanOrEqual(1);

    await fillComposer(page, "composer funcionando após clique na notificação");
    await expect(page.getByText("composer funcionando após clique na notificação")).toBeVisible();
  });
});
