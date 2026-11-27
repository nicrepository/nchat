/**
 * Gestos de mensagem no celular (issue #1152).
 *
 * O toque aqui é o de verdade: `Input.dispatchTouchEvent` do CDP entra pelo
 * mesmo pipeline de um dedo — Pointer Events, `touch-action`, eventos de mouse
 * de compatibilidade e `contextmenu` do long-press —, o que um `dispatchEvent`
 * sintético não reproduz. Por isso o spec roda só no Chromium; WebKit não está
 * instalado neste projeto.
 *
 * A regressão original: tocar numa mensagem mostrava a toolbar inteira (cerca
 * de 14 botões), mais larga do que qualquer tela de celular, e o clamp
 * horizontal a empurrava para fora da tela pela esquerda.
 */

import { expect, test, type CDPSession, type Locator, type Page } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_CHANNEL_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  emitConversationEvent,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  messagesFor,
  revealActions,
  selectMessageActions,
  uniqueId,
} from "../helpers/messagingApi";
import {
  PAGE_SIZE,
  bubbles,
  installPaginatedMessages,
  loadPreviousPage,
  noPageInFlight,
  quiet,
  settledViewport,
} from "../helpers/largeConversationFixture";

const WIDTHS = [320, 360, 390, 430] as const;
/** Tablet em retrato (issue #1238): touch sem hover, com sobra ao lado das bolhas. */
const TABLET = { width: 768, height: 1024 } as const;

const menu = (page: Page) => page.getByRole("toolbar", { name: "Reagir à mensagem" });
const composerQuote = (page: Page) => page.getByTestId("chat-composer-quote");

/** `prepare` corre com os mocks instalados, antes de a conversa carregar. */
async function openConversation(
  page: Page,
  testInfo: Parameters<typeof uniqueId>[0],
  prepare?: () => Promise<void>,
  { count = 12 }: { count?: number } = {},
) {
  const targetId = uniqueId(testInfo, "gestures");
  const messages = Array.from({ length: count }, (_, index) => {
    const mine = index % 2 === 1;
    const stamp = `2026-07-15T12:${String(index).padStart(2, "0")}:00.000Z`;
    return makeMessage({
      id: `${targetId}-m${index}`,
      sender_id: mine ? CURRENT_USER_ID : OTHER_USER_ID,
      sender_display_name: mine ? "E2E Autor" : OTHER_USER_NAME,
      body_text: mine ? "OK" : `Mensagem ${index} ${"longa ".repeat(index * 3)}`,
      created_at: stamp,
      updated_at: stamp,
    });
  });
  const scenario = createScenario({ kind: "channel", targetId, targetName: "Gestos", messages });
  await installMessagingMocks(page, scenario);
  await prepare?.();
  await page.goto(`/chat/channel/${targetId}`);
  await expect(page.getByTestId("chat-msg-header")).toBeVisible();
  return { scenario, messages };
}

/**
 * Segura a resposta de `allowed-emojis` até `release()`: as reações rápidas do
 * menu chegam depois de ele e o picker estarem abertos, e a sheet cresce.
 */
async function holdAllowedEmojis(page: Page) {
  let release!: () => void;
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/chat/reactions/allowed-emojis", async (route) => {
    await released;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { emojis: ["👍", "🎉", "❤️", "😂"], version: "16.0" } }),
    });
  });
  return release;
}

/** Um dedo, pelo pipeline de toque do navegador. */
class Finger {
  private constructor(private readonly cdp: CDPSession) {}

  static async on(page: Page) {
    return new Finger(await page.context().newCDPSession(page));
  }

  down(x: number, y: number) {
    return this.cdp.send("Input.dispatchTouchEvent", {
      type: "touchStart",
      touchPoints: [{ x, y }],
    });
  }

  async moveBy(from: { x: number; y: number }, dx: number, dy: number, steps = 8) {
    for (let step = 1; step <= steps; step += 1) {
      await this.cdp.send("Input.dispatchTouchEvent", {
        type: "touchMove",
        touchPoints: [{ x: from.x + (dx * step) / steps, y: from.y + (dy * step) / steps }],
      });
    }
  }

  up() {
    return this.cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  }
}

/** Um ponto de texto da bolha, longe das bordas da tela. */
async function bubblePoint(bubble: Locator) {
  const box = await bubble.locator(".chat-msg-area__msg-bubble").boundingBox();
  if (!box) throw new Error("bolha sem caixa");
  return { x: box.x + Math.min(box.width / 2, 40), y: box.y + box.height / 2 };
}

/**
 * O menu inteiro dentro da viewport visual, sem nada cortado — com os
 * deslocamentos dela (zoom, teclado), medidos no mesmo sistema de coordenadas
 * do elemento.
 */
async function expectWhollyVisible(target: Locator) {
  await expect(target).toBeVisible();
  const { box, view } = await target.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    const visual = window.visualViewport;
    const left = visual?.offsetLeft ?? 0;
    const top = visual?.offsetTop ?? 0;
    return {
      box: { left: rect.left, top: rect.top, right: rect.right, bottom: rect.bottom },
      view: {
        left,
        top,
        right: left + (visual?.width ?? window.innerWidth),
        bottom: top + (visual?.height ?? window.innerHeight),
      },
    };
  });
  // Meio pixel de folga para o arredondamento subpixel do zoom.
  expect(box.left).toBeGreaterThanOrEqual(view.left - 0.5);
  expect(box.top).toBeGreaterThanOrEqual(view.top - 0.5);
  expect(box.right).toBeLessThanOrEqual(view.right + 0.5);
  expect(box.bottom).toBeLessThanOrEqual(view.bottom + 0.5);
}

/**
 * O picker de um menu de toque não fica sob o menu: o menu é um popover, na
 * top layer, e cobre qualquer coisa que se sobreponha a ele, seja qual for o
 * z-index. As caixas não se cruzam, e o centro da célula é dela no hit-test —
 * é ali que um dedo a aciona.
 */
async function expectReachable(page: Page, cell: Locator) {
  const ownsItsCentre = await cell.evaluate((element) => {
    const box = element.getBoundingClientRect();
    const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
    return !!hit && element.contains(hit);
  });
  expect(ownsItsCentre, "outra superfície intercepta o toque na célula").toBe(true);
  const picker = await page.getByRole("dialog", { name: "Escolher reação" }).boundingBox();
  const sheet = await menu(page).boundingBox();
  const crosses =
    picker!.x < sheet!.x + sheet!.width &&
    sheet!.x < picker!.x + picker!.width &&
    picker!.y < sheet!.y + sheet!.height &&
    sheet!.y < picker!.y + picker!.height;
  expect(crosses, "o picker cruza o menu").toBe(false);
}

/**
 * Uma célula da última linha que o picker mostra inteira — a faixa que a sheet
 * cobria —, entre as que reagem direto, sem paleta de tons.
 */
async function lowestWholeCell(picker: Locator) {
  const cells = picker.locator(".chat-emoji-picker__emoji:not([aria-haspopup])");
  const index = await picker.evaluate((element) => {
    const results = element.querySelector(".chat-emoji-picker__sections")!.getBoundingClientRect();
    let lowest = -1;
    let bottom = -Infinity;
    element
      .querySelectorAll(".chat-emoji-picker__emoji:not([aria-haspopup])")
      .forEach((cell, index) => {
        const box = cell.getBoundingClientRect();
        if (box.top >= results.top && box.bottom <= results.bottom && box.bottom > bottom) {
          bottom = box.bottom;
          lowest = index;
        }
      });
    return lowest;
  });
  expect(index, "nenhuma célula inteira na área de resultados").toBeGreaterThanOrEqual(0);
  return cells.nth(index);
}

/**
 * Nada interativo cortado: os botões que o menu desenha estão dentro dele, sem
 * rolagem escondendo algum, e a busca, as abas e a área de resultados estão
 * dentro do picker.
 */
async function expectNothingCutOff(page: Page) {
  const cut = await page.evaluate(() => {
    const inside = (inner: Element, outer: DOMRect) => {
      const box = inner.getBoundingClientRect();
      if (box.width === 0 && box.height === 0) return true;
      return (
        box.left >= outer.left - 0.5 &&
        box.top >= outer.top - 0.5 &&
        box.right <= outer.right + 0.5 &&
        box.bottom <= outer.bottom + 0.5
      );
    };
    const menu = document.querySelector('[role="toolbar"][aria-label="Reagir à mensagem"]')!;
    const picker = document.querySelector('[role="dialog"][aria-label="Escolher reação"]')!;
    const parts = picker.querySelectorAll(
      ".chat-emoji-picker__search, .chat-emoji-picker__tabs, .chat-emoji-picker__sections",
    );
    return [
      ...[...menu.querySelectorAll("button")].filter(
        (button) => !inside(button, menu.getBoundingClientRect()),
      ),
      ...[...parts].filter((part) => !inside(part, picker.getBoundingClientRect())),
      ...(menu.scrollHeight > menu.clientHeight ? [menu] : []),
    ].map((element) => element.getAttribute("aria-label") ?? element.className);
  });
  expect(cut, "conteúdo interativo cortado").toEqual([]);
}

/** Menu de toque pelo botão de ações e picker aberto, com a busca focada. */
async function openTouchPicker(page: Page, target: Locator) {
  const actions = await selectMessageActions(target);
  await actions.tap();
  await expect(menu(page)).toBeVisible();
  await menu(page).getByRole("button", { name: "Mais reações" }).tap();
  const picker = page.getByRole("dialog", { name: "Escolher reação" });
  const search = picker.getByRole("searchbox", { name: "Buscar emoji" });
  await expect(search).toBeFocused();
  return { actions, picker, search };
}

/**
 * Entre o segundo e o terceiro Escape (CQR R6 da #1238, achado 3): no WebKit,
 * o mouseover que o picker fechado deixa na mensagem de baixo trocava o menu
 * de toque por um de hover e o fechava, uns 0,2 s depois, com o foco no body —
 * e o Escape seguinte encerrava a seleção. O menu de toque, o foco no "Mais
 * reações" e a seleção ficam, logo e depois de 0,6 s: a espera é o próprio
 * defeito, que vem com o tempo.
 */
async function expectTouchMenuHeld(page: Page, target: Locator) {
  const more = menu(page).getByRole("button", { name: "Mais reações" });
  for (const wait of [0, 600]) {
    if (wait) await page.waitForTimeout(wait);
    await expect(menu(page)).toHaveClass(/--touch/);
    await expect(more).toBeFocused();
    await expect(selectionMode(page)).toBeVisible();
    expect(await isSelected(target)).toBe(true);
    expect(await page.evaluate(() => document.activeElement === document.body)).toBe(false);
  }
}

/** A mesma posição de rolagem por 10 quadros seguidos. */
async function settledScroll(results: Locator) {
  return results.evaluate(
    (element) =>
      new Promise<number>((resolve) => {
        let last = -1;
        let still = 0;
        const sample = () => {
          still = element.scrollTop === last ? still + 1 : 0;
          last = element.scrollTop;
          if (still < 10) requestAnimationFrame(sample);
          else resolve(last);
        };
        requestAnimationFrame(sample);
      }),
  );
}

/**
 * A reprodução do CQ R10: busca "mão" e rola a nona célula com tons para 14 px
 * abaixo do topo dos resultados — a 320×210, y=64–108, sem espaço acima dela
 * para a paleta e com a sheet logo abaixo. A posição é escrita direto em
 * `scrollTop`, ao pixel; a rolagem pela roda tem o seu próprio teste.
 */
async function openScrolledPalette(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
  const { scenario, messages } = await openConversation(page, testInfo);
  const target = messageBubble(page, messages.at(-1)!.id);
  const { actions, picker, search } = await openTouchPicker(page, target);
  await search.fill("mão");
  const cell = picker.locator('.chat-emoji-picker__emoji[aria-haspopup="dialog"]').nth(8);
  await expect(cell).toBeAttached();
  await cell.evaluate((element) => {
    const results = element.closest(".chat-emoji-picker__sections")!;
    const offset = element.getBoundingClientRect().top - results.getBoundingClientRect().top;
    results.scrollTop += Math.round(offset - 14);
  });
  await expectReachable(page, cell);
  // A caixa de antes do toque, a que a paleta recebe: tocada, a célula pulsa
  // (`chat-emoji-pop`, até 122%).
  const cellBox = (await cell.evaluate((element) =>
    element.getBoundingClientRect().toJSON(),
  )) as DOMRect;
  await cell.tap();
  const palette = page.getByRole("dialog", { name: /^Tom de pele para / });
  await expect(palette.getByRole("button").first()).toBeFocused();
  return { scenario, messages, target, actions, picker, search, cellBox, palette };
}

/**
 * A geometria da paleta aberta: as caixas dela, da célula e do menu, o topo da
 * viewport visual, a mensagem dona, e se o centro de cada tom é dele no
 * hit-test.
 */
async function paletteGeometry(palette: Locator, cellBox: DOMRect) {
  // Medida depois da animação de abertura, que a desenha a 96% do tamanho.
  await palette.evaluate((element) =>
    Promise.all(element.getAnimations().map((animation) => animation.finished)),
  );
  return palette.evaluate((element, cellBox: DOMRect) => {
    const box = (target: Element) => target.getBoundingClientRect().toJSON() as DOMRect;
    const menu = document.querySelector('[role="toolbar"][aria-label="Reagir à mensagem"]')!;
    return {
      palette: box(element),
      cell: cellBox,
      menu: box(menu),
      viewTop: window.visualViewport?.offsetTop ?? 0,
      owner: element.getAttribute("data-reaction-picker-for"),
      hits: [...element.querySelectorAll("button")].map((option) => {
        const r = option.getBoundingClientRect();
        const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return !!hit && option.contains(hit);
      }),
    };
  }, cellBox);
}

/** O modo de seleção do header (CQR R3 da #1238) e os seus dois controles. */
const selectionMode = (page: Page) => page.getByRole("group", { name: "Mensagem selecionada" });
const selectionActions = (page: Page) => page.getByTestId("chat-selection-actions");
const cancelSelection = (page: Page) => page.getByTestId("chat-selection-cancel");
const isSelected = (target: Locator) =>
  target.evaluate((shell) => shell.classList.contains("chat-msg-area__msg--selected"));

/**
 * Os controles da conversa no header — Voltar, chamada, Agendar reunião,
 * Detalhes —, pelos nomes acessíveis, só os desenhados (Voltar só existe na
 * faixa de gaveta).
 */
async function headerControls(page: Page) {
  return page
    .getByTestId("chat-msg-header")
    .locator("button")
    .evaluateAll((buttons) =>
      buttons
        .filter((button) => button.getBoundingClientRect().width > 0)
        .map((button) => button.getAttribute("aria-label") ?? ""),
    );
}

/**
 * Cada controle é acionável de verdade: habilitado, inteiro na tela e dono do
 * próprio centro no hit-test — nada desenhado por cima.
 */
async function expectHeaderControlsActionable(page: Page, names: string[]) {
  expect(await headerControls(page)).toEqual(names);
  for (const name of names) {
    const button = page.getByTestId("chat-msg-header").getByRole("button", { name, exact: true });
    await expect(button).toBeEnabled();
    await expectWhollyVisible(button);
    await expectLinkOwnsItsBox(button);
  }
}

const isFocused = (target: Locator) =>
  target.evaluate((element) => element === document.activeElement);

const focusedLabel = (page: Page) =>
  page.evaluate(() => {
    const element = document.activeElement;
    return element?.getAttribute("aria-label") || element?.className || element?.tagName || "";
  });

async function expectNoHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
}

/**
 * Repouso (issue #1238): nenhuma mensagem desenha um botão de ações nem guarda
 * espaço para ele — o corpo da mensagem tem a largura da bolha, nem um pixel a
 * mais —, nenhuma está selecionada e o header mostra os controles da conversa
 * (CQR R3: as ações sem long-press são do modo de seleção do header).
 */
async function expectTimelineAtRest(page: Page) {
  const rows = await page.locator('[data-testid="chat-msg-bubble"]').evaluateAll((shells) =>
    shells.map((shell) => {
      const body = shell.querySelector(".chat-msg-area__msg-body")!;
      const bubble = body.querySelector(".chat-msg-area__msg-bubble")!.getBoundingClientRect();
      return {
        id: shell.getAttribute("data-message-id"),
        buttons: shell.querySelectorAll('[aria-label="Ações da mensagem"]').length,
        selected: shell.classList.contains("chat-msg-area__msg--selected"),
        reserved: body.getBoundingClientRect().width - bubble.width,
      };
    }),
  );
  expect(rows.length).toBeGreaterThan(0);
  for (const row of rows) {
    expect(row.buttons, `${row.id}: botão de ações na linha`).toBe(0);
    expect(row.selected, `${row.id}: selecionada em repouso`).toBe(false);
    expect(row.reserved, `${row.id}: espaço reservado`).toBeLessThanOrEqual(0.5);
  }
  await expect(selectionMode(page)).toHaveCount(0);
}

/**
 * Selecionar não mexe em nada (CQR R3 da #1238): a bolha, o header e a
 * timeline ficam onde estavam, do mesmo tamanho; o header troca os controles
 * da conversa — nenhum fica desenhado, encoberto, debaixo do modo — por
 * "Cancelar seleção" e "Ações da mensagem", alvos de 44px inteiros na tela, no
 * próprio header e não sobre a timeline. Só ela fica selecionada.
 */
async function expectRevealWithoutShift(page: Page, target: Locator) {
  const bubble = target.locator(".chat-msg-area__msg-bubble");
  const header = page.getByTestId("chat-msg-header");
  const list = page.locator(".chat-msg-area__list");
  const before = {
    bubble: await bubble.boundingBox(),
    header: await header.boundingBox(),
    list: await list.boundingBox(),
  };
  const controls = await headerControls(page);
  const actions = await selectMessageActions(target);
  await expect(actions).toHaveAttribute("aria-expanded", "false");
  expect(await bubble.boundingBox()).toEqual(before.bubble);
  expect(await header.boundingBox(), "o header muda de caixa").toEqual(before.header);
  expect(await list.boundingBox(), "a timeline se desloca").toEqual(before.list);
  await expect(selectionMode(page)).toBeVisible();
  expect(await headerControls(page)).toEqual(["Cancelar seleção", "Ações da mensagem"]);
  const button = await actions.boundingBox();
  for (const control of [actions, cancelSelection(page)]) {
    const box = await control.boundingBox();
    expect(box!.width).toBeGreaterThanOrEqual(44);
    expect(box!.height).toBeGreaterThanOrEqual(44);
    expect(box!.y + box!.height, "controle do modo sobre a timeline").toBeLessThanOrEqual(
      before.list!.y + 0.5,
    );
    await expectWhollyVisible(control);
    await expectLinkOwnsItsBox(control);
  }
  await expectNoHorizontalScroll(page);
  await expect(page.locator(".chat-msg-area__msg--selected")).toHaveCount(1);
  expect(await isSelected(target)).toBe(true);
  return { actions, button: button!, bubble: before.bubble!, controls };
}

