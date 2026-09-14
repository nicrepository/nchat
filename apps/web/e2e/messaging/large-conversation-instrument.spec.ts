/**
 * O instrumento do benchmark da #675, sob teste.
 *
 * Um benchmark que mede errado é pior do que nenhum: ele publica um número com
 * a mesma autoridade dos outros. As duas regras abaixo já produziram exatamente
 * isso, e é por isso que cada uma tem um teste que falha se ela voltar atrás.
 *
 * Vive num arquivo próprio, e não junto do cenário de esforço, porque não abre
 * conversa nenhuma: são segundos, contra o minuto que o cenário grande leva.
 *
 * Por que Playwright e não Vitest: a suíte unitária exclui `e2e/**`
 * deliberadamente, e é aqui que o helper mora. Trazer o helper para `src/` só
 * para poder testá-lo seria mover código de teste para dentro da aplicação.
 */

import { expect, test, type TestInfo } from "@playwright/test";

import {
  compareAnchors,
  installPaginatedMessages,
  buildHistory,
} from "../helpers/largeConversationFixture";
import { uniqueId } from "../helpers/messagingApi";

test.describe("compareAnchors", () => {
  test("recusa comparar deslocamentos de mensagens diferentes", async () => {
    // O caso exato que o Round 4 encontrou publicado como "drift de 0px": duas
    // mensagens distintas que calharam de cair na mesma coordenada. Zero ali
    // não é preservação de âncora — é o oposto, e afirmá-lo esconderia o leitor
    // tendo sido deslocado cinquenta mensagens.
    const comparison = compareAnchors({ id: "m-100", offset: 110 }, { id: "m-50", offset: 110 });

    expect(comparison.anchorIdentityPreserved).toBe(false);
    expect(comparison.anchorOffsetDriftPx).toBeNull();
    // Os dois ids continuam no relatório: "não é comparável" é uma informação,
    // e quem lê precisa poder ver o quanto a âncora escapou.
    expect(comparison.anchorMessageBefore).toBe("m-100");
    expect(comparison.anchorMessageAfter).toBe("m-50");
  });

  test("mede o deslocamento quando a mesma mensagem continua sendo a âncora", async () => {
    const comparison = compareAnchors({ id: "m-100", offset: 110 }, { id: "m-100", offset: 113 });

    expect(comparison.anchorIdentityPreserved).toBe(true);
    expect(comparison.anchorOffsetDriftPx).toBe(3);
  });

  test("mede zero quando nada se moveu", async () => {
    const comparison = compareAnchors({ id: "m-100", offset: 110 }, { id: "m-100", offset: 110 });

    expect(comparison.anchorIdentityPreserved).toBe(true);
    expect(comparison.anchorOffsetDriftPx).toBe(0);
  });

  test("não trata uma âncora ausente como identidade preservada", async () => {
    // Dois nulls são iguais, e uma comparação ingênua chamaria isso de
    // identidade preservada — declarando estável uma leitura que não encontrou
    // mensagem nenhuma.
    const comparison = compareAnchors({ id: null, offset: 0 }, { id: null, offset: 0 });

    expect(comparison.anchorIdentityPreserved).toBe(false);
    expect(comparison.anchorOffsetDriftPx).toBeNull();
  });
});

test.describe("o contador de mensagens servidas", () => {
  /**
   * Instala a rota paginada e devolve com que pedir uma página pelo navegador.
   *
   * A rota é a mesma do benchmark, inteira: o que está sob teste é a ordem em
   * que ela conta, e reimplementá-la aqui testaria outra coisa.
   */
  async function installTracker(page: import("@playwright/test").Page, testInfo: TestInfo) {
    const targetId = uniqueId(testInfo, "tracker");
    const all = buildHistory(targetId);
    const messages = await installPaginatedMessages(page, targetId, all);
    // Uma página em branco basta: o que se exercita é a rota, não a aplicação.
    await page.goto("about:blank");
    await page.route("**/tracker-host", (route) =>
      route.fulfill({ status: 200, contentType: "text/html", body: "<html></html>" }),
    );
    await page.goto("http://localhost/tracker-host");
    return { targetId, all, messages };
  }

  test("só conta uma página depois de a resposta ter sido entregue", async ({ page }, testInfo) => {
    const { targetId, all, messages } = await installTracker(page, testInfo);
    expect(messages.servedCount()).toBe(0);
    expect(messages.pagesInFlight()).toBe(0);

    // Uma página *com cursor*, que é a que passa pela latência deliberada da
    // rota — é essa janela que torna "em voo" observável.
    const cursor = all[all.length - 1].id;
    const fetched = page.evaluate(
      ([id, before]) =>
        fetch(`/api/chat/channels/${id}/messages?before=${before}`).then((r) => r.json()),
      [targetId, cursor],
    );

    // Enquanto a resposta não saiu: em voo, e nada contado como servido.
    await expect.poll(() => messages.pagesInFlight()).toBeGreaterThan(0);
    expect(messages.servedCount()).toBe(0);

    await fetched;

    await expect.poll(() => messages.pagesInFlight()).toBe(0);
    expect(messages.servedCount()).toBeGreaterThan(0);
  });

  test("não conta como servida uma resposta que o navegador abandonou", async ({
    page,
  }, testInfo) => {
    const { targetId, all, messages } = await installTracker(page, testInfo);
    const cursor = all[all.length - 1].id;

    // Abortado no meio da latência: o fulfill vai falhar, e ninguém recebeu
    // mensagem nenhuma.
    await page.evaluate(
      ([id, before]) => {
        const controller = new AbortController();
        const promise = fetch(`/api/chat/channels/${id}/messages?before=${before}`, {
          signal: controller.signal,
        }).catch(() => undefined);
        setTimeout(() => controller.abort(), 50);
        return promise;
      },
      [targetId, cursor],
    );

    await expect.poll(() => messages.pagesInFlight()).toBeGreaterThan(0);

    // O contador de "em voo" volta a zero mesmo assim — é o finally que garante
    // isso, e sem ele quem espera a fixture sossegar esperaria para sempre.
    await expect.poll(() => messages.pagesInFlight(), { timeout: 20_000 }).toBe(0);
    // E nada foi registrado como servido.
    expect(messages.servedCount()).toBe(0);
    // O pedido existiu: o que não existiu foi a entrega.
    expect(messages.pagesRequested()).toBeGreaterThan(0);
  });
});
