/**
 * A conversa grande da issue #675, como fixture reutilizável.
 *
 * Vive aqui, e não dentro de um spec, por um motivo concreto: o benchmark da
 * #675 só significa alguma coisa comparado com um "antes", e esse "antes" é
 * medido numa exportação read-only de `develop`. Para que a comparação seja
 * honesta, as duas árvores precisam da *mesma* fixture e da *mesma*
 * instrumentação — mesma quantidade de mensagens, mesma distribuição de anexos,
 * mesma latência de rota, mesma forma de contar pedidos.
 *
 * Por isso este arquivo não importa nada de `src/`: ele é copiável, tal como
 * está, para dentro do snapshot de `develop`, onde a virtualização, o agendador
 * e o gate de proximidade não existem. Só código de teste atravessa essa
 * fronteira — nunca código de produção da feature. O procedimento completo está
 * em docs/testing/chat-675-large-conversation-benchmark.md.
 */

import { expect, type Page, type Request } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  makeMessage,
  type RawMessageAttachment,
} from "./messagingApi";

export const MESSAGE_COUNT = 500;
export const PAGE_SIZE = 50;

/**
 * Os anexos vêm em blocos, não espalhados de cinco em cinco.
 *
 * Uma conversa real tem rajadas — alguém manda um álbum, e responde com mais
 * dois —, e é a rajada que testa o agendador: espalhados, dois ou três anexos
 * entram na região útil de cada vez e a concorrência nunca chega perto do
 * limite, o que faria o teste publicar "pico 2" e não medir nada.
 *
 * Quatro mensagens seguidas com dois anexos cada, a cada quarenta: treze blocos
 * de oito dão os ~100 anexos que a issue pede, e um bloco inteiro cabe dentro da
 * região útil — quatro linhas, não oito —, que é a condição para que a demanda
 * simultânea exista de fato.
 */
export const CLUSTER_SPAN = 40;
export const CLUSTER_MESSAGES = 4;
export const ATTACHMENTS_PER_MESSAGE = 2;
export const CLUSTER_SIZE = CLUSTER_MESSAGES * ATTACHMENTS_PER_MESSAGE;

/**
 * Latência do preview derivado.
 *
 * É o que torna a concorrência observável: sem ela cada pedido termina antes do
 * próximo começar e o pico seria sempre 1, medindo nada. Não é um sleep do
 * teste — é o servidor demorando, que é a condição em que o limite do agendador
 * existe para valer.
 */
export const PREVIEW_LATENCY_MS = 250;

/**
 * Latência de uma página de histórico.
 *
 * Generosa de propósito: é a janela em que o estado ainda é o anterior ao
 * prepend, e é dentro dela que a âncora tem de ser lida *com a viewport já
 * parada*. Uma janela curta faria a leitura competir com o assentamento e o
 * teste mediria ora um, ora outro — que é como uma medição vira flake.
 */
export const PAGE_LATENCY_MS = 4_000;

/** 1×1 JPEG, o menor preview derivado que um navegador realmente decodifica. */
export const TINY_JPEG = Buffer.from(
  "/9j/4AAQSkZJRgABAQEAYABgAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0a" +
    "HBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/wAALCAABAAEBAREA/8QAFAABAAAAAAAA" +
    "AAAAAAAAAAAACf/EABQQAQAAAAAAAAAAAAAAAAAAAAD/2gAIAQEAAD8AKp//2Q==",
  "base64",
);

/**
 * A posição da mensagem `index` dentro do bloco, ou null se não carrega anexos.
 *
 * Contados a partir do fim da conversa, não do começo: a timeline abre no fim, e
 * um bloco que caísse fora da janela inicial faria a abertura não pedir preview
 * nenhum — medindo, de novo, o nada.
 */
export function clusterPosition(index: number): number | null {
  const fromEnd = MESSAGE_COUNT - 1 - index;
  const position = fromEnd % CLUSTER_SPAN;
  return position < CLUSTER_MESSAGES ? CLUSTER_MESSAGES - 1 - position : null;
}