for (const width of WIDTHS) {
  test.describe(`gestos de mensagem a ${width}px`, () => {
    test.use({ viewport: { width, height: 740 }, hasTouch: true, isMobile: true });

    test("toque simples não abre a toolbar; long-press abre o menu inteiro na tela", async ({
      page,
    }, testInfo) => {
      const { messages } = await openConversation(page, testInfo);
      const finger = await Finger.on(page);
      const last = messageBubble(page, messages.at(-1)!.id);
      await expect(last).toBeVisible();
      await expectTimelineAtRest(page);

      // Toque simples: a regressão abria a toolbar aqui, fora da tela.
      const tapAt = await bubblePoint(last);
      await page.touchscreen.tap(tapAt.x, tapAt.y);
      await expect(menu(page)).toHaveCount(0);

      const point = await bubblePoint(last);
      await finger.down(point.x, point.y);
      await expect(menu(page)).toBeVisible();
      await finger.up();

      // Issue #1238: reações rápidas e "Mais reações" de imediato; as outras
      // ações atrás de "Mais ações", no mesmo menu e dentro da tela.
      await expectWhollyVisible(menu(page));
      await expect(
        menu(page)
          .getByRole("button", { name: /Reagir rapidamente com/ })
          .first(),
      ).toBeVisible();
      await expect(menu(page).getByRole("button", { name: "Mais reações" })).toBeVisible();
      await expect(menu(page).getByRole("button", { name: "Responder" })).toHaveCount(0);
      const more = menu(page).getByRole("button", { name: "Mais ações" });
      await more.tap();
      await expect(more).toHaveAttribute("aria-expanded", "true");
      await expect(menu(page).getByRole("button", { name: "Responder" })).toBeVisible();
      await expect(menu(page).getByRole("button", { name: "Editar mensagem" })).toBeVisible();
      await expectWhollyVisible(menu(page));
      await expectNoHorizontalScroll(page);

      // Um menu por vez, e Escape fecha.
      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);
    });

    test("long-press na primeira mensagem visível e reação rápida no alvo certo", async ({
      page,
    }, testInfo) => {
      const { scenario, messages } = await openConversation(page, testInfo);
      const finger = await Finger.on(page);
      const list = page.locator(".chat-msg-area__list");
      await list.evaluate((node) => (node.scrollTop = 0));
      const first = messageBubble(page, messages[0].id);
      await expect(first).toBeInViewport();

      const point = await bubblePoint(first);
      await finger.down(point.x, point.y);
      await expect(menu(page)).toBeVisible();
      await finger.up();
      await expectWhollyVisible(menu(page));

      await menu(page)
        .getByRole("button", { name: /Reagir rapidamente com/ })
        .first()
        .click();
      await expect(first.locator(".chat-msg-area__reactions")).toBeVisible();
      expect(scenario.requests.reactions).toEqual([
        expect.objectContaining({ messageId: messages[0].id, added: true }),
      ]);
    });

    test("swipe para a direita responde sem enviar; curto ou vertical não", async ({
      page,
    }, testInfo) => {
      const { scenario, messages } = await openConversation(page, testInfo);
      const finger = await Finger.on(page);
      const target = messageBubble(page, messages.at(-2)!.id);
      await expect(target).toBeVisible();
      const input = page.getByTestId("chat-composer-input");

      // O rascunho existe antes de qualquer gesto: o swipe tem de preservá-lo.
      await fillComposer(page, "rascunho antes do swipe");

      // Curto: abaixo do limiar, nada acontece.
      let point = await bubblePoint(target);
      await finger.down(point.x, point.y);
      await finger.moveBy(point, 30, 0);
      await finger.up();
      await expect(composerQuote(page)).toHaveCount(0);

      // Vertical: é rolagem, não resposta.
      point = await bubblePoint(target);
      await finger.down(point.x, point.y);
      await finger.moveBy(point, 4, -120);
      await finger.up();
      await expect(composerQuote(page)).toHaveCount(0);
      await expect(menu(page)).toHaveCount(0);

      // Deliberado: responde à mensagem certa, uma vez, sem enviar nada.
      await target.scrollIntoViewIfNeeded();
      point = await bubblePoint(target);
      await finger.down(point.x, point.y);
      await finger.moveBy(point, 110, 6);
      await finger.up();
      await expect(composerQuote(page)).toHaveCount(1);
      await expect(composerQuote(page)).toContainText(`Mensagem ${messages.length - 2} longa`);
      await expect(target).toHaveCSS("transform", "none");
      await expectNoHorizontalScroll(page);

      // Contrato do composer: responder anexa o quote e mantém o texto já
      // digitado, intacto; nada é enviado sozinho.
      await expect(input).toHaveText("rascunho antes do swipe");
      expect(scenario.requests.channelPosts).toHaveLength(0);
    });

    test("o botão de ações abre o mesmo menu sem long-press", async ({ page }, testInfo) => {
      const { scenario, messages } = await openConversation(page, testInfo);
      const target = messageBubble(page, messages.at(-1)!.id);
      await expect(target).toBeVisible();
      await expectTimelineAtRest(page);

      // Issue #1238: um toque simples seleciona a mensagem e só então desenha o
      // botão — na barra sobre o header, longe da bolha, sem cobri-la.
      const { actions, button, bubble } = await expectRevealWithoutShift(page, target);
      const overlaps =
        button.x < bubble.x + bubble.width &&
        bubble.x < button.x + button.width &&
        button.y < bubble.y + bubble.height &&
        bubble.y < button.y + button.height;
      expect(overlaps).toBe(false);
      // Selecionar não é agir: nada abriu, nada foi pedido ao servidor.
      await expect(menu(page)).toHaveCount(0);
      expect(scenario.requests.reactions).toEqual([]);

      await page.touchscreen.tap(button.x + button.width / 2, button.y + button.height / 2);
      await expect(menu(page)).toBeVisible();
      await expect(actions).toHaveAttribute("aria-expanded", "true");
      const menuId = await menu(page).getAttribute("id");
      await expect(actions).toHaveAttribute("aria-controls", menuId!);
      await expectWhollyVisible(menu(page));

      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);
      await expect(actions).toHaveAttribute("aria-expanded", "false");
      await expect(actions).toBeFocused();
    });

    test("teclado de verdade: Tab seleciona, Enter abre o menu, Escape fecha e encerra (CQR #1238)", async ({
      page,
    }, testInfo) => {
      const { messages } = await openConversation(page, testInfo);
      const target = messageBubble(page, messages.at(-1)!.id);
      await expect(target).toBeVisible();
      await expectTimelineAtRest(page);
      const controls = await headerControls(page);

      // Do fim do documento para trás, só com Shift+Tab, até a última
      // mensagem: chegar a ela pelo teclado a seleciona, sem toque e sem
      // `.focus()`. Nenhum toque antes — o eco de 800ms de um toque mudaria o
      // que o foco revela.
      const visited: string[] = [];
      for (let step = 0; step < 20 && !(await isFocused(target)); step += 1) {
        await page.keyboard.press("Shift+Tab");
        visited.push(await focusedLabel(page));
      }
      expect(await isFocused(target), `caminho do Tab: ${visited.join(" → ")}`).toBe(true);
      await expect.poll(() => isSelected(target)).toBe(true);
      await expect(selectionMode(page)).toBeVisible();
      await expect(selectionActions(page)).toHaveCSS("width", "44px");
      await expect(selectionActions(page)).toHaveAttribute("aria-expanded", "false");
      await expect(page.locator(".chat-msg-area__reaction-menu--touch")).toHaveCount(0);

      // Enter na mensagem selecionada abre o mesmo menu que o botão do header.
      await page.keyboard.press("Enter");
      await expect(menu(page)).toBeVisible();
      await expect(menu(page)).toHaveClass(/chat-msg-area__reaction-menu--touch/);
      await expect(selectionActions(page)).toHaveAttribute("aria-expanded", "true");
      const menuId = await menu(page).getAttribute("id");
      await expect(selectionActions(page)).toHaveAttribute("aria-controls", menuId!);
      await expect
        .poll(() => menu(page).evaluate((element) => element.contains(document.activeElement)))
        .toBe(true);
      await expectWhollyVisible(menu(page));
      for (const name of [/Reagir rapidamente com/, "Mais reações", "Mais ações"]) {
        await expect(menu(page).getByRole("button", { name }).first()).toBeEnabled();
      }

      // O primeiro Escape é do menu; a seleção fica.
      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);
      await expect(target).toBeFocused();
      await expect(selectionActions(page)).toHaveAttribute("aria-expanded", "false");
      await expect(selectionMode(page)).toBeVisible();

      // O segundo encerra a seleção; o foco segue na mensagem.
      await page.keyboard.press("Escape");
      await expect(selectionMode(page)).toHaveCount(0);
      await expect(target).toBeFocused();
      expect(await isSelected(target)).toBe(false);
      await expectHeaderControlsActionable(page, controls);

      // O botão do header também é do teclado: focado, Enter abre o menu e
      // Escape devolve o foco a ele.
      await page.keyboard.press("Shift+Tab");
      await page.keyboard.press("Tab");
      await expect(selectionMode(page)).toBeVisible();
      await selectionActions(page).focus();
      await page.keyboard.press("Enter");
      await expect(menu(page)).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);
      await expect(selectionActions(page)).toBeFocused();
    });

    test("a seleção é uma só, sem mexer na bolha que ocupa a linha, e sai com o foco", async ({
      page,
    }, testInfo) => {
      const { messages } = await openConversation(page, testInfo);
      // Recebida e longa: a linha ocupa a coluna inteira, sem sobra ao lado.
      const wide = messageBubble(page, messages.at(-2)!.id);
      await wide.scrollIntoViewIfNeeded();
      await expectTimelineAtRest(page);
      const { controls } = await expectRevealWithoutShift(page, wide);

      // Outra mensagem selecionada leva a seleção: uma por vez.
      const short = messageBubble(page, messages.at(-1)!.id);
      await expectRevealWithoutShift(page, short);
      expect(await isSelected(wide)).toBe(false);

      // O foco saindo das mensagens devolve a timeline e o header ao repouso.
      await page.getByTestId("chat-composer-input").tap();
      await expectTimelineAtRest(page);
      await expect(menu(page)).toHaveCount(0);
      await expectHeaderControlsActionable(page, controls);
    });

    test("Mais reações abre o picker completo dentro da tela", async ({ page }, testInfo) => {
      const { messages } = await openConversation(page, testInfo);
      const finger = await Finger.on(page);
      const last = messageBubble(page, messages.at(-1)!.id);
      const point = await bubblePoint(last);
      await finger.down(point.x, point.y);
      await expect(menu(page)).toBeVisible();
      await finger.up();

      await menu(page).getByRole("button", { name: "Mais reações" }).click();
      const picker = page.getByRole("dialog", { name: "Escolher reação" });
      await expect(picker).toBeVisible();
      await expectWhollyVisible(picker);
    });

    test("um resultado da busca do picker é tocável e reage uma vez na mensagem certa", async ({
      page,
    }, testInfo) => {
      const { scenario, messages } = await openConversation(page, testInfo);
      const target = messageBubble(page, messages.at(-2)!.id);
      const { picker, search } = await openTouchPicker(page, target);
      await search.fill("foguete");
      const rocket = picker.getByRole("button", { name: "foguete" });
      await expect(rocket).toBeVisible();

      await expectWhollyVisible(picker);
      await expectReachable(page, rocket);
      await rocket.tap();
      await expect(picker).toHaveCount(0);
      await expect(target.locator(".chat-msg-area__reactions")).toContainText("🚀");
      expect(scenario.requests.reactions).toEqual([
        { messageId: messages.at(-2)!.id, emoji: "🚀", added: true },
      ]);
    });

    test("a paleta de tons de um resultado da busca abre, escolhe e devolve o foco", async ({
      page,
    }, testInfo) => {
      const { scenario, messages } = await openConversation(page, testInfo);
      const target = messageBubble(page, messages.at(-2)!.id);
      const { actions, picker, search } = await openTouchPicker(page, target);
      await search.fill("polegar para cima");
      const thumbs = picker.getByRole("button", { name: "polegar para cima" });
      await expectReachable(page, thumbs);
      await thumbs.tap();
      const palette = page.getByRole("dialog", { name: "Tom de pele para polegar para cima" });
      await expect(palette.getByRole("button").first()).toBeFocused();
      await expect(menu(page)).toHaveClass(/--touch/);

      // Escape fecha um nível por vez e devolve o foco a quem abriu cada um.
      await page.keyboard.press("Escape");
      await expect(palette).toHaveCount(0);
      await expect(thumbs).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(picker).toHaveCount(0);
      await expect(menu(page).getByRole("button", { name: "Mais reações" })).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);
      await expect(actions).toBeFocused();
      expect(scenario.requests.reactions).toEqual([]);

      // De novo, até o fim: o tom escolhido reage uma vez, na mensagem certa.
      await openTouchPicker(page, target);
      await search.fill("polegar para cima");
      await thumbs.tap();
      const tone = palette.getByRole("button").nth(3);
      const emoji = (await tone.textContent())!.trim();
      await tone.tap();
      await expect(palette).toHaveCount(0);
      await expect(picker).toHaveCount(0);
      await expect(target.locator(".chat-msg-area__reactions")).toContainText(emoji);
      expect(scenario.requests.reactions).toEqual([
        { messageId: messages.at(-2)!.id, emoji, added: true },
      ]);
    });

    test("a sheet que cresce com o picker aberto leva o picker junto, ainda tocável", async ({
      page,
    }, testInfo) => {
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      let release!: () => void;
      const { scenario, messages } = await openConversation(page, testInfo, async () => {
        release = await holdAllowedEmojis(page);
      });
      const target = messageBubble(page, messages.at(-2)!.id);
      const { picker, search } = await openTouchPicker(page, target);
      await expect(picker.locator(".chat-emoji-picker__emoji").first()).toBeVisible();
      await expectReachable(page, await lowestWholeCell(picker));
      // As reações rápidas chegam com o picker aberto, sem fechá-lo nem tirar o
      // foco da busca.
      release();
      await expect(
        menu(page)
          .getByRole("button", { name: /Reagir rapidamente com/ })
          .first(),
      ).toBeVisible();
      await expect(picker).toHaveCount(1);
      await expect(search).toBeFocused();
      const before = (await menu(page).boundingBox())!;

      // O menu compacto (issue #1238) só cresce quando o leitor pede as outras
      // ações: a sheet ganha linhas, para cima.
      const more = menu(page).getByRole("button", { name: "Mais ações" });
      await more.tap();
      await expect(more).toHaveAttribute("aria-expanded", "true");
      await expect
        .poll(async () => (await menu(page).boundingBox())!.height)
        .toBeGreaterThan(before.height);
      const after = (await menu(page).boundingBox())!;
      expect(after.y).toBeLessThan(before.y);

      // O picker acompanhou, sem fechar, e parou: a posição dele é a mesma por
      // 30 quadros seguidos.
      await expect(picker).toHaveCount(1);
      const tops = await picker.evaluate(
        (element) =>
          new Promise<string[]>((resolve) => {
            const seen: string[] = [];
            const sample = () => {
              seen.push(element.style.top);
              if (seen.length < 30) requestAnimationFrame(sample);
              else resolve(seen);
            };
            requestAnimationFrame(sample);
          }),
      );
      expect(new Set(tops).size).toBe(1);
      await expectWhollyVisible(picker);
      const cell = await lowestWholeCell(picker);
      await expectReachable(page, cell);

      const emoji = (await cell.textContent())!.trim();
      await cell.tap();
      await expect(picker).toHaveCount(0);
      await expect(target.locator(".chat-msg-area__reactions")).toContainText(emoji);
      expect(scenario.requests.reactions).toEqual([
        { messageId: messages.at(-2)!.id, emoji, added: true },
      ]);
      expect(errors).toEqual([]);
    });
  });
}

/**
 * Telas baixas: a 320×220 a sheet inteira e um picker com uma linha de
 * resultados não cabem juntos, e o menu fica só com "Mais reações" enquanto o
 * picker está aberto, que por sua vez deixa as abas — busca e resultados
 * ficam. Nas outras alturas tudo cabe e nada muda.
 */
for (const [width, height, cramped] of [
  [320, 210, true],
  [320, 220, true],
  [320, 320, false],
  [360, 440, false],
  [390, 740, false],
  [430, 740, false],
] as const) {
  test.describe(`picker de toque a ${width}×${height}`, () => {
    test.use({ viewport: { width, height }, hasTouch: true, isMobile: true });

    test("busca, rolagem e seleção com o menu e o picker na mesma tela", async ({
      page,
    }, testInfo) => {
      const { scenario, messages } = await openConversation(page, testInfo);
      const target = messageBubble(page, messages.at(-1)!.id);
      const { picker, search } = await openTouchPicker(page, target);
      await expect(picker.locator(".chat-emoji-picker__emoji").first()).toBeVisible();
      await expectReachable(page, await lowestWholeCell(picker));
      await expectWhollyVisible(picker);
      await expectWhollyVisible(menu(page));
      await expectNothingCutOff(page);
      await expect(menu(page).getByRole("button", { name: "Mais reações" })).toBeVisible();
      await expect(menu(page).getByRole("button", { name: "Mais ações" })).toBeVisible({
        visible: !cramped,
      });
      await expect(picker.getByRole("tablist")).toBeVisible({ visible: !cramped });

      // A lista rola, e o que aparece também é tocável. Pela roda, não pelo
      // dedo: depois de uma rolagem por toque do CDP, o Chromium headless às
      // vezes não sintetiza o clique do toque seguinte, sem nenhum handler da
      // página envolvido (r9-scroll-tap-probe) — fica para o WebKit e o
      // aparelho. Parada é a mesma posição por 10 quadros seguidos. A roda leva
      // a primeira linha escondida abaixo ao topo dos resultados, como o leitor
      // faria: a 320×210 a área tem menos de duas linhas, e uma distância
      // qualquer pode deixar todas cortadas.
      const results = picker.locator(".chat-emoji-picker__sections");
      const area = (await results.boundingBox())!;
      const nextRow = await results.evaluate((element) => {
        const box = element.getBoundingClientRect();
        const hidden = [...element.querySelectorAll(".chat-emoji-picker__emoji")]
          .map((cell) => cell.getBoundingClientRect())
          .find((cell) => cell.bottom > box.bottom);
        return hidden!.top - box.top;
      });
      await page.mouse.move(area.x + area.width / 2, area.y + area.height / 2);
      await page.mouse.wheel(0, nextRow);
      expect(await settledScroll(results)).toBeGreaterThan(0);
      await expect(search).toBeFocused();
      const cell = await lowestWholeCell(picker);
      await expectReachable(page, cell);
      const emoji = (await cell.textContent())!.trim();
      await cell.tap();
      await expect(picker).toHaveCount(0);
      await expect(target.locator(".chat-msg-area__reactions")).toContainText(emoji);
      expect(scenario.requests.reactions).toEqual([
        { messageId: messages.at(-1)!.id, emoji, added: true },
      ]);
    });

    test("a paleta abre e fecha, e Escape devolve o foco e o menu inteiro", async ({
      page,
    }, testInfo) => {
      const { scenario, messages } = await openConversation(page, testInfo);
      const target = messageBubble(page, messages.at(-1)!.id);
      const { actions, picker, search } = await openTouchPicker(page, target);
      await search.fill("polegar para cima");
      const thumbs = picker.getByRole("button", { name: "polegar para cima" });
      await expectReachable(page, thumbs);
      await expectNothingCutOff(page);
      await thumbs.tap();
      const palette = page.getByRole("dialog", { name: "Tom de pele para polegar para cima" });
      const option = palette.getByRole("button").first();
      await expect(option).toBeFocused();
      await expect(menu(page)).toHaveClass(/--touch/);
      await expectWhollyVisible(palette);
      const ownsItsCentre = await option.evaluate((element) => {
        const box = element.getBoundingClientRect();
        const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
        return !!hit && element.contains(hit);
      });
      expect(ownsItsCentre, "outra superfície intercepta o toque na paleta").toBe(true);

      await page.keyboard.press("Escape");
      await expect(palette).toHaveCount(0);
      await expect(thumbs).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(picker).toHaveCount(0);
      await expect(menu(page).getByRole("button", { name: "Mais reações" })).toBeFocused();
      // O picker fechado devolve ao menu as ações que tinha deixado de lado.
      await expect(menu(page).getByRole("button", { name: "Mais ações" })).toBeVisible();
      await expectWhollyVisible(menu(page));
      await expectTouchMenuHeld(page, target);
      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);
      await expect(actions).toBeFocused();
      expect(scenario.requests.reactions).toEqual([]);
    });

    test("a paleta de uma célula rolada fica inteira fora do menu e aplica um só tom", async ({
      page,
    }, testInfo) => {
      const { scenario, messages, target, picker, cellBox, palette } = await openScrolledPalette(
        page,
        testInfo,
      );
      const geometry = await paletteGeometry(palette, cellBox);
      await testInfo.attach("geometria", {
        body: JSON.stringify(geometry),
        contentType: "application/json",
      });
      if (height === 210) {
        expect(Math.round(geometry.cell.top), "a célula da reprodução").toBe(64);
      }
      expect(geometry.owner).toBe(messages.at(-1)!.id);
      expect(geometry.hits, "outra superfície intercepta o toque na paleta").toEqual(
        Array(6).fill(true),
      );
      await expectWhollyVisible(palette);
      const crosses =
        geometry.palette.top < geometry.menu.bottom && geometry.menu.top < geometry.palette.bottom;
      expect(crosses, "a paleta cruza o menu").toBe(false);
      // Onde cabe acima da célula, continua acima dela, como antes: 6 px acima,
      // menos os 4% da altura que a medida pega no meio da animação de
      // abertura (scale 0.96), comportamento anterior a esta correção.
      if (geometry.cell.top - geometry.palette.height - 6 >= geometry.viewTop + 8) {
        expect(geometry.palette.bottom).toBeGreaterThanOrEqual(geometry.cell.top - 6 - 0.5);
        expect(geometry.palette.bottom).toBeLessThanOrEqual(geometry.cell.top);
      }

      // Tab não sai da paleta.
      await page.keyboard.press("Tab");
      await expect(palette.getByRole("button").first()).toBeFocused();
      const option = palette.getByRole("button").nth(3);
      const emoji = (await option.textContent())!.trim();
      await option.tap();
      await expect(palette).toHaveCount(0);
      await expect(picker).toHaveCount(0);
      await expect(target.locator(".chat-msg-area__reactions")).toContainText(emoji);
      expect(scenario.requests.reactions).toEqual([
        { messageId: messages.at(-1)!.id, emoji, added: true },
      ]);
    });
  });
}

/**
 * O ciclo de vida da paleta na tela da reprodução do CQ R10: o que a fecha sem
 * reação, e o que acontece quando a mensagem ou a conversa vão embora.
 */
test.describe("paleta de tons a 320×210", () => {
  test.use({ viewport: { width: 320, height: 210 }, hasTouch: true, isMobile: true });

  test("um toque fora da paleta a fecha, sem reagir", async ({ page }, testInfo) => {
    const { scenario, picker, search, palette } = await openScrolledPalette(page, testInfo);
    await search.tap();
    await expect(palette).toHaveCount(0);
    await expect(picker).toBeVisible();
    expect(scenario.requests.reactions).toEqual([]);
  });

  test("a mensagem removida leva menu, picker e paleta junto", async ({ page }, testInfo) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const { scenario, messages, picker, palette } = await openScrolledPalette(page, testInfo);
    const targetId = messages.at(-1)!.id.replace(/-m\d+$/, "");
    const stored = messagesFor(scenario, "channel", targetId);
    const index = stored.findIndex((message) => message.id === messages.at(-1)!.id);
    stored[index] = makeMessage({
      ...stored[index],
      body_text: undefined,
      status: "deleted",
      is_removed: true,
      deleted_at: "2026-07-15T12:30:00.000Z",
      updated_at: "2026-07-15T12:30:00.000Z",
    });
    await emitConversationEvent(page, scenario, {
      kind: "channel",
      targetId,
      message: stored[index],
    });
    await expect(palette).toHaveCount(0);
    await expect(picker).toHaveCount(0);
    await expect(menu(page)).toHaveCount(0);
    expect(scenario.requests.reactions).toEqual([]);
    expect(errors).toEqual([]);
  });

  test("trocar de conversa fecha menu, picker e paleta", async ({ page }, testInfo) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const { scenario, picker, palette } = await openScrolledPalette(page, testInfo);
    await page.evaluate((id) => {
      history.pushState(null, "", `/chat/channel/${id}`);
      dispatchEvent(new PopStateEvent("popstate"));
    }, OTHER_CHANNEL_ID);
    await expect(page).toHaveURL(`/chat/channel/${OTHER_CHANNEL_ID}`);
    await expect(palette).toHaveCount(0);
    await expect(picker).toHaveCount(0);
    await expect(menu(page)).toHaveCount(0);
    expect(scenario.requests.reactions).toEqual([]);
    expect(errors).toEqual([]);
  });
});

