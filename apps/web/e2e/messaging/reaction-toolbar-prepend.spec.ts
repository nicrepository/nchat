/**
 * A toolbar de reações depois de carregar mensagens anteriores (issue #839).
 *
 * A regressão só existe num navegador de verdade: a conversa abre com uma
 * página, abaixo do limiar de virtualização, e a primeira página anterior a
 * empurra para a timeline virtualizada, onde cada linha é posicionada por
 * `transform`. Um `position: fixed` dentro dessa linha passa a ser medido a
 * partir dela, não da viewport — e a toolbar, colocada em coordenadas de
 * viewport, aparece longe da mensagem. jsdom não faz layout, então o que este
 * spec afirma não pode ser afirmado em mais lugar nenhum.
 *
 * A referência de posicionamento é o estado saudável de antes do prepend, lido
 * na própria conversa e não inventado aqui: a relação horizontal entre toolbar
 * e bolha é capturada antes e exigida igual depois de cada página carregada.
 */

import { expect, test, type Page, type TestInfo } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";
import {
  PAGE_SIZE,
  bubbles,
  installPaginatedMessages,
  list,
  loadPreviousPage,
  quiet,
  settledViewport,
} from "../helpers/largeConversationFixture";

/** Quatro páginas: o suficiente para dois prepends com folga. */
const MESSAGE_COUNT = PAGE_SIZE * 4;

/**
 * Só texto, com tamanhos variados e bolhas curtas do próprio autor: a toolbar
 * é mais larga do que essas bolhas, que é o caso em que "alinhar pela bolha"
 * daria errado, e nenhum anexo — a altura das linhas não é o assunto.
 */
/**
 * A message taller than the list, in the first page and away from where the
 * other cases look: no room for the toolbar on either side of it.
 */
const TALL_MESSAGE_INDEX = 155;
const tallBody = Array.from({ length: 40 }, (_, line) => `Linha ${line + 1}`).join("\n");

function bodyFor(index: number, mine: boolean): string {
  if (index === TALL_MESSAGE_INDEX) return tallBody;
  return mine ? `Ok ${index}` : `Mensagem ${index} ${"palavra ".repeat((index % 5) * 4)}`;
}

function buildPlainHistory(targetId: string) {
  return Array.from({ length: MESSAGE_COUNT }, (_, index) => {
    const mine = index % 3 === 0;
    const stamp = `2026-07-1${Math.floor(index / 100)}T12:${String(index % 60).padStart(2, "0")}:${String(index % 60).padStart(2, "0")}.000Z`;
    return makeMessage({
      id: `${targetId}-m-${index}`,
      sender_id: mine ? CURRENT_USER_ID : OTHER_USER_ID,
      sender_display_name: mine ? "E2E Autor" : OTHER_USER_NAME,
      body_text: bodyFor(index, mine),
      created_at: stamp,
      updated_at: stamp,
    });
  });
}

const toolbar = (page: Page) => page.getByRole("toolbar", { name: "Reagir à mensagem" });
const messageShell = (page: Page, id: string) => page.locator(`[data-message-id="${id}"]`);

/** Um gap maior do que isto já não lê como "colado à mensagem". */
const SMALL_GAP_PX = 10;

interface Placement {
  /** bubble.top - toolbar.bottom: pequeno e estritamente positivo. */
  gap: number;
  /** A relação horizontal com a bolha, medida da mesma forma antes e depois. */
  horizontal: number;
}

interface VisibleMessage {
  id: string;
  mine: boolean;
}

/** Mensagens inteiramente dentro da lista, com espaço para a toolbar acima. */
async function visibleMessages(page: Page): Promise<VisibleMessage[]> {
  return list(page).evaluate((element) => {
    const band = element.getBoundingClientRect();
    const out: { id: string; mine: boolean }[] = [];
    for (const shell of element.querySelectorAll<HTMLElement>("[data-message-id]")) {
      const box = shell.querySelector(".chat-msg-area__msg-bubble")?.getBoundingClientRect();
      if (!box || box.top < band.top + 48 || box.bottom > band.bottom) continue;
      out.push({
        id: shell.dataset.messageId!,
        mine: shell.classList.contains("chat-msg-area__msg--mine"),
      });
    }
    return out;
  });
}

