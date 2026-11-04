import { expect, test, type Page } from "@playwright/test";

import { installPaginatedMessages } from "../helpers/largeConversationFixture";
import {
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  quoteFrom,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Issue #1099 — a reply to a message far older than the first page.
 *
 * The quote has to name its author from the server's projection, before the
 * original is anywhere in the client, and following it has to page back
 * through the ordinary history (#1088's MESSAGE_TARGET) to the exact message,
 * highlight it and keep the timeline virtualized. What cannot be reached is
 * said, never guessed at. The chat-service is mocked; the projection itself is
 * proven against PostgreSQL (quote_author_postgres_test.go).
 */

const PHONE = { width: 390, height: 844 };
const MISSED = "Não foi possível localizar a mensagem no histórico.";

type Kind = "channel" | "dm";
type Quote = ReturnType<typeof quoteFrom> & { author_display_name?: string };

/**
 * 119 messages and, newest, a reply quoting the fourth: with pages of 50 the
 * original is two older pages away from what the conversation opens on.
 */
function historyWithOldQuote(targetId: string, quote: (target: Quote) => Quote = (q) => q) {
  const older = Array.from({ length: 119 }, (_, index) =>
    makeMessage({
      id: `${targetId}-m-${index}`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: index === 3 ? "Salve" : `Mensagem ${index}`,
      created_at: new Date(Date.UTC(2026, 6, 10, 8, index)).toISOString(),
    }),
  );
  const target = older[3];
  const reply = makeMessage({
    id: `${targetId}-reply`,
    body_text: "respondendo ao antigo",
    created_at: "2026-07-10T12:00:00.000Z",
    quoted: quote({ ...quoteFrom(target), author_display_name: OTHER_USER_NAME }),
  });
  return { history: [...older, reply], target, reply };
}

async function openConversation(
  page: Page,
  kind: Kind,
  targetId: string,
  history: ReturnType<typeof makeMessage>[],
) {
  const scenario = createScenario({
    kind,
    conversationType: kind === "dm" ? "group" : undefined,
    targetId,
    targetName: kind === "dm" ? "Grupo antigo" : "infraestrutura",
    messages: history,
  });
  await installMessagingMocks(page, scenario);
  await installPaginatedMessages(page, targetId, history, kind);
  await page.goto(`/chat/${kind}/${targetId}`);
  await expect(messageBubble(page, history.at(-1)!.id)).toBeVisible();
}

/**
 * A keyboard reader reaches the quote after the opened conversation has handed
 * its initial focus to the composer; moving focus earlier races that hand-off.
 */
async function expectInitialFocusSettled(page: Page) {
  await expect(page.getByTestId("chat-composer-input")).toBeFocused();
}

/** The `before` cursors of the older pages the timeline asks for, in order. */
function olderPagesRequested(page: Page, kind: Kind, targetId: string): string[] {
  const collection = kind === "channel" ? "channels" : "dm";
  const cursors: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    const before = url.searchParams.get("before");
    if (before && url.pathname.endsWith(`/${collection}/${targetId}/messages`)) {
      cursors.push(before);
    }
  });
  return cursors;
}

const quoteOf = (page: Page, replyId: string) =>
  messageBubble(page, replyId).getByTestId("chat-message-quote");

function highlightOf(page: Page, messageId: string) {
  return page.waitForFunction(
    (id) =>
      document
        .querySelector(`[data-message-id="${id}"]`)
        ?.classList.contains("chat-msg-area__msg--highlight") === true,
    messageId,
  );
}

/** Landed on exactly the target, through the history, still virtualized. */
async function expectLandedOn(page: Page, history: ReturnType<typeof makeMessage>[], id: string) {
  await expect(messageBubble(page, id)).toBeInViewport();
  await expect(page.locator(".chat-msg-area__msg--highlight")).toHaveCount(1);
  await expect(page.getByTestId("chat-virtual-canvas")).toBeAttached();
  expect(await page.getByTestId("chat-msg-bubble").count()).toBeLessThan(history.length);
  await expect(page.getByText(MISSED)).toHaveCount(0);
}