test.describe("gestos a 360px numa timeline longa, depois de prepend", () => {
  test.use({ viewport: { width: 360, height: 740 }, hasTouch: true, isMobile: true });

  test("long-press e swipe seguem a mensagem certa na timeline virtualizada", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "gestures-long");
    const all = Array.from({ length: PAGE_SIZE * 3 }, (_, index) => {
      const stamp = `2026-07-1${Math.floor(index / 60)}T12:${String(index % 60).padStart(2, "0")}:00.000Z`;
      return makeMessage({
        id: `${targetId}-m${index}`,
        sender_id: index % 3 === 0 ? CURRENT_USER_ID : OTHER_USER_ID,
        sender_display_name: index % 3 === 0 ? "E2E Autor" : OTHER_USER_NAME,
        body_text: `Mensagem ${index} ${"texto ".repeat(index % 7)}`,
        created_at: stamp,
        updated_at: stamp,
      });
    });
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Gestos longos",
      messages: all.slice(-PAGE_SIZE),
    });
    await installMessagingMocks(page, scenario);
    const tracker = await installPaginatedMessages(page, targetId, all);
    await page.goto(`/chat/channel/${targetId}`);
    await expect(bubbles(page).first()).toBeVisible();
    await quiet(page);
    await settledViewport(page);

    await loadPreviousPage(page, tracker);
    await quiet(page);
    await settledViewport(page);
    await expect(page.getByTestId("chat-virtual-canvas")).toHaveCount(1);

    const finger = await Finger.on(page);
    const shell = page.locator("[data-testid=chat-msg-bubble]").filter({
      has: page.locator(".chat-msg-area__msg-bubble"),
    });
    // A primeira linha inteira na tela depois do prepend.
    const target = shell.nth(1);
    await target.scrollIntoViewIfNeeded();
    const targetId1 = await target.getAttribute("data-message-id");

    let point = await bubblePoint(target);
    await finger.down(point.x, point.y);
    await expect(menu(page)).toBeVisible();
    await finger.up();
    await expectWhollyVisible(menu(page));
    // O menu pertence à mensagem pressionada, não à que o virtualizer
    // reciclou para a mesma posição.
    await expect(page.locator(`[data-message-id="${targetId1}"]`).getByRole("toolbar")).toHaveCount(
      1,
    );
    await page.keyboard.press("Escape");
    await expect(menu(page)).toHaveCount(0);

    point = await bubblePoint(target);
    await finger.down(point.x, point.y);
    await finger.moveBy(point, 110, 4);
    await finger.up();
    const original = all.find((message) => message.id === targetId1)!;
    await expect(composerQuote(page)).toContainText(original.body_text!.trim());
    expect(scenario.requests.channelPosts).toHaveLength(0);
    await expectNoHorizontalScroll(page);
  });
});

/**
 * O botão de ações não cobre nada da mensagem (Code Quality Review da #1238):
 * a 360px a bolha recebida larga ia de x=61 a 348 e o botão, de 304 a 348 —
 * por cima do texto, e o hit-test de um link longo achava o botão. No header
 * (CQR R3), não cobre nada dela.
 */
async function expectCoversNothing(target: Locator) {
  const covered = await target.evaluate((shell) => {
    const button = document.querySelector('[data-testid="chat-selection-actions"]')!;
    const box = button.getBoundingClientRect();
    return [...shell.querySelectorAll<HTMLElement>("*")]
      .filter((element) => !element.contains(button) && !button.contains(element))
      .filter((element) => getComputedStyle(element).visibility === "visible")
      .filter((element) => {
        const rect = element.getBoundingClientRect();
        return (
          rect.width > 0 &&
          rect.height > 0 &&
          rect.left < box.right &&
          box.left < rect.right &&
          rect.top < box.bottom &&
          box.top < rect.bottom
        );
      })
      .map((element) => element.className || element.tagName);
  });
  expect(covered, "o botão cobre conteúdo da mensagem").toEqual([]);
}

/** Cada linha do link (ou controle) é dele no hit-test, nas pontas e no meio. */
async function expectLinkOwnsItsBox(link: Locator) {
  const lost = await link.evaluate((anchor) =>
    [...anchor.getClientRects()].flatMap((rect) =>
      [rect.left + 1, rect.left + rect.width / 2, rect.right - 1]
        .map((x) => ({ x, y: rect.top + rect.height / 2 }))
        .filter(({ x, y }) => !anchor.contains(document.elementFromPoint(x, y)))
        .map(({ x, y }) => `${Math.round(x)},${Math.round(y)}`),
    ),
  );
  expect(lost, "pontos do link tomados por outro elemento").toEqual([]);
}

/**
 * A linha de uma mensagem própria longa ocupa a coluna inteira da timeline —
 * a medida de leitura (560px) ou a coluna, a menor (re-review da #1238: um
 * `max-width: min(560px, 100% - 49px)` tirava 49px de toda mensagem própria
 * longa no toque, em repouso). A referência é a coluna, não o corpo da própria
 * mensagem: comparar a bolha com o seu corpo não via a redução vinda de fora.
 */
async function expectFillsColumn(target: Locator) {
  const geometry = await target.evaluate((shell) => {
    const column = shell.closest(".chat-msg-area__list-content")!;
    const style = getComputedStyle(column);
    const box = column.getBoundingClientRect();
    const left = box.left + parseFloat(style.paddingLeft) + parseFloat(style.borderLeftWidth);
    const right = box.right - parseFloat(style.paddingRight) - parseFloat(style.borderRightWidth);
    const bubble = shell.querySelector(".chat-msg-area__msg-bubble")!.getBoundingClientRect();
    return { column: right - left, right, bubble: { left: bubble.left, right: bubble.right } };
  });
  const expected = Math.min(560, geometry.column);
  expect(geometry.bubble.right, "a bolha própria encosta no fim da coluna").toBeCloseTo(
    geometry.right,
    0,
  );
  expect(
    geometry.bubble.right - geometry.bubble.left,
    `largura útil da bolha própria longa (coluna ${geometry.column}px)`,
  ).toBeCloseTo(expected, 0);
}

for (const width of [320, 360, 390] as const) {
  test.describe(`densidade da mensagem própria longa a ${width}px (re-review #1238)`, () => {
    test.use({ viewport: { width, height: 740 }, hasTouch: true, isMobile: true });

    test("ocupa a coluna em repouso e selecionada, sem cobrir menção nem anexo", async ({
      page,
    }, testInfo) => {
      const targetId = uniqueId(testInfo, "density");
      const mentionedId = "22222222-2222-4222-8222-222222222222";
      const long =
        "segue o resumo da revisão de hoje com os pontos que ainda precisam de resposta antes da reunião de amanhã";
      const attachmentId = `${targetId}-pdf`;
      const messages = [
        makeMessage({
          id: `${targetId}-hello`,
          sender_id: OTHER_USER_ID,
          sender_display_name: OTHER_USER_NAME,
          body_text: "Bom dia!",
          created_at: "2026-07-15T12:00:00.000Z",
          updated_at: "2026-07-15T12:00:00.000Z",
        }),
        makeMessage({
          id: `${targetId}-mention`,
          sender_id: CURRENT_USER_ID,
          sender_display_name: "E2E Autor",
          body_text: `Combinado com @[Marina Costa](mention:user:${mentionedId}), ${long}.`,
          body_format: "v3",
          created_at: "2026-07-15T12:01:00.000Z",
          updated_at: "2026-07-15T12:01:00.000Z",
        }),
        makeMessage({
          id: `${targetId}-file`,
          sender_id: CURRENT_USER_ID,
          sender_display_name: "E2E Autor",
          body_text: `Anexei o documento: ${long}.`,
          attachments: [
            {
              id: attachmentId,
              filename: "revisao-final-da-especificacao.pdf",
              content_type: "application/pdf",
              size: 48_213,
              status: "clean",
              preview_status: "unsupported",
            },
          ],
          created_at: "2026-07-15T12:02:00.000Z",
          updated_at: "2026-07-15T12:02:00.000Z",
        }),
      ];
      await installMessagingMocks(
        page,
        createScenario({ kind: "channel", targetId, targetName: "Densidade", messages }),
      );
      await page.goto(`/chat/channel/${targetId}`);
      await expect(page.getByTestId("chat-msg-header")).toBeVisible();
      // O header chega antes da timeline (CQR R8): medir antes de as mensagens
      // da fixture montarem lia zero bolhas. Espera a do anexo, a última, e
      // todas as três.
      await expect(
        page.getByTestId(`chat-message-attachment-download-${attachmentId}`),
      ).toBeVisible();
      await expect(page.locator('[data-testid="chat-msg-bubble"]')).toHaveCount(messages.length);
      await expectTimelineAtRest(page);
      await expectNoHorizontalScroll(page);

      const controls = [
        page.getByRole("button", { name: "Abrir conversa com Marina Costa" }),
        page.getByTestId(`chat-message-attachment-download-${attachmentId}`),
      ];
      for (const [index, message] of messages.slice(1).entries()) {
        const target = messageBubble(page, message.id);
        await target.scrollIntoViewIfNeeded();
        await expectFillsColumn(target);
        // Seleciona pelo começo do texto, longe da menção e do anexo.
        const bubble = await target.locator(".chat-msg-area__msg-bubble").boundingBox();
        await page.touchscreen.tap(bubble!.x + 14, bubble!.y + 12);
        await expectRevealWithoutShift(page, target);
        await expectFillsColumn(target);
        await expectCoversNothing(target);
        await expectLinkOwnsItsBox(controls[index]);
        await expect(controls[index]).toBeEnabled();
      }
    });
  });
}

test.describe("botão de ações sem cobrir conteúdo a 360×740 (CQR #1238)", () => {
  test.use({ viewport: { width: 360, height: 740 }, hasTouch: true, isMobile: true });

  const URL = `https://docs.example.test/${"especificacao-da-revisao/".repeat(6)}final`;

  test("bolha larga com link longo, própria larga e própria curta", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "cover");
    const link = (ordinal: number) => ({
      ordinal,
      target_key: "ab".repeat(16),
      text: URL,
      url: URL,
      hostname: "docs.example.test",
      safety: "safe" as const,
      click: "direct" as const,
      href: URL,
      updated_at: "2026-07-15T12:00:00.000Z",
    });
    const wide = (id: string, mine: boolean, minute: number) =>
      makeMessage({
        id: `${targetId}-${id}`,
        sender_id: mine ? CURRENT_USER_ID : OTHER_USER_ID,
        sender_display_name: mine ? "E2E Autor" : OTHER_USER_NAME,
        body_text: `Segue o documento para revisão antes da reunião: ${URL}`,
        body_format: "v2",
        link_safety_state: "safe",
        links: [link(0)],
        created_at: `2026-07-15T12:0${minute}:00.000Z`,
        updated_at: `2026-07-15T12:0${minute}:00.000Z`,
      });
    const messages = [
      wide("received", false, 1),
      wide("mine", true, 3),
      makeMessage({
        id: `${targetId}-short`,
        sender_id: CURRENT_USER_ID,
        sender_display_name: "E2E Autor",
        body_text: "OK",
        created_at: "2026-07-15T12:05:00.000Z",
        updated_at: "2026-07-15T12:05:00.000Z",
      }),
    ];
    const scenario = createScenario({ kind: "channel", targetId, targetName: "Links", messages });
    await installMessagingMocks(page, scenario);
    await page
      .context()
      .route("https://docs.example.test/**", (route) =>
        route.fulfill({ status: 200, contentType: "text/html", body: "<title>doc</title>" }),
      );
    await page.goto(`/chat/channel/${targetId}`);
    await expect(page.getByTestId("chat-msg-header")).toBeVisible();
    await expectTimelineAtRest(page);

    for (const message of messages) {
      const target = messageBubble(page, message.id);
      await target.scrollIntoViewIfNeeded();
      // Seleciona por um ponto sem link: o canto do texto, antes da URL.
      const bubble = await target.locator(".chat-msg-area__msg-bubble").boundingBox();
      await page.touchscreen.tap(bubble!.x + 14, bubble!.y + 12);
      const { actions } = await expectRevealWithoutShift(page, target);
      await expectCoversNothing(target);
      await expect(actions).toHaveAttribute("aria-label", "Ações da mensagem");
    }

    // O link da recebida larga segue acionável com ela selecionada.
    const received = messageBubble(page, messages[0].id);
    await received.scrollIntoViewIfNeeded();
    const bubble = await received.locator(".chat-msg-area__msg-bubble").boundingBox();
    await page.touchscreen.tap(bubble!.x + 14, bubble!.y + 12);
    await selectMessageActions(received);
    const anchor = received.locator(`a.rtr-link[href="${URL}"]`);
    await expectLinkOwnsItsBox(anchor);
    const lastLine = await anchor.evaluate((element) => {
      const rects = [...element.getClientRects()];
      const rect = rects[rects.length - 1];
      return { x: rect.right - 4, y: rect.top + rect.height / 2 };
    });
    const popup = page.waitForEvent("popup");
    await page.touchscreen.tap(lastLine.x, lastLine.y);
    expect((await popup).url()).toBe(URL);
  });
});

/**
 * `setViewportSize` resolve com `innerWidth`/`innerHeight` já novos, mas o
 * `resize` só chega à página no frame seguinte (CQR #1238). Antes dele o menu
 * ancorado ainda está onde a tela antiga o pôs — um estado que nunca é pintado.
 * O callback do primeiro frame corre depois do `resize`.
 */
async function resizeDelivered(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())));
}

test.describe("gestos a 390px: rotação e redimensionamento", () => {
  test.use({ viewport: { width: 390, height: 740 }, hasTouch: true, isMobile: true });

  test("o menu segue dentro da tela depois de girar o aparelho", async ({ page }, testInfo) => {
    const { messages } = await openConversation(page, testInfo);
    const finger = await Finger.on(page);
    const last = messageBubble(page, messages.at(-1)!.id);
    const point = await bubblePoint(last);
    await finger.down(point.x, point.y);
    await expect(menu(page)).toBeVisible();
    await finger.up();

    await page.setViewportSize({ width: 740, height: 390 });
    await resizeDelivered(page);
    // A âncora segue na faixa da lista depois de girar: o menu continua aberto,
    // inteiro na tela — `expectWhollyVisible` espera ele sair do frame em que se
    // esconde enquanto a lista ainda não rolou.
    await expectWhollyVisible(menu(page));
    await expectNoHorizontalScroll(page);
  });

  test("o picker segue fora do menu e dentro da tela entre larguras", async ({
    page,
  }, testInfo) => {
    const { messages } = await openConversation(page, testInfo);
    const target = messageBubble(page, messages.at(-2)!.id);
    const { picker, search } = await openTouchPicker(page, target);
    await search.fill("foguete");
    const rocket = picker.getByRole("button", { name: "foguete" });
    for (const width of [320, 430, 390]) {
      await page.setViewportSize({ width, height: 740 });
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width);
      await resizeDelivered(page);
      await expectWhollyVisible(picker);
      await expectReachable(page, rocket);
    }
  });
});

test.describe("gestos a 390px com zoom", () => {
  test.use({ viewport: { width: 390, height: 740 }, hasTouch: true, isMobile: true });

  test("o menu e o picker ficam dentro da viewport visual ampliada e deslocada", async ({
    page,
  }, testInfo) => {
    await openConversation(page, testInfo);
    // A 2× a viewport visual começa em y=0…370, à esquerda: a mensagem é a
    // recebida mais baixa que ela ainda mostra. Uma que ela deixa de fora
    // encerra a seleção (CQR R5 #1238), e aqui a seleção segue até o menu abrir.
    const id = await page.locator(".chat-msg-area__list").evaluate((list) =>
      [...list.querySelectorAll('[data-testid="chat-msg-bubble"]:not(.chat-msg-area__msg--mine)')]
        .filter((row) => {
          const box = row.querySelector(".chat-msg-area__msg-bubble")!.getBoundingClientRect();
          return box.top >= list.getBoundingClientRect().top && box.bottom <= 360;
        })
        .at(-1)
        ?.getAttribute("data-message-id"),
    );
    expect(id, "nenhuma mensagem acima de y=360").toBeTruthy();
    const target = messageBubble(page, id!);
    // Selecionada antes do zoom: sob ele, os toques do Playwright não
    // acompanham a escala da página (o arrasto abaixo é no header, que não rola).
    const actions = await selectMessageActions(target);
    // Zoom de 2x, como depois de uma pinça, e um arrasto de dedo no cabeçalho
    // (que não rola) para deslocar a viewport visual sobre a página ampliada.
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Emulation.setPageScaleFactor", { pageScaleFactor: 2 });
    await expect
      .poll(() => page.evaluate(() => window.visualViewport?.scale ?? 1))
      .toBeGreaterThan(1.5);
    const zoomed = await bubbleAgainstVisualViewport(target);
    expect(zoomed.inView, `fora da viewport visual: ${JSON.stringify(zoomed)}`).toBe(true);
    const finger = await Finger.on(page);
    const header = await page.getByTestId("chat-msg-header").boundingBox();
    const from = { x: header!.x + 40, y: header!.y + header!.height / 2 };
    // Para o lado, com a mensagem ainda à vista.
    await finger.down(from.x, from.y);
    await finger.moveBy(from, -120, 0);
    await finger.up();
    await expect
      .poll(() =>
        page.evaluate(
          () => (window.visualViewport?.offsetLeft ?? 0) + (window.visualViewport?.offsetTop ?? 0),
        ),
      )
      .toBeGreaterThan(0);
    const panned = await bubbleAgainstVisualViewport(target);
    expect(panned.inView, `saiu da viewport visual: ${JSON.stringify(panned)}`).toBe(true);
    expect(await isSelected(target), `seleção perdida: ${JSON.stringify(panned)}`).toBe(true);

    // Pelo teclado (o mesmo botão, a outra forma de acioná-lo): sob zoom, as
    // coordenadas de toque do Playwright não acompanham a escala da página.
    await expect(selectionMode(page)).toBeVisible();
    await actions.focus();
    await page.keyboard.press("Enter");
    await expectWhollyVisible(menu(page));
    await menu(page).getByRole("button", { name: "Mais reações" }).focus();
    await page.keyboard.press("Enter");
    await expectWhollyVisible(page.getByRole("dialog", { name: "Escolher reação" }));
    const search = page.getByRole("searchbox", { name: "Buscar emoji" });
    await search.fill("foguete");
    await expectReachable(page, page.getByRole("button", { name: "foguete" }));
  });
});

test.describe(`tablet a ${TABLET.width}×${TABLET.height} (issue #1238)`, () => {
  test.use({ viewport: TABLET, hasTouch: true, isMobile: true });

  test("repouso limpo, long-press em recebida e própria, e reação uma vez no alvo certo", async ({
    page,
  }, testInfo) => {
    const { scenario, messages } = await openConversation(page, testInfo);
    const finger = await Finger.on(page);
    await expect(messageBubble(page, messages.at(-1)!.id)).toBeVisible();
    await expectTimelineAtRest(page);

    for (const message of [messages.at(-3)!, messages.at(-2)!]) {
      const target = messageBubble(page, message.id);
      const point = await bubblePoint(target);
      await finger.down(point.x, point.y);
      await expect(menu(page)).toBeVisible();
      await finger.up();
      await expectWhollyVisible(menu(page));
      await expect(menu(page).getByRole("button", { name: "Mais reações" })).toBeVisible();
      await menu(page).getByRole("button", { name: "Mais ações" }).tap();
      await expect(menu(page).getByRole("button", { name: "Responder" })).toBeVisible();
      await expectWhollyVisible(menu(page));
      await expectNoHorizontalScroll(page);
      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);
    }

    const own = messageBubble(page, messages.at(-1)!.id);
    const point = await bubblePoint(own);
    await finger.down(point.x, point.y);
    await expect(menu(page)).toBeVisible();
    await finger.up();
    await menu(page)
      .getByRole("button", { name: /Reagir rapidamente com/ })
      .first()
      .tap();
    await expect(menu(page)).toHaveCount(0);
    await expect(own.locator(".chat-msg-area__reactions")).toBeVisible();
    expect(scenario.requests.reactions).toEqual([
      expect.objectContaining({ messageId: messages.at(-1)!.id, added: true }),
    ]);
  });
});

