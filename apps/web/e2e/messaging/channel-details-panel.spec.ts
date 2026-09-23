import { expect, test } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  OTHER_CHANNEL_ID,
  OTHER_CHANNEL_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  channelDetailsFixture,
  channelRosterFixture,
  createScenario,
  emitConversationUpdated,
  installMessagingMocks,
  makeMessage,
  setServerChannelName,
  uniqueId,
} from "../helpers/messagingApi";

test.describe("painel de detalhes do canal", () => {
  test("abre pelo cabeçalho, mostra as seções, preserva a conversa e devolve o foco", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-details");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Infraestrutura E2E",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no canal" })],
    });
    for (const channel of scenario.sidebarChannels) {
      const roster = [
        {
          user_id: CURRENT_USER_ID,
          display_name: CURRENT_USER_NAME,
          role: "moderator" as const,
        },
        { user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME, role: "member" as const },
        ...offlineRoster(10),
      ];
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(
          channel,
          [
            {
              user_id: CURRENT_USER_ID,
              display_name: CURRENT_USER_NAME,
              role: "moderator",
              presence: "online",
            },
            {
              user_id: OTHER_USER_ID,
              display_name: OTHER_USER_NAME,
              role: "member",
              presence: "online",
            },
          ],
          // The channel has more members than are connected: the panel must
          // report both figures without confusing them.
          12,
        ),
      );
      scenario.channelRosters.set(channel.id, channelRosterFixture(roster, 12));
    }
    // Distinct About metadata per channel (issue #894), so switching channels
    // with the panel open has something to actually change. The details for the
    // channel the user starts in name a creator; the other one deliberately
    // does not, which is the historical case the panel must not fill in.
    scenario.channelDetails.get(targetId)!.description =
      "Infraestrutura, processos internos e operações.";
    scenario.channelDetails.get(targetId)!.creator_display_name = OTHER_USER_NAME;
    scenario.channelDetails.get(OTHER_CHANNEL_ID)!.description = "Outro canal, outra descrição.";
    scenario.channelAttachments.set(targetId, [
      {
        id: `${targetId}-file`,
        filename: "relatorio-backup.pdf",
        contentType: "application/pdf",
        size: 2_516_582,
        status: "clean",
        createdAt: "2026-07-15T12:24:00Z",
      },
    ]);
    scenario.channelAttachments.set(OTHER_CHANNEL_ID, [
      {
        id: `${OTHER_CHANNEL_ID}-file`,
        filename: "topologia-rede.png",
        contentType: "image/png",
        size: 911_360,
        status: "pending_scan",
        createdAt: "2026-07-14T09:00:00Z",
      },
    ]);

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const composer = page.getByTestId("chat-composer-input");
    await expect(composer).toBeVisible();
    await composer.click();
    await page.keyboard.insertText("rascunho que precisa sobreviver");
    await expect(composer).toContainText("rascunho que precisa sobreviver");

    const routeBeforeOpening = new URL(page.url()).pathname;
    const toggle = page.getByRole("button", { name: "Detalhes do canal", exact: true });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");

    // ── 1. abrir o painel ────────────────────────────────────────────────
    await toggle.click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    await expect(panel).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");

    // ── 2. seções principais com dados reais do canal ────────────────────
    await expect(panel.getByRole("heading", { name: "Detalhes do canal" })).toBeVisible();
    await expect(panel.getByRole("heading", { name: "Descrição" })).toBeVisible();
    await expect(panel.getByTestId("chat-details-description")).toHaveText(
      "Infraestrutura, processos internos e operações.",
    );
    await expect(panel.getByText(/Criado em 12 de janeiro de 2024/)).toBeVisible();
    await expect(panel.getByText(`Criado por ${OTHER_USER_NAME}`)).toBeVisible();
    await expect(panel.getByText("Canal público")).toBeVisible();
    await expect(panel.getByText("12 membros")).toBeVisible();
    // Nothing technical reached the block: no identifier, and no truncation of
    // one standing in for the creator's name.
    await expect(panel.getByText(OTHER_USER_ID)).toHaveCount(0);
    await expect(panel.getByText(OTHER_USER_ID.slice(0, 8))).toHaveCount(0);
    await expect(panel.getByRole("heading", { name: "Membros (12)" })).toBeVisible();
    const members = panel.getByRole("list", { name: "Membros do canal" });
    await expect(members.getByText(CURRENT_USER_NAME)).toBeVisible();
    await expect(members.getByText("Você")).toBeVisible();
    await expect(members.getByText(OTHER_USER_NAME)).toBeVisible();
    await expect(members.getByText("Pessoa Offline 01")).toBeVisible();
    await expect(panel.getByRole("heading", { name: "Mensagem fixada" })).toBeVisible();
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("relatorio-backup.pdf"),
    ).toBeVisible();

    // O roster completo tem conteúdo além do compacto, mesmo que somente duas
    // pessoas estejam online. A expansão pertence à lista de membros.
    await expect(panel.getByRole("button", { name: "Ver todos Membros" })).toBeVisible();
    await expect(panel.getByText(/ainda não está disponível nesta versão/)).toHaveCount(0);

    // "Adicionar membros" deixou de ser um placeholder (issue #398): virou fluxo
    // real, e este cenário não concede a permissão, então a ação fica ausente —
    // o padrão seguro. O fluxo em si é coberto por add-members.spec.ts.
    await expect(panel.getByTestId("chat-details-add-members")).toHaveCount(0);

    // ── 3. a rota não mudou e o compositor segue utilizável ──────────────
    expect(new URL(page.url()).pathname).toBe(routeBeforeOpening);
    await expect(composer).toBeVisible();
    await expect(composer).toContainText("rascunho que precisa sobreviver");
    await composer.click();
    await page.keyboard.insertText(" e continua editável");
    await expect(composer).toContainText("rascunho que precisa sobreviver e continua editável");

    // ── 4. trocar de canal com o painel aberto ───────────────────────────
    await page.getByRole("option", { name: /Canal E2E/ }).click();
    await expect(panel).toBeVisible();
    // The whole About block moved to the other channel: its own description,
    // and its own neutral creator state. Neither field is one channel behind.
    await expect(panel.getByTestId("chat-details-description")).toHaveText(
      "Outro canal, outra descrição.",
    );
    await expect(panel.getByText("Criador não identificado")).toBeVisible();
    await expect(panel.getByText(`Criado por ${OTHER_USER_NAME}`)).toHaveCount(0);
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("topologia-rede.png"),
    ).toBeVisible();
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("relatorio-backup.pdf"),
    ).toHaveCount(0);
    // Arquivo ainda em análise aparece marcado e nunca como link.
    await expect(panel.getByText("Em análise")).toBeVisible();
    await expect(panel.getByRole("link")).toHaveCount(0);
    await expect(panel.locator("a")).toHaveCount(0);

    // ── 5. fechar pelo botão do painel e validar o retorno do foco ───────
    await panel.getByRole("button", { name: "Fechar detalhes do canal" }).click();
    await expect(panel).toHaveCount(0);

    const toggleAfterClose = page.getByRole("button", { name: "Detalhes do canal", exact: true });
    await expect(toggleAfterClose).toHaveAttribute("aria-expanded", "false");
    await expect(toggleAfterClose).toBeFocused();
    // Fechar também não trocou de rota: continua no canal aberto no passo 4.
    expect(new URL(page.url()).pathname).toContain(OTHER_CHANNEL_ID);
  });

  test("abre e fecha pelo mesmo controle do cabeçalho, por teclado", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-details-keyboard");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Teclado",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(channel, [
          {
            user_id: CURRENT_USER_ID,
            display_name: CURRENT_USER_NAME,
            role: "member",
            presence: "online",
          },
        ]),
      );
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const toggle = page.getByRole("button", { name: "Detalhes do canal", exact: true });
    await expect(toggle).toBeVisible();
    await toggle.focus();
    await page.keyboard.press("Enter");

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    await expect(panel).toBeVisible();
    // O foco entra no painel, no seu botão de fechar.
    await expect(panel.getByRole("button", { name: "Fechar detalhes do canal" })).toBeFocused();

    await toggle.click();
    await expect(panel).toHaveCount(0);
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
  });

  /**
   * ISSUE #891 — the shell's promise across a full toggle cycle, in the one
   * place only a browser can answer it: opening the panel narrows the
   * conversation column on a wide desktop, so the timeline genuinely reflows.
   * What has to survive that is the reader's context — the message they were
   * reading and the draft they were writing — not a pixel offset, which a
   * reflow legitimately changes.
   *
   * Forty messages: enough history for a reading position well away from the
   * tail, and still under VIRTUALIZE_MIN_ROWS, so the anchor is a real element
   * throughout rather than one the virtualizer may unmount.
   */
  test("alterna pelo mesmo controle preservando rascunho, âncora da timeline e foco", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-details-toggle");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Alternância",
      messages: Array.from({ length: 40 }, (_, i) =>
        makeMessage({
          id: `${targetId}-m${i}`,
          body_text: `Mensagem ${i}`,
          created_at: `2026-07-15T09:${String(i).padStart(2, "0")}:00.000Z`,
        }),
      ),
    });
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(channel, [
          {
            user_id: CURRENT_USER_ID,
            display_name: CURRENT_USER_NAME,
            role: "member",
            presence: "online",
          },
        ]),
      );
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const timeline = page.getByRole("log", { name: "Mensagens" });
    const composer = page.getByTestId("chat-composer-input");
    await expect(timeline).toBeVisible();
    await expect(composer).toBeVisible();

    // Uma posição de leitura não trivial, com uma mensagem concreta como
    // âncora lógica — é ela, e não um scrollTop, que o leitor perceberia.
    const anchor = page.locator(`[data-message-id="${targetId}-m8"]`);
    await anchor.scrollIntoViewIfNeeded();
    await expect(anchor).toBeInViewport();
    expect(await timeline.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);

    await composer.click();
    await page.keyboard.insertText("rascunho que atravessa o toggle");
    await expect(composer).toContainText("rascunho que atravessa o toggle");

    const toggle = page.getByTestId("chat-details-toggle");
    const panel = page.getByTestId("chat-conversation-details");
    const closeButton = panel.getByRole("button", { name: "Fechar detalhes do canal" });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    const restingBackground = await toggle.evaluate((el) => getComputedStyle(el).backgroundColor);

    // ── fechado → aberto ────────────────────────────────────────────────
    await toggle.click();
    await expect(panel).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    // O estado visual ativo acompanha a abertura, e não é o de repouso.
    expect(await toggle.evaluate((el) => getComputedStyle(el).backgroundColor)).not.toBe(
      restingBackground,
    );
    // O foco entra no painel; nada além dele fica preso.
    await expect(closeButton).toBeFocused();

    // A conversa continua ao lado, com o leitor onde estava.
    await expect(timeline).toBeVisible();
    await expect(composer).toBeVisible();
    await expect(composer).toContainText("rascunho que atravessa o toggle");
    await expect(anchor).toBeInViewport();

    // ── aberto → fechado pelo mesmo controle ────────────────────────────
    await toggle.click();
    await expect(panel).toHaveCount(0);
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    // Sem estado visual residual.
    await expect(toggle).toHaveCSS("background-color", restingBackground);
    // O foco fica no acionador que o leitor acabou de usar, nunca no <body>.
    await expect(toggle).toBeFocused();
    await expect(anchor).toBeInViewport();
    await expect(composer).toContainText("rascunho que atravessa o toggle");

    // ── reabrir e fechar pelo X ─────────────────────────────────────────
    await toggle.click();
    await expect(panel).toBeVisible();
    await closeButton.click();
    await expect(panel).toHaveCount(0);
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(toggle).toBeFocused();
    await expect(anchor).toBeInViewport();
    await expect(composer).toContainText("rascunho que atravessa o toggle");
  });

  test("mostra membros offline quando ninguém está online", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-details-empty");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Vazio",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    for (const channel of scenario.sidebarChannels) {
      // 31 membros no canal, nenhum conectado — exatamente o cenário em que o
      // painel não pode dizer que o canal está vazio.
      scenario.channelDetails.set(channel.id, channelDetailsFixture(channel, [], 31));
      scenario.channelRosters.set(channel.id, channelRosterFixture(offlineRoster(31), 31));
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByRole("button", { name: "Detalhes do canal", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    await expect(panel.getByText("Nenhum membro online no momento.")).toHaveCount(0);
    // O tamanho do canal continua reportado e não vira zero.
    await expect(panel.getByText("Canal público")).toBeVisible();
    await expect(panel.getByText("31 membros")).toBeVisible();
    await expect(panel.getByRole("heading", { name: "Membros (31)" })).toBeVisible();
    const members = panel.getByRole("list", { name: "Membros do canal" });
    await expect(members.getByRole("listitem")).toHaveCount(5);
    await expect(members.getByText("Pessoa Offline 01")).toBeVisible();
    await expect(panel.getByText("Nenhuma mensagem fixada neste canal.")).toBeVisible();
    await expect(panel.getByText("Nenhum arquivo enviado neste canal.")).toBeVisible();
    await expect(panel.getByText("Este canal ainda não tem descrição.")).toBeVisible();
  });

  test("ordena primeiro o membro online dentro do roster completo", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-details-online-cut");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Grande",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    // O servidor filtra por presença antes do limite, então o único membro
    // conectado — o 31º em ordem alfabética — é o que chega ao cliente.
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(
          channel,
          [
            {
              user_id: "e2e-ultimo-alfabetico",
              display_name: "Zulmira Última",
              role: "member",
              presence: "online",
            },
          ],
          31,
        ),
      );
      scenario.channelRosters.set(
        channel.id,
        channelRosterFixture(
          [
            ...offlineRoster(30),
            { user_id: "e2e-ultimo-alfabetico", display_name: "Zulmira Última", role: "member" },
          ],
          31,
        ),
      );
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByRole("button", { name: "Detalhes do canal", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    const members = panel.getByRole("list", { name: "Membros do canal" });
    await expect(members.getByText("Zulmira Última")).toBeVisible();
    await expect(members.getByRole("listitem")).toHaveCount(5);
    await expect(panel.getByRole("heading", { name: "Membros (31)" })).toBeVisible();
    await expect(panel.getByText("Canal público")).toBeVisible();
    await expect(panel.getByText("31 membros")).toBeVisible();
  });
});