/** O ordinal global do `slot`-ésimo anexo da mensagem `index`. */
export function attachmentOrdinal(index: number, slot: number): number {
  const cluster = Math.floor((MESSAGE_COUNT - 1 - index) / CLUSTER_SPAN);
  return cluster * CLUSTER_SIZE + clusterPosition(index)! * ATTACHMENTS_PER_MESSAGE + slot;
}

/**
 * Os cinco tipos, distribuídos dentro do bloco.
 *
 * Seis das oito posições pedem preview derivado (imagem pronta, PDF, GIF), o
 * que é mais do que o limite do agendador — é essa folga que faz o pico de
 * concorrência ser uma medição e não uma coincidência. As outras duas são os
 * casos que não podem pedir nada: preview ainda sendo gerado e vídeo.
 */
export const CLUSTER_KINDS = [
  "image",
  "pdf",
  "gif",
  "image",
  "pdf",
  "pending",
  "video",
  "gif",
] as const;

export function attachmentFor(index: number, slot: number): RawMessageAttachment {
  const kindIndex = clusterPosition(index)! * ATTACHMENTS_PER_MESSAGE + slot;
  const kind = CLUSTER_KINDS[kindIndex];
  const ordinal = attachmentOrdinal(index, slot);
  const base = { id: `att-${ordinal}`, status: "clean" as const, size: 900_000 };
  if (kind === "image") {
    return {
      ...base,
      filename: `foto-${ordinal}.png`,
      content_type: "image/png",
      preview_status: "ready",
    };
  }
  if (kind === "pending") {
    return {
      ...base,
      filename: `pendente-${ordinal}.png`,
      content_type: "image/png",
      // Preview ainda sendo gerado: precisa virar shell, nunca original.
      preview_status: "pending",
    };
  }
  if (kind === "video") {
    return {
      ...base,
      filename: `clipe-${ordinal}.mp4`,
      content_type: "video/mp4",
      size: 8_000_000,
      preview_status: "unsupported",
    };
  }
  if (kind === "pdf") {
    return {
      ...base,
      filename: `relatorio-${ordinal}.pdf`,
      content_type: "application/pdf",
      size: 400_000,
      preview_status: "ready",
    };
  }
  return {
    ...base,
    filename: `animacao-${ordinal}.gif`,
    content_type: "image/gif",
    size: 1_200_000,
    preview_status: "ready",
  };
}

/** Os ids dos GIFs, o único tipo que pode buscar o original na timeline. */
export function gifAttachmentIds(): Set<string> {
  const ids = new Set<string>();
  for (let index = 0; index < MESSAGE_COUNT; index += 1) {
    if (clusterPosition(index) === null) continue;
    for (let slot = 0; slot < ATTACHMENTS_PER_MESSAGE; slot += 1) {
      const kindIndex = clusterPosition(index)! * ATTACHMENTS_PER_MESSAGE + slot;
      if (CLUSTER_KINDS[kindIndex] === "gif") ids.add(`att-${attachmentOrdinal(index, slot)}`);
    }
  }
  return ids;
}

export function buildHistory(targetId: string) {
  return Array.from({ length: MESSAGE_COUNT }, (_, index) => {
    const day = 10 + Math.floor(index / 120);
    // Corpos de tamanhos bem diferentes, para que nenhuma linha tenha a mesma
    // altura da vizinha e a virtualização não possa "acertar por sorte".
    const filler = "palavra ".repeat((index % 7) * 6 + 1);
    return makeMessage({
      id: `${targetId}-m-${index}`,
      sender_id: index % 3 === 0 ? CURRENT_USER_ID : OTHER_USER_ID,
      sender_display_name: index % 3 === 0 ? "E2E Autor" : OTHER_USER_NAME,
      body_text: `Mensagem ${index} ${filler}`,
      created_at: `2026-07-${day}T12:${String(index % 60).padStart(2, "0")}:00.000Z`,
      updated_at: `2026-07-${day}T12:${String(index % 60).padStart(2, "0")}:00.000Z`,
      ...(clusterPosition(index) !== null
        ? {
            attachments: Array.from({ length: ATTACHMENTS_PER_MESSAGE }, (_, slot) =>
              attachmentFor(index, slot),
            ),
          }
        : {}),
    });
  });
}