test.describe("desktop continua no hover (issues #839/#852)", () => {
  test("hover mostra a toolbar ancorada, sem a superfície móvel", async ({ page }, testInfo) => {
    const { messages } = await openConversation(page, testInfo);
    const bubble = await revealActions(page, messages.at(-1)!.id);
    await expect(menu(page)).toBeVisible();
    await expect(menu(page)).not.toHaveClass(/--sheet/);
    await expect(bubble.getByRole("button", { name: "Responder" })).toBeVisible();
    // O modo de seleção é do toque: o desktop não o tem, nem pelo clique.
    await bubble.locator(".chat-msg-area__msg-bubble").click();
    await expect(page.getByRole("button", { name: "Ações da mensagem" })).toHaveCount(0);
    await expect(selectionMode(page)).toHaveCount(0);
    // Nem a hierarquia do toque (issue #1238): as ações seguem na toolbar, sem
    // "Mais ações".
    await expect(menu(page).getByRole("button", { name: "Mais ações" })).toHaveCount(0);
  });

  test("teclado: Tab chega à mensagem e a toolbar de hover, sem o botão do toque", async ({
    page,
  }, testInfo) => {
    const { messages } = await openConversation(page, testInfo);
    const target = messageBubble(page, messages.at(-1)!.id);
    await target.focus();
    await expect(menu(page)).toBeVisible();
    await expect(menu(page).getByRole("button", { name: "Responder" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Ações da mensagem" })).toHaveCount(0);
    await expect(selectionMode(page)).toHaveCount(0);
  });
});

/**
 * Tela larga com touch (issue #1238): a classificação vem da capacidade do
 * ponteiro (`hover`/`pointer`), não da largura. Com o touch emulado o Chromium
 * informa ponteiro grosso, então vale o contrato do toque a 1280px: repouso
 * limpo, seleção pelo toque e long-press. O híbrido de mouse principal (hover
 * fino) é o contrato do desktop, coberto acima; não há como emulá-lo com touch
 * aqui — fica para o QA físico.
 */
test.describe("tela larga com touch: o contrato vem do ponteiro, não da largura", () => {
  test.use({ viewport: { width: 1280, height: 800 }, hasTouch: true });

  test("repouso limpo, seleção pelo toque e long-press a 1280px", async ({ page }, testInfo) => {
    const { scenario, messages } = await openConversation(page, testInfo);
    const coarse = await page.evaluate(
      () => matchMedia("(hover: none), (pointer: coarse)").matches,
    );
    expect(coarse).toBe(true);
    const target = messageBubble(page, messages.at(-2)!.id);
    await expect(target).toBeVisible();
    await expectTimelineAtRest(page);
    await expectRevealWithoutShift(page, target);
    await expect(menu(page)).toHaveCount(0);

    const finger = await Finger.on(page);
    const point = await bubblePoint(target);
    await finger.down(point.x, point.y);
    await expect(menu(page)).toHaveClass(/--touch/);
    await finger.up();
    await expectWhollyVisible(menu(page));
    await menu(page)
      .getByRole("button", { name: /Reagir rapidamente com/ })
      .first()
      .tap();
    await expect(menu(page)).toHaveCount(0);
    expect(scenario.requests.reactions).toEqual([
      expect.objectContaining({ messageId: messages.at(-2)!.id, added: true }),
    ]);
  });
});

/**
 * O modo de seleção do header (CQR R3 da #1238): a barra anterior cobria
 * Voltar, chamada, Agendar reunião e Detalhes — também a 768 e a 1280px com
 * touch — e não tinha saída além do foco. Agora o header troca os controles
 * pelo modo e os devolve, acionáveis, ao cancelar pelo toque, por Escape, ou
 * depois de Escape fechar o menu. Interação de verdade, não só visibilidade.
 */
for (const viewport of [
  { width: 360, height: 740, isMobile: true },
  { width: 768, height: 1024, isMobile: true },
  { width: 1280, height: 800, isMobile: false },
] as const) {
  test.describe(`modo de seleção do header a ${viewport.width}px com touch (CQR R3 #1238)`, () => {
    test.use({
      viewport: { width: viewport.width, height: viewport.height },
      hasTouch: true,
      isMobile: viewport.isMobile,
    });

    test("entra pelo toque, sai por Cancelar e por Escape, e os controles voltam acionáveis", async ({
      page,
    }, testInfo) => {
      const { messages } = await openConversation(page, testInfo);
      const target = messageBubble(page, messages.at(-1)!.id);
      await expect(target).toBeVisible();
      const controls = await headerControls(page);
      expect(controls).toEqual(expect.arrayContaining(["Agendar reunião", "Detalhes do canal"]));
      expect(controls.includes("Voltar"), "Voltar só na faixa de gaveta").toBe(
        viewport.width < 1024,
      );
      await expectHeaderControlsActionable(page, controls);

      // Cancelar pelo toque: o foco volta à mensagem, nada aberto.
      await expectRevealWithoutShift(page, target);
      await cancelSelection(page).tap();
      await expect(selectionMode(page)).toHaveCount(0);
      await expect(target).toBeFocused();
      expect(await isSelected(target)).toBe(false);
      await expect(menu(page)).toHaveCount(0);
      await expectHeaderControlsActionable(page, controls);

      // Escape com só a seleção ativa.
      await expectRevealWithoutShift(page, target);
      await page.keyboard.press("Escape");
      await expect(selectionMode(page)).toHaveCount(0);
      await expect(target).toBeFocused();
      await expectHeaderControlsActionable(page, controls);

      // Menu aberto: o primeiro Escape é dele, o segundo encerra a seleção.
      const actions = await selectMessageActions(target);
      await actions.tap();
      await expect(menu(page)).toBeVisible();
      await expect(actions).toHaveAttribute(
        "aria-controls",
        (await menu(page).getAttribute("id"))!,
      );
      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);
      await expect(actions).toBeFocused();
      await expect(actions).toHaveAttribute("aria-expanded", "false");
      await expect(selectionMode(page)).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(selectionMode(page)).toHaveCount(0);
      await expect(target).toBeFocused();
      await expectTimelineAtRest(page);
      await expectHeaderControlsActionable(page, controls);

      // Os controles devolvidos funcionam: Detalhes abre e fecha o painel,
      // Agendar reunião abre o diálogo, Voltar leva à lista.
      const header = page.getByTestId("chat-msg-header");
      const details = page.getByTestId("chat-details-toggle");
      await details.tap();
      await expect(details).toHaveAttribute("aria-expanded", "true");
      await page.keyboard.press("Escape");
      await expect(details).toHaveAttribute("aria-expanded", "false");
      await header.getByRole("button", { name: "Agendar reunião" }).tap();
      const scheduling = page.getByRole("dialog", { name: "Agendar reunião", exact: true });
      await expect(scheduling).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(scheduling).toHaveCount(0);
      if (controls.includes("Voltar")) {
        // Sem conversa anterior, Voltar abre a lista de conversas (#1124).
        await header.getByRole("button", { name: "Voltar", exact: true }).tap();
        await expect(page.getByTestId("chat-sidebar")).toBeVisible();
      }
    });
  });
}

const DOC_URL = `https://docs.example.test/${"guia-da-conversa/".repeat(3)}final`;

/** Cerca de 300 mensagens, uma em cada dez com um link; a última página primeiro. */
async function openLongConversation(
  page: Page,
  testInfo: Parameters<typeof uniqueId>[0],
  prepare?: () => Promise<void>,
) {
  const targetId = uniqueId(testInfo, "selection-long");
  const all = Array.from({ length: 300 }, (_, index) => {
    const stamp = `2026-07-1${Math.floor(index / 60)}T12:${String(index % 60).padStart(2, "0")}:00.000Z`;
    const linked = index % 10 === 5;
    return makeMessage({
      id: `${targetId}-m${index}`,
      sender_id: index % 3 === 0 ? CURRENT_USER_ID : OTHER_USER_ID,
      sender_display_name: index % 3 === 0 ? "E2E Autor" : OTHER_USER_NAME,
      body_text: linked
        ? `Mensagem ${index}: o guia está em ${DOC_URL}`
        : `Mensagem ${index} ${"texto ".repeat(index % 7)}`,
      ...(linked
        ? {
            body_format: "v2" as const,
            link_safety_state: "safe" as const,
            links: [
              {
                ordinal: 0,
                target_key: "cd".repeat(16),
                text: DOC_URL,
                url: DOC_URL,
                hostname: "docs.example.test",
                safety: "safe" as const,
                click: "direct" as const,
                href: DOC_URL,
                updated_at: stamp,
              },
            ],
          }
        : {}),
      created_at: stamp,
      updated_at: stamp,
    });
  });
  const scenario = createScenario({
    kind: "channel",
    targetId,
    targetName: "Seleção longa",
    // Todas no cenário, para as reações acharem qualquer uma; as páginas vêm
    // da paginação instalada abaixo, que responde antes dele.
    messages: all,
  });
  await installMessagingMocks(page, scenario);
  const tracker = await installPaginatedMessages(page, targetId, all);
  await page
    .context()
    .route("https://docs.example.test/**", (route) =>
      route.fulfill({ status: 200, contentType: "text/html", body: "<title>guia</title>" }),
    );
  await prepare?.();
  await page.goto(`/chat/channel/${targetId}`);
  await expect(bubbles(page).first()).toBeVisible();
  await quiet(page);
  await settledViewport(page);
  for (let prepend = 0; prepend < 2; prepend += 1) {
    await loadPreviousPage(page, tracker);
    await quiet(page);
    await settledViewport(page);
  }
  await expect(page.getByTestId("chat-virtual-canvas")).toHaveCount(1);
  return { scenario, all, tracker };
}

/**
 * A primeira mensagem inteira na área visível da timeline, com link se pedido.
 * Sem nenhuma assim na tela, a primeira montada que serve é rolada ao meio dela.
 */
async function firstWhollyInView(page: Page, { linked = false } = {}) {
  const id = await page.locator(".chat-msg-area__list").evaluate((list, linked) => {
    const view = list.getBoundingClientRect();
    const rows = [...list.querySelectorAll<HTMLElement>('[data-testid="chat-msg-bubble"]')].filter(
      (shell) => !linked || !!shell.querySelector("a.rtr-link"),
    );
    const inView = rows.find((shell) => {
      const box = shell.getBoundingClientRect();
      return box.top >= view.top + 8 && box.bottom <= view.bottom - 8;
    });
    const row = inView ?? rows[0];
    if (row && !inView) {
      const box = row.getBoundingClientRect();
      list.scrollTop += box.top - view.top - (view.height - box.height) / 2;
    }
    return row?.getAttribute("data-message-id") ?? null;
  }, linked);
  expect(id, "nenhuma mensagem que sirva montada").not.toBeNull();
  const target = messageBubble(page, id!);
  await expect(target).toBeInViewport({ ratio: 1 });
  return target;
}

/** Seleciona pelo começo do texto, longe de links e controles. */
async function tapToSelect(page: Page, target: Locator) {
  const bubble = await target.locator(".chat-msg-area__msg-bubble").boundingBox();
  await page.touchscreen.tap(bubble!.x + 14, bubble!.y + 12);
}

/** Nem modo, nem mensagem selecionada, nem menu: nada órfão. */
async function expectNothingSelected(page: Page) {
  await expect(selectionMode(page)).toHaveCount(0);
  await expect(page.locator(".chat-msg-area__msg--selected")).toHaveCount(0);
  await expect(page.locator(".chat-msg-area__reaction-menu")).toHaveCount(0);
}

/**
 * O CQR R3 achou a barra, sem Popover API, em `position: fixed` dentro da
 * linha virtualizada transformada — sobre a mensagem, a y=424…484, com o header
 * a y=57…117 — e, sem anchor positioning, por cima da sidebar. O modo agora é
 * do header real, fora de qualquer ancestral transformado; o que resta de
 * popover é o menu da mensagem, que tem de funcionar sem a API também.
 */
for (const fallback of ["sem Popover API", "sem anchor positioning"] as const) {
  for (const viewport of [
    { width: 360, height: 740, isMobile: true },
    { width: 1280, height: 800, isMobile: false },
  ] as const) {
    test.describe(`seleção ${fallback} a ${viewport.width}px, timeline virtualizada (CQR R3 #1238)`, () => {
      test.use({
        viewport: { width: viewport.width, height: viewport.height },
        hasTouch: true,
        isMobile: viewport.isMobile,
      });

      test("seleciona, abre as ações, reage, segue o link e não deixa nada órfão ao rolar", async ({
        page,
      }, testInfo) => {
        const errors: string[] = [];
        page.on("pageerror", (error) => errors.push(error.message));
        const { scenario, tracker } = await openLongConversation(page, testInfo, async () => {
          if (fallback !== "sem Popover API") return;
          await page.addInitScript(() => {
            for (const name of ["togglePopover", "showPopover", "hidePopover"]) {
              delete (HTMLElement.prototype as unknown as Record<string, unknown>)[name];
            }
          });
        });
        if (fallback === "sem Popover API") {
          expect(await page.evaluate(() => "togglePopover" in HTMLElement.prototype)).toBe(false);
        } else {
          // Nenhuma regra da aplicação depende de anchor positioning; e, mesmo
          // que dependesse, nenhum elemento é âncora daqui em diante.
          const anchored = await page.evaluate(() =>
            [...document.styleSheets].flatMap((sheet) => {
              try {
                return [...sheet.cssRules]
                  .map((rule) => rule.cssText)
                  .filter((text) => /anchor-name:\s*--|position-anchor:\s*--|anchor\(/.test(text));
              } catch {
                return [];
              }
            }),
          );
          expect(anchored).toEqual([]);
          await page.addStyleTag({
            content: "*, *::before, *::after { anchor-name: none !important; }",
          });
        }

        const list = page.locator(".chat-msg-area__list");
        const target = await firstWhollyInView(page, { linked: true });
        const targetId = (await target.getAttribute("data-message-id"))!;
        const controls = await headerControls(page);
        await tapToSelect(page, target);
        const { actions } = await expectRevealWithoutShift(page, target);

        // O modo fica dentro do header, longe da sidebar e da timeline.
        const header = (await page.getByTestId("chat-msg-header").boundingBox())!;
        const mode = (await selectionMode(page).boundingBox())!;
        expect(mode.x).toBeGreaterThanOrEqual(header.x - 0.5);
        expect(mode.x + mode.width).toBeLessThanOrEqual(header.x + header.width + 0.5);
        expect(mode.y).toBeGreaterThanOrEqual(header.y - 0.5);
        expect(mode.y + mode.height).toBeLessThanOrEqual(header.y + header.height + 0.5);
        const sidebar = page.getByTestId("chat-sidebar");
        if (await sidebar.isVisible()) {
          const side = (await sidebar.boundingBox())!;
          expect(mode.x, "o modo cobre a sidebar").toBeGreaterThanOrEqual(
            side.x + side.width - 0.5,
          );
        }

        // O link da mensagem selecionada segue dela, e abre.
        const anchor = target.locator("a.rtr-link");
        await expectLinkOwnsItsBox(anchor);
        const popup = page.waitForEvent("popup");
        await anchor.tap();
        expect((await popup).url()).toBe(DOC_URL);
        await page.bringToFront();
        await expect(selectionMode(page)).toBeVisible();

        // As ações: o mesmo menu, inteiro na tela, tocável e no alvo certo.
        await actions.tap();
        await expect(menu(page)).toBeVisible();
        await expectWhollyVisible(menu(page));
        const menuBox = (await menu(page).boundingBox())!;
        const crossesMode =
          menuBox.x < mode.x + mode.width &&
          mode.x < menuBox.x + menuBox.width &&
          menuBox.y < mode.y + mode.height &&
          mode.y < menuBox.y + menuBox.height;
        expect(crossesMode, "o menu cobre o modo do header").toBe(false);
        const quick = menu(page)
          .getByRole("button", { name: /Reagir rapidamente com/ })
          .first();
        await expectLinkOwnsItsBox(quick);
        await quick.tap();
        await expect(menu(page)).toHaveCount(0);
        await expect(target.locator(".chat-msg-area__reactions")).toBeVisible();
        expect(scenario.requests.reactions).toEqual([
          expect.objectContaining({ messageId: targetId, added: true }),
        ]);

        // Rolada para longe, a linha sai e é reciclada: nada fica órfão.
        //
        // CQR R4: a m155 fica perto do topo do que foi carregado, e a seleção
        // pede a página anterior (4 s de latência). Chegando com a lista no fim,
        // esse prepend acrescenta conteúdo acima, e o scrollTop lido antes dele
        // passava a apontar 50 mensagens acima: a m155 nunca voltava, com o
        // carregamento seguinte já pedido. A posição só é lida com nenhuma
        // página em voo, e a volta é conferida pela identidade da mensagem, sem
        // deslocamento, depois de qualquer prepend que ela peça ter terminado.
        // A distância ao fim não serve: as linhas não montadas são estimadas
        // de novo depois de um prepend, e medidas no fim encolhem o conteúdo
        // (−182 px a 360, −343 px a 1280, na investigação do R4).
        await noPageInFlight(tracker);
        await settledViewport(page);
        const offsetInList = () =>
          target.evaluate(
            (shell) =>
              shell.getBoundingClientRect().top -
              shell.closest(".chat-msg-area__list")!.getBoundingClientRect().top,
          );
        const offsetBefore = await offsetInList();
        const back = await list.evaluate((element) => element.scrollTop);
        await list.evaluate((element) => {
          element.scrollTop = element.scrollHeight;
        });
        await expect(target).toHaveCount(0);
        await expectNothingSelected(page);
        await expectHeaderControlsActionable(page, controls);
        await list.evaluate((element, top) => {
          element.scrollTop = top;
        }, back);
        await noPageInFlight(tracker);
        await settledViewport(page);
        testInfo.annotations.push({
          type: "prepend-sync",
          description: JSON.stringify({ targetId, pages: tracker.pagesRequested() }),
        });
        await expect(target).toBeVisible();
        expect(
          Math.abs((await offsetInList()) - offsetBefore),
          "a mensagem voltou deslocada",
        ).toBeLessThanOrEqual(1);
        expect(await isSelected(target)).toBe(false);
        await expectNothingSelected(page);
        expect(errors).toEqual([]);
      });
    });
  }
}

/**
 * O CQR R3 achou a barra ainda ativa com a mensagem fora da tela e o menu
 * fechado. A seleção acompanha a mensagem: sair da área visível da timeline
 * (não da janela), ser removida, a troca de conversa, a reciclagem da linha.
 */
test.describe("ciclo de vida da seleção a 360px (CQR R3 #1238)", () => {
  test.use({ viewport: { width: 360, height: 740 }, hasTouch: true, isMobile: true });

  test("sair da área visível da timeline encerra a seleção, com a linha ainda montada", async ({
    page,
  }, testInfo) => {
    await openLongConversation(page, testInfo);
    const list = page.locator(".chat-msg-area__list");
    const target = await firstWhollyInView(page);
    await tapToSelect(page, target);
    await expectRevealWithoutShift(page, target);

    // Um pouco além da borda de cima da lista: fora da área visível dela,
    // ainda dentro da janela e montada pelo overscan do virtualizer.
    const past = await target.evaluate((shell) => {
      const view = shell.closest(".chat-msg-area__list")!.getBoundingClientRect();
      return shell.getBoundingClientRect().bottom - view.top + 2;
    });
    // A mesma linha, não outra com o mesmo id: quem encerra é a saída da área
    // visível, não a reciclagem que o virtualizer faz depois.
    const row = await target.elementHandle();
    await list.evaluate((element, by) => {
      element.scrollTop += by;
    }, past);
    await expectNothingSelected(page);
    expect(await row!.evaluate((node) => node.isConnected), "encerrada com a linha montada").toBe(
      true,
    );
    await expect(target).toHaveCount(1);
    const box = (await target.boundingBox())!;
    expect(box.y + box.height, "a linha segue na janela, sob o header").toBeGreaterThan(0);

    // De volta, ela não volta selecionada.
    await list.evaluate((element, by) => {
      element.scrollTop -= by;
    }, past);
    await expect(target).toBeInViewport();
    expect(await isSelected(target)).toBe(false);
    await expectNothingSelected(page);
  });

  test("com o menu aberto, a rolagem fecha o menu e encerra a seleção", async ({
    page,
  }, testInfo) => {
    await openLongConversation(page, testInfo);
    const list = page.locator(".chat-msg-area__list");
    const target = await firstWhollyInView(page);
    await tapToSelect(page, target);
    await (await selectMessageActions(target)).tap();
    await expect(menu(page)).toBeVisible();
    await list.evaluate((element) => {
      element.scrollTop += element.clientHeight * 2;
    });
    await expectNothingSelected(page);
  });

  // Sem Popover API o menu vai para o body: ao fechar, o foco não volta à
  // lista, e nada além da saída da área visível encerra a seleção.
  for (const popover of ["com Popover API", "sem Popover API"] as const) {
    test(`com o menu aberto (${popover}), sair da área visível com a linha montada encerra a seleção`, async ({
      page,
    }, testInfo) => {
      await openLongConversation(page, testInfo, async () => {
        if (popover === "com Popover API") return;
        await page.addInitScript(() => {
          for (const name of ["togglePopover", "showPopover", "hidePopover"]) {
            delete (HTMLElement.prototype as unknown as Record<string, unknown>)[name];
          }
        });
      });
      const list = page.locator(".chat-msg-area__list");
      const target = await firstWhollyInView(page);
      await tapToSelect(page, target);
      await (await selectMessageActions(target)).tap();
      await expect(menu(page)).toBeVisible();
      // Logo além da borda de cima: o menu perde a âncora e fecha, e a linha
      // segue montada pelo overscan — nenhuma desmontagem encerra por ela.
      const past = await target.evaluate((shell) => {
        const view = shell.closest(".chat-msg-area__list")!.getBoundingClientRect();
        return shell.getBoundingClientRect().bottom - view.top + 2;
      });
      const row = await target.elementHandle();
      await list.evaluate((element, by) => {
        element.scrollTop += by;
      }, past);
      await expect(menu(page)).toHaveCount(0);
      await expectNothingSelected(page);
      expect(await row!.evaluate((node) => node.isConnected), "encerrada com a linha montada").toBe(
        true,
      );
    });
  }

  test("a mensagem selecionada removida encerra a seleção", async ({ page }, testInfo) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const { scenario, messages } = await openConversation(page, testInfo);
    const target = messageBubble(page, messages.at(-1)!.id);
    const { controls } = await expectRevealWithoutShift(page, target);
    const targetId = messages.at(-1)!.id.replace(/-m\d+$/, "");
    const stored = messagesFor(scenario, "channel", targetId);
    const index = stored.findIndex((message) => message.id === messages.at(-1)!.id);
    stored[index] = makeMessage({
      ...stored[index],
      body_text: undefined,
      status: "deleted",
      is_removed: true,
      deleted_at: "2026-07-15T12:30:00.000Z",
      updated_at: "2026-07-15T12:30:00.000Z",
    });
    await emitConversationEvent(page, scenario, {
      kind: "channel",
      targetId,
      message: stored[index],
    });
    await expectNothingSelected(page);
    await expectHeaderControlsActionable(page, controls);
    expect(errors).toEqual([]);
  });

  test("trocar de conversa encerra a seleção", async ({ page }, testInfo) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const { messages } = await openConversation(page, testInfo);
    await expectRevealWithoutShift(page, messageBubble(page, messages.at(-1)!.id));
    await page.evaluate((id) => {
      history.pushState(null, "", `/chat/channel/${id}`);
      dispatchEvent(new PopStateEvent("popstate"));
    }, OTHER_CHANNEL_ID);
    await expect(page).toHaveURL(`/chat/channel/${OTHER_CHANNEL_ID}`);
    await expect(page.getByTestId("chat-msg-header")).toBeVisible();
    await expectNothingSelected(page);
    expect(await headerControls(page)).toEqual(
      expect.arrayContaining(["Agendar reunião", "Detalhes do canal"]),
    );
    expect(errors).toEqual([]);
  });

  test("na timeline virtualizada, depois de prepend, a seleção é da mensagem e não da posição", async ({
    page,
  }, testInfo) => {
    const { scenario } = await openLongConversation(page, testInfo);
    const list = page.locator(".chat-msg-area__list");
    const first = await firstWhollyInView(page);
    await tapToSelect(page, first);
    await expectRevealWithoutShift(page, first);

    // Ao topo, que pede mais uma página: a mensagem sai da área visível, a
    // seleção acaba e nenhuma linha reciclada para aquela posição a herda.
    await list.evaluate((element) => {
      element.scrollTop = 0;
    });
    await expectNothingSelected(page);
    await quiet(page);
    await settledViewport(page);
    await expectNothingSelected(page);

    // Outra mensagem selecionada leva as ações para ela, pelo id.
    const second = await firstWhollyInView(page);
    const secondId = (await second.getAttribute("data-message-id"))!;
    await tapToSelect(page, second);
    const { actions } = await expectRevealWithoutShift(page, second);
    await actions.tap();
    await expect(second.getByRole("toolbar", { name: "Reagir à mensagem" })).toHaveCount(1);
    await menu(page)
      .getByRole("button", { name: /Reagir rapidamente com/ })
      .first()
      .tap();
    await expect(second.locator(".chat-msg-area__reactions")).toBeVisible();
    expect(scenario.requests.reactions).toEqual([
      expect.objectContaining({ messageId: secondId, added: true }),
    ]);
  });
});

/*
 * CQR R4 da #1238. Os testes abaixo não usam o CDP, para rodarem também no
 * WebKit (com uma configuração local que acrescenta o projeto): só teclado,
 * `locator.tap()` e o scrollTop da lista.
 */

/** Sem Popover API: o menu de toque vai para o body (CQR R3). */
function withoutPopover(page: Page) {
  return page.addInitScript(() => {
    for (const name of ["togglePopover", "showPopover", "hidePopover"]) {
      delete (HTMLElement.prototype as unknown as Record<string, unknown>)[name];
    }
  });
}

/**
 * O menu e o picker abertos dentro da coluna da conversa — os lados da lista —
 * e longe da sidebar: pelas caixas e pelo hit-test de dois pontos da sidebar na
 * altura de cada superfície, que têm de ser dela. A 1280×800 sem Popover API, o
 * R4 mediu a sheet em x=8…1272 sobre a sidebar em x=0…240.
 */
async function expectInConversationColumn(page: Page) {
  const surfaces = await page.evaluate(() => {
    const list = document.querySelector(".chat-msg-area__list")!.getBoundingClientRect();
    const sidebar = document.querySelector<HTMLElement>('[data-testid="chat-sidebar"]');
    const side = sidebar?.getBoundingClientRect();
    // Na faixa de gaveta (< 1024px) a sidebar fica fora da tela, à esquerda.
    const onScreen = !!side && side.width > 0 && side.right > 0 && side.left < innerWidth;
    const drawn = onScreen && sidebar!.checkVisibility() ? side : null;
    return [
      document.querySelector('[role="toolbar"][aria-label="Reagir à mensagem"]'),
      document.querySelector('[role="dialog"][aria-label="Escolher reação"]'),
    ]
      .filter((node): node is Element => !!node)
      .map((surface) => {
        const box = surface.getBoundingClientRect();
        const y = box.top + box.height / 2;
        const owns = (x: number) => {
          const hit = document.elementFromPoint(x, y);
          return !!hit && sidebar!.contains(hit);
        };
        return {
          name: surface.getAttribute("aria-label"),
          left: box.left,
          right: box.right,
          column: { left: list.left, right: list.right },
          sidebar: drawn && {
            right: drawn.right,
            ownsHits: [owns(drawn.left + drawn.width / 2), owns(drawn.right - 4)],
          },
        };
      });
  });
  expect(surfaces.length, "nenhuma superfície aberta").toBeGreaterThan(0);
  for (const surface of surfaces) {
    expect(surface.left, `${surface.name}: além da coluna`).toBeGreaterThanOrEqual(
      surface.column.left - 0.5,
    );
    expect(surface.right, `${surface.name}: além da coluna`).toBeLessThanOrEqual(
      surface.column.right + 0.5,
    );
    if (!surface.sidebar) continue;
    expect(surface.left, `${surface.name}: sobre a sidebar`).toBeGreaterThanOrEqual(
      surface.sidebar.right - 0.5,
    );
    expect(surface.sidebar.ownsHits, `${surface.name}: intercepta a sidebar`).toEqual([true, true]);
  }
}

/** O elemento focado: nunca o body, e dentro do documento. */
const activeElementState = (page: Page) =>
  page.evaluate(() => ({
    body: document.activeElement === document.body || !document.activeElement,
    connected: !!document.activeElement?.isConnected,
  }));

for (const viewport of [
  { width: 390, height: 740, isMobile: true, anchored: false },
  { width: 768, height: 1024, isMobile: true, anchored: null },
  { width: 1280, height: 800, isMobile: false, anchored: true },
  // Baixa demais para o picker ao lado do menu: a sheet, dentro da coluna.
  { width: 1280, height: 560, isMobile: false, anchored: false },
] as const) {
  test.describe(`superfícies de toque na coluna a ${viewport.width}×${viewport.height} (CQR R4 #1238)`, () => {
    test.use({
      viewport: { width: viewport.width, height: viewport.height },
      hasTouch: true,
      isMobile: viewport.isMobile,
    });

    test("sem Popover API: menu e picker na coluna, uma reação na mensagem certa, composer utilizável", async ({
      page,
    }, testInfo) => {
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      const { scenario, messages } = await openConversation(page, testInfo, () =>
        withoutPopover(page),
      );
      expect(await page.evaluate(() => "togglePopover" in HTMLElement.prototype)).toBe(false);
      const target = messageBubble(page, messages.at(-2)!.id);
      await expect(target).toBeVisible();

      const actions = await selectMessageActions(target);
      await actions.tap();
      await expect(menu(page)).toBeVisible();
      await expectWhollyVisible(menu(page));
      await expectInConversationColumn(page);

      await menu(page).getByRole("button", { name: "Mais reações" }).tap();
      const picker = page.getByRole("dialog", { name: "Escolher reação" });
      const search = picker.getByRole("searchbox", { name: "Buscar emoji" });
      await expect(search).toBeFocused();
      await search.fill("foguete");
      const rocket = picker.getByRole("button", { name: "foguete" });
      await expect(rocket).toBeVisible();
      await expectWhollyVisible(picker);
      await expectWhollyVisible(menu(page));
      await expectInConversationColumn(page);
      await expectReachable(page, rocket);
      // Com espaço, o menu fica junto da mensagem quando o picker abre; num
      // celular, os dois dividem a sheet.
      const sheet = await menu(page).evaluate((element) =>
        element.classList.contains("chat-msg-area__reaction-menu--sheet"),
      );
      testInfo.annotations.push({ type: "menu-layout", description: sheet ? "sheet" : "anchored" });
      if (viewport.anchored !== null) expect(sheet).toBe(!viewport.anchored);

      await rocket.tap();
      await expect(picker).toHaveCount(0);
      await expect(menu(page)).toHaveCount(0);
      await expect(target.locator(".chat-msg-area__reactions")).toContainText("🚀");
      expect(scenario.requests.reactions).toEqual([
        { messageId: messages.at(-2)!.id, emoji: "🚀", added: true },
      ]);
      // A seleção segue, com o foco no controle do modo que abriu o menu.
      await expect(actions).toBeFocused();
      await expect(selectionMode(page)).toBeVisible();

      await fillComposer(page, "depois da reação");
      await expect(page.getByTestId("chat-composer-input")).toHaveText("depois da reação");
      expect(errors).toEqual([]);
    });
  });
}

for (const popover of ["com Popover API", "sem Popover API"] as const) {
  test.describe(`conclusão e foco das reações a 360px, ${popover} (CQR R4 #1238)`, () => {
    test.use({ viewport: { width: 360, height: 740 }, hasTouch: true, isMobile: true });

    const open = async (page: Page, testInfo: Parameters<typeof uniqueId>[0]) => {
      const opened = await openConversation(page, testInfo, async () => {
        if (popover === "sem Popover API") await withoutPopover(page);
      });
      const target = messageBubble(page, opened.messages.at(-2)!.id);
      await expect(target).toBeVisible();
      const actions = await selectMessageActions(target, { via: "keyboard" });
      await actions.focus();
      await actions.press("Enter");
      await expect(menu(page)).toBeVisible();
      return { ...opened, target, actions };
    };

    test("reação rápida por Enter: uma reação, menu fechado, foco no botão do header", async ({
      page,
    }, testInfo) => {
      const { scenario, messages, actions } = await open(page, testInfo);
      const quick = menu(page)
        .getByRole("button", { name: /Reagir rapidamente com/ })
        .first();
      await expect(quick).toBeFocused();
      await page.keyboard.press("Enter");
      await expect(menu(page)).toHaveCount(0);
      expect(scenario.requests.reactions).toEqual([
        expect.objectContaining({ messageId: messages.at(-2)!.id, added: true }),
      ]);
      await expect(actions).toBeFocused();
      expect(await activeElementState(page)).toEqual({ body: false, connected: true });
      await expect(selectionMode(page)).toBeVisible();
    });

    test("reação rápida por toque: uma reação, menu fechado, foco no botão do header", async ({
      page,
    }, testInfo) => {
      const { scenario, messages, actions } = await open(page, testInfo);
      await menu(page)
        .getByRole("button", { name: /Reagir rapidamente com/ })
        .first()
        .tap();
      await expect(menu(page)).toHaveCount(0);
      expect(scenario.requests.reactions).toHaveLength(1);
      expect(scenario.requests.reactions[0]).toMatchObject({ messageId: messages.at(-2)!.id });
      await expect(actions).toBeFocused();
    });

    test("picker pelo teclado: Escape volta ao menu; Enter num emoji fecha tudo e reage uma vez", async ({
      page,
    }, testInfo) => {
      const { scenario, messages, actions } = await open(page, testInfo);
      const more = menu(page).getByRole("button", { name: "Mais reações" });
      await more.focus();
      await page.keyboard.press("Enter");
      const picker = page.getByRole("dialog", { name: "Escolher reação" });
      const search = picker.getByRole("searchbox", { name: "Buscar emoji" });
      await expect(search).toBeFocused();

      // Cancelar o picker é um nível só: o menu fica, com o foco em Mais reações.
      await page.keyboard.press("Escape");
      await expect(picker).toHaveCount(0);
      await expect(menu(page)).toBeVisible();
      await expect(more).toBeFocused();
      expect(scenario.requests.reactions).toEqual([]);

      await page.keyboard.press("Enter");
      await expect(search).toBeFocused();
      await search.fill("foguete");
      const rocket = picker.getByRole("button", { name: "foguete" });
      await rocket.focus();
      await page.keyboard.press("Enter");
      await expect(picker).toHaveCount(0);
      await expect(menu(page)).toHaveCount(0);
      expect(scenario.requests.reactions).toEqual([
        { messageId: messages.at(-2)!.id, emoji: "🚀", added: true },
      ]);
      await expect(actions).toBeFocused();
      expect(await activeElementState(page)).toEqual({ body: false, connected: true });

      // Escape do menu, sem picker: fecha e devolve o foco ao botão.
      await page.keyboard.press("Enter");
      await expect(menu(page)).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);
      await expect(actions).toBeFocused();
      expect(scenario.requests.reactions).toHaveLength(1);
    });

    test("picker por toque: um emoji fecha picker e menu, sem foco no body", async ({
      page,
    }, testInfo) => {
      const { scenario, messages, actions } = await open(page, testInfo);
      await menu(page).getByRole("button", { name: "Mais reações" }).tap();
      const picker = page.getByRole("dialog", { name: "Escolher reação" });
      await picker.getByRole("searchbox", { name: "Buscar emoji" }).fill("foguete");
      await picker.getByRole("button", { name: "foguete" }).tap();
      await expect(picker).toHaveCount(0);
      await expect(menu(page)).toHaveCount(0);
      expect(scenario.requests.reactions).toEqual([
        { messageId: messages.at(-2)!.id, emoji: "🚀", added: true },
      ]);
      await expect(actions).toBeFocused();
    });

    test("trocar de conversa com o menu aberto não deixa foco nem seleção para trás", async ({
      page,
    }, testInfo) => {
      const { scenario } = await open(page, testInfo);
      await page.evaluate((id) => {
        history.pushState(null, "", `/chat/channel/${id}`);
        dispatchEvent(new PopStateEvent("popstate"));
      }, OTHER_CHANNEL_ID);
      await expect(page).toHaveURL(`/chat/channel/${OTHER_CHANNEL_ID}`);
      await expectNothingSelected(page);
      expect(scenario.requests.reactions).toEqual([]);
    });
  });

  test.describe(`seleção órfã a 360px, ${popover} (CQR R4 #1238)`, () => {
    test.use({ viewport: { width: 360, height: 740 }, hasTouch: true, isMobile: true });

    test("rolagem e Escape no mesmo ciclo encerram a seleção da linha que saiu da vista", async ({
      page,
    }, testInfo) => {
      await openLongConversation(page, testInfo, async () => {
        if (popover === "sem Popover API") await withoutPopover(page);
      });
      const target = await firstWhollyInView(page);
      await selectMessageActions(target, { via: "keyboard" });
      await expect(target).toBeFocused();
      await page.keyboard.press("Enter");
      await expect(menu(page)).toBeVisible();
      const row = await target.elementHandle();

      // A reprodução do R4: a linha sai da área visível e Escape chega na mesma
      // tarefa — o menu fecha e devolve o foco à linha, já fora da vista.
      await page.evaluate(() => {
        const focused = document.activeElement!;
        const shell = document.querySelector(".chat-msg-area__msg--selected")!;
        const list = shell.closest(".chat-msg-area__list")!;
        list.scrollTop +=
          shell.getBoundingClientRect().bottom - list.getBoundingClientRect().top + 2;
        focused.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      });
      await expect(menu(page)).toHaveCount(0);
      await expectNothingSelected(page);
      expect(await row!.evaluate((node) => node.isConnected), "linha ainda montada").toBe(true);
    });

    test("a entrada pelo teclado numa linha fora da vista a seleciona e a mantém, rolada para dentro", async ({
      page,
    }, testInfo) => {
      await openLongConversation(page, testInfo, async () => {
        if (popover === "sem Popover API") await withoutPopover(page);
      });
      const list = page.locator(".chat-msg-area__list");
      // A linha logo abaixo da borda de baixo da lista, montada pelo overscan,
      // e o foco na anterior sem rolar: o Tab chega a ela fora da vista.
      const targetId = await list.evaluate((element) => {
        const view = element.getBoundingClientRect();
        const rows = [...element.querySelectorAll<HTMLElement>('[data-testid="chat-msg-bubble"]')];
        const index = rows.findIndex((row) => row.getBoundingClientRect().top >= view.bottom);
        rows[index - 1].focus({ preventScroll: true });
        return rows[index].getAttribute("data-message-id");
      });
      const target = messageBubble(page, targetId!);
      // Do anterior até a linha seguinte pelo Tab, atravessando o que houver
      // de focável dentro da anterior.
      for (let step = 0; step < 10 && !(await isFocused(target)); step += 1) {
        await page.keyboard.press("Tab");
      }
      await expect(target).toBeFocused();
      await expect.poll(() => isSelected(target)).toBe(true);
      // Vinte quadros depois, rolada para dentro, segue selecionada.
      await page.evaluate(
        () =>
          new Promise<void>((resolve) => {
            let frames = 0;
            const tick = () => (++frames < 20 ? requestAnimationFrame(tick) : resolve());
            requestAnimationFrame(tick);
          }),
      );
      await expect(target).toBeInViewport();
      expect(await isSelected(target)).toBe(true);
      await expect(selectionMode(page)).toBeVisible();
    });
  });
}

