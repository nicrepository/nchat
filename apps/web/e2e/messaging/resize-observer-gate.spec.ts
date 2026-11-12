/**
 * O gate estrito do aviso de ResizeObserver do picker de toque (issue #1152).
 *
 * Os cenários positivos (RO-A, RO-D, RO-S) só passam com o veredito do gate
 * vazio: o aviso conhecido aparece, no máximo quatro vezes, enquanto o picker
 * é posicionado, e a abertura se prova — vinte quadros estáveis, geometria
 * certa, hit-test, uma reação só e observers desligados ao fechar. Os
 * controles negativos (RO-N1 a RO-N5) provocam cada regressão que o gate tem
 * de pegar e conferem o motivo exato da reprovação, não só que ela houve.
 *
 * Feito para o WebKit e para o Chromium: tudo por `locator.tap()` sem force, e
 * a rolagem da lista por scrollTop — a roda do mouse não existe no WebKit
 * móvel, e uma rolagem programática não prova o gesto nativo, que fica para o
 * aparelho.
 */

import { expect, test, type Locator, type Page, type TestInfo } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  createScenario,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  uniqueId,
} from "../helpers/messagingApi";
import {
  RESIZE_OBSERVER_LOOP,
  attachGateOnTeardown,
  installResizeObserverGate,
  provokedErrorProblems,
  type Reason,
  type Verdict,
} from "../helpers/resizeObserverGate";

type Gate = Awaited<ReturnType<typeof installResizeObserverGate>>;

// O veredito do gate é dele, nunca de uma nova tentativa: falha aqui é achado,
// também no CI, onde a configuração padrão repete testes.
test.describe.configure({ retries: 0 });

// O log de um teste que falhou antes do veredito é anexado do mesmo jeito. A
// página ainda está aberta no afterEach; `page` só aparece porque um padrão
// vazio não passa no lint.
test.afterEach(async ({ page }, testInfo) => {
  void page;
  await attachGateOnTeardown(testInfo);
});

const menu = (page: Page) => page.getByRole("toolbar", { name: "Reagir à mensagem" });