function pick(messages: VisibleMessage[], mine: boolean): string {
  const found = messages.find((message) => message.mine === mine);
  expect(found, `uma mensagem ${mine ? "própria" : "recebida"} visível`).toBeDefined();
  return found!.id;
}

async function scrollMetrics(page: Page) {
  return list(page).evaluate((element) => ({
    top: element.scrollTop,
    height: element.scrollHeight,
  }));
}

interface Boxes {
  bubble: { x: number; y: number; width: number; height: number };
  bar: { x: number; y: number; width: number; height: number };
}

/**
 * Passa o mouse na mensagem, mede toolbar e bolha, e esconde a toolbar.
 *
 * Exibir e esconder a toolbar não pode mover a timeline: scrollTop e
 * scrollHeight são lidos antes, durante e depois.
 */
async function hoverAndRead(page: Page, id: string): Promise<Boxes> {
  const before = await scrollMetrics(page);
  await page.mouse.move(0, 0);
  // Um movimento de ponteiro, e não `hover()`: este rola o elemento para a
  // vista se for preciso, e o que se quer medir é a timeline sem mais nenhuma
  // ação além de mostrar a toolbar.
  const target = (await messageShell(page, id)
    .locator(".chat-msg-area__msg-bubble")
    .boundingBox())!;
  await page.mouse.move(target.x + target.width / 2, target.y + target.height / 2);
  await expect(toolbar(page)).toBeVisible();

  const bubble = (await messageShell(page, id)
    .locator(".chat-msg-area__msg-bubble")
    .boundingBox())!;
  const bar = (await toolbar(page).boundingBox())!;
  expect(bar.width, "a toolbar mantém a própria largura").toBeGreaterThan(100);

  const during = await scrollMetrics(page);
  await page.mouse.move(0, 0);
  await expect(toolbar(page)).toHaveCount(0);
  const after = await scrollMetrics(page);
  for (const reading of [during, after]) {
    expect(Math.abs(reading.top - before.top), "scrollTop estável").toBeLessThanOrEqual(1);
    expect(Math.abs(reading.height - before.height), "scrollHeight estável").toBeLessThanOrEqual(1);
  }
  return { bubble, bar };
}

/**
 * A vertical é um invariante absoluto: a toolbar termina um pouco acima de
 * onde a bolha começa — sem cobrir, sem encostar, sem se afastar. A horizontal
 * é devolvida para ser comparada com a leitura saudável, não com uma regra
 * escrita aqui.
 */
async function hoverAndMeasure(page: Page, id: string, mine: boolean): Promise<Placement> {
  const { bubble, bar } = await hoverAndRead(page, id);
  const gap = bubble.y - (bar.y + bar.height);
  expect(gap, `toolbar acima da bolha ${id}, sem encostar`).toBeGreaterThan(0);
  expect(gap, `toolbar próxima da bolha ${id}`).toBeLessThanOrEqual(SMALL_GAP_PX);
  const midX = bubble.x + bubble.width / 2;
  return { gap, horizontal: mine ? bar.x + bar.width - midX : bar.x - midX };
}

/**
 * Leva a bolha para perto do topo real da lista — abaixo do header, mas sem
 * espaço para a toolbar acima dela dentro da lista.
 */
async function scrollBubbleNearTop(page: Page, id: string) {
  await messageShell(page, id).evaluate((shell) => {
    const bubble = shell.querySelector(".chat-msg-area__msg-bubble")!.getBoundingClientRect();
    const scroller = shell.closest<HTMLElement>(".chat-msg-area__list")!;
    scroller.scrollTop += bubble.top - scroller.getBoundingClientRect().top - 12;
  });
  await settledViewport(page);
}