/** Quadros de animação, para a interação assentar antes de medir. */
const afterFrames = (page: Page, count = 10) =>
  page.evaluate(
    (count) =>
      new Promise<void>((resolve) => {
        let frames = 0;
        const tick = () => (++frames < count ? requestAnimationFrame(tick) : resolve());
        requestAnimationFrame(tick);
      }),
    count,
  );

const FALLBACKS = ["com Popover API", "sem Popover API", "sem anchor positioning"] as const;
type Fallback = (typeof FALLBACKS)[number];

/** Antes de a conversa carregar: sem Popover API, quando pedido. */
async function prepareFallback(page: Page, fallback: Fallback) {
  if (fallback === "sem Popover API") await withoutPopover(page);
}

/** Depois de carregar: nenhum elemento é âncora, quando pedido. */
async function applyFallback(page: Page, fallback: Fallback) {
  if (fallback === "sem Popover API") {
    expect(await page.evaluate(() => "togglePopover" in HTMLElement.prototype)).toBe(false);
  }
  if (fallback === "sem anchor positioning") {
    await page.addStyleTag({ content: "*, *::before, *::after { anchor-name: none !important; }" });
  }
}

/**
 * As superfícies abertas — menu, picker, paleta — contra o header e o
 * compositor, e o hit-test do centro do compositor, do campo, de cada botão
 * dele e de cada botão do header: cada um tem de ser dono do próprio centro.
 */
async function composerGeometry(page: Page) {
  return page.evaluate(() => {
    const rect = (node: Element) => node.getBoundingClientRect();
    const owns = (node: Element) => {
      const box = rect(node);
      const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
      return !!hit && node.contains(hit);
    };
    const drawn = (node: Element) => rect(node).width > 0 && rect(node).height > 0;
    const composer = document.querySelector('[data-testid="chat-composer-box"]')!;
    const header = document.querySelector('[data-testid="chat-msg-header"]')!;
    const surfaces = [
      '[role="toolbar"][aria-label="Reagir à mensagem"]',
      '[role="dialog"][aria-label="Escolher reação"]',
      ".chat-emoji-tone",
    ]
      .flatMap((selector) => [...document.querySelectorAll(selector)])
      .map((node) => {
        const box = rect(node);
        return { name: node.getAttribute("aria-label") ?? "", top: box.top, bottom: box.bottom };
      });
    const named = (node: Element) =>
      node.getAttribute("aria-label") ?? node.getAttribute("data-testid") ?? node.tagName;
    const composerTargets = [
      composer,
      composer.querySelector('[data-testid="chat-composer-input"]')!,
      ...composer.querySelectorAll("button"),
    ].filter(drawn);
    return {
      surfaces,
      headerBottom: rect(header).bottom,
      composerTop: rect(composer).top,
      composer: composerTargets.map((node) => ({ name: named(node), owns: owns(node) })),
      header: [...header.querySelectorAll("button")]
        .filter(drawn)
        .map((node) => ({ name: named(node), owns: owns(node) })),
    };
  });
}

type ComposerGeometry = Awaited<ReturnType<typeof composerGeometry>>;

function expectComposerClear(geometry: ComposerGeometry) {
  expect(geometry.surfaces.length, "nenhuma superfície aberta").toBeGreaterThanOrEqual(2);
  for (const surface of geometry.surfaces) {
    expect(surface.top, `${surface.name}: sobre o header`).toBeGreaterThanOrEqual(
      geometry.headerBottom - 0.5,
    );
    expect(surface.bottom, `${surface.name}: sobre o compositor`).toBeLessThanOrEqual(
      geometry.composerTop + 0.5,
    );
  }
  expect(geometry.composer.length).toBeGreaterThanOrEqual(3);
  expect(
    geometry.composer.filter((target) => !target.owns).map((target) => target.name),
    "o compositor não recebe o toque",
  ).toEqual([]);
  expect(
    geometry.header.filter((target) => !target.owns).map((target) => target.name),
    "o header não recebe o toque",
  ).toEqual([]);
}