export interface PreviewTracker {
  total: () => number;
  peakConcurrent: () => number;
}

/**
 * Serve os previews derivados e mede quantos estão em voo ao mesmo tempo.
 *
 * "Em voo" são os que o navegador ainda quer. Um pedido que o cliente abortou —
 * uma linha que saiu da região útil no meio da rolagem — continua pendente aqui
 * até esta rota tentar respondê-lo, e contá-lo faria o pico descrever o atraso
 * do harness em vez do limite do agendador. O evento `requestfailed` é o
 * momento em que o navegador diz que desistiu; é ele que fecha a conta.
 */
export async function installPreviewRoutes(page: Page): Promise<PreviewTracker> {
  const live = new Set<unknown>();
  let peak = 0;
  let total = 0;
  page.on("requestfailed", (request) => {
    live.delete(request);
  });
  const serve = async (route: Parameters<Parameters<Page["route"]>[1]>[0]) => {
    const request = route.request();
    live.add(request);
    total += 1;
    peak = Math.max(peak, live.size);
    await new Promise((resolve) => setTimeout(resolve, PREVIEW_LATENCY_MS));
    live.delete(request);
    // Um pedido abortado no meio do caminho não pode mais ser respondido, e
    // isso não é falha do cenário.
    await route
      .fulfill({
        status: 200,
        headers: { "Content-Type": "image/jpeg", "Cache-Control": "private, no-store" },
        body: TINY_JPEG,
      })
      .catch(() => {});
  };
  await page.route("**/api/files/attachments/*/preview", serve);
  await page.route("**/api/files/attachments/*/document-preview/pages/*", serve);
  return { total: () => total, peakConcurrent: () => peak };
}

export interface MessageTracker {
  /**
   * Quantidade de ids de mensagens distintos incluídos em respostas da fixture
   * **concluídas com sucesso** durante o cenário.
   *
   * É isso, e só isso. Não é o tamanho do estado do React, nem do store, nem o
   * número de mensagens renderizadas — nada disso é observável daqui sem
   * instrumentar a aplicação, e um nome que prometesse mais do que mede seria
   * pior do que um nome modesto.
   */
  servedCount: () => number;
  /** Páginas *pedidas*, contadas na entrada da rota, antes da latência. */
  pagesRequested: () => number;
  /** Páginas pedidas cuja resposta ainda não terminou. Zero é "nada em voo". */
  pagesInFlight: () => number;
}

/** Paginação por cursor sobre o histórico completo, que o mock partilhado não faz. */
export async function installPaginatedMessages(
  page: Page,
  targetId: string,
  all: ReturnType<typeof buildHistory>,
): Promise<MessageTracker> {
  const served = new Set<string>();
  let pages = 0;
  let inFlight = 0;
  /**
   * Pedidos que o navegador desistiu de esperar.
   *
   * `route.fulfill()` não reclama quando isso acontece: ele responde para
   * ninguém, sem erro. O evento `requestfailed` é o único momento em que o
   * navegador diz que abandonou, e sem ele uma página que ninguém recebeu
   * entraria na contagem de "servidas" — que é justamente o que o nome promete
   * não fazer.
   */
  const abandoned = new WeakSet<Request>();
  page.on("requestfailed", (request) => abandoned.add(request));
  await page.route(`**/api/chat/channels/${targetId}/messages*`, async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const request = route.request();
    const before = new URL(request.url()).searchParams.get("before");
    pages += 1;
    inFlight += 1;
    try {
      // Latência de servidor, não um sleep de teste: é o que dá ao teste uma
      // janela observável entre "a página foi pedida" e "a página chegou", que
      // é onde a âncora tem de ser lida. O contador acima sobe na entrada,
      // justamente para que essa janela seja detectável e não presumida.
      if (before) await new Promise((resolve) => setTimeout(resolve, PAGE_LATENCY_MS));
      const end = before ? all.findIndex((message) => message.id === before) : all.length;
      const start = Math.max(0, end - PAGE_SIZE);
      const page_ = all.slice(start, end);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            messages: page_,
            next_cursor: start > 0 ? page_[0].id : "",
          },
        }),
      });
      // Só depois de a resposta ter sido entregue, e só se havia alguém para
      // recebê-la. É isso que o nome promete: ids incluídos em respostas
      // concluídas com sucesso.
      if (!abandoned.has(request)) {
        for (const message of page_) served.add(message.id);
      }
    } finally {
      // No finally, e não no caminho feliz: um pedido abortado tem de liberar a
      // contagem de "em voo" do mesmo jeito, senão quem espera a fixture
      // sossegar espera para sempre.
      inFlight -= 1;
    }
  });
  return {
    servedCount: () => served.size,
    pagesRequested: () => pages,
    pagesInFlight: () => inFlight,
  };
}

