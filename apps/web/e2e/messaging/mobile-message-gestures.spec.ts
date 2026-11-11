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
  uniqueId,
} from "../helpers/messagingApi";
import {
  PAGE_SIZE,
  bubbles,
  installPaginatedMessages,
  loadPreviousPage,
  quiet,
  settledViewport,
} from "../helpers/largeConversationFixture";

const WIDTHS = [320, 360, 390, 430] as const;

const menu = (page: Page) => page.getByRole("toolbar", { name: "Reagir à mensagem" });
const composerQuote = (page: Page) => page.getByTestId("chat-composer-quote");

/** `prepare` corre com os mocks instalados, antes de a conversa carregar. */
async function openConversation(
  page: Page,
  testInfo: Parameters<typeof uniqueId>[0],
  prepare?: () => Promise<void>,
) {
  const targetId = uniqueId(testInfo, "gestures");
  const messages = Array.from({ length: 12 }, (_, index) => {
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
  const actions = target.getByRole("button", { name: "Ações da mensagem" });
  await actions.tap();
  await expect(menu(page)).toBeVisible();
  await menu(page).getByRole("button", { name: "Mais reações" }).tap();
  const picker = page.getByRole("dialog", { name: "Escolher reação" });
  const search = picker.getByRole("searchbox", { name: "Buscar emoji" });
  await expect(search).toBeFocused();
  return { actions, picker, search };
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

async function expectNoHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
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

      // Toque simples: a regressão abria a toolbar aqui, fora da tela.
      const tapAt = await bubblePoint(last);
      await page.touchscreen.tap(tapAt.x, tapAt.y);
      await expect(menu(page)).toHaveCount(0);

      const point = await bubblePoint(last);
      await finger.down(point.x, point.y);
      await expect(menu(page)).toBeVisible();
      await finger.up();

      await expectWhollyVisible(menu(page));
      await expect(menu(page).getByRole("button", { name: "Responder" })).toBeVisible();
      await expect(menu(page).getByRole("button", { name: "Mais reações" })).toBeVisible();
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
      const { messages } = await openConversation(page, testInfo);
      const target = messageBubble(page, messages.at(-2)!.id);
      const actions = target.getByRole("button", { name: "Ações da mensagem" });
      await expect(actions).toBeVisible();
      await expect(actions).toHaveAttribute("aria-expanded", "false");

      // Ao lado da bolha, nunca sobre ela, e um alvo de toque de 44px.
      const button = await actions.boundingBox();
      const bubble = await target.locator(".chat-msg-area__msg-bubble").boundingBox();
      expect(button!.width).toBeGreaterThanOrEqual(44);
      expect(button!.height).toBeGreaterThanOrEqual(44);
      const overlaps =
        button!.x < bubble!.x + bubble!.width &&
        bubble!.x < button!.x + button!.width &&
        button!.y < bubble!.y + bubble!.height &&
        bubble!.y < button!.y + button!.height;
      expect(overlaps).toBe(false);
      await expectWhollyVisible(actions);

      await page.touchscreen.tap(button!.x + button!.width / 2, button!.y + button!.height / 2);
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
      const before = (await menu(page).boundingBox())!;

      // As reações rápidas chegam: a sheet ganha uma linha, para cima.
      release();
      await expect
        .poll(async () => (await menu(page).boundingBox())!.height)
        .toBeGreaterThan(before.height);
      const after = (await menu(page).boundingBox())!;
      expect(after.y).toBeLessThan(before.y);

      // O picker acompanhou, sem fechar nem tirar o foco da busca, e parou: a
      // posição dele é a mesma por 30 quadros seguidos.
      await expect(picker).toHaveCount(1);
      await expect(search).toBeFocused();
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
      await expect(menu(page).getByRole("button", { name: "Responder" })).toBeVisible({
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
      await expect(menu(page).getByRole("button", { name: "Responder" })).toBeVisible();
      await expectWhollyVisible(menu(page));
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
    // Girar pode tirar a âncora da tela; aberto, o menu tem de estar inteiro.
    if ((await menu(page).count()) > 0) await expectWhollyVisible(menu(page));
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
    const { messages } = await openConversation(page, testInfo);
    const target = messageBubble(page, messages.at(-2)!.id);
    const actions = target.getByRole("button", { name: "Ações da mensagem" });
    await expect(actions).toBeVisible();
    // Zoom de 2x, como depois de uma pinça, e um arrasto de dedo no cabeçalho
    // (que não rola) para deslocar a viewport visual sobre a página ampliada.
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Emulation.setPageScaleFactor", { pageScaleFactor: 2 });
    await expect
      .poll(() => page.evaluate(() => window.visualViewport?.scale ?? 1))
      .toBeGreaterThan(1.5);
    const finger = await Finger.on(page);
    const header = await page.getByTestId("chat-msg-header").boundingBox();
    const from = { x: header!.x + 40, y: header!.y + header!.height / 2 };
    await finger.down(from.x, from.y);
    await finger.moveBy(from, -120, -200);
    await finger.up();
    await expect
      .poll(() =>
        page.evaluate(
          () => (window.visualViewport?.offsetLeft ?? 0) + (window.visualViewport?.offsetTop ?? 0),
        ),
      )
      .toBeGreaterThan(0);

    // Pelo teclado (o mesmo botão, a outra forma de acioná-lo): sob zoom, as
    // coordenadas de toque do Playwright não acompanham a escala da página.
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

test.describe("desktop continua no hover (issues #839/#852)", () => {
  test("hover mostra a toolbar ancorada, sem a superfície móvel", async ({ page }, testInfo) => {
    const { messages } = await openConversation(page, testInfo);
    const bubble = await revealActions(page, messages.at(-1)!.id);
    await expect(menu(page)).toBeVisible();
    await expect(menu(page)).not.toHaveClass(/--sheet/);
    await expect(bubble.getByRole("button", { name: "Responder" })).toBeVisible();
    // O botão de ações é do toque: o desktop não o desenha.
    await expect(bubble.getByRole("button", { name: "Ações da mensagem" })).toBeHidden();
  });
});