for (const kind of ["channel", "dm"] as const) {
  const where = kind === "channel" ? "canal" : "grupo";

  test(`${where}: a citação antiga nomeia o autor e o clique pagina até a mensagem exata`, async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, kind);
    const { history, target, reply } = historyWithOldQuote(targetId);
    const older = olderPagesRequested(page, kind, targetId);
    await openConversation(page, kind, targetId, history);

    // Named by the server before the original is anywhere in the client.
    const quote = quoteOf(page, reply.id);
    await expect(quote).toContainText(OTHER_USER_NAME);
    await expect(quote).not.toContainText("Usuário desconhecido");
    await expect(quote).toHaveAttribute("role", "button");
    expect(older).toEqual([]);

    const highlighted = highlightOf(page, target.id);
    await quote.click();

    await expect(page).toHaveURL(
      `/chat/${kind}/${targetId}?message=${encodeURIComponent(target.id)}`,
    );
    await highlighted;
    // 70..119 opened; 20..69, then 0..19, which holds the target and ends there.
    expect(older).toEqual([history[70].id, history[20].id]);
    await expectLandedOn(page, history, target.id);
  });
}

test("teclado: Enter na citação segue para o original e o foco chega nele", async ({
  page,
}, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, target, reply } = historyWithOldQuote(targetId);
  await openConversation(page, "channel", targetId, history);

  const highlighted = highlightOf(page, target.id);
  await expectInitialFocusSettled(page);
  await quoteOf(page, reply.id).focus();
  await expect(quoteOf(page, reply.id)).toBeFocused();
  await page.keyboard.press("Enter");

  await highlighted;
  await expectLandedOn(page, history, target.id);
  // The list that held the quote was replaced on the way; focus was not lost
  // with it, it travelled to the destination.
  await expect(messageBubble(page, target.id)).toBeFocused();
  expect(await page.evaluate(() => document.activeElement === document.body)).toBe(false);
});

test("teclado: original inalcançável devolve o foco à lista de mensagens", async ({
  page,
}, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, reply } = historyWithOldQuote(targetId, (quote) => ({
    ...quote,
    id: `${targetId}-inacessivel`,
  }));
  await openConversation(page, "channel", targetId, history);

  await expectInitialFocusSettled(page);
  await quoteOf(page, reply.id).focus();
  await page.keyboard.press("Enter");

  // Two older pages, each answered with the fixture's server latency.
  await expect(page.getByRole("status").filter({ hasText: MISSED })).toBeVisible({
    timeout: 20_000,
  });
  await expect(page.getByRole("log", { name: "Mensagens" })).toBeFocused();
  expect(await page.evaluate(() => document.activeElement === document.body)).toBe(false);
});

test("teclado: foco movido pelo leitor durante a busca não é tomado de volta", async ({
  page,
}, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, target, reply } = historyWithOldQuote(targetId);
  await openConversation(page, "channel", targetId, history);

  const highlighted = highlightOf(page, target.id);
  await expectInitialFocusSettled(page);
  await quoteOf(page, reply.id).focus();
  await page.keyboard.press("Enter");
  // While the history is still being paged in, the reader goes to the composer.
  const composer = page.getByTestId("chat-composer-input");
  await composer.click();
  await expect(composer).toBeFocused();

  await highlighted;
  await expectLandedOn(page, history, target.id);
  await expect(composer).toBeFocused();
});

/**
 * Holds every matching request of this conversation until `release()`, then
 * aborts it (`fail`) or lets the fixture answer it. `newest` picks the page the
 * reopening loads; otherwise the older pages the search asks for.
 */
async function holdMessages(page: Page, targetId: string, newest: boolean) {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  const state = { fail: false };
  await page.route(`**/api/chat/channels/${targetId}/messages*`, async (route) => {
    const before = new URL(route.request().url()).searchParams.get("before");
    if (route.request().method() !== "GET" || Boolean(before) === newest) {
      await route.fallback();
      return;
    }
    await gate;
    if (state.fail) await route.abort("internetdisconnected");
    else await route.fallback();
  });
  const requested = (before: boolean) =>
    page.waitForRequest((request) => {
      const url = new URL(request.url());
      return (
        url.pathname.endsWith(`/channels/${targetId}/messages`) &&
        url.searchParams.has("before") === before
      );
    });
  return { release, state, requested: () => requested(!newest) };
}