function expectSamePlacement(actual: Placement, healthy: Placement, label: string) {
  expect(
    Math.abs(actual.horizontal - healthy.horizontal),
    `${label}: horizontal`,
  ).toBeLessThanOrEqual(1);
  expect(Math.abs(actual.gap - healthy.gap), `${label}: gap`).toBeLessThanOrEqual(1);
}

/** Depois de um prepend, hover em mensagens do topo e nas mesmas do começo. */
async function expectPlacementAfterPrepend(
  page: Page,
  healthy: { received: Placement; mine: Placement },
  original: { received: string; mine: string },
  label: string,
) {
  const near = await visibleMessages(page);
  expectSamePlacement(
    await hoverAndMeasure(page, pick(near, false), false),
    healthy.received,
    `${label}, recebida recém-carregada`,
  );
  expectSamePlacement(
    await hoverAndMeasure(page, pick(near, true), true),
    healthy.mine,
    `${label}, própria recém-carregada`,
  );

  // As mesmas mensagens do começo, agora no fim de uma timeline virtualizada
  // — linhas com `translateY` alto, que é onde o deslocamento era maior.
  await list(page).evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await settledViewport(page);
  expectSamePlacement(
    await hoverAndMeasure(page, original.received, false),
    healthy.received,
    `${label}, recebida original`,
  );
  expectSamePlacement(
    await hoverAndMeasure(page, original.mine, true),
    healthy.mine,
    `${label}, própria original`,
  );
}

/**
 * Sem mouse: o foco na mensagem mostra a toolbar, e Tab chega a ela logo
 * depois dos controles da própria mensagem (o nome do autor, num canal ou
 * grupo, é um botão) — a toolbar continua no lugar dela no DOM.
 */
async function expectKeyboardReach(page: Page, id: string) {
  await page.mouse.move(0, 0);
  await messageShell(page, id).focus();
  await expect(toolbar(page)).toBeVisible();
  const first = toolbar(page).getByRole("button").first();
  const focused = () => first.evaluate((element) => element === document.activeElement);
  for (let step = 0; step < 3 && !(await focused()); step += 1) {
    await page.keyboard.press("Tab");
  }
  await expect(first).toBeFocused();
  const bubble = (await messageShell(page, id)
    .locator(".chat-msg-area__msg-bubble")
    .boundingBox())!;
  const bar = (await toolbar(page).boundingBox())!;
  expect(bubble.y - (bar.y + bar.height)).toBeGreaterThanOrEqual(0);
}

interface Conversation {
  label: string;
  kind: "channel" | "dm";
  conversationType?: "direct" | "group";
  path: (id: string) => string;
}

const conversations: Conversation[] = [
  { label: "canal", kind: "channel", path: (id) => `/chat/channel/${id}` },
  { label: "DM 1:1", kind: "dm", conversationType: "direct", path: (id) => `/chat/dm/${id}` },
  { label: "grupo", kind: "dm", conversationType: "group", path: (id) => `/chat/dm/${id}` },
];

async function openConversation(page: Page, testInfo: TestInfo, conversation: Conversation) {
  const targetId = uniqueId(testInfo, "toolbar");
  const all = buildPlainHistory(targetId);
  const scenario = createScenario({
    kind: conversation.kind,
    conversationType: conversation.conversationType,
    targetId,
    targetName: conversation.conversationType === "group" ? "Grupo E2E" : OTHER_USER_NAME,
    messages: all.slice(-PAGE_SIZE),
  });
  await installMessagingMocks(page, scenario);
  const messages = await installPaginatedMessages(page, targetId, all, conversation.kind);
  await page.goto(conversation.path(targetId));
  await expect(bubbles(page).first()).toBeVisible();
  await quiet(page);
  await settledViewport(page);
  return { messages, targetId };
}