/** Espera a lista de mensagens parar de mudar — nenhuma busca ainda em voo. */
export async function quiet(page: Page) {
  let last = -1;
  await expect
    .poll(
      async () => {
        const now = await page.evaluate(
          () => document.querySelectorAll("[data-testid='chat-msg-bubble']").length,
        );
        const stable = now === last;
        last = now;
        return stable;
      },
      { timeout: 20_000, intervals: [200] },
    )
    .toBe(true);
}

/**
 * Espera não haver nenhuma página de histórico em voo.
 *
 * Zero em voo *e* nenhum pedido novo desde a leitura anterior. Só a primeira
 * condição deixaria passar o instante entre o navegador emitir o pedido e ele
 * chegar à rota: naquele intervalo nada está em voo, e a página chegaria depois,
 * no meio da medição seguinte.
 */
export async function noPageInFlight(messages: MessageTracker) {
  let previousRequested = -1;
  await expect
    .poll(
      () => {
        const settled =
          messages.pagesInFlight() === 0 && messages.pagesRequested() === previousRequested;
        previousRequested = messages.pagesRequested();
        return settled;
      },
      { timeout: 30_000, intervals: [250] },
    )
    .toBe(true);
}

/** O elemento que rola a timeline. */
export const list = (page: Page) => page.getByRole("log", { name: "Mensagens" });

/** As bolhas de mensagem montadas. */
export const bubbles = (page: Page) => page.getByTestId("chat-msg-bubble");

/**
 * Leva a viewport ao topo de um jeito que o sentinel possa relatar.
 *
 * O sentinel do topo é um IntersectionObserver, e um observer só relata
 * *transições*: com a viewport já em zero, reafirmar zero não produz evento
 * nenhum e o pedido nunca sai. Então sai-se primeiro, esperam-se dois quadros —
 * para que as duas posições sejam realmente observadas, e não coalesçam numa só
 * — e só então volta-se ao topo.
 */
export async function goToTop(page: Page) {
  await list(page).evaluate((element) => {
    element.scrollTop = element.clientHeight;
  });
  await page.evaluate(
    () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))),
  );
  await list(page).evaluate((element) => {
    element.scrollTop = 0;
  });
}

/**
 * Sobe até o topo e espera a página anterior chegar.
 *
 * A chegada é observada nas respostas da rota, nunca no DOM: sob virtualização
 * a janela montada tem praticamente o mesmo tamanho antes e depois de um
 * prepend, então
 * "o número de bolhas mudou" é um sinal que ora dispara sozinho, ora nunca
 * dispara.
 */
export async function loadPreviousPage(page: Page, messages: MessageTracker) {
  const servedBefore = messages.servedCount();
  await expect
    .poll(
      async () => {
        await goToTop(page);
        return messages.servedCount();
      },
      { timeout: 30_000, intervals: [250] },
    )
    .toBeGreaterThan(servedBefore);
}

/**
 * Espera a viewport parar de se mexer.
 *
 * A altura do conteúdo junto com a posição, e não só a posição: uma linha ainda
 * por medir mexe nas duas, e olhar apenas o scrollTop declararia assentado o
 * instante entre duas remedições.
 */