test("teclado: reabertura que falha leva o foco a Tentar novamente, e o retry chega ao original", async ({
  page,
}, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, target, reply } = historyWithOldQuote(targetId);
  await openConversation(page, "channel", targetId, history);
  let offline = true;
  await page.route(`**/api/chat/channels/${targetId}/messages*`, async (route) => {
    const before = new URL(route.request().url()).searchParams.get("before");
    if (offline && !before) await route.abort("internetdisconnected");
    else await route.fallback();
  });

  await expectInitialFocusSettled(page);
  await quoteOf(page, reply.id).focus();
  await page.keyboard.press("Enter");

  const retry = page.getByRole("button", { name: "Tentar novamente" });
  await expect(retry).toBeFocused();
  expect(await page.evaluate(() => document.activeElement === document.body)).toBe(false);

  offline = false;
  const highlighted = highlightOf(page, target.id);
  await page.keyboard.press("Enter");

  await highlighted;
  await expectLandedOn(page, history, target.id);
  await expect(messageBubble(page, target.id)).toBeFocused();
});

test("teclado: foco escolhido pelo leitor enquanto a reabertura falha não é tomado", async ({
  page,
}, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, reply } = historyWithOldQuote(targetId);
  await openConversation(page, "channel", targetId, history);
  const reopening = await holdMessages(page, targetId, true);
  reopening.state.fail = true;

  await expectInitialFocusSettled(page);
  await quoteOf(page, reply.id).focus();
  const requested = reopening.requested();
  await page.keyboard.press("Enter");
  await requested;
  // The composer is disabled while the conversation loads; the header is not.
  const details = page.getByRole("button", { name: "Detalhes do canal" });
  await details.focus();
  await expect(details).toBeFocused();
  reopening.release();

  await expect(page.getByRole("button", { name: "Tentar novamente" })).toBeVisible();
  await expect(details).toBeFocused();
});

test("teclado: outra mensagem focada pelo leitor durante a busca não perde o foco para o original", async ({
  page,
}, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, target, reply } = historyWithOldQuote(targetId);
  await openConversation(page, "channel", targetId, history);
  const search = await holdMessages(page, targetId, false);

  await expectInitialFocusSettled(page);
  await quoteOf(page, reply.id).focus();
  const requested = search.requested();
  await page.keyboard.press("Enter");
  await requested;
  // The reopened list is up and paging back; the reader moves to another message.
  const chosen = messageBubble(page, history.at(-2)!.id);
  await chosen.focus();
  await expect(chosen).toBeFocused();
  const highlighted = highlightOf(page, target.id);
  search.release();

  await highlighted;
  await expectLandedOn(page, history, target.id);
  // The trip still travels, and the virtualized list lets the chosen row go;
  // focus follows #675's rule for a row that unmounts (the list), and the
  // return to the original stays cancelled.
  await expect(page.getByRole("log", { name: "Mensagens" })).toBeFocused();
  await expect(messageBubble(page, target.id)).not.toBeFocused();
});

/** How many document-level focusin listeners are attached right now. */
async function trackFocusInListeners(page: Page) {
  await page.addInitScript(() => {
    const live = new Set<unknown>();
    const add = document.addEventListener.bind(document);
    const remove = document.removeEventListener.bind(document);
    document.addEventListener = (type: string, listener: unknown, options?: unknown) => {
      if (type === "focusin") live.add(listener);
      add(type, listener as EventListener, options as AddEventListenerOptions);
    };
    document.removeEventListener = (type: string, listener: unknown, options?: unknown) => {
      if (type === "focusin") live.delete(listener);
      remove(type, listener as EventListener, options as EventListenerOptions);
    };
    (window as unknown as { focusInListeners: () => number }).focusInListeners = () => live.size;
  });
  return () =>
    page.evaluate(() =>
      (window as unknown as { focusInListeners: () => number }).focusInListeners(),
    );
}

test("teclado: Voltar no navegador durante a busca cancela o retorno do foco", async ({
  page,
}, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, target, reply } = historyWithOldQuote(targetId);
  const focusInListeners = await trackFocusInListeners(page);
  await openConversation(page, "channel", targetId, history);
  const search = await holdMessages(page, targetId, false);

  await expectInitialFocusSettled(page);
  const before = await focusInListeners();
  await quoteOf(page, reply.id).focus();
  const requested = search.requested();
  await page.keyboard.press("Enter");
  await requested;
  // Pending: the return is watching the reader's focus.
  expect(await focusInListeners()).toBe(before + 1);

  await page.goBack();

  await expect(page).toHaveURL(`/chat/channel/${targetId}`);
  await expect.poll(focusInListeners).toBe(before);
  await expect(quoteOf(page, reply.id)).toBeVisible();
  search.release();

  // Nothing late takes the trip up again: no target, no highlight, no focus.
  await expect(page).toHaveURL(`/chat/channel/${targetId}`);
  await expect(page.locator(".chat-msg-area__msg--highlight")).toHaveCount(0);
  await expect(messageBubble(page, target.id)).toHaveCount(0);
  expect(await focusInListeners()).toBe(before);
});

