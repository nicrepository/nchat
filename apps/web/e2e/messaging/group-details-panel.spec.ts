import { expect, test } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  GROUP_DM_ID,
  GROUP_DM_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  groupDetailsFixture,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Painel de detalhes do grupo (issue #441).
 *
 * O ponto destes testes é que um grupo não é um canal: o painel usa o próprio
 * vocabulário ("Detalhes do grupo", "Participantes"), nunca visibilidade
 * público/privado, e os dados vêm do recurso de conversa.
 */
test.describe("painel de detalhes do grupo", () => {
  test("abre pelo cabeçalho, mostra as seções do grupo, preserva a conversa e devolve o foco", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "grupo");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId,
      targetName: "Time de Infra E2E",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no grupo" })],
    });
    scenario.groupDetails.set(
      targetId,
      groupDetailsFixture(
        { id: targetId, name: "Time de Infra E2E" },
        [
          { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" },
          // Participante offline: num grupo ele continua na lista.
          { user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME, presence: "offline" },
        ],
        // Mais participantes do que a prévia mostra: os dois números são
        // independentes e o painel não pode confundi-los.
        9,
      ),
    );
    // Metadados "Sobre" próprios do grupo (issue #894): descrição e criador
    // diferentes dos do canal, para que a troca de conversa tenha o que trocar.
    scenario.groupDetails.get(targetId)!.description = "O grupo que cuida da malha.";
    scenario.groupDetails.get(targetId)!.creator_display_name = OTHER_USER_NAME;
    // Quando o alvo do cenário já é um grupo, a sidebar padrão traz só ele; o
    // segundo grupo é adicionado para a troca do passo 7.
    scenario.sidebarDMs.push({
      id: GROUP_DM_ID,
      type: "group",
      name: GROUP_DM_NAME,
      unread_count: 0,
    });
    scenario.groupDetails.set(
      GROUP_DM_ID,
      groupDetailsFixture({ id: GROUP_DM_ID, name: GROUP_DM_NAME }, [
        { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" },
      ]),
    );
    scenario.conversationAttachments.set(targetId, [
      {
        id: `${targetId}-file`,
        filename: "ata-da-reuniao.pdf",
        contentType: "application/pdf",
        size: 1_258_291,
        status: "clean",
        createdAt: "2026-07-15T12:24:00Z",
      },
    ]);
    scenario.conversationAttachments.set(GROUP_DM_ID, [
      {
        id: `${GROUP_DM_ID}-file`,
        filename: "checklist.png",
        contentType: "image/png",
        size: 204_800,
        status: "pending_scan",
        createdAt: "2026-07-14T09:00:00Z",
      },
    ]);

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    // ── 1. compositor preenchido e não enviado ───────────────────────────
    const composer = page.getByTestId("chat-composer-input");
    await expect(composer).toBeVisible();
    await composer.click();
    await page.keyboard.insertText("rascunho que precisa sobreviver");
    await expect(composer).toContainText("rascunho que precisa sobreviver");

    const routeBeforeOpening = new URL(page.url()).pathname;
    const toggle = page.getByRole("button", { name: "Detalhes do grupo", exact: true });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");

    // ── 2. abrir o painel ────────────────────────────────────────────────
    await toggle.click();

    const panel = page.getByRole("complementary", { name: "Detalhes do grupo" });
    await expect(panel).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");

    // ── 3. seções do grupo, com dados reais ──────────────────────────────
    await expect(panel.getByRole("heading", { name: "Detalhes do grupo" })).toBeVisible();
    await expect(panel.getByTestId("chat-details-group-name")).toHaveText("Time de Infra E2E");
    await expect(panel.getByRole("heading", { name: "Descrição" })).toBeVisible();
    await expect(panel.getByTestId("chat-details-description")).toHaveText(
      "O grupo que cuida da malha.",
    );
    await expect(panel.getByText(/Criado em 4 de março de 2024/)).toBeVisible();
    await expect(panel.getByText(`Criado por ${OTHER_USER_NAME}`)).toBeVisible();
    await expect(panel.getByTestId("chat-details-people-count")).toContainText("9 participantes");
    // Dois dos nove participantes chegaram na prévia, então a seção diz isso em
    // vez de oferecer "Ver todos" para uma lista que não tem (issue #895).
    await expect(panel.getByTestId("chat-details-roster-shortfall")).toHaveText(
      "2 de 9 participantes carregados.",
    );
    await expect(panel.getByRole("button", { name: /Ver todos Participantes/ })).toHaveCount(0);
    // Nenhum identificador técnico no bloco, nem um pedaço de um.
    await expect(panel.getByText(OTHER_USER_ID)).toHaveCount(0);
    await expect(panel.getByText(OTHER_USER_ID.slice(0, 8))).toHaveCount(0);
    await expect(panel.getByRole("heading", { name: "Participantes (9)" })).toBeVisible();

    const participants = panel.getByRole("list", { name: "Participantes do grupo" });
    await expect(participants.getByText(CURRENT_USER_NAME)).toBeVisible();
    await expect(participants.getByText("Você")).toBeVisible();
    // O participante offline continua visível — presença é informação, não filtro.
    await expect(participants.getByText(OTHER_USER_NAME)).toBeVisible();

    // ── 4. nada de vocabulário de canal ──────────────────────────────────
    await expect(panel.getByText(/Canal público/)).toHaveCount(0);
    await expect(panel.getByText(/Canal privado/)).toHaveCount(0);
    await expect(panel.getByRole("heading", { name: /Membros online/ })).toHaveCount(0);
    // Um grupo tem descrição (issue #894), mas nunca a contagem de um canal.
    await expect(panel.getByText(/membros/)).toHaveCount(0);

    // ── 5. arquivos do grupo ─────────────────────────────────────────────
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("ata-da-reuniao.pdf"),
    ).toBeVisible();
    // Metadado apenas: nada aqui é link para download.
    await expect(panel.getByRole("link")).toHaveCount(0);

    // ── 6. rota inalterada e compositor preservado ───────────────────────
    expect(new URL(page.url()).pathname).toBe(routeBeforeOpening);
    await expect(composer).toContainText("rascunho que precisa sobreviver");
    await composer.click();
    await page.keyboard.insertText(" e continua editável");
    await expect(composer).toContainText("rascunho que precisa sobreviver e continua editável");

    // ── 7. trocar para outro grupo com o painel aberto ───────────────────
    await page.getByRole("option", { name: new RegExp(GROUP_DM_NAME) }).click();
    await expect(panel).toBeVisible();
    await expect(panel.getByTestId("chat-details-group-name")).toHaveText(GROUP_DM_NAME);
    // O outro grupo não tem descrição nem criador resolvido: os dois estados de
    // ausência aparecem, e nenhum metadado do grupo anterior sobrevive.
    await expect(panel.getByTestId("chat-details-description")).toHaveText(
      "Este grupo ainda não tem descrição.",
    );
    await expect(panel.getByText("Criador não identificado")).toBeVisible();
    await expect(panel.getByText(`Criado por ${OTHER_USER_NAME}`)).toHaveCount(0);
    await expect(panel.getByText("O grupo que cuida da malha.")).toHaveCount(0);
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("checklist.png"),
    ).toBeVisible();
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("ata-da-reuniao.pdf"),
    ).toHaveCount(0);
    // Arquivo ainda em análise aparece marcado e nunca como link.
    await expect(panel.getByText("Em análise")).toBeVisible();
    await expect(panel.getByRole("link")).toHaveCount(0);

    // ── 8. fechar pelo botão do painel e validar o retorno do foco ───────
    await panel.getByRole("button", { name: "Fechar detalhes do grupo" }).click();
    await expect(panel).toHaveCount(0);

    const toggleAfterClose = page.getByRole("button", { name: "Detalhes do grupo", exact: true });
    await expect(toggleAfterClose).toHaveAttribute("aria-expanded", "false");
    await expect(toggleAfterClose).toBeFocused();
    expect(new URL(page.url()).pathname).toContain(GROUP_DM_ID);
  });

  /**
   * Roster compacto e expansível (issue #895).
   *
   * O grupo é a superfície que já possui membership autoritativa — o contrato
   * lista todo participante ativo, sem filtro de presença — então é aqui que o
   * roster da #895 existe por inteiro: cinco linhas compactas, expansão pela
   * primitive da #892, e cada participante que não seja o próprio leitor abrindo
   * a DM pelo fluxo idempotente já existente.
   */
  test("mostra cinco participantes, expande, colapsa e abre DM pelo fluxo existente", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "roster");
    // Sete participantes, dos quais apenas um está online: o roster é
    // membership, não presença, e os cinco slots compactos têm de ser
    // preenchidos mesmo assim.
    const others = Array.from({ length: 6 }, (_, index) => ({
      userId: `e2e-roster-${index}`,
      displayName: `Participante ${String.fromCharCode(66 + index)}`,
    }));
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId,
      targetName: "Roster E2E",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem do grupo" })],
      // O destino do clique precisa ser um candidato conhecido para que
      // get-or-create responda — é o mesmo caminho que a menção usa.
      dmCandidates: others.map((person) => ({
        userId: person.userId,
        displayName: person.displayName,
      })),
    });
    scenario.groupDetails.set(
      targetId,
      groupDetailsFixture(
        { id: targetId, name: "Roster E2E" },
        [
          { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" },
          ...others.map((person) => ({
            user_id: person.userId,
            display_name: person.displayName,
            presence: "offline" as const,
          })),
        ],
        7,
      ),
    );

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    await page.getByRole("button", { name: "Detalhes do grupo", exact: true }).click();
    const panel = page.getByRole("complementary", { name: "Detalhes do grupo" });
    await expect(panel).toBeVisible();

    // ── 1. compacto: título com o total e exatamente cinco linhas ────────
    await expect(panel.getByRole("heading", { name: "Participantes (7)" })).toBeVisible();
    const roster = panel.getByRole("list", { name: "Participantes do grupo" });
    await expect(roster.getByRole("listitem")).toHaveCount(5);
    // Nenhum identificador técnico, nem inteiro nem em pedaço.
    await expect(panel.getByText(others[0].userId)).toHaveCount(0);

    // ── 2. expandir e colapsar pela primitive da #892 ────────────────────
    await panel.getByRole("button", { name: /^Ver todos Participantes/ }).click();
    await expect(roster.getByRole("listitem")).toHaveCount(7);
    // A timeline continua onde estava: o painel é irmão da conversa.
    await expect(page.getByText("Mensagem do grupo")).toBeVisible();

    await panel.getByRole("button", { name: /^Mostrar menos Participantes/ }).click();
    await expect(roster.getByRole("listitem")).toHaveCount(5);

    // ── 3. a própria linha não oferece conversa consigo mesmo ────────────
    await expect(roster.getByText("Você")).toBeVisible();
    await expect(
      panel.getByRole("button", { name: new RegExp(`Abrir conversa com ${CURRENT_USER_NAME}`) }),
    ).toHaveCount(0);

    // ── 4. teclado alcança e ativa a linha de outro participante ─────────
    const target = others[0];
    const action = panel.getByRole("button", {
      name: new RegExp(`^Abrir conversa com ${target.displayName}\\.`),
    });
    await expect(action).toBeVisible();
    await action.focus();
    await expect(action).toBeFocused();

    const created = page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/chat/dms") && response.request().method() === "POST",
    );
    await page.keyboard.press("Enter");
    await created;

    // ── 5. navegou para a conversa que o servidor devolveu ───────────────
    // Nunca para o userId: a rota usa o conversation_id da resposta.
    await expect(page).toHaveURL(new RegExp(`/chat/dm/e2e-dm-with-${target.userId}$`));
    expect(new URL(page.url()).pathname).not.toContain(`/chat/dm/${target.userId}`);
    await expect(page.getByTestId("chat-msg-header")).toContainText(target.displayName);
    expect(scenario.requests.dmCreates).toEqual([{ otherUserId: target.userId }]);
  });

  /**
   * Dedupe em voo (issue #895).
   *
   * Ativar o mesmo participante de novo *enquanto a primeira requisição ainda
   * está pendente* não pode produzir uma segunda: o fluxo compartilhado recusa
   * um destinatário que já está sendo resolvido. Provar isso exige segurar a
   * resposta — um cenário sequencial (abre, navega, volta, abre) demonstraria
   * só a idempotência do endpoint, que é do servidor e não deste código.
   */
  test("ativar o mesmo participante com a requisição em voo não cria uma segunda", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "roster-dedupe");
    const other = { userId: "e2e-roster-dedupe", displayName: "Participante Único" };
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId,
      targetName: "Roster Dedupe",
      messages: [makeMessage({ id: `${targetId}-m1` })],
      dmCandidates: [other],
    });
    scenario.groupDetails.set(
      targetId,
      groupDetailsFixture({ id: targetId, name: "Roster Dedupe" }, [
        { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" },
        { user_id: other.userId, display_name: other.displayName, presence: "offline" },
      ]),
    );

    await installMessagingMocks(page, scenario);

    // A interceptação existe antes da primeira ativação, então nenhuma
    // requisição pode escapar da contagem — e a primeira fica retida até que
    // este teste a libere, sem sleep nenhum.
    let requestCount = 0;
    let releaseFirst!: () => void;
    const firstHeld = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });
    await page.route("**/api/chat/dms", async (route) => {
      if (route.request().method() !== "POST") {
        await route.fallback();
        return;
      }
      requestCount += 1;
      // Só a primeira é retida; uma segunda seria respondida na hora e o
      // contador já teria denunciado o defeito.
      if (requestCount === 1) await firstHeld;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: { conversation_id: `e2e-dm-with-${other.userId}`, created: true },
        }),
      });
    });

    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    await page.getByRole("button", { name: "Detalhes do grupo", exact: true }).click();
    const panel = page.getByRole("complementary", { name: "Detalhes do grupo" });
    const action = panel.getByRole("button", {
      name: new RegExp(`^Abrir conversa com ${other.displayName}\\.`),
    });

    await action.click();
    // A linha anuncia que está resolvendo: é o sinal observável de que a
    // primeira requisição está em voo, e o que este teste espera em vez de um
    // intervalo arbitrário.
    await expect(action).toHaveAttribute("aria-busy", "true");
    expect(requestCount).toBe(1);

    // Segunda e terceira ativações, com a primeira ainda pendente.
    await action.click();
    await action.click();
    await expect(action).toHaveAttribute("aria-busy", "true");
    expect(requestCount).toBe(1);

    releaseFirst();

    // Navegou para a conversa que o servidor devolveu — nunca para o userId.
    const conversationId = `e2e-dm-with-${other.userId}`;
    expect(conversationId).not.toBe(other.userId);
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${conversationId}$`));
    expect(new URL(page.url()).pathname).toContain(conversationId);
    expect(new URL(page.url()).pathname).not.toBe(`/chat/dm/${other.userId}`);
    // E uma única requisição do começo ao fim: as ativações extras não deixaram
    // nada em voo para chegar depois.
    expect(requestCount).toBe(1);
  });

  /**
   * O roster expandido é um container de rolagem (#892), e `overflow-y: auto`
   * torna o `overflow-x` computado `auto` também — foi assim que uma rolagem
   * horizontal já chegou à conversa nesta aplicação (issue #467). Com nomes
   * longos, numa viewport estreita, nem a lista nem a página podem rolar de
   * lado, e a linha continua acionável.
   */
  test("nomes longos não fazem o roster nem a página rolarem de lado em viewport estreita", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "roster-estreito");
    const others = Array.from({ length: 6 }, (_, index) => ({
      userId: `e2e-roster-longo-${index}`,
      displayName: `Participante ${index + 1} de Infraestrutura, Redes e Resposta a Incidentes`,
    }));
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId,
      targetName: "Roster Estreito",
      messages: [makeMessage({ id: `${targetId}-m1` })],
      dmCandidates: others,
    });
    scenario.groupDetails.set(
      targetId,
      groupDetailsFixture(
        { id: targetId, name: "Roster Estreito" },
        [
          { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" },
          ...others.map((person) => ({
            user_id: person.userId,
            display_name: person.displayName,
            presence: "offline" as const,
          })),
        ],
        7,
      ),
    );

    await page.setViewportSize({ width: 390, height: 844 });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    await page.getByRole("button", { name: "Detalhes do grupo", exact: true }).click();
    const panel = page.getByRole("complementary", { name: "Detalhes do grupo" });
    await expect(panel).toBeVisible();

    const roster = panel.getByRole("list", { name: "Participantes do grupo" });
    await expect(roster.getByRole("listitem")).toHaveCount(5);
    await panel.getByRole("button", { name: /^Ver todos Participantes/ }).click();
    await expect(roster.getByRole("listitem")).toHaveCount(7);

    // A lista expandida rola na vertical e nunca na horizontal, e a página
    // também não.
    const overflow = await page.evaluate(() => {
      const list = document.querySelector(".chat-details__collection--expanded");
      const root = document.documentElement;
      return {
        list: list ? list.scrollWidth - list.clientWidth : -1,
        page: Math.max(
          root.scrollWidth - root.clientWidth,
          document.body.scrollWidth - root.clientWidth,
        ),
      };
    });
    expect(overflow.list).toBe(0);
    expect(overflow.page).toBeLessThanOrEqual(1);

    // E a linha continua sendo um alvo acionável de tamanho utilizável.
    const action = panel.getByRole("button", {
      name: /^Abrir conversa com Participante 1 de Infraestrutura/,
    });
    const box = await action.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.height).toBeGreaterThanOrEqual(44);
  });

  test("não empresta o painel de grupo para uma DM 1:1", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-direta");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "direct",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    // Uma DM 1:1 tem o próprio painel — "Perfil" (issue #443) — e nunca o
    // vocabulário de grupo ou de canal.
    await expect(page.getByRole("button", { name: "Detalhes do grupo", exact: true })).toHaveCount(
      0,
    );
    await expect(page.getByRole("button", { name: "Detalhes do canal", exact: true })).toHaveCount(
      0,
    );
    await expect(
      page.getByRole("button", { name: `Abrir perfil de ${OTHER_USER_NAME}`, exact: true }),
    ).toBeVisible();
  });

  test("mostra estados vazios com o vocabulário do grupo", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "grupo-vazio");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId,
      targetName: "Grupo Vazio",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    scenario.groupDetails.set(
      targetId,
      groupDetailsFixture({ id: targetId, name: "Grupo Vazio" }, [], 0),
    );

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await page.getByRole("button", { name: "Detalhes do grupo", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Detalhes do grupo" });
    await expect(panel.getByText("Nenhuma mensagem fixada neste grupo.")).toBeVisible();
    await expect(panel.getByText("Nenhum arquivo enviado neste grupo.")).toBeVisible();
    await expect(panel.getByText("Nenhum participante para exibir.")).toBeVisible();
    // Nada a expandir em lugar nenhum: sem participantes, sem arquivos e sem
    // pin, nenhuma seção oferece controle (issue #892).
    await expect(panel.getByRole("button", { name: /Ver todos/ })).toHaveCount(0);
    await expect(panel.getByText(/ainda não está disponível nesta versão/)).toHaveCount(0);

    // "Adicionar participantes" deixou de ser um placeholder (issue #398): virou
    // fluxo real, e esta fixture não concede a permissão, então a ação fica
    // ausente — o padrão seguro. O fluxo em si é coberto por add-members.spec.ts.
    await expect(panel.getByTestId("chat-details-add-members")).toHaveCount(0);
  });

  // ── ISSUE #893: renomeação inline do grupo no painel ────────────────────
  //
  // A issue cobre canais E grupos, e um grupo não é um canal: endpoint
  // próprio, autorização própria (participação) e vocabulário próprio. O que
  // se prova aqui é a convergência real das três superfícies sem reload.
  test("renomeia o grupo pelo painel e converge painel, cabeçalho e sidebar sem recarregar", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "grupo-rename-inline");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId,
      targetName: "Time de Infra E2E",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no grupo" })],
    });
    scenario.groupDetails.set(
      targetId,
      groupDetailsFixture({ id: targetId, name: "Time de Infra E2E" }, [
        { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" },
        { user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME, presence: "offline" },
      ]),
    );

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    await page.evaluate(() => {
      (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload = true;
    });

    await page.getByRole("button", { name: "Detalhes do grupo", exact: true }).click();
    const panel = page.getByRole("complementary", { name: "Detalhes do grupo" });
    await expect(panel.getByTestId("chat-details-group-name")).toHaveText("Time de Infra E2E");

    await panel.getByRole("button", { name: "Renomear grupo" }).click();
    const field = panel.getByRole("textbox", { name: "Nome do grupo" });
    await expect(field).toBeFocused();
    await expect(page.getByRole("dialog")).toHaveCount(0);

    await field.fill("  Squad Plataforma  ");
    await panel.getByRole("button", { name: "Salvar novo nome do grupo" }).click();

    await expect(panel.getByTestId("chat-details-group-name")).toHaveText("Squad Plataforma");
    await expect(panel.getByRole("textbox", { name: "Nome do grupo" })).toHaveCount(0);
    await expect(page.getByTestId("chat-msg-header")).toContainText("Squad Plataforma");
    await expect(page.getByRole("option", { name: /Squad Plataforma/ })).toBeVisible();

    expect(scenario.requests.groupRenames).toEqual([
      { conversationId: targetId, title: "Squad Plataforma" },
    ]);
    await expect
      .poll(() =>
        page.evaluate(
          () => (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload === true,
        ),
      )
      .toBe(true);

    // Persistido: um reload real reencontra o nome novo.
    await page.reload();
    await expect(page.getByTestId("chat-msg-header")).toContainText("Squad Plataforma");
  });

  // Uma conversa 1:1 não tem nome próprio — o título é o do interlocutor,
  // resolvido por leitor —, então o perfil não ganha renomeação.
  test("o perfil de uma conversa 1:1 não oferece renomeação", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-sem-rename");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await page.getByRole("button", { name: `Abrir perfil de ${OTHER_USER_NAME}` }).click();
    const panel = page.getByRole("complementary", { name: "Perfil" });
    await expect(panel).toBeVisible();
    await expect(panel.getByRole("button", { name: /Renomear/ })).toHaveCount(0);
  });
});
