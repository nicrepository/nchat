/**
 * O instrumento do benchmark da #675 — o mesmo nas duas árvores.
 *
 * Este spec não afirma nada sobre a implementação: ele **mede**. É essa a razão
 * de existir separado do spec de comportamento. As asserções da #675 só fazem
 * sentido na branch, onde a virtualização existe; o número que dá sentido a elas
 * — o "antes" — precisa sair de `develop`, onde nada disso existe e qualquer
 * asserção da feature falharia por definição.
 *
 * Então tudo o que ele faz é abrir a conversa grande, puxar histórico, varrer a
 * timeline, provocar um prepend e anexar os números ao relatório. Rodado aqui,
 * descreve o "depois". Copiado para um snapshot de `develop` — junto com
 * `e2e/helpers/largeConversationFixture.ts`, e só com ele —, descreve o "antes",
 * com a mesma fixture, a mesma latência e a mesma forma de contar.
 *
 * Nenhum import de `src/`: é o que torna a cópia possível sem levar junto uma
 * linha sequer de código de produção da feature. O procedimento completo está em
 * docs/testing/chat-675-large-conversation-benchmark.md.
 */

import { expect, test, type TestInfo } from "@playwright/test";

import { createScenario, installMessagingMocks, uniqueId } from "../helpers/messagingApi";
import {
  ATTACHMENTS_PER_MESSAGE,
  CLUSTER_SIZE,
  CLUSTER_SPAN,
  MESSAGE_COUNT,
  PAGE_SIZE,
  PREVIEW_LATENCY_MS,
  bubbles,
  buildHistory,
  gifAttachmentIds,
  goToTop,
  installPaginatedMessages,
  installPreviewRoutes,
  list,
  anchorBelowTheTop,
  compareAnchors,
  loadPreviousPage,
  noPageInFlight,
  quiet,
  readingAnchor,
  settledViewport,
  type AnchorComparison,
} from "../helpers/largeConversationFixture";

/** Quantas páginas de histórico o cenário puxa depois de abrir. */
const PAGES_TO_PULL = 5;

interface BenchmarkMetrics {
  servedMessagesCount: number;
  servedMessagesOnOpen: number;
  maxMountedMessageRows: number;
  mountedMessageRowsAtOpen: number;
  previewRequestsAtOpen: number;
  previewRequestsTotal: number;
  originalRequestsAtOpen: number;
  originalRequestsTotal: number;
  heavyOriginalRequestsAtOpen: number;
  heavyOriginalRequestsTotal: number;
  videoElementsBeforePlay: number;
  maxConcurrentPreviewRequests: number;
  /**
   * A âncora antes e depois do prepend, sob a regra de compareAnchors: o
   * deslocamento só é um número quando a mesma mensagem continuou sendo a
   * âncora. Em `develop` ele pode legitimamente vir null — é o comportamento
   * antigo sendo medido, não uma falha do instrumento.
   */
  anchor: AnchorComparison;
  reachedRealBottom: boolean;
}