/** Menu pelo header e picker aberto, com o catálogo carregado e assentado. */
async function openPickerFromHeader(page: Page, target: Locator) {
  const actions = await selectMessageActions(target);
  await actions.tap();
  await expect(menu(page)).toBeVisible();
  await menu(page).getByRole("button", { name: "Mais reações" }).tap();
  const picker = page.getByRole("dialog", { name: "Escolher reação" });
  const search = picker.getByRole("searchbox", { name: "Buscar emoji" });
  await expect(search).toBeFocused();
  await expect(picker.locator(".chat-emoji-picker__emoji").first()).toBeVisible();
  await afterFrames(page);
  return { actions, picker, search };
}

/**
 * O que o menu e o picker abertos pedem de altura, medido neles como o
 * stylesheet os desenha: o menu reduzido a "Mais reações", a folga entre os
 * dois, o picker com a busca e uma linha de resultados, e o respiro da sheet.
 */
async function compactNeed(page: Page) {
  return page.evaluate(() => {
    const sum = (node: Element, ...properties: string[]) => {
      const style = getComputedStyle(node);
      return properties.reduce(
        (total, name) => total + Number.parseFloat(style.getPropertyValue(name)),
        0,
      );
    };
    const menu = document.querySelector('[role="toolbar"][aria-label="Reagir à mensagem"]')!;
    const more = menu.querySelector('[aria-label="Mais reações"]')!;
    const picker = document.querySelector('[role="dialog"][aria-label="Escolher reação"]')!;
    const sections = picker.querySelector(".chat-emoji-picker__sections")!;
    const cell = sections.querySelector(".chat-emoji-picker__emoji")!;
    const menuHeight =
      more.getBoundingClientRect().height +
      sum(menu, "padding-top", "padding-bottom", "border-top-width", "border-bottom-width");
    // Abas, quando desenhadas, não contam: o picker compacto não as tem.
    const tabs = picker.querySelector(".chat-emoji-picker__tabs")!;
    const tabsHeight = tabs.getBoundingClientRect().height;
    const pickerHeight =
      cell.getBoundingClientRect().bottom +
      sections.scrollTop -
      picker.getBoundingClientRect().top -
      (tabsHeight ? tabsHeight + sum(tabs, "margin-top", "margin-bottom") : 0) +
      sum(picker, "padding-bottom", "border-bottom-width");
    const listBottom = document
      .querySelector(".chat-msg-area__list")!
      .getBoundingClientRect().bottom;
    return {
      menu: menuHeight,
      picker: pickerHeight,
      need: menuHeight + 7 + pickerHeight + 16,
      above: listBottom - visualViewport!.offsetTop,
    };
  });
}

/**
 * Risco residual do achado 1 (CQR R6 da #1238), telas baixas com o picker
 * aberto. Medido: o menu reduzido a "Mais reações" (62px) e um picker com a
 * busca e uma linha de resultados (120px) pedem 205px com as folgas.
 * - Onde a faixa do topo da viewport visual até o fim da lista os comporta
 *   (360×375: 0…222; 360×385: 0…232, a regressão do R6; 360×400; 360×440), a
 *   sheet e o picker ficam nela, sobre o header: o compositor recebe o toque,
 *   que fecha tudo sem reagir. Uma faixa de 15rem fixos deixava 360×385 com a
 *   altura inteira, e o picker sobre o compositor.
 * - Onde não comporta (320×210 e 320×220: lista até 141; 320×320: até 167;
 *   360×350: até 197), nada que mostre um resultado cabe acima do compositor,
 *   e o picker usa a altura inteira — o compositor fica sob ele. As saídas são
 *   o "Mais reações", que fecha o picker e devolve o menu, e Escape, que
 *   devolve o foco a ele.
 * Nos dois casos, uma reação escolhida no picker chega uma vez.
 */
for (const [width, height, composerFree] of [
  [320, 210, false],
  [320, 220, false],
  [320, 320, false],
  [360, 350, false],
  [360, 375, true],
  [360, 385, true],
  [360, 400, true],
  [360, 440, true],
] as const) {
  for (const fallback of FALLBACKS) {
    test.describe(`picker aberto numa tela baixa, ${width}×${height}, ${fallback} (CQR R6 #1238)`, () => {
      test.use({ viewport: { width, height }, hasTouch: true, isMobile: true });

      test("o compositor recebe o toque onde a faixa acima dele comporta o picker; senão, há saída", async ({
        page,
      }, testInfo) => {
        const errors: string[] = [];
        page.on("pageerror", (error) => errors.push(error.message));
        const { scenario, messages } = await openConversation(page, testInfo, () =>
          prepareFallback(page, fallback),
        );
        await applyFallback(page, fallback);
        const target = messageBubble(page, messages.at(-1)!.id);
        const { picker } = await openPickerFromHeader(page, target);
        await expectWhollyVisible(picker);
        await expectWhollyVisible(menu(page));
        await expectReachable(page, await lowestWholeCell(picker));

        const geometry = await composerGeometry(page);
        const room = await compactNeed(page);
        const placed = await page.evaluate(() =>
          [
            '[role="toolbar"][aria-label="Reagir à mensagem"]',
            '[role="dialog"][aria-label="Escolher reação"]',
          ].map((selector) => {
            const box = document.querySelector(selector)!.getBoundingClientRect();
            return { top: box.top, bottom: box.bottom };
          }),
        );
        testInfo.annotations.push({
          type: "geometry",
          description: JSON.stringify({ ...room, placed, geometry }),
        });
        expect(room.above >= room.need, "a faixa acima do compositor medida").toBe(composerFree);

        if (composerFree) {
          for (const surface of geometry.surfaces) {
            expect(surface.bottom, `${surface.name}: sobre o compositor`).toBeLessThanOrEqual(
              geometry.composerTop + 0.5,
            );
          }
          expect(
            geometry.composer.filter((each) => !each.owns).map((each) => each.name),
            "o compositor não recebe o toque",
          ).toEqual([]);
          const input = page.getByTestId("chat-composer-input");
          await input.tap();
          await expect(picker).toHaveCount(0);
          await expect(menu(page)).toHaveCount(0);
          await afterFrames(page);
          await expect(input).toBeFocused();
          await expectNothingSelected(page);
        } else {
          // A restrição demonstrada: acima do compositor cabe menos do que
          // menu e picker compactos pedem.
          expect(room.above).toBeLessThan(room.need);
          const more = menu(page).getByRole("button", { name: "Mais reações" });
          await expectReachable(page, more);
          await more.tap();
          await expect(picker).toHaveCount(0);
          await expect(more).toHaveAttribute("aria-expanded", "false");
          await expect(menu(page)).toBeVisible();
          // De novo, e por Escape: o foco volta ao "Mais reações".
          await more.tap();
          await expect(picker.getByRole("searchbox", { name: "Buscar emoji" })).toBeFocused();
          await page.keyboard.press("Escape");
          await expect(picker).toHaveCount(0);
          await expect(more).toBeFocused();
        }
        expect(scenario.requests.reactions).toEqual([]);

        // A reação pelo picker, na mesma geometria: chega uma vez, e o foco
        // não cai no body.
        // Sem espaço, o menu continua aberto: pelo "Mais reações" dele.
        if (composerFree) await openPickerFromHeader(page, target);
        else {
          await menu(page).getByRole("button", { name: "Mais reações" }).tap();
          await expect(picker.locator(".chat-emoji-picker__emoji").first()).toBeVisible();
          await afterFrames(page);
        }
        const cell = await lowestWholeCell(picker);
        await expectReachable(page, cell);
        const emoji = (await cell.textContent())!.trim();
        await cell.tap();
        await expect(picker).toHaveCount(0);
        await expect(target.locator(".chat-msg-area__reactions")).toContainText(emoji);
        expect(scenario.requests.reactions).toEqual([
          { messageId: messages.at(-1)!.id, emoji, added: true },
        ]);
        expect(await page.evaluate(() => document.activeElement === document.body)).toBe(false);
        expect(errors).toEqual([]);
      });
    });
  }
}

/**
 * CQR R6 da #1238, achado 3, com e sem Popover API: a sequência de Escapes da
 * paleta inteira. O primeiro fecha a paleta; o segundo, o picker, com o menu de
 * toque e o foco no "Mais reações" mantidos (expectTouchMenuHeld); o terceiro,
 * o menu, com o foco em "Ações da mensagem"; o quarto encerra a seleção.
 */
for (const [width, height] of [
  [360, 440],
  [390, 740],
  [430, 740],
] as const) {
  for (const fallback of ["com Popover API", "sem Popover API"] as const) {
    test.describe(`Escapes da paleta a ${width}×${height}, ${fallback} (CQR R6 #1238, achado 3)`, () => {
      test.use({ viewport: { width, height }, hasTouch: true, isMobile: true });

      test("cada Escape fecha só uma superfície, e o menu de toque resiste ao hover", async ({
        page,
      }, testInfo) => {
        const errors: string[] = [];
        page.on("pageerror", (error) => errors.push(error.message));
        const { scenario, messages } = await openConversation(page, testInfo, () =>
          prepareFallback(page, fallback),
        );
        await applyFallback(page, fallback);
        const target = messageBubble(page, messages.at(-1)!.id);
        const { actions, picker, search } = await openPickerFromHeader(page, target);
        await search.fill("polegar para cima");
        const thumbs = picker.getByRole("button", { name: "polegar para cima" });
        await thumbs.tap();
        const palette = page.getByRole("dialog", { name: "Tom de pele para polegar para cima" });
        await expect(palette.getByRole("button").first()).toBeFocused();

        await page.keyboard.press("Escape");
        await expect(palette).toHaveCount(0);
        await expect(picker).toHaveCount(1);
        await expect(thumbs).toBeFocused();

        await page.keyboard.press("Escape");
        await expect(picker).toHaveCount(0);
        await expectTouchMenuHeld(page, target);

        await page.keyboard.press("Escape");
        await expect(menu(page)).toHaveCount(0);
        await expect(actions).toBeFocused();
        expect(await isSelected(target)).toBe(true);

        await page.keyboard.press("Escape");
        await expectNothingSelected(page);
        expect(scenario.requests.reactions).toEqual([]);
        expect(errors).toEqual([]);
      });
    });
  }
}

const NO_RESULT = "zzzzzzzz";

/**
 * Depois de cada troca de termo (CQR R7 da #1238): o picker continua aberto e
 * no lugar, é da mesma mensagem que o menu de toque, a busca tem o foco e a
 * seleção continua. Medido alguns quadros depois: o defeito vinha no quadro
 * seguinte ao da troca, quando a faixa nova era aplicada.
 */
async function expectSearchHeld(page: Page, messageId: string) {
  const picker = page.getByRole("dialog", { name: "Escolher reação" });
  await afterFrames(page, 4);
  await expect(picker).toHaveCount(1);
  await expect(picker.getByRole("searchbox", { name: "Buscar emoji" })).toBeFocused();
  await expect(picker).toHaveCSS("visibility", "visible");
  await expectWhollyVisible(picker);
  await expect(menu(page)).toHaveClass(/--touch/);
  await expect(selectionMode(page)).toBeVisible();
  const owners = await page.evaluate(() => {
    const menuNode = document.querySelector<HTMLElement>(
      '[role="toolbar"][aria-label="Reagir à mensagem"]',
    )!;
    const pickerNode = document.querySelector<HTMLElement>(
      '[role="dialog"][aria-label="Escolher reação"]',
    )!;
    return {
      menu:
        menuNode.closest<HTMLElement>("[data-message-id]")?.dataset.messageId ??
        menuNode.dataset.reactionPickerFor,
      picker: pickerNode.dataset.reactionPickerFor,
      selected: document
        .querySelector<HTMLElement>(".chat-msg-area__msg--selected")
        ?.closest<HTMLElement>("[data-message-id]")?.dataset.messageId,
      body: document.activeElement === document.body,
    };
  });
  expect(owners).toEqual({ menu: messageId, picker: messageId, selected: messageId, body: false });
}

/**
 * CQR R7 da #1238: a 360×350, trocar uma busca sem resultado por uma com
 * resultado fechava o picker, com o foco no body e a seleção ativa. O
 * resultado encolhia o que a sheet pede, a faixa dela subia para acima do
 * compositor, e o picker julgava a âncora pelo menu ainda onde a faixa antiga
 * o tinha posto — inteiro abaixo da nova —, como se ela tivesse saído da tela.
 * Agora o menu vai para a faixa nova antes de o picker se medir contra ele.
 * Cada transição de termo, e a rajada delas, nas telas do R6 e do R5.
 */
for (const viewport of [
  { width: 320, height: 320, isMobile: true },
  { width: 360, height: 350, isMobile: true },
  { width: 360, height: 375, isMobile: true },
  { width: 360, height: 385, isMobile: true },
  { width: 360, height: 440, isMobile: true },
  { width: 390, height: 740, isMobile: true },
  { width: 768, height: 1024, isMobile: true },
  { width: 1280, height: 560, isMobile: false },
] as const) {
  for (const fallback of FALLBACKS) {
    test.describe(`busca do picker a ${viewport.width}×${viewport.height}, ${fallback} (CQR R7 #1238)`, () => {
      test.use({
        viewport: { width: viewport.width, height: viewport.height },
        hasTouch: true,
        isMobile: viewport.isMobile,
      });

      test("sem resultado → com resultado mantém o picker aberto e o foco na busca", async ({
        page,
      }, testInfo) => {
        const errors: string[] = [];
        page.on("pageerror", (error) => errors.push(error.message));
        const { scenario, messages } = await openConversation(page, testInfo, () =>
          prepareFallback(page, fallback),
        );
        await applyFallback(page, fallback);
        const messageId = messages.at(-1)!.id;
        const target = messageBubble(page, messageId);
        const { picker, search } = await openPickerFromHeader(page, target);
        const empty = picker.getByText("Nenhum emoji encontrado");
        const rocket = picker.getByRole("button", { name: "foguete" });
        const thumbs = picker.getByRole("button", { name: "polegar para cima" });

        // A reprodução do revisor: sem resultado, depois com resultado.
        await search.fill(NO_RESULT);
        await expect(empty).toBeVisible();
        await expectSearchHeld(page, messageId);
        await search.fill("foguete");
        await expect(rocket).toBeVisible();
        await expectSearchHeld(page, messageId);
        await expectReachable(page, rocket);
        testInfo.annotations.push({
          type: "geometry",
          description: JSON.stringify(await composerGeometry(page)),
        });

        // Com resultado → sem; sem → outro resultado; resultado → resultado.
        await search.fill(NO_RESULT);
        await expect(empty).toBeVisible();
        await expectSearchHeld(page, messageId);
        await search.fill("polegar para cima");
        await expect(thumbs).toBeVisible();
        await expectSearchHeld(page, messageId);
        await search.fill("foguete");
        await expect(rocket).toBeVisible();
        await expectSearchHeld(page, messageId);

        // Uma rajada de termos, sem esperar nenhum assentar.
        for (const term of [NO_RESULT, "foguete", NO_RESULT, "polegar para cima", NO_RESULT]) {
          await search.fill(term);
        }
        await search.fill("foguete");
        await expect(rocket).toBeVisible();
        await expectSearchHeld(page, messageId);
        expect(scenario.requests.reactions).toEqual([]);

        // Escape fecha só o picker: o menu de toque fica, com o foco no
        // "Mais reações".
        await page.keyboard.press("Escape");
        await expect(picker).toHaveCount(0);
        await expectTouchMenuHeld(page, target);

        // De novo pelo menu, pela mesma transição, e o emoji escolhido chega
        // uma vez, na mensagem certa.
        await menu(page).getByRole("button", { name: "Mais reações" }).tap();
        await expect(search).toBeFocused();
        await search.fill(NO_RESULT);
        await expect(empty).toBeVisible();
        await search.fill("foguete");
        await expect(rocket).toBeVisible();
        await expectSearchHeld(page, messageId);
        expect(scenario.requests.reactions).toEqual([]);
        await rocket.tap();
        await expect(picker).toHaveCount(0);
        await expect(target.locator(".chat-msg-area__reactions")).toContainText("🚀");
        expect(scenario.requests.reactions).toEqual([{ messageId, emoji: "🚀", added: true }]);
        expect(await page.evaluate(() => document.activeElement === document.body)).toBe(false);
        expect(errors).toEqual([]);
      });
    });
  }
}

/**
 * Estado, geometria e foco de um redimensionamento (CQR R8 da #1238), para as
 * anotações: caixas verticais arredondadas, ou null quando não há o elemento.
 */
function resizeSnapshot(page: Page, messageId: string) {
  return page.evaluate((id) => {
    const box = (selector: string) => {
      const node = document.querySelector(selector);
      if (!node) return null;
      const rect = node.getBoundingClientRect();
      return [Math.round(rect.top), Math.round(rect.bottom)] as const;
    };
    const active = document.activeElement as HTMLElement | null;
    return {
      viewport: [innerWidth, innerHeight],
      menu: box('[role="toolbar"][aria-label="Reagir à mensagem"]'),
      picker: box('[role="dialog"][aria-label="Escolher reação"]'),
      palette: box(".chat-emoji-tone"),
      bubble: box(`[data-message-id="${id}"] .chat-msg-area__msg-bubble`),
      list: box(".chat-msg-area__list"),
      selected: document.querySelectorAll(".chat-msg-area__msg--selected").length,
      focus:
        active === document.body
          ? "BODY"
          : (active?.getAttribute("aria-label") ?? active?.className ?? null),
    };
  }, messageId);
}

/** O foco num elemento que ainda está no documento: nada removido o retém. */
const focusConnected = (page: Page) => page.evaluate(() => !!document.activeElement?.isConnected);

/**
 * CQR R7 da #1238, o ciclo de vida em volta da transição a 360×350: o picker
 * fecha quando deve — Escape, a mensagem removida, outra conversa —, mesmo com
 * a reposição da faixa nova ainda por vir, e resiste ao que não deve fechá-lo —
 * o catálogo ainda carregando, a tela girada e de volta.
 */
