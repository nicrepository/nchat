import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";

import { createScenario, installMessagingMocks, makeMessage, uniqueId } from "../helpers/messagingApi";

const EVIDENCE_DIR =
  "/tmp/claude-1000/-home-mateusevangelista-Documentos-Projetos-internos-nchat/2857ce59-b684-456c-9075-fde6ca7aa8c3/scratchpad/evidence";

async function shot(page: Page, name: string) {
  await page.screenshot({ path: `${EVIDENCE_DIR}/${name}.png`, fullPage: false });
}

/**
 * Objetivo: divisor "Mensagens não lidas" dentro da conversa (não a sidebar,
 * já coberta em outro spec) — ao abrir uma conversa com não lidas, a lista
 * deve rolar até a fronteira lida/não-lida e mostrar um divisor visual ali,
 * em vez de sempre rolar até o fim.
 */
test.describe("divisor de mensagens não lidas", () => {
  test("rola até o divisor e mostra as mensagens não lidas logo abaixo dele", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "unread-divider-channel");
    // 30 não lidas de 60 (metade) — o suficiente para que o conteúdo abaixo
    // do divisor sozinho já exceda a altura da viewport, garantindo que
    // "rolar até o divisor" e "rolar até o fim" acabem em posições
    // numericamente diferentes (com poucas não lidas, o clamp de scroll no
    // fim do conteúdo tornaria as duas posições indistinguíveis).
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Com Não Lidas",
      messages: Array.from({ length: 60 }, (_, i) =>
        makeMessage({ id: `${targetId}-m${i}`, body_text: `Mensagem ${i}` }),
      ),
    });
    const sidebarEntry = scenario.sidebarChannels.find((c) => c.id === targetId)!;
    sidebarEntry.unread_count = 30; // fronteira em 60 - 30 = índice 30

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const divider = page.getByTestId("unread-divider");
    await expect(divider).toBeVisible();
    await expect(divider).toContainText("Mensagens não lidas");
    await expect(page.getByText("Mensagem 30", { exact: true })).toBeVisible();

    // Prova de que a rolagem não foi até o final da conversa: a última
    // mensagem (bem depois do divisor, com 30 não lidas de sobra) não está
    // visível — se tivesse rolado para o fundo como no comportamento antigo,
    // ela estaria.
    await expect(page.getByText("Mensagem 59", { exact: true })).not.toBeInViewport();
    // E o divisor está perto do topo da área visível, não no meio/fim dela.
    const list = page.locator(".chat-msg-area__list");
    await expect
      .poll(async () => {
        const listBox = (await list.boundingBox())!;
        const dividerBox = (await divider.boundingBox())!;
        return dividerBox.y - listBox.y;
      })
      .toBeLessThan(80);
    await shot(page, "11-divisor-mensagens-nao-lidas");
  });

  test("sem mensagens não lidas, não mostra divisor e rola até o fim como hoje", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "no-unread-divider-channel");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Sem Não Lidas",
      messages: Array.from({ length: 60 }, (_, i) =>
        makeMessage({ id: `${targetId}-m${i}`, body_text: `Mensagem ${i}` }),
      ),
    });
    // unread_count 0 já é o padrão do fixture.

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await expect(page.getByTestId("unread-divider")).toHaveCount(0);
    const list = page.locator(".chat-msg-area__list");
    await expect
      .poll(async () =>
        list.evaluate((el) => Math.abs(el.scrollHeight - el.clientHeight - el.scrollTop)),
      )
      .toBeLessThanOrEqual(1);
    await shot(page, "12-sem-divisor-rola-ate-o-fim");
  });
});