export async function settledViewport(page: Page) {
  let previous = "";
  await expect
    .poll(
      async () => {
        const now = await list(page).evaluate(
          (element) => `${element.scrollTop}/${element.scrollHeight}`,
        );
        const settled = now === previous;
        previous = now;
        return settled;
      },
      { timeout: 15_000, intervals: [120] },
    )
    .toBe(true);
}

/**
 * O id da mensagem encostada na borda superior da viewport, e seu deslocamento.
 *
 * A mesma regra que o produto usa para dizer qual mensagem está sendo lida: a de
 * menor deslocamento que ainda começa dentro da viewport. Medir com uma
 * definição diferente da restaurada responderia a outra pergunta.
 */
export async function readingAnchor(page: Page) {
  return list(page).evaluate((element) => {
    const top = element.getBoundingClientRect().top;
    let best: { id: string | null; offset: number } | null = null;
    for (const candidate of element.querySelectorAll<HTMLElement>("[data-message-id]")) {
      const offset = Math.round(candidate.getBoundingClientRect().top - top);
      if (offset >= -4 && offset < element.clientHeight && (!best || offset < best.offset)) {
        best = { id: candidate.dataset.messageId ?? null, offset };
      }
    }
    return best ?? { id: null as string | null, offset: 0 };
  });
}

/** Uma leitura de âncora: qual mensagem, e a que distância da borda de cima. */
export interface AnchorReading {
  id: string | null;
  offset: number;
}

export interface AnchorComparison {
  anchorMessageBefore: string | null;
  anchorMessageAfter: string | null;
  /** A mesma mensagem continua sendo a âncora. */
  anchorIdentityPreserved: boolean;
  anchorOffsetBefore: number;
  anchorOffsetAfter: number;
  /**
   * O quanto a âncora se moveu — **null** quando a identidade não foi
   * preservada.
   *
   * Esta é a regra que o benchmark existia sem: subtrair o deslocamento de duas
   * mensagens *diferentes* produz um número, e esse número pode até ser zero,
   * mas ele não diz nada sobre preservação de âncora. Zero ali significaria
   * apenas "outra mensagem calhou de cair na mesma coordenada", que é o oposto
   * do que se quer provar. Sem identidade não há o que comparar, e o campo diz
   * isso em vez de fingir um resultado.
   */
  anchorOffsetDriftPx: number | null;
}

/** Compara duas leituras de âncora sob a regra acima. */
export function compareAnchors(before: AnchorReading, after: AnchorReading): AnchorComparison {
  const anchorIdentityPreserved = before.id !== null && before.id === after.id;
  return {
    anchorMessageBefore: before.id,
    anchorMessageAfter: after.id,
    anchorIdentityPreserved,
    anchorOffsetBefore: before.offset,
    anchorOffsetAfter: after.offset,
    anchorOffsetDriftPx: anchorIdentityPreserved ? Math.abs(after.offset - before.offset) : null,
  };
}

/**
 * Desce uma viewport a partir do topo, e devolve a leitura de âncora dali.
 *
 * A preparação que torna a comparação de identidade honesta, e a mesma nas duas
 * árvores. Encostada no topo, a mensagem-âncora é a mais antiga carregada e o
 * que está acima dela é cromagem — divisor de dia e indicador de carregamento.
 * Depois de um prepend aquele espaço passa a ser ocupado por mensagens recém
 * chegadas, então *qual* mensagem encosta na borda muda por construção, mesmo
 * com a restauração perfeita: é exatamente assim que o benchmark passou a
 * comparar m-100 com m-50 e a chamar a diferença de zero.
 *
 * Uma viewport abaixo, o que está acima da âncora são mensagens que já estavam
 * carregadas e que o prepend não toca. Aí a identidade da âncora é uma
 * afirmação sobre o que a timeline fez, e não sobre o conteúdo novo.
 */
export async function anchorBelowTheTop(page: Page): Promise<AnchorReading> {
  await list(page).evaluate((element) => {
    element.scrollTop = element.clientHeight;
  });
  await settledViewport(page);
  return readingAnchor(page);
}
