/**
 * Conversa grande — virtualização e carga sob demanda (issue #675).
 *
 * Só o que um navegador real pode provar está aqui. jsdom não tem layout, não
 * tem IntersectionObserver de verdade, não decodifica mídia e não executa
 * `Element.scrollTo` — ou seja, tudo o que decide *quantas* linhas existem,
 * *quando* um anexo entra na região útil e *quantos* pedidos saem ao mesmo
 * tempo. Os testes em jsdom cobrem a lista de linhas, o agendador e a máquina
 * de estados; este cobre o comportamento medido.
 *
 * O cenário é o da própria issue: ~500 mensagens e ~100 anexos mistos, com
 * alturas bem diferentes, previews prontos e não prontos.
 *
 * # O que cada métrica mede, e por que os nomes importam
 *
 * A revisão anterior aceitou uma regressão porque três números diziam uma coisa
 * e mediam outra. Então a separação agora é explícita e cada campo do relatório
 * pertence a exatamente uma dessas famílias:
 *
 *   SERVIDAS     quantos ids distintos de mensagens as respostas da fixture
 *                entregaram com sucesso. Contadas na rota, nunca no DOM, e é o
 *                número que a virtualização NÃO limita. Não diz nada sobre o
 *                estado do React — ver a definição no tipo Metrics.
 *   DOM          quantas linhas estão montadas. É o número que a virtualização
 *                limita, e a comparação entre os dois é a tese da issue.
 *   REQUESTS     previews derivados e originais, separados por "na abertura" e
 *                "na sessão inteira". O valor de abertura é capturado uma vez e
 *                nunca sobrescrito — antes ele era, e o relatório publicava o
 *                total como se fosse a abertura.
 *   CONCORRÊNCIA o pico de pedidos de preview simultâneos no servidor.
 *   ÂNCORA       a mensagem no topo da viewport antes e depois de um prepend, e
 *                o quanto ELA se moveu. O "depois" é descoberto medindo de
 *                novo, nunca copiado do "antes".
 *
 * As métricas são anexadas ao relatório (`metrics.json`) para que o "depois"
 * seja reproduzível por quem revisar. A documentação do baseline, e o motivo de
 * ele não poder ser medido nesta árvore, está em `docs/` — ver o README da
 * issue no final deste arquivo.
 */

import { expect, test, type Page, type TestInfo } from "@playwright/test";

import { createScenario, installMessagingMocks, uniqueId } from "../helpers/messagingApi";
import {
  CLUSTER_SIZE,
  CLUSTER_SPAN,
  MESSAGE_COUNT,
  PAGE_SIZE,
  PREVIEW_LATENCY_MS,
  bubbles,
  buildHistory,
  compareAnchors,
  gifAttachmentIds,
  goToTop,
  installPaginatedMessages,
  installPreviewRoutes,
  list,
  loadPreviousPage,
  noPageInFlight,
  quiet,
  readingAnchor,
  settledViewport,
  type AnchorComparison,
} from "../helpers/largeConversationFixture";
import { MAX_CONCURRENT_PREVIEWS } from "../../src/chat/previewScheduler";
import { NEAR_DEBOUNCE_MS } from "../../src/chat/lazyAttachment";

/**
 * Quanto a mensagem que está sendo lida pode escorregar num prepend.
 *
 * Quatro pixels — arredondamento de layout, não uma linha e muito menos uma
 * viewport. A restauração mede a caixa real da âncora e corrige até ela, então
 * qualquer coisa maior que isto é deriva com causa. A tolerância anterior era
 * de 800px e foi exatamente o que deixou passar uma deriva de 722px.
 */
const ANCHOR_TOLERANCE_PX = 4;

/**
 * A que distância da borda de cima a mensagem-âncora é posta para medir.
 *
 * Longe de qualquer fronteira: a regra que decide qual mensagem está sendo lida
 * exige que ela comece dentro da viewport, e com offset zero um resíduo de um
 * pixel passa esse título para a linha seguinte. Quarenta pixels é onde um
 * leitor de verdade costuma estar.
 */