/**
 * A roster longer than the compact cap, so the section really has something to
 * reveal (issue #892). Thirty is the server's own ceiling for this preview.
 */
function onlineRoster(count: number) {
  return Array.from({ length: count }, (_, index) => ({
    user_id: `e2e-online-${index}`,
    display_name: `Pessoa Online ${String(index + 1).padStart(2, "0")}`,
    role: "member" as const,
    presence: "online" as const,
  }));
}

function offlineRoster(count: number) {
  return Array.from({ length: count }, (_, index) => ({
    user_id: `e2e-offline-${index}`,
    display_name: `Pessoa Offline ${String(index + 1).padStart(2, "0")}`,
    role: "member" as const,
  }));
}

function rosterMembers(members: ReturnType<typeof onlineRoster>) {
  return members.map(({ user_id, display_name, role }) => ({ user_id, display_name, role }));
}

test.describe("seção expansível de membros", () => {
  test("mostra 5, expande com scroll interno e volta ao compacto sem mover a conversa", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-expand");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Infra Expansível",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no canal" })],
    });
    for (const channel of scenario.sidebarChannels) {
      const members = onlineRoster(12);
      scenario.channelDetails.set(channel.id, channelDetailsFixture(channel, members, 12));
      scenario.channelRosters.set(channel.id, channelRosterFixture(rosterMembers(members), 12));
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await page.getByRole("button", { name: "Detalhes do canal", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    const members = panel.getByRole("list", { name: "Membros do canal" });
    const messageArea = page.getByTestId("chat-message-area");

    // ── 1. compacto ──────────────────────────────────────────────────────
    await expect(members.getByRole("listitem")).toHaveCount(5);
    await expect(panel.getByRole("heading", { name: "Membros (12)" })).toBeVisible();
    await expect(members.getByText("Pessoa Online 06")).toHaveCount(0);

    // One locator for both states: the control is the same element throughout —
    // only its label and its aria-expanded change — and a locator that matched
    // just one of the two labels would be asserting that it is replaced.
    const toggle = panel.getByRole("button", {
      name: /(Ver todos|Mostrar menos) Membros/,
    });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    // Alvo de toque utilizável: o controle é pequeno em texto, não em área.
    const toggleBox = await toggle.boundingBox();
    expect(toggleBox?.height ?? 0).toBeGreaterThanOrEqual(16);

    const timelineBefore = await messageArea.boundingBox();

    // ── 2. expandir pelo teclado ─────────────────────────────────────────
    await toggle.focus();
    await page.keyboard.press("Enter");

    await expect(members.getByRole("listitem")).toHaveCount(12);
    await expect(members.getByText("Pessoa Online 12")).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");

    // ── 3. o crescimento fica dentro da própria seção ─────────────────────
    const scroll = await members.evaluate((list) => ({
      clientHeight: list.clientHeight,
      scrollHeight: list.scrollHeight,
      overflowY: getComputedStyle(list).overflowY,
      tabIndex: list.tabIndex,
    }));
    expect(scroll.overflowY).toBe("auto");
    // Mais conteúdo do que altura: o scroll é interno, e não do painel inteiro.
    expect(scroll.scrollHeight).toBeGreaterThan(scroll.clientHeight);
    // Uma região rolável precisa ser alcançável por teclado.
    expect(scroll.tabIndex).toBe(0);

    // A conversa não se move nem muda de largura por causa da expansão. O
    // contrato é geométrico, não bit-a-bit: o que não pode mudar é a coluna da
    // timeline — onde ela começa e quanto ela ocupa. Comparar o boundingBox
    // inteiro transformaria um arredondamento subpixel do navegador, ou uma
    // mudança de altura que o contrato não proíbe, em falha de teste.
    const timelineAfter = await messageArea.boundingBox();
    expect(timelineAfter?.x).toBeCloseTo(timelineBefore?.x ?? NaN, 1);
    expect(timelineAfter?.width).toBeCloseTo(timelineBefore?.width ?? NaN, 1);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    // Nada de rolagem horizontal, nem no painel nem no documento.
    const overflow = await page.evaluate(() => ({
      document: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      panel: (() => {
        const aside = document.querySelector<HTMLElement>(
          "[data-testid=chat-conversation-details]",
        );
        return aside ? aside.scrollWidth - aside.clientWidth : 0;
      })(),
    }));
    expect(overflow.document).toBeLessThanOrEqual(0);
    expect(overflow.panel).toBeLessThanOrEqual(0);

    // ── 4. colapsar mantém o foco no mesmo controle ───────────────────────
    await page.keyboard.press("Enter");
    await expect(members.getByRole("listitem")).toHaveCount(5);
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(toggle).toBeFocused();
  });

  test("continua utilizável e sem overflow horizontal em viewport estreito", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const targetId = uniqueId(testInfo, "channel-expand-phone");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Infra Estreita",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no canal" })],
    });
    for (const channel of scenario.sidebarChannels) {
      const members = onlineRoster(12);
      scenario.channelDetails.set(channel.id, channelDetailsFixture(channel, members, 12));
      scenario.channelRosters.set(channel.id, channelRosterFixture(rosterMembers(members), 12));
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await page.getByTestId("chat-details-toggle").click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    const toggle = panel.getByRole("button", {
      name: /(Ver todos|Mostrar menos) Membros/,
    });
    await toggle.click();

    // O controle fica acima da lista, então expandir nunca o empurra para fora:
    // "Mostrar menos" continua visível e clicável no mesmo lugar.
    await expect(toggle).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(
      panel.getByRole("list", { name: "Membros do canal" }).getByRole("listitem"),
    ).toHaveCount(12);

    const horizontal = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(horizontal).toBeLessThanOrEqual(0);

    await toggle.click();
    await expect(
      panel.getByRole("list", { name: "Membros do canal" }).getByRole("listitem"),
    ).toHaveCount(5);
  });
});