/** Doze mensagens, como na evidência da Fase 2; `single`, a mensagem única do cenário D. */
async function openConversation(page: Page, testInfo: TestInfo, single = false) {
  const targetId = uniqueId(testInfo, "ro-gate");
  const messages = Array.from({ length: single ? 1 : 12 }, (_, index) => {
    const stamp = `2026-07-15T12:${String(index).padStart(2, "0")}:00.000Z`;
    return makeMessage({
      id: `${targetId}-m${index}`,
      sender_id: index % 2 ? CURRENT_USER_ID : OTHER_USER_ID,
      body_text: single
        ? "Âncora da mensagem no redimensionamento"
        : index % 2
          ? "OK"
          : `Mensagem ${index} ${"longa ".repeat(index * 3)}`,
      created_at: stamp,
      updated_at: stamp,
    });
  });
  const scenario = createScenario({ kind: "channel", targetId, targetName: "Gate", messages });
  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/channel/${targetId}`);
  await expect(page.getByTestId("chat-msg-header")).toBeVisible();
  return { scenario, messages };
}

/** Menu pelo botão de ações; o gate abre a janela antes do toque em "Mais reações". */
async function openGatedPicker(page: Page, gate: Gate, target: Locator) {
  const actions = target.getByRole("button", { name: "Ações da mensagem" });
  await actions.tap();
  await expect(menu(page)).toBeVisible();
  const more = menu(page).getByRole("button", { name: "Mais reações" });
  await gate.opening();
  await more.tap();
  const picker = page.getByRole("dialog", { name: "Escolher reação" });
  const search = picker.getByRole("searchbox", { name: "Buscar emoji" });
  await expect(search).toBeFocused();
  return { actions, more, picker, search };
}

/**
 * Se o centro do alvo é dele mesmo: nenhuma outra superfície por cima. Quando
 * não é, anexa quem está no centro, as duas caixas, o ponto e o scrollTop da lista.
 */
async function ownsItsCentre(target: Locator) {
  const { owns, miss } = await target.evaluate((element) => {
    const rect = (r: DOMRect) => ({ x: r.x, y: r.y, width: r.width, height: r.height });
    const box = element.getBoundingClientRect();
    const point = { x: box.left + box.width / 2, y: box.top + box.height / 2 };
    const hit = document.elementFromPoint(point.x, point.y);
    if (hit && element.contains(hit)) return { owns: true, miss: null };
    const attributes = (node: Element) =>
      Object.fromEntries(
        [...node.attributes]
          .filter((a) => /^(id|role|aria-|data-|name|type|popover)/.test(a.name))
          .map((a) => [a.name, a.value]),
      );
    return {
      owns: false,
      miss: {
        point,
        target: {
          tag: element.tagName,
          className: element.className,
          attributes: attributes(element),
          box: rect(box),
        },
        interceptor: hit && {
          tag: hit.tagName,
          className: typeof hit.className === "string" ? hit.className : "",
          attributes: attributes(hit),
          box: rect(hit.getBoundingClientRect()),
        },
        listScrollTop: document.querySelector(".chat-msg-area__list")?.scrollTop ?? null,
        viewport: { width: innerWidth, height: innerHeight },
      },
    };
  });
  if (miss) {
    const body = JSON.stringify(miss, null, 2);
    console.log(`[hit-test-miss] ${body}`);
    await test.info().attach("hit-test-miss", { body, contentType: "application/json" });
  }
  return owns;
}

/** As animações finitas do picker terminadas: medir no meio de uma escala engana. */
function finishAnimations(picker: Locator) {
  return picker.evaluate(async (element) => {
    const finite = element
      .getAnimations({ subtree: true })
      .filter((animation) => animation.effect?.getTiming().iterations !== Infinity);
    await Promise.all(finite.map((animation) => animation.finished));
  });
}

/** Uma célula inteira, ≥ 44 px, dentro da área de resultados e dona do seu centro. */
async function expectTappable(picker: Locator, cell: Locator) {
  await finishAnimations(picker);
  const box = (await cell.boundingBox())!;
  const results = (await picker.locator(".chat-emoji-picker__sections").boundingBox())!;
  expect(box.height).toBeGreaterThanOrEqual(44);
  expect(box.y).toBeGreaterThanOrEqual(results.y);
  expect(box.y + box.height).toBeLessThanOrEqual(results.y + results.height);
  const hit = await ownsItsCentre(cell);
  expect(hit, "outra superfície intercepta a célula").toBe(true);
  return hit;
}

/** Só os motivos esperados, todos presentes; `incidental` pode aparecer junto. */
function expectRejectedFor(verdict: Verdict, expected: Reason[], incidental: Reason[] = []) {
  const found = verdict.failures.map((failure) => failure.reason);
  for (const reason of expected)
    expect(found, `o gate não reprovou por ${reason}`).toContain(reason);
  const other = found.filter(
    (reason) => !expected.includes(reason) && !incidental.includes(reason),
  );
  expect(other, "o gate reprovou por outro motivo").toEqual([]);
}

test.describe("gate do ResizeObserver a 320×210", () => {
  test.use({ viewport: { width: 320, height: 210 }, hasTouch: true, isMobile: true });

  test("RO-A: aviso conhecido na abertura, uma reação e a reabertura pelo contrato do menu", async ({
    page,
  }, testInfo) => {
    const gate = await installResizeObserverGate(page, testInfo);
    const { scenario, messages } = await openConversation(page, testInfo);
    const message = messages.at(-2)!;
    const target = messageBubble(page, message.id);
    // Configuração pronta: as reações rápidas já chegaram quando o picker abre.
    await target.getByRole("button", { name: "Ações da mensagem" }).tap();
    await expect(
      menu(page).getByRole("button", { name: "Reagir rapidamente com 👍" }),
    ).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(menu(page)).toHaveCount(0);

    const { actions, more, picker, search } = await openGatedPicker(page, gate, target);
    expect(await gate.settle(), "o picker não parou por 20 quadros").toBe(true);
    await search.fill("foguete");
    const rocket = picker.getByRole("button", { name: "foguete", exact: true });
    await expect(rocket).toBeVisible();
    const hit = await expectTappable(picker, rocket);
    await rocket.tap();
    await expect(picker).toHaveCount(0);
    expect(scenario.requests.reactions).toEqual([
      { messageId: message.id, emoji: "🚀", added: true },
    ]);
    gate.selected(hit, scenario.requests.reactions.length);
    await expect(target.locator(".chat-msg-area__reactions")).toContainText("🚀");

    // O contrato: escolher pelo picker fecha só o picker. O menu fica, com o
    // foco em "Mais reações", e é por ele que o picker reabre.
    await expect(menu(page)).toBeVisible();
    await expect(more).toBeFocused();
    await gate.closed();
    expect(await ownsItsCentre(more), "outra superfície intercepta Mais reações").toBe(true);
    await gate.opening();
    await more.tap();
    await expect(search).toBeFocused();
    expect(await gate.settle(), "o picker reaberto não parou por 20 quadros").toBe(true);
    await page.keyboard.press("Escape");
    await expect(picker).toHaveCount(0);
    await expect(more).toBeFocused();
    await gate.closed();

    // Fechado o menu, o botão de ações volta a ser o caminho — tocável de novo.
    await page.keyboard.press("Escape");
    await expect(menu(page)).toHaveCount(0);
    await expect(actions).toBeFocused();
    // O ensaio rola até o centro ficar livre e falha se nenhuma posição servir;
    // scrollIntoViewIfNeeded aceitava o botão meio visível sob o composer.
    await actions.tap({ trial: true });
    expect(await ownsItsCentre(actions), "outra superfície intercepta o botão de ações").toBe(true);
    await actions.tap();
    await expect(menu(page)).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(menu(page)).toHaveCount(0);

    expect(scenario.requests.reactions).toHaveLength(1);
    expect((await gate.verdict()).failures).toEqual([]);
  });
});

test.describe("gate do ResizeObserver com o fallback a 390×740", () => {
  test.use({ viewport: { width: 390, height: 740 }, hasTouch: true, isMobile: true });

  test("RO-D: o fallback que se alarga ao ser posicionado, girando a tela", async ({
    page,
  }, testInfo) => {
    const gate = await installResizeObserverGate(page, testInfo);
    const { scenario, messages } = await openConversation(page, testInfo, true);
    const message = messages[0];
    const target = messageBubble(page, message.id);

    // Primeira abertura da página: o catálogo ainda não carregou, e o picker
    // nasce com o "Carregando emojis…" de largura automática.
    const { actions, more, picker, search } = await openGatedPicker(page, gate, target);
    await page.setViewportSize({ width: 740, height: 390 });
    await expect.poll(() => page.evaluate(() => innerWidth)).toBe(740);
    await page.setViewportSize({ width: 390, height: 740 });
    await expect.poll(() => page.evaluate(() => innerWidth)).toBe(390);
    await expect(picker).toBeVisible();
    expect(await gate.settle(), "o picker não parou por 20 quadros").toBe(true);
    await search.fill("foguete");
    await expect(picker.getByRole("button", { name: "foguete", exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(picker).toHaveCount(0);
    await expect(more).toBeFocused();
    await gate.closed();
    await page.keyboard.press("Escape");
    await expect(menu(page)).toHaveCount(0);
    await expect(actions).toBeFocused();

    // Segunda abertura, com o catálogo carregado, até a reação.
    const second = await openGatedPicker(page, gate, target);
    expect(await gate.settle(), "o picker reaberto não parou por 20 quadros").toBe(true);
    await second.search.fill("foguete");
    const rocket = second.picker.getByRole("button", { name: "foguete", exact: true });
    await expect(rocket).toBeVisible();
    const hit = await expectTappable(second.picker, rocket);
    await rocket.tap();
    await expect(second.picker).toHaveCount(0);
    expect(scenario.requests.reactions).toEqual([
      { messageId: message.id, emoji: "🚀", added: true },
    ]);
    gate.selected(hit, scenario.requests.reactions.length);
    await expect(menu(page)).toBeVisible();
    await expect(second.more).toBeFocused();
    await gate.closed();
    await page.keyboard.press("Escape");
    await expect(menu(page)).toHaveCount(0);

    expect((await gate.verdict()).failures).toEqual([]);
  });
});

for (const height of [210, 220]) {
  test.describe(`gate do ResizeObserver na rolagem a 320×${height}`, () => {
    test.use({ viewport: { width: 320, height }, hasTouch: true, isMobile: true });

    test(`RO-S ${height}: rolagem adaptada até uma célula inteira e uma reação`, async ({
      page,
    }, testInfo) => {
      const gate = await installResizeObserverGate(page, testInfo);
      const { scenario, messages } = await openConversation(page, testInfo);
      const message = messages.at(-1)!;
      const target = messageBubble(page, message.id);
      const { more, picker } = await openGatedPicker(page, gate, target);
      await expect(picker.locator(".chat-emoji-picker__emoji").first()).toBeVisible();
      expect(await gate.settle(), "o picker não parou por 20 quadros").toBe(true);
      // Tela baixa: o menu ficou só com "Mais reações" enquanto o picker está aberto.
      await expect(menu(page).getByRole("button", { name: "Responder" })).toBeHidden();

      // A primeira linha escondida abaixo vai ao topo dos resultados.
      const results = picker.locator(".chat-emoji-picker__sections");
      const index = await results.evaluate((element) => {
        const box = element.getBoundingClientRect();
        const cells = [...element.querySelectorAll(".chat-emoji-picker__emoji")];
        const hidden = cells.findIndex((cell) => cell.getBoundingClientRect().bottom > box.bottom);
        if (hidden >= 0) {
          const top = cells[hidden].getBoundingClientRect().top;
          element.scrollTop += top - box.top;
        }
        return hidden;
      });
      expect(index, "nenhuma célula escondida abaixo para rolar até ela").toBeGreaterThan(0);
      await expect.poll(() => results.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
      const cell = results.locator(".chat-emoji-picker__emoji").nth(index);
      const hit = await expectTappable(picker, cell);
      const emoji = (await cell.textContent())!.trim();
      await cell.tap();
      await expect(picker).toHaveCount(0);
      expect(scenario.requests.reactions).toEqual([{ messageId: message.id, emoji, added: true }]);
      gate.selected(hit, scenario.requests.reactions.length);
      await expect(more).toBeFocused();
      await gate.closed();
      await page.keyboard.press("Escape");
      await expect(menu(page)).toHaveCount(0);

      expect((await gate.verdict()).failures).toEqual([]);
    });
  });
}

/**
 * Controles negativos: cada um provoca uma regressão e confere que o gate a
 * reprova pelo motivo dela. Numa tela onde a abertura não dá aviso nenhum, para
 * que o motivo seja só o provocado; "missing-functional-evidence" pode vir
 * junto, porque estes testes não chegam a reagir.
 */
test.describe("controles negativos do gate do ResizeObserver", () => {
  test.use({ viewport: { width: 430, height: 740 }, hasTouch: true, isMobile: true });

  async function openAndSettle(page: Page, testInfo: TestInfo) {
    const gate = await installResizeObserverGate(page, testInfo);
    const { messages } = await openConversation(page, testInfo);
    const opened = await openGatedPicker(page, gate, messageBubble(page, messages.at(-1)!.id));
    return { gate, ...opened };
  }

  async function closeAll(page: Page, gate: Gate, picker: Locator) {
    await page.keyboard.press("Escape");
    await expect(picker).toHaveCount(0);
    await gate.closed();
    await page.keyboard.press("Escape");
    await expect(menu(page)).toHaveCount(0);
  }

  test("RO-N1: um observer que alterna de tamanho a cada quadro reprova por persistência", async ({
    page,
  }, testInfo) => {
    const { gate, picker } = await openAndSettle(page, testInfo);
    // Uma caixa que muda de tamanho dentro do próprio callback: um aviso por
    // quadro. Limitada a 600 entregas, e desligada assim que o gate decidir.
    await page.evaluate((limit) => {
      const box = document.createElement("div");
      box.style.cssText = "position:fixed;left:0;top:0;width:10px;height:10px;pointer-events:none";
      document.body.append(box);
      let deliveries = 0;
      const observer = new ResizeObserver(() => {
        deliveries++;
        if (deliveries >= limit) return stop();
        box.style.height = deliveries % 2 ? "20px" : "10px";
      });
      const stop = () => {
        observer.disconnect();
        box.remove();
      };
      (window as unknown as { __stopGateControl: () => void }).__stopGateControl = stop;
      observer.observe(box);
    }, 600);
    expect(await gate.settle(), "a alternância deixou o picker parar").toBe(false);
    await page.evaluate(() =>
      (window as unknown as { __stopGateControl: () => void }).__stopGateControl(),
    );
    await closeAll(page, gate, picker);

    const verdict = await gate.verdict();
    // Os avisos vêm de um observer que não é do picker nem do menu.
    expectRejectedFor(
      verdict,
      ["too-many-notices", "not-settled", "unattributed-notice"],
      ["missing-functional-evidence"],
    );
  });

  test("RO-N2: uma exceção diferente reprova, nos dois canais", async ({ page }, testInfo) => {
    const { gate, picker } = await openAndSettle(page, testInfo);
    const message = "controle negativo: erro diferente";
    await page.evaluate((text) => {
      setTimeout(() => {
        throw new Error(text);
      });
    }, message);
    await expect.poll(async () => (await gate.log())?.pageErrors).toContain(message);
    expect(await gate.settle(), "o picker não parou por 20 quadros").toBe(true);
    await closeAll(page, gate, picker);

    // Cada falha tem de ser o erro provocado, uma vez em cada canal: o motivo
    // `unexpected-error` sozinho aceitaria outra exceção junto. O registro
    // completo fica no anexo do veredito.
    const verdict = await gate.verdict();
    expect(provokedErrorProblems(verdict, message), "falhas além do erro provocado").toEqual([]);
  });

  test("RO-N3: um aviso de ResizeObserver com o picker fechado reprova", async ({
    page,
  }, testInfo) => {
    const { gate, picker } = await openAndSettle(page, testInfo);
    expect(await gate.settle(), "o picker não parou por 20 quadros").toBe(true);
    await closeAll(page, gate, picker);
    // Uma caixa que muda de tamanho uma vez dentro do próprio callback: um
    // aviso só, e o observer se desliga na entrega seguinte.
    await page.evaluate(() => {
      const box = document.createElement("div");
      box.style.cssText = "position:fixed;left:0;top:0;width:10px;height:10px;pointer-events:none";
      document.body.append(box);
      let deliveries = 0;
      const observer = new ResizeObserver(() => {
        if (++deliveries === 1) {
          box.style.height = "20px";
          return;
        }
        observer.disconnect();
        box.remove();
      });
      observer.observe(box);
    });
    await expect
      .poll(async () => (await gate.log())?.native.map((event) => event.message))
      .toContain(RESIZE_OBSERVER_LOOP);

    const verdict = await gate.verdict();
    expectRejectedFor(verdict, ["notice-while-closed"], ["missing-functional-evidence"]);
  });

  test("RO-N5: um aviso de outro observer durante a abertura reprova sem atribuição", async ({
    page,
  }, testInfo) => {
    const { gate, picker } = await openAndSettle(page, testInfo);
    // O picker já carregado e parado: nenhum quadro com callback dele ou do
    // menu. Uma caixa alheia muda de tamanho uma vez no próprio callback.
    await expect(picker.locator(".chat-emoji-picker__emoji").first()).toBeVisible();
    await finishAnimations(picker);
    await page.evaluate(() => {
      const box = document.createElement("div");
      box.style.cssText = "position:fixed;left:0;top:0;width:10px;height:10px;pointer-events:none";
      document.body.append(box);
      let deliveries = 0;
      const observer = new ResizeObserver(() => {
        if (++deliveries === 1) {
          box.style.height = "20px";
          return;
        }
        observer.disconnect();
        box.remove();
      });
      observer.observe(box);
    });
    await expect
      .poll(async () => (await gate.log())?.native.map((event) => event.message))
      .toContain(RESIZE_OBSERVER_LOOP);
    expect(await gate.settle(), "o picker não parou por 20 quadros").toBe(true);
    await closeAll(page, gate, picker);

    const verdict = await gate.verdict();
    expectRejectedFor(verdict, ["unattributed-notice"], ["missing-functional-evidence"]);
  });

  test("RO-N4: o picker sobre a sheet reprova pela geometria", async ({ page }, testInfo) => {
    const { gate, picker } = await openAndSettle(page, testInfo);
    expect(await gate.settle(), "o picker não parou por 20 quadros").toBe(true);
    // A sobreposição da variante sheet-only, reproduzida no harness: o picker
    // entra 10 px na sheet, pelo lado em que está, sem sair da tela. Só a
    // posição muda; o tamanho não, e nenhum observer é acionado.
    await picker.evaluate((element) => {
      const sheet = document
        .querySelector('[role="toolbar"][aria-label="Reagir à mensagem"]')!
        .getBoundingClientRect();
      const box = element.getBoundingClientRect();
      const top = box.top < sheet.top ? sheet.top - box.height + 10 : sheet.bottom - 10;
      element.style.top = `${top}px`;
    });
    await gate.measure();
    await closeAll(page, gate, picker);

    const verdict = await gate.verdict();
    expectRejectedFor(verdict, ["picker-overlaps-sheet"], ["missing-functional-evidence"]);
  });
});