const ANCHOR_CLEARANCE_PX = 40;

/** Quanto o vídeo precisa ficar acima da dobra para estar claramente fora dela. */
const VIDEO_ABOVE_FOLD_MARGIN_PX = 80;

interface Metrics {
  // ── SERVIDAS ──────────────────────────────────────────────────────────────
  //
  // Quantidade de ids distintos de mensagens incluídos em respostas da fixture
  // concluídas com sucesso durante o cenário. Contadas na rota.
  //
  // Não é o tamanho do estado do React, nem do store, nem o número de mensagens
  // renderizadas: nada disso é observável daqui sem instrumentar a aplicação, e
  // o benchmark não instrumenta. O nome é modesto de propósito — um que
  // prometesse mais do que mede seria pior.
  servedMessagesCount: number;
  /** Só a primeira página: a paginação por cursor é o primeiro limite. */
  servedMessagesOnOpen: number;

  // ── DOM ───────────────────────────────────────────────────────────────────
  //
  // Picos de verdade: amostrados depois de cada transição do cenário — abertura,
  // cada página, cada passo da varredura, prepend, remeasure e volta ao fim —
  // e não em dois ou três pontos escolhidos a dedo.
  maxMountedMessageRows: number;
  maxMountedVirtualRows: number;
  /** Bolhas montadas ao abrir, antes de qualquer paginação. */
  mountedMessageRowsAtOpen: number;

  // ── REQUESTS ──────────────────────────────────────────────────────────────
  previewRequestsAtOpen: number;
  previewRequestsTotal: number;
  originalRequestsAtOpen: number;
  originalRequestsTotal: number;
  /** Originais que não são GIF: vídeo, documento, imagem estática. Zero. */
  heavyOriginalRequestsAtOpen: number;
  heavyOriginalRequestsTotal: number;
  videoElementsBeforePlay: number;

  // ── CONCORRÊNCIA ──────────────────────────────────────────────────────────
  maxConcurrentPreviewRequests: number;

  // ── ÂNCORA ────────────────────────────────────────────────────────────────
  //
  // Identidade e pixel, nas duas pontas. O "depois" é sempre redescoberto pela
  // mesma regra que produziu o "antes" — nunca copiado —, e a identidade é o
  // contrato: a mesma coordenada com outra mensagem não é preservação de
  // âncora.
  /**
   * A âncora antes e depois do prepend, sob a regra de compareAnchors: o
   * deslocamento só é um número quando a mesma mensagem continuou sendo a
   * âncora. Aqui as duas coisas são exigidas.
   */
  afterPrepend: AnchorComparison;
  /** E de novo, depois de uma linha acima dela mudar de altura de verdade. */
  afterRemeasure: AnchorComparison;
  /** O que provocou a remedição, para o relatório dizer o que foi medido. */
  remeasureTrigger: string;

  reachedRealBottom: boolean;
}

/**
 * O espaço que a cromagem transitória ocupa acima das linhas, em pixels.
 *
 * Na prática, o indicador de "carregando histórico": ele existe só enquanto uma
 * página está em voo, ou seja, está presente na leitura "antes" de um prepend e
 * ausente na "depois". Sem descontá-lo, as duas medidas descrevem layouts
 * diferentes e a altura dele — margens incluídas, que é por que isto mede a
 * distância entre o conteúdo e o canvas em vez de ler a altura do elemento —
 * seria atribuída à âncora. O teste então reprovaria a restauração por um
 * deslocamento que ela não causou, e que o leitor não vê como deslocamento: é a
 * cromagem saindo da tela.
 */

/**
 * As linhas do canvas virtual. Fica aqui, e não na fixture partilhada, porque
 * é a única coisa medida aqui que não existe em `develop`.
 */
const virtualRows = (page: Page) =>
  page.locator('[data-testid="chat-virtual-canvas"] [data-index]');