for (const fallback of FALLBACKS) {
  test.describe(`ciclo de vida da busca a 360×350, ${fallback} (CQR R7 #1238)`, () => {
    test.use({ viewport: { width: 360, height: 350 }, hasTouch: true, isMobile: true });

    async function openAt350(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      const opened = await openConversation(page, testInfo, () => prepareFallback(page, fallback));
      await applyFallback(page, fallback);
      const messageId = opened.messages.at(-1)!.id;
      return { ...opened, errors, messageId, target: messageBubble(page, messageId) };
    }

    test("com o catálogo ainda carregando, a busca sem resultado e depois com resultado", async ({
      page,
    }, testInfo) => {
      const { scenario, errors, messageId, target } = await openAt350(page, testInfo);
      // Sem esperar a primeira linha do catálogo: a busca já focada recebe o
      // termo enquanto o picker ainda se mede.
      const { picker, search } = await openTouchPicker(page, target);
      await search.fill(NO_RESULT);
      await expect(picker.getByText("Nenhum emoji encontrado")).toBeVisible();
      await search.fill("foguete");
      await expect(picker.getByRole("button", { name: "foguete" })).toBeVisible();
      await expectSearchHeld(page, messageId);
      expect(scenario.requests.reactions).toEqual([]);
      expect(errors).toEqual([]);
    });

    test("Escape logo depois da troca fecha só o picker, com a reposição pendente", async ({
      page,
    }, testInfo) => {
      const { scenario, errors, target } = await openAt350(page, testInfo);
      const { picker, search } = await openPickerFromHeader(page, target);
      await search.fill(NO_RESULT);
      await expect(picker.getByText("Nenhum emoji encontrado")).toBeVisible();
      await search.fill("foguete");
      await page.keyboard.press("Escape");
      await expect(picker).toHaveCount(0);
      await expectTouchMenuHeld(page, target);
      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);
      expect(scenario.requests.reactions).toEqual([]);
      expect(errors).toEqual([]);
    });

    /*
     * Redimensionar com o resultado aberto, ida e volta (CQR R8 da #1238): ao
     * encolher de volta para 360×350, o WebKit põe a lista mais baixa um quadro
     * antes de a timeline devolver a rolagem, e nesse quadro a mensagem lia
     * 306–347 com a lista em 117–197. O menu fechava por essa leitura — picker,
     * busca e foco junto —, com a seleção ativa. Agora a leitura é refeita dois
     * quadros depois, e a mensagem está de volta: nada fecha.
     */
    test("redimensionar com o resultado aberto, ida e volta, mantém picker e foco", async ({
      page,
    }, testInfo) => {
      const { scenario, errors, messageId, target } = await openAt350(page, testInfo);
      const { picker, search } = await openPickerFromHeader(page, target);
      await search.fill(NO_RESULT);
      await expect(picker.getByText("Nenhum emoji encontrado")).toBeVisible();
      await search.fill("foguete");
      await expect(picker.getByRole("button", { name: "foguete" })).toBeVisible();
      await expectSearchHeld(page, messageId);
      for (const viewport of [
        { width: 360, height: 440 },
        { width: 430, height: 440 },
        { width: 360, height: 350 },
      ]) {
        const before = await resizeSnapshot(page, messageId);
        await page.setViewportSize(viewport);
        await expectSearchHeld(page, messageId);
        const after = await resizeSnapshot(page, messageId);
        testInfo.annotations.push({
          type: `resize ${viewport.width}×${viewport.height}`,
          description: JSON.stringify({ before, after }),
        });
      }
      await expect(picker.getByRole("button", { name: "foguete" })).toBeVisible();
      await search.fill(NO_RESULT);
      await expect(picker.getByText("Nenhum emoji encontrado")).toBeVisible();
      await expectSearchHeld(page, messageId);
      await search.fill("foguete");
      await expect(picker.getByRole("button", { name: "foguete" })).toBeVisible();
      await expectSearchHeld(page, messageId);
      expect(scenario.requests.reactions).toEqual([]);
      expect(errors).toEqual([]);
    });

    test("redimensionar ida e volta com a paleta de tons aberta não fecha nada", async ({
      page,
    }, testInfo) => {
      const { scenario, errors, messageId, target } = await openAt350(page, testInfo);
      const { picker, search } = await openPickerFromHeader(page, target);
      await search.fill("polegar para cima");
      const thumbs = picker.getByRole("button", { name: "polegar para cima" });
      await thumbs.tap();
      const palette = page.getByRole("dialog", { name: "Tom de pele para polegar para cima" });
      await expect(palette.getByRole("button").first()).toBeFocused();
      for (const viewport of [
        { width: 360, height: 440 },
        { width: 430, height: 440 },
        { width: 360, height: 350 },
      ]) {
        await page.setViewportSize(viewport);
        await afterFrames(page, 4);
        await expect(palette).toHaveCount(1);
        await expect(palette.getByRole("button").first()).toBeFocused();
        await expect(picker).toHaveCount(1);
        await expect(menu(page)).toHaveClass(/--touch/);
        await expect(selectionMode(page)).toBeVisible();
        testInfo.annotations.push({
          type: `resize ${viewport.width}×${viewport.height}`,
          description: JSON.stringify(await resizeSnapshot(page, messageId)),
        });
      }
      // A sequência de Escape do F3 segue inteira depois da volta.
      await page.keyboard.press("Escape");
      await expect(palette).toHaveCount(0);
      await expect(thumbs).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(picker).toHaveCount(0);
      await expectTouchMenuHeld(page, target);
      expect(scenario.requests.reactions).toEqual([]);
      expect(errors).toEqual([]);
    });

    /*
     * O outro lado (CQR R8): a mensagem que a tela encolhida deixa de fato fora
     * da lista. Menu e picker fecham juntos, a seleção acaba e o foco que estava
     * na busca vai para a lista da conversa — nunca para o body.
     */
    test("encolher a tela até a mensagem sair de vez fecha tudo, com o foco na lista", async ({
      page,
    }, testInfo) => {
      const { scenario, errors, messages } = await openAt350(page, testInfo);
      await page.setViewportSize({ width: 360, height: 740 });
      await afterFrames(page, 4);
      // Abaixo de 197 a 740 — a lista de 360×350 termina aí —, mantida a
      // rolagem ou colada ao fim, ela fica fora.
      const messageId = messages.at(-3)!.id;
      const { picker, search } = await openPickerFromHeader(page, messageBubble(page, messageId));
      await search.fill("foguete");
      await expect(picker.getByRole("button", { name: "foguete" })).toBeVisible();
      const before = await resizeSnapshot(page, messageId);
      await page.setViewportSize({ width: 360, height: 350 });
      await afterFrames(page, 4);
      const after = await resizeSnapshot(page, messageId);
      testInfo.annotations.push({
        type: "resize 360×350",
        description: JSON.stringify({ before, after }),
      });
      // De fato fora da lista, não por um quadro.
      const [bubbleTop, bubbleBottom] = after.bubble ?? [0, 0];
      const [listTop, listBottom] = after.list!;
      expect(
        after.bubble === null || bubbleBottom <= listTop || bubbleTop >= listBottom,
        JSON.stringify({ before, after }),
      ).toBe(true);
      await expect(picker).toHaveCount(0);
      await expect(menu(page)).toHaveCount(0);
      await expectNothingSelected(page);
      await expect(page.locator(".chat-msg-area__list")).toBeFocused();
      expect(scenario.requests.reactions).toEqual([]);
      expect(errors).toEqual([]);
    });

    test("a mensagem removida logo depois da troca leva menu e picker, sem foco órfão", async ({
      page,
    }, testInfo) => {
      const { scenario, errors, messageId, target } = await openAt350(page, testInfo);
      const { picker, search } = await openPickerFromHeader(page, target);
      await search.fill(NO_RESULT);
      await expect(picker.getByText("Nenhum emoji encontrado")).toBeVisible();
      const targetId = messageId.replace(/-m\d+$/, "");
      const stored = messagesFor(scenario, "channel", targetId);
      const index = stored.findIndex((message) => message.id === messageId);
      stored[index] = makeMessage({
        ...stored[index],
        body_text: undefined,
        status: "deleted",
        is_removed: true,
        deleted_at: "2026-07-15T12:30:00.000Z",
        updated_at: "2026-07-15T12:30:00.000Z",
      });
      await search.fill("foguete");
      await emitConversationEvent(page, scenario, {
        kind: "channel",
        targetId,
        message: stored[index],
      });
      await expect(picker).toHaveCount(0);
      await expectNothingSelected(page);
      expect(await focusConnected(page)).toBe(true);
      expect(scenario.requests.reactions).toEqual([]);
      expect(errors).toEqual([]);
    });

    test("trocar de conversa logo depois da troca de termo fecha tudo, sem foco órfão", async ({
      page,
    }, testInfo) => {
      const { scenario, errors, target } = await openAt350(page, testInfo);
      const { picker, search } = await openPickerFromHeader(page, target);
      await search.fill(NO_RESULT);
      await expect(picker.getByText("Nenhum emoji encontrado")).toBeVisible();
      await search.fill("foguete");
      await page.evaluate((id) => {
        history.pushState(null, "", `/chat/channel/${id}`);
        dispatchEvent(new PopStateEvent("popstate"));
      }, OTHER_CHANNEL_ID);
      await expect(page).toHaveURL(`/chat/channel/${OTHER_CHANNEL_ID}`);
      await expect(picker).toHaveCount(0);
      await expectNothingSelected(page);
      expect(await focusConnected(page)).toBe(true);
      expect(scenario.requests.reactions).toEqual([]);
      expect(errors).toEqual([]);
    });
  });
}

/**
 * Cada tom da paleta aberta (CQR R9 da #1238), medido depois da animação de
 * abertura: a caixa, se está inteira na viewport visual e quem é dono do centro
 * dele no hit-test.
 */
async function tonesReachable(palette: Locator) {
  await palette.evaluate((element) =>
    Promise.all(element.getAnimations().map((animation) => animation.finished)),
  );
  return palette.evaluate((element) => {
    const view = window.visualViewport;
    const top = view?.offsetTop ?? 0;
    const left = view?.offsetLeft ?? 0;
    const bottom = top + (view?.height ?? innerHeight);
    const right = left + (view?.width ?? innerWidth);
    return [...element.querySelectorAll("button")].map((option) => {
      const r = option.getBoundingClientRect();
      const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      const owner = hit?.closest<HTMLElement>('[role="toolbar"], [role="dialog"]');
      return {
        name: option.getAttribute("aria-label"),
        box: [Math.round(r.top), Math.round(r.bottom), Math.round(r.left), Math.round(r.right)],
        inView: r.top >= top && r.bottom <= bottom && r.left >= left && r.right <= right,
        own: !!hit && option.contains(hit),
        hitBy: owner?.getAttribute("aria-label") ?? hit?.tagName ?? null,
      };
    });
  });
}

/** Os seis tons inteiros na viewport visual, cada um dono do próprio centro. */
async function expectTonesReachable(
  palette: Locator,
  testInfo: Parameters<typeof uniqueId>[0],
  label: string,
) {
  const tones = await tonesReachable(palette);
  testInfo.annotations.push({ type: `tons ${label}`, description: JSON.stringify(tones) });
  expect(tones).toHaveLength(6);
  for (const tone of tones) {
    expect(tone.inView, `${label}, ${tone.name}: fora da viewport visual`).toBe(true);
    expect(tone.own, `${label}, ${tone.name}: o centro é de ${tone.hitBy}`).toBe(true);
  }
}

/**
 * CQR R9 da #1238: a paleta de tons aberta a 430×440 ficava onde a célula
 * estava ao abrir (y 133–187) depois de a tela encolher para 360×350; a célula
 * ia para 65–114 e o menu de toque para 127–189, por cima de todos os tons — o
 * foco no "Padrão", nenhum tom tocável. A paleta agora acompanha a célula
 * sempre que o picker é posicionado, e fecha, com o foco na busca, quando a
 * célula sai de vista.
 */
for (const fallback of FALLBACKS) {
  test.describe(`paleta de tons ao redimensionar, ${fallback} (CQR R9 #1238)`, () => {
    test.use({ viewport: { width: 430, height: 440 }, hasTouch: true, isMobile: true });

    async function openPicker(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      const opened = await openConversation(page, testInfo, () => prepareFallback(page, fallback));
      await applyFallback(page, fallback);
      const messageId = opened.messages.at(-1)!.id;
      const target = messageBubble(page, messageId);
      return {
        ...opened,
        errors,
        messageId,
        target,
        ...(await openPickerFromHeader(page, target)),
      };
    }

    async function openThumbsPalette(page: Page, picker: Locator, search: Locator) {
      await search.fill("polegar para cima");
      const thumbs = picker.getByRole("button", { name: "polegar para cima" });
      await thumbs.tap();
      const palette = page.getByRole("dialog", { name: "Tom de pele para polegar para cima" });
      await expect(palette.getByRole("button").first()).toBeFocused();
      return { thumbs, palette };
    }

    async function openPalette(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
      const opened = await openPicker(page, testInfo);
      return { ...opened, ...(await openThumbsPalette(page, opened.picker, opened.search)) };
    }

    async function resize(page: Page, width: number, height: number) {
      await page.setViewportSize({ width, height });
      await afterFrames(page, 4);
    }

    /** A paleta ainda aberta, com o foco num tom que se vê, e a seleção de pé. */
    async function expectPaletteHeld(page: Page, palette: Locator) {
      await expect(palette).toHaveCount(1);
      const focused = palette.getByRole("button").first();
      await expect(focused).toBeFocused();
      await expect(focused).toBeInViewport();
      await expect(menu(page)).toHaveClass(/--touch/);
      await expect(selectionMode(page)).toBeVisible();
    }

    /**
     * Um toque de verdade num tom, sem `force`: uma reação, na mensagem certa,
     * nenhuma nas vizinhas, tudo fechado e o foco num elemento que existe.
     */
    async function tapToneOnce(page: Page, opened: Awaited<ReturnType<typeof openPalette>>) {
      const tone = opened.palette.getByRole("button").nth(3);
      const emoji = (await tone.textContent())!.trim();
      await tone.tap();
      await expect(opened.palette).toHaveCount(0);
      await expect(opened.picker).toHaveCount(0);
      await expect(opened.target.locator(".chat-msg-area__reactions")).toContainText(emoji);
      await expect(page.locator(".chat-msg-area__reactions")).toHaveCount(1);
      expect(opened.scenario.requests.reactions).toEqual([
        { messageId: opened.messageId, emoji, added: true },
      ]);
      expect(await page.evaluate(() => document.activeElement === document.body)).toBe(false);
      expect(await focusConnected(page)).toBe(true);
    }

    test("aberta a 430×440 e encolhida para 360×350: seis tons tocáveis e uma reação", async ({
      page,
    }, testInfo) => {
      const opened = await openPalette(page, testInfo);
      await expectTonesReachable(opened.palette, testInfo, "430×440");
      await resize(page, 360, 350);
      await expectPaletteHeld(page, opened.palette);
      testInfo.annotations.push({
        type: "360×350",
        description: JSON.stringify(await resizeSnapshot(page, opened.messageId)),
      });
      await expectTonesReachable(opened.palette, testInfo, "360×350");
      await tapToneOnce(page, opened);
      expect(opened.errors).toEqual([]);
    });

    test("encolhida para 360×350, Escape fecha a paleta e depois o picker, com o foco certo", async ({
      page,
    }, testInfo) => {
      const opened = await openPalette(page, testInfo);
      await resize(page, 360, 350);
      await expectTonesReachable(opened.palette, testInfo, "360×350");
      await page.keyboard.press("Escape");
      await expect(opened.palette).toHaveCount(0);
      await expect(opened.thumbs).toBeFocused();
      await expect(opened.thumbs).toBeInViewport();
      await page.keyboard.press("Escape");
      await expect(opened.picker).toHaveCount(0);
      await expectTouchMenuHeld(page, opened.target);
      expect(opened.scenario.requests.reactions).toEqual([]);
      expect(opened.errors).toEqual([]);
    });

    test("redimensionada em sequência, de volta à medida inicial, segue tocável", async ({
      page,
    }, testInfo) => {
      const opened = await openPalette(page, testInfo);
      for (const [width, height] of [
        [360, 350],
        [430, 440],
        [360, 350],
        [430, 440],
      ] as const) {
        await resize(page, width, height);
        await expectPaletteHeld(page, opened.palette);
        await expectTonesReachable(opened.palette, testInfo, `${width}×${height}`);
      }
      await tapToneOnce(page, opened);
      expect(opened.errors).toEqual([]);
    });

    test("crescendo de 360×350 para 430×440, segue tocável", async ({ page }, testInfo) => {
      await page.setViewportSize({ width: 360, height: 350 });
      const opened = await openPalette(page, testInfo);
      await expectTonesReachable(opened.palette, testInfo, "360×350");
      await resize(page, 430, 440);
      await expectPaletteHeld(page, opened.palette);
      await expectTonesReachable(opened.palette, testInfo, "430×440");
      await tapToneOnce(page, opened);
      expect(opened.errors).toEqual([]);
    });

    test("aberta depois de encolher, tocável", async ({ page }, testInfo) => {
      const opened = await openPicker(page, testInfo);
      await resize(page, 360, 350);
      const palette = await openThumbsPalette(page, opened.picker, opened.search);
      await expectTonesReachable(palette.palette, testInfo, "360×350");
      await tapToneOnce(page, { ...opened, ...palette });
      expect(opened.errors).toEqual([]);
    });

    test("a 390×740, tocável", async ({ page }, testInfo) => {
      await page.setViewportSize({ width: 390, height: 740 });
      const opened = await openPalette(page, testInfo);
      await expectTonesReachable(opened.palette, testInfo, "390×740");
      await tapToneOnce(page, opened);
      expect(opened.errors).toEqual([]);
    });

    test("a 360×385, e encolhida para 360×350, tocável", async ({ page }, testInfo) => {
      await page.setViewportSize({ width: 360, height: 385 });
      const opened = await openPalette(page, testInfo);
      await expectTonesReachable(opened.palette, testInfo, "360×385");
      await resize(page, 360, 350);
      await expectPaletteHeld(page, opened.palette);
      await expectTonesReachable(opened.palette, testInfo, "360×350");
      await tapToneOnce(page, opened);
      expect(opened.errors).toEqual([]);
    });

    test("uma busca nova com a paleta aberta a fecha, com o foco na busca", async ({
      page,
    }, testInfo) => {
      const opened = await openPalette(page, testInfo);
      await resize(page, 360, 350);
      await opened.search.fill("foguete");
      await expect(opened.picker.getByRole("button", { name: "foguete" })).toBeVisible();
      await expect(opened.palette).toHaveCount(0);
      await expect(opened.search).toBeFocused();
      await expectSearchHeld(page, opened.messageId);
      expect(opened.scenario.requests.reactions).toEqual([]);
      expect(opened.errors).toEqual([]);
    });

    test("Escape logo depois do resize, antes de assentar, fecha só a paleta", async ({
      page,
    }, testInfo) => {
      const opened = await openPalette(page, testInfo);
      await page.setViewportSize({ width: 360, height: 350 });
      await page.keyboard.press("Escape");
      await expect(opened.palette).toHaveCount(0);
      await afterFrames(page, 4);
      // Nada atrasado a reabre nem tira o foco do emoji.
      await expect(opened.palette).toHaveCount(0);
      await expect(opened.thumbs).toBeFocused();
      await expect(opened.thumbs).toBeInViewport();
      await expect(opened.picker).toHaveCount(1);
      expect(opened.scenario.requests.reactions).toEqual([]);
      expect(opened.errors).toEqual([]);
    });

    test("a célula rolada para fora dos resultados fecha a paleta, com o foco na busca", async ({
      page,
    }, testInfo) => {
      const opened = await openPicker(page, testInfo);
      await opened.search.fill("mão");
      const cell = opened.picker
        .locator('.chat-emoji-picker__emoji[aria-haspopup="dialog"]')
        .first();
      await cell.tap();
      const palette = page.getByRole("dialog", { name: /^Tom de pele para / });
      await expect(palette.getByRole("button").first()).toBeFocused();
      await cell.evaluate((element) => {
        const results = element.closest(".chat-emoji-picker__sections")!;
        results.scrollTop +=
          element.getBoundingClientRect().bottom - results.getBoundingClientRect().top + 8;
      });
      await expect(palette).toHaveCount(0);
      await expect(opened.search).toBeFocused();
      await expect(opened.picker).toHaveCount(1);
      await expect(selectionMode(page)).toBeVisible();
      expect(opened.scenario.requests.reactions).toEqual([]);
      expect(opened.errors).toEqual([]);
    });

    test("o foco que o leitor leva para a busca fica lá, com ou sem resize", async ({
      page,
    }, testInfo) => {
      // A 360×350 a paleta fica abaixo da célula e a busca, acima, livre.
      const opened = await openPalette(page, testInfo);
      await resize(page, 360, 350);
      await opened.search.tap();
      await expect(opened.palette).toHaveCount(0);
      await expect(opened.search).toBeFocused();
      await resize(page, 430, 440);
      await expect(opened.search).toBeFocused();
      await expect(opened.palette).toHaveCount(0);
      await expect(opened.picker).toHaveCount(1);
      expect(opened.scenario.requests.reactions).toEqual([]);
      expect(opened.errors).toEqual([]);
    });

    test("trocar de conversa com a paleta aberta fecha tudo, sem reação", async ({
      page,
    }, testInfo) => {
      const opened = await openPalette(page, testInfo);
      await resize(page, 360, 350);
      await page.evaluate((id) => {
        history.pushState(null, "", `/chat/channel/${id}`);
        dispatchEvent(new PopStateEvent("popstate"));
      }, OTHER_CHANNEL_ID);
      await expect(page).toHaveURL(`/chat/channel/${OTHER_CHANNEL_ID}`);
      await expect(opened.palette).toHaveCount(0);
      await expect(opened.picker).toHaveCount(0);
      await expectNothingSelected(page);
      expect(await focusConnected(page)).toBe(true);
      expect(opened.scenario.requests.reactions).toEqual([]);
      expect(opened.errors).toEqual([]);
    });

    test("a mensagem removida com a paleta aberta leva tudo, sem reação", async ({
      page,
    }, testInfo) => {
      const opened = await openPalette(page, testInfo);
      await resize(page, 360, 350);
      const targetId = opened.messageId.replace(/-m\d+$/, "");
      const stored = messagesFor(opened.scenario, "channel", targetId);
      const index = stored.findIndex((message) => message.id === opened.messageId);
      stored[index] = makeMessage({
        ...stored[index],
        body_text: undefined,
        status: "deleted",
        is_removed: true,
        deleted_at: "2026-07-15T12:30:00.000Z",
        updated_at: "2026-07-15T12:30:00.000Z",
      });
      await emitConversationEvent(page, opened.scenario, {
        kind: "channel",
        targetId,
        message: stored[index],
      });
      await expect(opened.palette).toHaveCount(0);
      await expect(opened.picker).toHaveCount(0);
      await expectNothingSelected(page);
      expect(await focusConnected(page)).toBe(true);
      expect(opened.scenario.requests.reactions).toEqual([]);
      expect(opened.errors).toEqual([]);
    });

    test("fechar o picker pelo Mais reações leva a paleta junto, sem reação", async ({
      page,
    }, testInfo) => {
      const opened = await openPalette(page, testInfo);
      await resize(page, 360, 350);
      await menu(page).getByRole("button", { name: "Mais reações" }).tap();
      await expect(opened.palette).toHaveCount(0);
      await expect(opened.picker).toHaveCount(0);
      await expect(menu(page)).toHaveClass(/--touch/);
      expect(await focusConnected(page)).toBe(true);
      expect(opened.scenario.requests.reactions).toEqual([]);
      expect(opened.errors).toEqual([]);
    });
  });
}

/**
 * CQR R5 da #1238, achado 1: com o picker aberto, o centro do compositor era
 * do menu ou do picker no hit-test, em Chromium e WebKit, a 390×740, 768×1024 e
 * 1280×560 — a sheet e o picker usavam a altura inteira da viewport visual. A
 * faixa deles agora é a da lista: abaixo do header, acima do compositor.
 */
for (const viewport of [
  { width: 390, height: 740, isMobile: true },
  { width: 768, height: 1024, isMobile: true },
  { width: 1280, height: 560, isMobile: false },
] as const) {
  for (const fallback of FALLBACKS) {
    test.describe(`compositor com o picker aberto a ${viewport.width}×${viewport.height}, ${fallback} (CQR R5 #1238)`, () => {
      test.use({
        viewport: { width: viewport.width, height: viewport.height },
        hasTouch: true,
        isMobile: viewport.isMobile,
      });

      test("o compositor e o header recebem o toque; o picker reage uma vez", async ({
        page,
      }, testInfo) => {
        const errors: string[] = [];
        page.on("pageerror", (error) => errors.push(error.message));
        const { scenario, messages } = await openConversation(page, testInfo, () =>
          prepareFallback(page, fallback),
        );
        await applyFallback(page, fallback);
        const target = messageBubble(page, messages.at(-2)!.id);
        await expect(target).toBeVisible();

        const { picker, search } = await openPickerFromHeader(page, target);
        const geometry = await composerGeometry(page);
        testInfo.annotations.push({ type: "geometry", description: JSON.stringify(geometry) });
        expectComposerClear(geometry);
        await expectWhollyVisible(picker);
        await expectWhollyVisible(menu(page));
        await expectInConversationColumn(page);

        // O picker segue funcional, e o compositor livre depois da busca também.
        await search.fill("foguete");
        const rocket = picker.getByRole("button", { name: "foguete" });
        await expect(rocket).toBeVisible();
        await expectReachable(page, rocket);
        expectComposerClear(await composerGeometry(page));

        // Com o picker aberto, o toque no compositor é dele: o foco vai para o
        // campo, menu e picker fecham, a seleção acaba e nada reage.
        const input = page.getByTestId("chat-composer-input");
        await input.tap();
        await expect(picker).toHaveCount(0);
        await expect(menu(page)).toHaveCount(0);
        await afterFrames(page);
        await expect(input).toBeFocused();
        await expectNothingSelected(page);
        expect(scenario.requests.reactions).toEqual([]);

        // De novo, pelo picker: uma reação, na mensagem certa.
        const again = await openPickerFromHeader(page, target);
        await again.search.fill("foguete");
        await again.picker.getByRole("button", { name: "foguete" }).tap();
        await expect(again.picker).toHaveCount(0);
        await expect(target.locator(".chat-msg-area__reactions")).toContainText("🚀");
        expect(scenario.requests.reactions).toEqual([
          { messageId: messages.at(-2)!.id, emoji: "🚀", added: true },
        ]);
        expect(errors).toEqual([]);
      });
    });
  }
}