test.describe("toolbar de reações após carregar mensagens anteriores (#839)", () => {
  // Sem espaço acima dentro da lista, a toolbar vai para baixo da bolha — e
  // continua dentro da lista, nunca sobre o header. A regra é a mesma nos três
  // tipos de conversa (é o helper compartilhado), então um navegador basta.
  test("canal: coloca a toolbar abaixo de uma bolha encostada ao topo da lista", async ({
    page,
  }, testInfo) => {
    await openConversation(page, testInfo, conversations[0]);
    // A meio da história, para haver por onde subir uma bolha até a borda.
    await list(page).evaluate((element) => {
      element.scrollTop = element.scrollHeight / 2;
    });
    await settledViewport(page);
    const id = pick(await visibleMessages(page), false);
    await scrollBubbleNearTop(page, id);
    const band = (await list(page).boundingBox())!;

    const { bubble, bar } = await hoverAndRead(page, id);
    expect(bubble.y - band.y, "bolha visível, encostada ao topo da lista").toBeLessThan(40);
    expect(bar.y, "toolbar abaixo da bolha").toBeGreaterThan(bubble.y + bubble.height);
    expect(bar.y - (bubble.y + bubble.height), "perto da bolha").toBeLessThanOrEqual(SMALL_GAP_PX);
    expect(bar.y, "toolbar dentro da lista").toBeGreaterThanOrEqual(band.y);
    expect(bar.y + bar.height, "toolbar dentro da lista").toBeLessThanOrEqual(band.y + band.height);
  });

  // Dois prepends com latência de servidor deliberada, por conversa.
  test.setTimeout(120_000);

  // A bubble filling the list has no room for the toolbar above or below it,
  // and a toolbar squeezed across the message would misattribute its actions:
  // there is none, and the timeline does not move for the attempt.
  test("canal: não mostra a toolbar numa mensagem mais alta do que a lista", async ({
    page,
  }, testInfo) => {
    const { targetId } = await openConversation(page, testInfo, conversations[0]);
    const id = `${targetId}-m-${TALL_MESSAGE_INDEX}`;
    await scrollBubbleNearTop(page, id);
    const band = (await list(page).boundingBox())!;
    const bubble = (await messageShell(page, id)
      .locator(".chat-msg-area__msg-bubble")
      .boundingBox())!;
    expect(bubble.height, "a bolha ocupa a lista inteira").toBeGreaterThan(band.height);

    const before = await scrollMetrics(page);
    await page.mouse.move(0, 0);
    await page.mouse.move(bubble.x + bubble.width / 2, band.y + band.height / 2);
    await expect(toolbar(page)).toHaveCount(0);
    const after = await scrollMetrics(page);
    expect(Math.abs(after.top - before.top), "scrollTop estável").toBeLessThanOrEqual(1);
    expect(Math.abs(after.height - before.height), "scrollHeight estável").toBeLessThanOrEqual(1);
  });

  for (const conversation of conversations) {
    test(`${conversation.label}: mantém a toolbar na bolha antes e depois de cada prepend`, async ({
      page,
    }, testInfo) => {
      const { messages } = await openConversation(page, testInfo, conversation);

      // O estado saudável, lido da própria conversa antes de qualquer página.
      const initial = await visibleMessages(page);
      const original = { received: pick(initial, false), mine: pick(initial, true) };
      const healthy = {
        received: await hoverAndMeasure(page, original.received, false),
        mine: await hoverAndMeasure(page, original.mine, true),
      };
      await expect(page.getByTestId("chat-virtual-canvas")).toHaveCount(0);

      for (const prepend of [1, 2]) {
        await loadPreviousPage(page, messages);
        await quiet(page);
        await settledViewport(page);
        await expect(page.getByTestId("chat-virtual-canvas")).toHaveCount(1);
        await expectPlacementAfterPrepend(page, healthy, original, `prepend ${prepend}`);
      }

      await expectKeyboardReach(page, original.received);
    });
  }
});