/**
 * Encontra uma posição de leitura logo abaixo de uma linha com vídeo, e devolve
 * o scrollTop correspondente.
 *
 * Duas coisas de uma vez, e as duas importam para a medição que vem depois. A
 * âncora deixa de ser a mensagem mais antiga carregada — encostada no topo, o
 * que está acima dela é cromagem, e depois de um prepend aquele espaço vira
 * mensagem nova, então *qual* linha encosta na borda mudaria por construção. E
 * garante que exista, logo acima da âncora, uma linha cuja altura o fluxo real
 * pode mudar mais tarde.
 *
 * A varredura acontece antes de qualquer pedido estar em voo, e o resultado é
 * um número: com a lista de linhas inalterada, voltar a esse scrollTop
 * reproduz exatamente esta posição. É isso que permite medir dentro da janela
 * curta entre "a página foi pedida" e "a página chegou".
 */
async function readingPositionBelowAVideo(page: Page): Promise<number | null> {
  // Uma varredura só, um round-trip por passo: procura um candidato utilizável
  // entre as linhas montadas e, se não houver, desce um pouco. Só linhas
  // montadas existem no DOM, então procurar sem rolar acharia apenas o que já
  // está na janela.
  let found: number | null = null;
  await expect
    .poll(
      async () => {
        found = await list(page).evaluate(
          (element, sizes) => {
            const [clearancePx, marginPx] = sizes;
            const rows = [...element.querySelectorAll<HTMLElement>("[data-message-id]")];
            const foldTop = element.getBoundingClientRect().top;
            for (const button of element.querySelectorAll<HTMLElement>(
              '[data-testid^="chat-message-attachment-video-play-"]',
            )) {
              const video = button.closest<HTMLElement>("[data-message-id]");
              if (!video) continue;
              // A linha entre o vídeo e a âncora é a que vai cruzar a dobra, e ela
              // precisa ser alta o bastante para deixar o vídeo inteiramente fora
              // do campo de visão com folga. Encostado na dobra, "acima do leitor"
              // vira uma comparação de subpixel.
              const between = rows[rows.indexOf(video) + 1];
              const anchor = rows[rows.indexOf(video) + 2];
              if (!between || !anchor) continue;
              if (between.getBoundingClientRect().height < clearancePx + marginPx) continue;
              // Põe a borda de baixo da linha do meio `clearancePx` abaixo da
              // dobra: a âncora começa exatamente aí, longe de qualquer fronteira,
              // e o vídeo fica pelo menos `marginPx` acima dela.
              element.scrollTop += between.getBoundingClientRect().bottom - foldTop - clearancePx;
              return element.scrollTop;
            }
            element.scrollTop += element.clientHeight * 0.75;
            return null;
          },
          [ANCHOR_CLEARANCE_PX, VIDEO_ABOVE_FOLD_MARGIN_PX],
        );
        return found !== null;
      },
      { timeout: 30_000, intervals: [120] },
    )
    .toBe(true);
  return found;
}

/**
 * Aperta Play no primeiro vídeo montado acima de `messageId`, e devolve o id da
 * mensagem que o contém.
 *
 * Acima da âncora de propósito: é a linha cuja mudança de altura o leitor não
 * pode ver, e portanto exatamente a que precisa ser compensada para que ele não
 * se mova. Devolve null quando não há nenhum — o chamador trata isso como falha
 * da fixture, não como cenário válido.
 */
interface PlayedVideo {
  messageId: string | null;
  attachmentId: string;
}

async function playFirstVideoOutOfSight(page: Page): Promise<PlayedVideo | null> {
  return list(page).evaluate((element) => {
    const foldTop = element.getBoundingClientRect().top;
    for (const button of element.querySelectorAll<HTMLElement>(
      '[data-testid^="chat-message-attachment-video-play-"]',
    )) {
      const row = button.closest<HTMLElement>("[data-message-id]");
      if (!row) continue;
      // Inteiramente acima da borda de cima da viewport, e não apenas acima da
      // âncora: são linhas diferentes. A âncora fica alguns pixels abaixo da
      // borda, então a linha logo acima dela ainda *cruza* a borda — e uma
      // linha que cruza cresce dentro do campo de visão do leitor, que é
      // justamente o caso em que a compensação integral não deve ocorrer.
      if (row.getBoundingClientRect().bottom > foldTop) continue;
      button.click();
      return {
        messageId: row.dataset.messageId ?? null,
        attachmentId: (button.dataset.testid ?? "").replace(
          "chat-message-attachment-video-play-",
          "",
        ),
      };
    }
    return null;
  });
}