test("o nome da citação persiste após recarregar a página", async ({ page }, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, reply } = historyWithOldQuote(targetId);
  await openConversation(page, "channel", targetId, history);

  await page.reload();

  await expect(messageBubble(page, reply.id)).toBeVisible();
  await expect(quoteOf(page, reply.id)).toContainText(OTHER_USER_NAME);
});

test("original removido: a citação não é acionável e nada é buscado", async ({
  page,
}, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, reply } = historyWithOldQuote(targetId, (quote) => ({
    ...quote,
    body: "",
    is_removed: true,
    deleted_at: "2026-07-11T08:00:00.000Z",
  }));
  const older = olderPagesRequested(page, "channel", targetId);
  await openConversation(page, "channel", targetId, history);

  const quote = quoteOf(page, reply.id);
  await expect(quote).toContainText("Mensagem original indisponível.");
  await expect(quote).toHaveAttribute("aria-disabled", "true");
  await expect(quote).not.toHaveAttribute("role", "button");
  await expect(quote).not.toHaveAttribute("tabindex");

  await quote.click();

  await expect(page).toHaveURL(`/chat/channel/${targetId}`);
  expect(older).toEqual([]);
});

test("original fora do alcance do leitor: busca limitada, aviso e nenhum destaque falso", async ({
  page,
}, testInfo) => {
  // The original the quote points at is in no page the server serves this
  // reader — removed for everyone since, or no longer visible to them.
  const targetId = uniqueId(testInfo, "channel");
  const { history, reply } = historyWithOldQuote(targetId, (quote) => ({
    ...quote,
    id: `${targetId}-inacessivel`,
  }));
  const older = olderPagesRequested(page, "channel", targetId);
  await openConversation(page, "channel", targetId, history);

  await quoteOf(page, reply.id).click();

  // Two older pages, each answered with the fixture's server latency.
  await expect(page.getByRole("status").filter({ hasText: MISSED })).toBeVisible({
    timeout: 20_000,
  });
  await expect(page.locator(".chat-msg-area__msg--highlight")).toHaveCount(0);
  // The whole history was two older pages: the search ended with it.
  expect(older).toEqual([history[70].id, history[20].id]);
});

test("falha de rede: avisa e uma nova ativação busca de novo e encontra", async ({
  page,
}, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, target, reply } = historyWithOldQuote(targetId);
  await openConversation(page, "channel", targetId, history);

  let offline = true;
  await page.route(`**/api/chat/channels/${targetId}/messages*`, async (route) => {
    const before = new URL(route.request().url()).searchParams.get("before");
    if (offline && before) await route.abort("internetdisconnected");
    else await route.fallback();
  });

  await quoteOf(page, reply.id).click();
  await expect(page.getByText(MISSED)).toBeVisible();
  await expect(page.locator(".chat-msg-area__msg--highlight")).toHaveCount(0);

  offline = false;
  const highlighted = highlightOf(page, target.id);
  await quoteOf(page, reply.id).click();

  await highlighted;
  await expectLandedOn(page, history, target.id);
});

test("o rascunho do composer sobrevive à navegação pela citação", async ({ page }, testInfo) => {
  const targetId = uniqueId(testInfo, "channel");
  const { history, target, reply } = historyWithOldQuote(targetId);
  await openConversation(page, "channel", targetId, history);

  const composer = page.getByTestId("chat-composer-input");
  await composer.click();
  await page.keyboard.type("rascunho em andamento");

  const highlighted = highlightOf(page, target.id);
  await quoteOf(page, reply.id).click();
  await highlighted;

  await expect(composer).toContainText("rascunho em andamento");
});

test("celular: a citação antiga segue até o original", async ({ page }, testInfo) => {
  await page.setViewportSize(PHONE);
  const targetId = uniqueId(testInfo, "channel");
  const { history, target, reply } = historyWithOldQuote(targetId);
  await openConversation(page, "channel", targetId, history);

  const highlighted = highlightOf(page, target.id);
  await quoteOf(page, reply.id).click();

  await highlighted;
  await expectLandedOn(page, history, target.id);
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
});