/**
 * Renomeação inline do nome no painel de detalhes (issue #893).
 *
 * Um describe próprio porque o assunto é outro: não a leitura do painel,
 * mas a escrita que ele passa a oferecer — e a convergência das três
 * superfícies a partir de uma única confirmação.
 */
test.describe("renomeação inline do canal no painel", () => {
  // ── ISSUE #893: renomeação inline do canal no painel ────────────────────
  //
  // O que só o navegador responde: o nome persistido converge no painel, no
  // cabeçalho e na sidebar a partir de uma única confirmação, sem recarregar a
  // página — e sobrevive a um reload de verdade.
  test("renomeia o canal pelo painel e converge painel, cabeçalho e sidebar sem recarregar", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-rename-inline");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Infraestrutura E2E",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no canal" })],
    });
    // A capacidade é do servidor: a fixture a concede como o payload real faria.
    for (const channel of scenario.sidebarChannels) {
      channel.can_rename = channel.id === targetId;
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(channel, [
          {
            user_id: CURRENT_USER_ID,
            display_name: CURRENT_USER_NAME,
            role: "moderator",
            presence: "online",
          },
        ]),
      );
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    // Sentinela de "a página não recarregou": um reload apaga a propriedade.
    await page.evaluate(() => {
      (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload = true;
    });

    await page.getByRole("button", { name: "Detalhes do canal", exact: true }).click();
    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    await expect(panel.getByTestId("chat-details-channel-name")).toHaveText("Infraestrutura E2E");

    await panel.getByRole("button", { name: "Renomear canal" }).click();
    const field = panel.getByRole("textbox", { name: "Nome do canal" });
    await expect(field).toBeFocused();
    await expect(field).toHaveValue("Infraestrutura E2E");
    // Edição inline: nenhum modal se abriu.
    await expect(page.getByRole("dialog")).toHaveCount(0);

    await field.fill("  Plataforma E2E  ");
    await field.press("Enter");

    // O painel volta ao estado de leitura já com o nome persistido, e o
    // cabeçalho e a sidebar convergem pela mesma fonte canônica.
    await expect(panel.getByTestId("chat-details-channel-name")).toHaveText("Plataforma E2E");
    await expect(panel.getByRole("textbox", { name: "Nome do canal" })).toHaveCount(0);
    await expect(page.getByTestId("chat-msg-header")).toContainText("Plataforma E2E");
    await expect(page.getByRole("option", { name: /Plataforma E2E/ })).toBeVisible();
    await expect(page.getByRole("option", { name: /Infraestrutura E2E/ })).toHaveCount(0);

    // O servidor recebeu o nome já trimado, uma única vez.
    expect(scenario.requests.channelRenames).toEqual([
      { channelId: targetId, displayName: "Plataforma E2E" },
    ]);
    await expect
      .poll(() =>
        page.evaluate(
          () => (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload === true,
        ),
      )
      .toBe(true);

    // ── persistência: reload real, painel reaberto ────────────────────────
    await page.reload();
    await expect(page.getByTestId("chat-msg-header")).toContainText("Plataforma E2E");
    await page.getByRole("button", { name: "Detalhes do canal", exact: true }).click();
    await expect(
      page
        .getByRole("complementary", { name: "Detalhes do canal" })
        .getByTestId("chat-details-channel-name"),
    ).toHaveText("Plataforma E2E");
  });

  test("Escape descarta o rascunho e nada é persistido", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-rename-escape");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Escape",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    for (const channel of scenario.sidebarChannels) {
      channel.can_rename = true;
      scenario.channelDetails.set(channel.id, channelDetailsFixture(channel, []));
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await page.getByRole("button", { name: "Detalhes do canal", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    const editAction = panel.getByRole("button", { name: "Renomear canal" });
    await editAction.click();
    const field = panel.getByRole("textbox", { name: "Nome do canal" });
    await field.fill("Nome abandonado");
    await field.press("Escape");

    // O editor fechou, o painel continua aberto e o nome autoritativo voltou.
    await expect(panel.getByRole("textbox", { name: "Nome do canal" })).toHaveCount(0);
    await expect(panel).toBeVisible();
    await expect(panel.getByTestId("chat-details-channel-name")).toHaveText("Canal Escape");
    await expect(editAction).toBeFocused();
    await expect(page.getByTestId("chat-msg-header")).toContainText("Canal Escape");
    expect(scenario.requests.channelRenames).toEqual([]);
  });

  // O canal geral do workspace é estrutural: a fixture o marca com is_general e
  // ainda assim concede can_rename, para que a ausência da ação prove o flag
  // estrutural e não a capacidade. O backend recusa o PATCH de qualquer forma
  // (channel_rename_authorization_test.go).
  test("o canal geral não oferece renomeação no painel", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-rename-general");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Anúncios",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    for (const channel of scenario.sidebarChannels) {
      channel.can_rename = true;
      channel.is_general = channel.id === targetId;
      scenario.channelDetails.set(channel.id, channelDetailsFixture(channel, []));
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await page.getByRole("button", { name: "Detalhes do canal", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    await expect(panel.getByTestId("chat-details-channel-name")).toHaveText("Anúncios");
    await expect(panel.getByRole("button", { name: "Renomear canal" })).toHaveCount(0);
    expect(scenario.requests.channelRenames).toEqual([]);
  });

  test("renomeia inteiramente pelo teclado, sem mouse", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-rename-keyboard");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Teclado",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    for (const channel of scenario.sidebarChannels) {
      channel.can_rename = true;
      scenario.channelDetails.set(channel.id, channelDetailsFixture(channel, []));
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const toggle = page.getByRole("button", { name: "Detalhes do canal", exact: true });
    await toggle.focus();
    await page.keyboard.press("Enter");

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    const editAction = panel.getByRole("button", { name: "Renomear canal" });
    await expect(editAction).toBeVisible();

    // Tab a partir do foco que o painel deu (o botão fechar) até a ação.
    for (
      let stop = 0;
      stop < 10 && !(await editAction.evaluate((el) => el === document.activeElement));
      stop += 1
    ) {
      await page.keyboard.press("Tab");
    }
    await expect(editAction).toBeFocused();

    await page.keyboard.press("Enter");
    const field = panel.getByRole("textbox", { name: "Nome do canal" });
    await expect(field).toBeFocused();
    await page.keyboard.type("Canal Renomeado");
    await page.keyboard.press("Enter");

    await expect(panel.getByTestId("chat-details-channel-name")).toHaveText("Canal Renomeado");
    // O foco volta para a ação de renomear, previsivelmente.
    await expect(editAction).toBeFocused();
    expect(scenario.requests.channelRenames).toHaveLength(1);
  });

  // ── CQ-893-02: rename remoto com o painel aberto ─────────────────────────
  //
  // O painel guarda o próprio display_name de GET /details, então um rename
  // feito por outra pessoa o deixava desatualizado até ser fechado e reaberto.
  // Nada aqui empurra um nome para o cliente: o estado do mock server muda,
  // o frame conversation.updated chega pelo socket real da aplicação, e o
  // painel precisa perceber e reler a própria projeção.
  //
  // Vale para os dois hosts do painel — o do cabeçalho e o aberto pelo menu da
  // linha —, e este exercita os dois de uma vez: o menu da linha é usado para
  // um canal que NÃO é o aberto, que é exatamente o caso que só esse host tem.
  test("o painel aberto pelo menu da linha converge quando outro cliente renomeia", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-remote-rename");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Aberto",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(channel.id, channelDetailsFixture(channel, []));
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    await page.evaluate(() => {
      (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload = true;
    });

    // Detalhes do OUTRO canal, pelo menu da linha: o painel deste host descreve
    // uma conversa que não é a aberta.
    await page
      .getByRole("button", { name: `Mais opções para canal ${OTHER_CHANNEL_NAME}` })
      .click();
    await page.getByRole("menuitem", { name: "Detalhes do canal" }).click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    await expect(panel.getByTestId("chat-details-channel-name")).toHaveText(OTHER_CHANNEL_NAME);

    // O servidor muda, e só então o frame chega — sem payload, como o real.
    setServerChannelName(scenario, OTHER_CHANNEL_ID, "Canal Renomeado Remotamente");
    await emitConversationUpdated(page, { kind: "channel", targetId: OTHER_CHANNEL_ID });

    // A linha converge, como já convergia…
    await expect(page.getByRole("option", { name: /Canal Renomeado Remotamente/ })).toBeVisible();
    // …e agora o painel também, sem ser fechado e reaberto.
    await expect(panel.getByTestId("chat-details-channel-name")).toHaveText(
      "Canal Renomeado Remotamente",
    );
    await expect(panel).toBeVisible();
    await expect
      .poll(() =>
        page.evaluate(
          () => (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload === true,
        ),
      )
      .toBe(true);
  });
});