/** Onde está o foco, e se é um elemento que o leitor vê. */
const focusState = (page: Page) =>
  page.evaluate(() => {
    const active = document.activeElement as HTMLElement | null;
    const body = !active || active === document.body;
    const box = active?.getBoundingClientRect();
    const view = window.visualViewport;
    const top = view?.offsetTop ?? 0;
    const bottom = top + (view?.height ?? innerHeight);
    return {
      body,
      connected: !!active?.isConnected,
      visible:
        !body &&
        !!box &&
        box.width > 0 &&
        box.bottom > top &&
        box.top < bottom &&
        (active!.checkVisibility?.() ?? true),
      label: active?.getAttribute("aria-label") || active?.className || active?.tagName || "",
    };
  });

/** A bolha contra a viewport visual, com as duas caixas para o relatório. */
const bubbleAgainstVisualViewport = (target: Locator) =>
  target.evaluate((shell) => {
    const box = shell.querySelector(".chat-msg-area__msg-bubble")!.getBoundingClientRect();
    const view = window.visualViewport!;
    const round = (a: number, b: number) => `${Math.round(a)}…${Math.round(b)}`;
    return {
      inView:
        box.bottom > view.offsetTop &&
        box.top < view.offsetTop + view.height &&
        box.right > view.offsetLeft &&
        box.left < view.offsetLeft + view.width,
      bubble: `x=${round(box.left, box.right)} y=${round(box.top, box.bottom)}`,
      view: `x=${round(view.offsetLeft, view.offsetLeft + view.width)} y=${round(view.offsetTop, view.offsetTop + view.height)} ×${view.scale}`,
    };
  });
const bubbleInVisualViewport = async (target: Locator) =>
  (await bubbleAgainstVisualViewport(target)).inView;

async function setPageScale(page: Page, scale: number) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Emulation.setPageScaleFactor", { pageScaleFactor: scale });
  await expect
    .poll(() => page.evaluate(() => window.visualViewport?.scale ?? 1))
    .toBeCloseTo(scale, 1);
  await afterFrames(page);
}

/**
 * CQR R5 da #1238, achado 2: selecionada, sem menu, e o zoom de 1× para 2× —
 * a viewport visual em y=0…370 e a bolha em y=528…569 —, a linha seguia
 * montada, focada e selecionada, e o header oferecia ações para ela. O
 * IntersectionObserver da lista não vê a viewport visual mudar.
 */
test.describe("seleção sob zoom a 390×740 (CQR R5 #1238)", () => {
  test.use({ viewport: { width: 390, height: 740 }, hasTouch: true, isMobile: true });

  for (const entry of ["toque", "teclado", "menu aberto", "menu fechado"] as const) {
    test(`o zoom que tira a mensagem da viewport visual encerra a seleção (${entry})`, async ({
      page,
      browserName,
    }, testInfo) => {
      test.skip(browserName !== "chromium", "o zoom de página só é emulável pelo CDP");
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      const { scenario, messages } = await openConversation(page, testInfo);
      // A 2× a viewport visual fica em y=0…370: a mensagem é uma das de baixo,
      // e a pré-condição é conferida abaixo. A última para o teclado (o
      // Shift+Tab chega a ela primeiro) e com o menu aberto.
      const id = messages.at(entry === "teclado" || entry === "menu aberto" ? -1 : -2)!.id;
      const target = messageBubble(page, id);
      await expect(target).toBeVisible();
      if (entry === "teclado") {
        // O Shift+Tab de verdade, do fim do documento até a última mensagem.
        for (let step = 0; step < 20 && !(await isFocused(target)); step += 1) {
          await page.keyboard.press("Shift+Tab");
        }
        await expect(target).toBeFocused();
        await expect.poll(() => isSelected(target)).toBe(true);
      }
      const actions = await selectMessageActions(target);
      if (entry === "menu aberto" || entry === "menu fechado") {
        await actions.tap();
        await expect(menu(page)).toBeVisible();
      }
      if (entry === "menu fechado") {
        await page.keyboard.press("Escape");
        await expect(menu(page)).toHaveCount(0);
        await expect(actions).toBeFocused();
      }
      await expect(selectionMode(page)).toBeVisible();
      expect(await bubbleInVisualViewport(target)).toBe(true);

      await setPageScale(page, 2);
      const zoomed = await bubbleAgainstVisualViewport(target);
      testInfo.annotations.push({ type: "zoom 2×", description: JSON.stringify(zoomed) });
      expect(zoomed.inView, `a mensagem segue visível: ${JSON.stringify(zoomed)}`).toBe(false);
      await expectNothingSelected(page);
      expect(await headerControls(page)).not.toContain("Ações da mensagem");
      await expect.poll(() => focusState(page)).toMatchObject({ body: false, visible: true });
      expect(await target.evaluate((row) => row.contains(document.activeElement))).toBe(false);

      // De volta a 1×: nada ressuscita.
      await setPageScale(page, 1);
      expect(await bubbleInVisualViewport(target)).toBe(true);
      await afterFrames(page, 20);
      await expectNothingSelected(page);
      expect(await isSelected(target)).toBe(false);
      expect(scenario.requests.reactions).toEqual([]);
      expect(errors).toEqual([]);
    });
  }
});

/**
 * CQR R5 da #1238, achado 3: com o menu aberto pelo header, um toque no texto
 * "Mensagem selecionada" — fora do menu, não focável — fechava o menu e
 * deixava o foco no body, com a seleção ainda ativa.
 */
for (const popover of ["com Popover API", "sem Popover API"] as const) {
  test.describe(`toque fora do menu a 360×740, ${popover} (CQR R5 #1238)`, () => {
    test.use({ viewport: { width: 360, height: 740 }, hasTouch: true, isMobile: true });

    const open = async (page: Page, testInfo: Parameters<typeof uniqueId>[0], count = 12) => {
      const opened = await openConversation(
        page,
        testInfo,
        async () => {
          if (popover === "sem Popover API") await withoutPopover(page);
        },
        { count },
      );
      const target = messageBubble(page, opened.messages.at(-1)!.id);
      await expect(target).toBeVisible();
      const actions = await selectMessageActions(target);
      await actions.tap();
      await expect(menu(page)).toBeVisible();
      return { ...opened, target, actions };
    };

    /** Menu fechado, nada reagido, e o foco num elemento visível — nunca o body. */
    const expectClosedWithFocus = async (
      page: Page,
      scenario: Awaited<ReturnType<typeof open>>["scenario"],
    ) => {
      await expect(menu(page)).toHaveCount(0);
      await afterFrames(page);
      expect(await focusState(page)).toMatchObject({ body: false, connected: true, visible: true });
      expect(scenario.requests.reactions).toEqual([]);
    };

    test("no texto do header: a seleção segue, com o foco no botão de ações", async ({
      page,
    }, testInfo) => {
      const { scenario, actions } = await open(page, testInfo);
      await page.getByText("Mensagem selecionada", { exact: true }).tap();
      await expectClosedWithFocus(page, scenario);
      await expect(selectionMode(page)).toBeVisible();
      await expect(actions).toBeFocused();
      await expect(actions).toHaveAttribute("aria-expanded", "false");
    });

    test("na área vazia da conversa: foco num destino válido da conversa", async ({
      page,
    }, testInfo) => {
      const { scenario, actions } = await open(page, testInfo, 2);
      const point = await page.locator(".chat-msg-area__list").evaluate((list) => {
        const box = list.getBoundingClientRect();
        const x = box.left + box.width / 2;
        for (let y = box.top + 4; y < box.bottom - 4; y += 4) {
          const hit = document.elementFromPoint(x, y);
          if (!hit || !list.contains(hit) || hit.closest("[data-message-id], button, a")) continue;
          // Nothing with a role of its own between the point and the log.
          if (hit.closest("[role]") === list) return { x, y };
        }
        return null;
      });
      expect(point, "nenhum ponto vazio na lista").not.toBeNull();
      await page.touchscreen.tap(point!.x, point!.y);
      await expectClosedWithFocus(page, scenario);
      // A seleção seguindo, o foco é do modo; encerrada, da lista da conversa.
      if (await selectionMode(page).count()) await expect(actions).toBeFocused();
      else await expect(page.locator(".chat-msg-area__list")).toBeFocused();
    });

    test("no compositor: o foco é dele e não é roubado", async ({ page }, testInfo) => {
      const { scenario } = await open(page, testInfo);
      const input = page.getByTestId("chat-composer-input");
      await input.tap();
      await expectClosedWithFocus(page, scenario);
      await afterFrames(page, 20);
      await expect(input).toBeFocused();
      await expectNothingSelected(page);
    });

    test("noutro controle focável: Cancelar seleção encerra, com o foco na mensagem", async ({
      page,
    }, testInfo) => {
      const { scenario, target } = await open(page, testInfo);
      await cancelSelection(page).tap();
      await expectClosedWithFocus(page, scenario);
      await expectNothingSelected(page);
      await expect(target).toBeFocused();
    });
  });
}

/**
 * Observa, quadro a quadro, o estado que o CQR R10 (F-1 da #1238) achou depois
 * de girar o celular: o foco no body com a seleção ainda ativa. Conta os
 * quadros em que isso aconteceu desde `watchOrphanSelection`.
 */
async function watchOrphanSelection(page: Page) {
  await page.evaluate(() => {
    const w = window as unknown as { __orphan: number; __orphanFrame: number };
    w.__orphan = 0;
    const tick = () => {
      const selected = document.querySelector(".chat-msg-area__msg--selected");
      if (selected && document.activeElement === document.body) w.__orphan += 1;
      w.__orphanFrame = requestAnimationFrame(tick);
    };
    tick();
  });
  return () =>
    page.evaluate(() => {
      const w = window as unknown as { __orphan: number; __orphanFrame: number };
      cancelAnimationFrame(w.__orphanFrame);
      return w.__orphan;
    });
}

/** A bolha da mensagem dentro da faixa da lista (o que o menu de toque confirma). */
const messageInList = (target: Locator) =>
  target.evaluate((shell) => {
    const bubble = shell.querySelector(".chat-msg-area__msg-bubble")!.getBoundingClientRect();
    const list = document.querySelector(".chat-msg-area__list")!.getBoundingClientRect();
    return bubble.bottom > list.top && bubble.top < list.bottom;
  });

/** Tudo fechado com a mensagem fora de vista: seleção encerrada, foco visível. */
async function expectAllClosedWithFocus(page: Page) {
  await expect(page.getByRole("dialog", { name: "Escolher reação" })).toHaveCount(0);
  await expect(page.getByRole("dialog", { name: /^Tom de pele para / })).toHaveCount(0);
  await expectNothingSelected(page);
  expect(await focusState(page)).toMatchObject({ body: false, connected: true, visible: true });
}

/**
 * CQR R10 da #1238, F-1: girar 390×740 → 740×390 com o picker de toque aberto
 * o fechava — o foco no body, a seleção e o menu de pé. No primeiro quadro
 * depois da rotação a lista se desenha com a mensagem abaixo dela; o menu,
 * ancorado, fica onde estava enquanto essa leitura é confirmada, e o picker,
 * julgando-se contra essa caixa velha, fechava na hora. No WebKit, 740×390 →
 * 390×740 fechava tudo por outro caminho: o mouse sintético que ele dispara
 * depois do resize saía da linha, e o fechamento do hover tirava da mensagem o
 * estado de toque. Agora o picker espera a confirmação do menu: volta com ele,
 * ou fecha com ele e com a seleção, o foco na lista.
 */
const ROTATIONS = [
  [
    [390, 740],
    [740, 390],
  ],
  [
    [390, 740],
    [844, 390],
  ],
  [
    [390, 740],
    [360, 385],
  ],
  [
    [740, 390],
    [390, 740],
  ],
] as const;

for (const fallback of FALLBACKS) {
  for (const [[fromWidth, fromHeight], [toWidth, toHeight]] of ROTATIONS) {
    const rotation = `${fromWidth}×${fromHeight} → ${toWidth}×${toHeight}`;
    test.describe(`rotação ${rotation} com o picker de toque aberto, ${fallback} (CQR R10 F-1 #1238)`, () => {
      test.use({
        viewport: { width: fromWidth, height: fromHeight },
        hasTouch: true,
        isMobile: true,
      });

      async function openPicker(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
        const errors: string[] = [];
        page.on("pageerror", (error) => errors.push(error.message));
        const opened = await openConversation(page, testInfo, () =>
          prepareFallback(page, fallback),
        );
        await applyFallback(page, fallback);
        const messageId = opened.messages.at(-1)!.id;
        const target = messageBubble(page, messageId);
        return {
          ...opened,
          errors,
          messageId,
          target,
          ...(await openPickerFromHeader(page, target)),
        };
      }

      /**
       * Gira e espera a confirmação e o fechamento por hover do WebKit, que vem
       * com o tempo (`expectTouchMenuHeld`): o que tiver de fechar já fechou.
       */
      async function rotate(
        page: Page,
        testInfo: Parameters<typeof uniqueId>[0],
        messageId: string,
      ) {
        const orphanFrames = await watchOrphanSelection(page);
        await page.setViewportSize({ width: toWidth, height: toHeight });
        await afterFrames(page, 4);
        await page.waitForTimeout(600);
        await afterFrames(page, 2);
        testInfo.annotations.push({
          type: rotation,
          description: JSON.stringify(await resizeSnapshot(page, messageId)),
        });
        expect(await orphanFrames(), "quadros com o foco no body e a seleção ativa").toBe(0);
      }

      test("picker com a busca: volta com a mensagem, inteiro e tocável, e reage uma vez", async ({
        page,
      }, testInfo) => {
        const opened = await openPicker(page, testInfo);
        await rotate(page, testInfo, opened.messageId);
        if (!(await messageInList(opened.target))) {
          await expectAllClosedWithFocus(page);
          expect(opened.errors).toEqual([]);
          return;
        }
        await expectSearchHeld(page, opened.messageId);
        await expectReachable(page, opened.search);
        await opened.search.fill("foguete");
        const rocket = opened.picker.getByRole("button", { name: "foguete" });
        await expect(rocket).toBeVisible();
        await expectReachable(page, rocket);
        await rocket.tap();
        await expect(opened.picker).toHaveCount(0);
        await expect(opened.target.locator(".chat-msg-area__reactions")).toContainText("🚀");
        await expect(page.locator(".chat-msg-area__reactions")).toHaveCount(1);
        expect(opened.scenario.requests.reactions).toEqual([
          { messageId: opened.messageId, emoji: "🚀", added: true },
        ]);
        expect(await focusState(page)).toMatchObject({ body: false, connected: true });
        expect(opened.errors).toEqual([]);
      });

      test("paleta de tons: volta com a mensagem, seis tons tocáveis, e reage uma vez", async ({
        page,
      }, testInfo) => {
        const opened = await openPicker(page, testInfo);
        await opened.search.fill("polegar para cima");
        const thumbs = opened.picker.getByRole("button", { name: "polegar para cima" });
        await thumbs.tap();
        const palette = page.getByRole("dialog", { name: "Tom de pele para polegar para cima" });
        await expect(palette.getByRole("button").first()).toBeFocused();
        await rotate(page, testInfo, opened.messageId);
        if (!(await messageInList(opened.target))) {
          await expectAllClosedWithFocus(page);
          expect(opened.errors).toEqual([]);
          return;
        }
        await expect(palette).toHaveCount(1);
        await expect(palette.getByRole("button").first()).toBeFocused();
        await expect(menu(page)).toHaveClass(/--touch/);
        await expect(selectionMode(page)).toBeVisible();
        await expectTonesReachable(palette, testInfo, rotation);
        const tone = palette.getByRole("button").nth(3);
        const emoji = (await tone.textContent())!.trim();
        await tone.tap();
        await expect(palette).toHaveCount(0);
        await expect(opened.picker).toHaveCount(0);
        await expect(opened.target.locator(".chat-msg-area__reactions")).toContainText(emoji);
        await expect(page.locator(".chat-msg-area__reactions")).toHaveCount(1);
        expect(opened.scenario.requests.reactions).toEqual([
          { messageId: opened.messageId, emoji, added: true },
        ]);
        expect(await focusState(page)).toMatchObject({ body: false, connected: true });
        expect(opened.errors).toEqual([]);
      });

      // O ciclo de vida em volta da rotação, numa transição por fallback.
      if (rotation !== "390×740 → 740×390") return;

      test("Escape logo depois da rotação, antes de assentar, fecha só o picker", async ({
        page,
      }, testInfo) => {
        const opened = await openPicker(page, testInfo);
        const orphanFrames = await watchOrphanSelection(page);
        await page.setViewportSize({ width: toWidth, height: toHeight });
        await page.keyboard.press("Escape");
        await expect(opened.picker).toHaveCount(0);
        await afterFrames(page, 4);
        // Nada atrasado o reabre.
        await expect(opened.picker).toHaveCount(0);
        await expectTouchMenuHeld(page, opened.target);
        expect(await orphanFrames()).toBe(0);
        expect(opened.scenario.requests.reactions).toEqual([]);
        expect(opened.errors).toEqual([]);
      });

      test("um toque fora logo depois da rotação fecha o picker e o menu, sem reação", async ({
        page,
      }, testInfo) => {
        // Só o estado assentado: entre o pointerdown que fecha o menu e o click
        // que foca o compositor há um quadro com o foco no body — o mesmo sem
        // rotação (r12/probe-outside-tap-chromium.log), do toque fora do R5.
        const opened = await openPicker(page, testInfo);
        await page.setViewportSize({ width: toWidth, height: toHeight });
        await page.getByTestId("chat-composer-box").tap();
        await expect(opened.picker).toHaveCount(0);
        await expect(menu(page)).toHaveCount(0);
        await afterFrames(page, 4);
        await page.waitForTimeout(600);
        await expect(opened.picker).toHaveCount(0);
        await expectNothingSelected(page);
        expect(await focusState(page)).toMatchObject({
          body: false,
          connected: true,
          visible: true,
        });
        expect(opened.scenario.requests.reactions).toEqual([]);
        expect(opened.errors).toEqual([]);
      });

      test("a lista rolada à mão depois da rotação leva tudo, com o foco visível", async ({
        page,
      }, testInfo) => {
        const opened = await openPicker(page, testInfo);
        await rotate(page, testInfo, opened.messageId);
        await expectSearchHeld(page, opened.messageId);
        const orphanFrames = await watchOrphanSelection(page);
        await page.locator(".chat-msg-area__list").evaluate((list) => (list.scrollTop = 0));
        await expectAllClosedWithFocus(page);
        expect(await orphanFrames()).toBe(0);
        expect(opened.scenario.requests.reactions).toEqual([]);
        expect(opened.errors).toEqual([]);
      });

      test("a mensagem removida depois da rotação leva tudo, sem reação", async ({
        page,
      }, testInfo) => {
        const opened = await openPicker(page, testInfo);
        await rotate(page, testInfo, opened.messageId);
        const targetId = opened.messageId.replace(/-m\d+$/, "");
        const stored = messagesFor(opened.scenario, "channel", targetId);
        const index = stored.findIndex((message) => message.id === opened.messageId);
        stored[index] = makeMessage({
          ...stored[index],
          body_text: undefined,
          status: "deleted",
          is_removed: true,
          deleted_at: "2026-07-15T12:30:00.000Z",
          updated_at: "2026-07-15T12:30:00.000Z",
        });
        await emitConversationEvent(page, opened.scenario, {
          kind: "channel",
          targetId,
          message: stored[index],
        });
        await expect(opened.picker).toHaveCount(0);
        await expectNothingSelected(page);
        expect(await focusConnected(page)).toBe(true);
        expect(opened.scenario.requests.reactions).toEqual([]);
        expect(opened.errors).toEqual([]);
      });

      test("trocar de conversa logo depois da rotação fecha tudo, sem reação", async ({
        page,
      }, testInfo) => {
        const opened = await openPicker(page, testInfo);
        await page.setViewportSize({ width: toWidth, height: toHeight });
        await page.evaluate((id) => {
          history.pushState(null, "", `/chat/channel/${id}`);
          dispatchEvent(new PopStateEvent("popstate"));
        }, OTHER_CHANNEL_ID);
        await expect(page).toHaveURL(`/chat/channel/${OTHER_CHANNEL_ID}`);
        await afterFrames(page, 4);
        await page.waitForTimeout(600);
        await expect(opened.picker).toHaveCount(0);
        await expectNothingSelected(page);
        expect(await focusConnected(page)).toBe(true);
        expect(opened.scenario.requests.reactions).toEqual([]);
        expect(opened.errors).toEqual([]);
      });
    });
  }
}