async function setupLargeConversation(page: Page, testInfo: TestInfo) {
  const targetId = uniqueId(testInfo, "perf");
  const all = buildHistory(targetId);
  const scenario = createScenario({
    kind: "channel",
    targetId,
    targetName: "Canal grande",
    // A rota paginada abaixo substitui esta lista; o cenário só precisa do id.
    messages: all.slice(-PAGE_SIZE),
  });
  await installMessagingMocks(page, scenario);
  const previews = await installPreviewRoutes(page);
  const messages = await installPaginatedMessages(page, targetId, all);
  await page.goto(`/chat/channel/${targetId}`);
  await expect(bubbles(page).first()).toBeVisible();
  return { targetId, scenario, previews, messages };
}

test.describe("conversa grande (#675)", () => {
  // Um teste de esforço: seis páginas de histórico, ~100 anexos e uma varredura
  // pela timeline inteira, tudo com latência de servidor deliberada. O default
  // do Playwright é curto demais para isso e cortaria a medição no meio.
  test.setTimeout(180_000);

  test("abre uma conversa de 500 mensagens sem montar a timeline inteira nem baixar originais", async ({
    page,
  }, testInfo: TestInfo) => {
    const gifs = gifAttachmentIds();
    const { scenario, previews, messages } = await setupLargeConversation(page, testInfo);
    const heavyOriginals = () =>
      scenario.requests.attachmentContentFetches.filter((id) => !gifs.has(id));

    // A conversa abre no fim (#492) e o sentinel confirma o fim real.
    await expect
      .poll(() => list(page).evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight))
      .toBeLessThan(4);

    // Os previews da região útil saem depois do debounce de proximidade; medir
    // antes disso mediria o nada. Espera-se o efeito, nunca um tempo fixo.
    await expect.poll(() => previews.total(), { timeout: 20_000 }).toBeGreaterThan(0);
    // O preview derivado é o que aparece no card — nunca o original.
    await expect(page.locator("img.chat-msg-area__attachment-preview-img").first()).toBeVisible();
    await quiet(page);

    // ── Fotografia da abertura, tirada uma vez ────────────────────────────────
    //
    // Estes cinco campos descrevem o estado inicial e não são tocados de novo:
    // sobrescrevê-los no fim, como a versão anterior fazia, publicava o total da
    // sessão sob o nome da abertura.
    const metrics: Metrics = {
      servedMessagesCount: messages.servedCount(),
      servedMessagesOnOpen: messages.servedCount(),
      maxMountedMessageRows: 0,
      maxMountedVirtualRows: 0,
      mountedMessageRowsAtOpen: await bubbles(page).count(),
      previewRequestsAtOpen: previews.total(),
      previewRequestsTotal: previews.total(),
      originalRequestsAtOpen: scenario.requests.attachmentContentFetches.length,
      originalRequestsTotal: 0,
      heavyOriginalRequestsAtOpen: heavyOriginals().length,
      heavyOriginalRequestsTotal: 0,
      videoElementsBeforePlay: await page.locator("video").count(),
      maxConcurrentPreviewRequests: previews.peakConcurrent(),
      afterPrepend: compareAnchors({ id: null, offset: 0 }, { id: null, offset: 0 }),
      afterRemeasure: compareAnchors({ id: null, offset: 0 }, { id: null, offset: 0 }),
      remeasureTrigger: "",
      reachedRealBottom: false,
    };

    /**
     * Registra o DOM montado agora.
     *
     * Chamado depois de cada transição do cenário — e só delas —, que é o que
     * torna "max" um pico de verdade em vez de duas ou três amostras escolhidas
     * a dedo. Sem polling: cada chamada corresponde a algo que acabou de
     * acontecer.
     */
    const sample = async () => {
      metrics.maxMountedMessageRows = Math.max(
        metrics.maxMountedMessageRows,
        await bubbles(page).count(),
      );
      metrics.maxMountedVirtualRows = Math.max(
        metrics.maxMountedVirtualRows,
        await virtualRows(page).count(),
      );
    };
    await sample();

    // ── Abrir não busca o histórico inteiro ──────────────────────────────────
    // Uma página, não quinhentas mensagens: a paginação por cursor é o primeiro
    // limite, e a virtualização só passa a valer quando o leitor puxa mais.
    expect(metrics.servedMessagesOnOpen).toBeLessThanOrEqual(PAGE_SIZE);

    // ── Nenhum original pesado: nem vídeo, nem documento, nem imagem estática ─
    //
    // A única exceção que a issue permite é a animação de um GIF, e só enquanto
    // ele está realmente na viewport: não existe forma derivada de animar. Tudo
    // o mais desenha preview ou shell.
    expect(metrics.heavyOriginalRequestsAtOpen).toBe(0);
    expect(metrics.videoElementsBeforePlay).toBe(0);
    await expect(page.getByTestId(/^chat-message-attachment-video-play-/).first()).toBeVisible();

    // ── Puxar histórico: as servidas crescem, o DOM não ─────────────────────
    for (let pageIndex = 0; pageIndex < 5; pageIndex += 1) {
      await loadPreviousPage(page, messages);
      await sample();
    }
    await quiet(page);

    // Passado o limiar, a timeline está virtualizada e só uma janela existe.
    await expect(page.getByTestId("chat-virtual-canvas")).toBeAttached();
    metrics.servedMessagesCount = messages.servedCount();

    // Seis páginas servidas, uma janela no DOM: é exatamente isto que a issue
    // pede quando diz que o DOM não pode crescer com o histórico. As duas
    // asserções são sobre números diferentes de propósito — uma versão anterior
    // lia o DOM nas duas, e por isso não podia falhar.
    expect(metrics.servedMessagesCount).toBeGreaterThanOrEqual(PAGE_SIZE * 5);
    expect(metrics.maxMountedMessageRows).toBeLessThan(PAGE_SIZE * 2);
    expect(metrics.maxMountedVirtualRows).toBeLessThan(PAGE_SIZE * 2);

    // ── Concorrência sob rajada ──────────────────────────────────────────────
    //
    // Uma varredura pela timeline: cada bloco de oito anexos entra na região
    // útil de uma vez e pede seis previews, mais do que o agendador deixa correr
    // junto. É a fixture que produz a demanda — nada aqui empurra o agendador.
    // Passos de meia viewport, e uma pausa maior que o debounce de proximidade:
    // um bloco tem de *permanecer* na região útil o suficiente para que os seis
    // previews dele sejam liberados juntos. Atravessá-lo depressa é a rolagem
    // que o debounce existe para não atender, e mediria justamente o contrário.
    const viewportHeight = await list(page).evaluate((el) => el.clientHeight);
    const dwellMs = NEAR_DEBOUNCE_MS + PREVIEW_LATENCY_MS;
    for (let step = 0; step < 30; step += 1) {
      await list(page).evaluate(
        (element, top) => {
          element.scrollTop = top;
        },
        step * viewportHeight * 0.5,
      );
      await page.waitForTimeout(dwellMs);
      await sample();
      if (previews.peakConcurrent() >= MAX_CONCURRENT_PREVIEWS) break;
    }
    metrics.maxConcurrentPreviewRequests = previews.peakConcurrent();
    // O limite é respeitado…
    expect(metrics.maxConcurrentPreviewRequests).toBeLessThanOrEqual(MAX_CONCURRENT_PREVIEWS);
    // …e a fixture gerou demanda suficiente para encostar nele, senão o teto
    // acima não teria sido exercitado por nada.
    expect(metrics.maxConcurrentPreviewRequests).toBe(MAX_CONCURRENT_PREVIEWS);

    // ── Uma página nova, inserida acima, não move quem está lendo ────────────
    //
    // Longe do sentinel do topo, e parado: sem isto a medição pegaria o rastro
    // das páginas que o laço acima ainda estava carregando.
    await noPageInFlight(messages);
    await list(page).evaluate((element) => {
      element.scrollTop = element.clientHeight * 3;
    });
    await quiet(page);
    await noPageInFlight(messages);
    // Longe do topo de verdade antes de voltar a ele: sem sair, não há
    // transição para o observer relatar.
    expect(await list(page).evaluate((el) => el.scrollTop)).toBeGreaterThan(0);

    // Uma subida ao topo antes de escolher a posição, e não por causa da página
    // que ela carrega: é para que as linhas do caminho já estejam *medidas*. Uma
    // linha medida pela primeira vez desloca o conteúdo abaixo dela, então um
    // scrollTop anotado antes dessa medição deixa de apontar para o mesmo lugar
    // depois — e a posição de medição escolhida com tanto cuidado escorregaria.
    await goToTop(page);
    await noPageInFlight(messages);
    await quiet(page);
    await settledViewport(page);

    // A posição de leitura é escolhida antes de o pedido medido sair, para que a
    // busca por uma linha com vídeo não consuma a janela em que a âncora tem de
    // ser lida. Com o caminho já medido, este scrollTop reproduz exatamente esta
    // posição quando voltarmos a ela.
    const measurementTop = await readingPositionBelowAVideo(page);
    expect(
      measurementTop,
      "a fixture precisa ter um vídeo para posicionar a âncora",
    ).not.toBeNull();
    await settledViewport(page);

    const servedBeforePrepend = messages.servedCount();
    const requestsBeforePrepend = messages.pagesRequested();
    // Reafirmado a cada tentativa, e não uma vez só: o sentinel do topo é um
    // IntersectionObserver, que só relata *transições*. Um scrollTop já em zero,
    // ou desfeito antes do quadro seguinte, não produz transição nenhuma e o
    // pedido nunca sai.
    await expect
      .poll(
        async () => {
          await goToTop(page);
          return messages.pagesRequested();
        },
        { timeout: 30_000, intervals: [250] },
      )
      // A página foi *pedida*: a partir daqui existe a janela em que o estado
      // ainda é o anterior ao prepend, e é dentro dela que a âncora é lida.
      .toBeGreaterThan(requestsBeforePrepend);

    // Desce uma viewport enquanto a página vem.
    //
    // Isto não é conveniência de teste: é a diferença entre medir a invariante e
    // medir um artefato. Encostado no topo, a mensagem-âncora é a mais antiga
    // carregada e o que está acima dela é cromagem — divisor de dia e indicador
    // de carregamento. Depois do prepend aquele espaço passa a ser ocupado por
    // mensagens que acabaram de chegar, então *qual* mensagem encosta na borda
    // muda por construção, mesmo com a restauração perfeita.
    //
    // De volta à posição escolhida, com a página ainda a caminho.
    await list(page).evaluate((element, top) => {
      element.scrollTop = top;
    }, measurementTop!);

    // Ler a âncora exige uma viewport parada. Não é tempo: a janela virtual só é
    // recalculada no quadro seguinte a um scroll — ler antes disso devolve as
    // linhas da posição anterior — e a restauração da página *anterior* pode
    // ainda estar assentando. Espera-se pelo próprio resultado.
    let before = await readingAnchor(page);
    let previousTop = Number.NaN;
    await expect
      .poll(
        async () => {
          const top = await list(page).evaluate((element) => element.scrollTop);
          const settled = top === previousTop;
          previousTop = top;
          if (!settled) return false;
          before = await readingAnchor(page);
          return before.id !== null && Math.abs(before.offset) < viewportHeight;
        },
        { timeout: 10_000, intervals: [80] },
      )
      .toBe(true);
    // E isto tudo aconteceu antes de a página chegar — a latência da rota existe
    // para que haja essa janela, o que a asserção confirma em vez de supor.
    expect(messages.servedCount()).toBe(servedBeforePrepend);

    await expect
      .poll(() => messages.servedCount(), { timeout: 20_000 })
      .toBeGreaterThan(servedBeforePrepend);
    await quiet(page);
    await settledViewport(page);
    await sample();

    // O "depois" é descoberto pela mesma regra que produziu o "antes", nunca
    // copiado dele: qual mensagem ocupa a posição de âncora agora é exatamente
    // o que o prepend poderia ter mudado.
    metrics.afterPrepend = compareAnchors(before, await readingAnchor(page));

    // A MESMA mensagem, no MESMO lugar. As duas coisas, porque cada uma sozinha
    // passa com a outra quebrada: a mesma coordenada com outra mensagem é o
    // leitor deslocado uma linha, e a mesma mensagem em outra coordenada é o
    // leitor deslocado em pixels. O deslocamento nem sequer é um número quando
    // a identidade se perde, e é por isso que ele é conferido contra null antes
    // de ser comparado.
    expect(metrics.afterPrepend.anchorIdentityPreserved).toBe(true);
    expect(metrics.afterPrepend.anchorOffsetDriftPx).not.toBeNull();
    expect(metrics.afterPrepend.anchorOffsetDriftPx).toBeLessThanOrEqual(ANCHOR_TOLERANCE_PX);

    // ── E uma linha acima dela mudando de altura de verdade ──────────────────
    //
    // Medir de novo sem nada mudar não é remedição. O que muda altura aqui é o
    // fluxo real: apertar Play num vídeo troca o pôster de 260px pelo elemento
    // de vídeo, limitado a 200px. É o próprio pipeline de anexos — busca do
    // blob, troca do elemento, ResizeObserver — e não CSS injetado.
    // Repetido até pegar, porque a janela montada é recalculada um quadro depois
    // do scroll: a linha do vídeo pode ainda não existir no DOM no primeiro
    // instante. A condição é observável — o clique aconteceu —, não um tempo.
    // Coletado numa lista em vez de numa variável reatribuída dentro do
    // callback: assim o valor sai daqui com o tipo que tem, sem um cast para
    // convencer o compilador de algo que a asserção já garantiu.
    const played: PlayedVideo[] = [];
    await expect
      .poll(
        async () => {
          const found = await playFirstVideoOutOfSight(page);
          if (found) played.push(found);
          return played.length > 0;
        },
        { timeout: 20_000, intervals: [150] },
      )
      .toBe(true);
    const grown = played[0];
    expect(grown, "a fixture precisa ter um vídeo montado acima da viewport").toBeDefined();
    metrics.remeasureTrigger = `Play no vídeo ${grown.attachmentId}`;
    // Condição observável, nunca um tempo: o elemento de vídeo existir é o que
    // prova que a troca aconteceu e que a linha foi remedida.
    await expect(page.locator("video")).toHaveCount(1, { timeout: 20_000 });
    await settledViewport(page);
    await sample();

    metrics.afterRemeasure = compareAnchors(before, await readingAnchor(page));

    expect(metrics.afterRemeasure.anchorIdentityPreserved).toBe(true);
    expect(metrics.afterRemeasure.anchorOffsetDriftPx).not.toBeNull();
    expect(metrics.afterRemeasure.anchorOffsetDriftPx).toBeLessThanOrEqual(ANCHOR_TOLERANCE_PX);

    // ── Voltar ao fim chega ao fim real ──────────────────────────────────────
    const goToBottom = page.getByRole("button", { name: /Ir para o final/i });
    await expect(goToBottom).toBeVisible();
    await goToBottom.click();

    // O sinal é do produto, não do relógio: o botão só desaparece quando a
    // própria timeline entra em AT_BOTTOM, e ela só entra quando o sentinel do
    // fim é observado inteiramente na viewport. Esperar por ele é esperar pela
    // chegada; esperar por uma distância seria acreditar na geometria antes de
    // o componente concordar com ela.
    // Os dois sinais juntos, numa condição só. O do produto: o botão só some
    // quando a própria timeline entra em AT_BOTTOM, e ela só entra quando o
    // sentinel do fim é observado inteiro na viewport. E o geométrico: a
    // distância real até o fim. Um sem o outro passaria com o leitor encalhado —
    // o componente acreditando ter chegado enquanto o conteúdo cresceu debaixo
    // dele, ou a geometria momentaneamente certa antes da última remedição.
    await expect
      .poll(
        async () =>
          (await goToBottom.isHidden()) &&
          (await list(page).evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight)) < 4,
        { timeout: 30_000, intervals: [200] },
      )
      .toBe(true);
    metrics.reachedRealBottom = true;

    // ── Totais da sessão, sob os nomes que dizem "total" ─────────────────────
    metrics.previewRequestsTotal = previews.total();
    metrics.originalRequestsTotal = scenario.requests.attachmentContentFetches.length;
    metrics.heavyOriginalRequestsTotal = heavyOriginals().length;
    metrics.servedMessagesCount = messages.servedCount();

    // Nunca todos os ~100 anexos: só os que passaram perto.
    const attachmentsInHistory = Math.ceil(MESSAGE_COUNT / CLUSTER_SPAN) * CLUSTER_SIZE;
    expect(attachmentsInHistory).toBeGreaterThanOrEqual(100);
    expect(metrics.previewRequestsTotal).toBeLessThan(attachmentsInHistory);
    // A abertura pediu muito menos que a sessão inteira, e o campo da abertura
    // continua descrevendo a abertura.
    expect(metrics.previewRequestsAtOpen).toBeLessThan(metrics.previewRequestsTotal);

    // Seis páginas de histórico depois, o único original pesado da sessão é o
    // vídeo que este teste mandou tocar — a interação que a issue permite. Todo
    // o resto continua sendo preview ou shell, e os GIFs (que não têm forma
    // derivada animada) não contam como pesados.
    const playedAttachmentId = grown.attachmentId;
    expect(heavyOriginals().filter((id) => id !== playedAttachmentId)).toEqual([]);

    await testInfo.attach("metrics.json", {
      body: JSON.stringify(metrics, null, 2),
      contentType: "application/json",
    });
  });

  test("não perde o foco quando a linha que o continha é desmontada", async ({
    page,
  }, testInfo: TestInfo) => {
    const { messages } = await setupLargeConversation(page, testInfo);
    await expect
      .poll(() => list(page).evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight))
      .toBeLessThan(4);

    // Uma página não basta para virtualizar; a partir daqui só uma janela fica
    // montada, que é a condição em que o foco pode sumir.
    await loadPreviousPage(page, messages);
    await loadPreviousPage(page, messages);
    await quiet(page);
    await expect(page.getByTestId("chat-virtual-canvas")).toBeAttached();

    // Uma linha real, escolhida entre as montadas — a própria bolha, que é
    // focável (tabIndex 0) e existe em toda mensagem, em vez de um controle de
    // um tipo de anexo que pode não estar na janela neste instante.
    const focusedId = await bubbles(page)
      .first()
      .evaluate((element: HTMLElement) => {
        element.focus();
        return element.dataset.messageId ?? null;
      });
    expect(focusedId).not.toBeNull();
    expect(
      await page.evaluate(
        () => (document.activeElement as HTMLElement | null)?.dataset.messageId ?? null,
      ),
    ).toBe(focusedId);

    // Rola o suficiente para a linha sair da janela virtual e ser desmontada.
    await expect
      .poll(
        async () => {
          await list(page).evaluate((element) => {
            element.scrollTop = element.scrollHeight;
          });
          return page.evaluate(
            (id) => document.querySelector(`[data-message-id="${id}"]`) === null,
            focusedId!,
          );
        },
        { timeout: 20_000, intervals: [150] },
      )
      .toBe(true);

    // O contrato do produto (#675): o foco volta para a própria timeline, que
    // continua se anunciando como "Mensagens" e continua rolando pelo teclado.
    // O que não pode acontecer é ele cair silenciosamente em <body>.
    await expect
      .poll(() =>
        page.evaluate(() => {
          const active = document.activeElement;
          if (!active || active === document.body) return "body";
          return active.getAttribute("aria-label") ?? active.tagName;
        }),
      )
      .toBe("Mensagens");
  });
});