test.describe("conversa grande (#675) — instrumento de benchmark", () => {
  // Um cenário de esforço: seis páginas de histórico, ~100 anexos e uma
  // varredura pela timeline inteira, tudo com latência de servidor deliberada.
  test.setTimeout(180_000);

  test("mede a mesma conversa de 500 mensagens", async ({ page }, testInfo: TestInfo) => {
    const targetId = uniqueId(testInfo, "bench");
    const all = buildHistory(targetId);
    const gifs = gifAttachmentIds();
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal grande",
      messages: all.slice(-PAGE_SIZE),
    });
    await installMessagingMocks(page, scenario);
    const previews = await installPreviewRoutes(page);
    const messages = await installPaginatedMessages(page, targetId, all);
    await page.goto(`/chat/channel/${targetId}`);
    await expect(bubbles(page).first()).toBeVisible();

    const heavyOriginals = () =>
      scenario.requests.attachmentContentFetches.filter((id) => !gifs.has(id));

    // Abre no fim e assenta.
    await expect
      .poll(() => list(page).evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight))
      .toBeLessThan(4);
    await expect.poll(() => previews.total(), { timeout: 20_000 }).toBeGreaterThan(0);
    await quiet(page);

    // A fotografia da abertura, tirada uma vez e nunca sobrescrita.
    const metrics: BenchmarkMetrics = {
      servedMessagesCount: messages.servedCount(),
      servedMessagesOnOpen: messages.servedCount(),
      maxMountedMessageRows: 0,
      mountedMessageRowsAtOpen: await bubbles(page).count(),
      previewRequestsAtOpen: previews.total(),
      previewRequestsTotal: 0,
      originalRequestsAtOpen: scenario.requests.attachmentContentFetches.length,
      originalRequestsTotal: 0,
      heavyOriginalRequestsAtOpen: heavyOriginals().length,
      heavyOriginalRequestsTotal: 0,
      videoElementsBeforePlay: await page.locator("video").count(),
      maxConcurrentPreviewRequests: 0,
      anchor: compareAnchors({ id: null, offset: 0 }, { id: null, offset: 0 }),
      reachedRealBottom: false,
    };

    /** Amostra o DOM montado depois de cada transição — daí "max" ser um pico. */
    const sample = async () => {
      metrics.maxMountedMessageRows = Math.max(
        metrics.maxMountedMessageRows,
        await bubbles(page).count(),
      );
    };
    await sample();

    // Puxa histórico.
    for (let index = 0; index < PAGES_TO_PULL; index += 1) {
      await loadPreviousPage(page, messages);
      await sample();
    }
    await quiet(page);
    metrics.servedMessagesCount = messages.servedCount();

    // Varre a timeline, permanecendo em cada bloco o suficiente para que os
    // previews dele sejam pedidos juntos.
    const viewportHeight = await list(page).evaluate((el) => el.clientHeight);
    for (let step = 0; step < 30; step += 1) {
      await list(page).evaluate(
        (element, top) => {
          element.scrollTop = top;
        },
        step * viewportHeight * 0.5,
      );
      await page.waitForTimeout(PREVIEW_LATENCY_MS * 2);
      await sample();
    }
    metrics.maxConcurrentPreviewRequests = previews.peakConcurrent();

    // Um prepend, com a âncora lida antes e depois pela mesma regra.
    await noPageInFlight(messages);
    await list(page).evaluate((element) => {
      element.scrollTop = element.clientHeight * 3;
    });
    await quiet(page);
    await noPageInFlight(messages);

    const servedBefore = messages.servedCount();
    const requestedBefore = messages.pagesRequested();
    // Sobe ao topo até o pedido sair — o sentinel do topo só relata transições.
    await expect
      .poll(
        async () => {
          await goToTop(page);
          return messages.pagesRequested();
        },
        { timeout: 30_000, intervals: [250] },
      )
      .toBeGreaterThan(requestedBefore);

    // Com a página ainda a caminho, desce uma viewport: é essa preparação que
    // faz a comparação de identidade dizer alguma coisa. Ver anchorBelowTheTop.
    const before = await anchorBelowTheTop(page);

    await expect
      .poll(() => messages.servedCount(), { timeout: 30_000, intervals: [250] })
      .toBeGreaterThan(servedBefore);
    await quiet(page);
    await settledViewport(page);
    const after = await readingAnchor(page);
    metrics.anchor = compareAnchors(before, after);
    await sample();

    // Volta ao fim.
    const goToBottom = page.getByRole("button", { name: /Ir para o final/i });
    if (await goToBottom.isVisible().catch(() => false)) {
      await goToBottom.click();
      await expect
        .poll(
          async () =>
            list(page).evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight < 4),
          { timeout: 30_000, intervals: [250] },
        )
        .toBe(true)
        .catch(() => {
          // Não chegar ao fim é um resultado do benchmark, não um erro dele.
        });
    }
    metrics.reachedRealBottom = await list(page).evaluate(
      (el) => el.scrollHeight - el.scrollTop - el.clientHeight < 4,
    );

    metrics.previewRequestsTotal = previews.total();
    metrics.originalRequestsTotal = scenario.requests.attachmentContentFetches.length;
    metrics.heavyOriginalRequestsTotal = heavyOriginals().length;
    metrics.servedMessagesCount = messages.servedCount();

    // O que a fixture tem, para o relatório poder ser lido sem abrir o código.
    const attachmentsInHistory = Math.ceil(MESSAGE_COUNT / CLUSTER_SPAN) * CLUSTER_SIZE;
    await testInfo.attach("benchmark.json", {
      body: JSON.stringify(
        {
          fixture: {
            messages: MESSAGE_COUNT,
            attachments: attachmentsInHistory,
            attachmentsPerMessage: ATTACHMENTS_PER_MESSAGE,
            pageSize: PAGE_SIZE,
          },
          metrics,
        },
        null,
        2,
      ),
      contentType: "application/json",
    });

    // A única asserção: o instrumento mediu alguma coisa. Se a conversa não
    // abriu, o relatório não vale nada e não deve ser publicado em silêncio.
    expect(metrics.servedMessagesOnOpen).toBeGreaterThan(0);
    expect(metrics.maxMountedMessageRows).toBeGreaterThan(0);
  });
});
