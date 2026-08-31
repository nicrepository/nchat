import { expect, test } from "@playwright/test";

import {
  OTHER_USER_ID,
  OTHER_USER_NAME,
  SECOND_CANDIDATE_ID,
  SECOND_CANDIDATE_NAME,
  THIRD_CANDIDATE_ID,
  THIRD_CANDIDATE_NAME,
  createScenario,
  emitConversationAvailable,
  grantConversationAccess,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Objetivo: criar DM 1:1 e grupo ad-hoc de ponta a ponta pelo diálogo "Nova
 * conversa" — busca de pessoas, seleção, submit, navegação para a nova
 * conversa e atualização da sidebar via retry() (ChatSidebar.handleDMOpened /
 * handleChannelCreated).
 */
test.describe("criação de conversas — DM 1:1 e grupo ad-hoc", () => {
  test("cria uma DM 1:1 a partir da busca e a sidebar reflete a nova conversa", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
      dmCandidates: [{ userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME }],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();

    await page.getByRole("button", { name: "Nova conversa" }).click();
    const dialog = page.getByRole("dialog", { name: "Nova conversa" });
    await expect(dialog).toBeVisible();

    await dialog.getByLabel("Pesquisar pessoa").fill(SECOND_CANDIDATE_NAME);
    await dialog.getByRole("button", { name: SECOND_CANDIDATE_NAME }).click();

    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(new RegExp(`/chat/dm/e2e-dm-with-${SECOND_CANDIDATE_ID}$`));

    // Sidebar refetch (retry()) placed the new 1:1 under "Mensagens diretas".
    await expect(
      page
        .getByRole("region", { name: "Mensagens diretas" })
        .getByRole("option", { name: `Mensagem direta com ${SECOND_CANDIDATE_NAME}` }),
    ).toBeVisible();

    expect(scenario.requests.dmCreates).toEqual([{ otherUserId: SECOND_CANDIDATE_ID }]);
  });

  test("cria um grupo ad-hoc com título e a sidebar reflete o novo grupo", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
      dmCandidates: [
        { userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME },
        { userId: THIRD_CANDIDATE_ID, displayName: THIRD_CANDIDATE_NAME },
      ],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();

    await page.getByRole("button", { name: "Nova conversa" }).click();
    const dialog = page.getByRole("dialog", { name: "Nova conversa" });
    await dialog.getByRole("radio", { name: "Grupo" }).check();

    await dialog.getByLabel("Pesquisar pessoa").fill(SECOND_CANDIDATE_NAME);
    await dialog.getByRole("button", { name: SECOND_CANDIDATE_NAME }).click();
    await dialog.getByLabel("Pesquisar pessoa").fill(THIRD_CANDIDATE_NAME);
    await dialog.getByRole("button", { name: THIRD_CANDIDATE_NAME }).click();
    await dialog.getByLabel("Nome do grupo (opcional)").fill("Infraestrutura E2E");

    await dialog.getByRole("button", { name: "Criar grupo" }).click();

    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(/\/chat\/dm\/e2e-group-1$/);
    await expect(
      page
        .getByRole("region", { name: "Grupos" })
        .getByRole("option", { name: "Grupo Infraestrutura E2E" }),
    ).toBeVisible();

    expect(scenario.requests.groupCreates).toEqual([
      {
        participantUserIds: [SECOND_CANDIDATE_ID, THIRD_CANDIDATE_ID],
        title: "Infraestrutura E2E",
      },
    ]);
  });

  test("pessoa indisponível: mostra erro genérico, mantém o diálogo aberto e não cria conversa", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
      // O candidato aparece na busca mas o servidor recusa a criação (404):
      // simula alguém que saiu do workspace entre a busca e o clique.
      dmCandidates: [],
    });
    await installMessagingMocks(page, scenario);
    // A busca é servida separadamente do candidato "oficial": injeta um
    // candidato só na resposta de busca para forçar a divergência.
    await page.route("**/api/chat/dm-candidates**", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: { candidates: [{ user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME }] },
        }),
      }),
    );
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();

    await page.getByRole("button", { name: "Nova conversa" }).click();
    const dialog = page.getByRole("dialog", { name: "Nova conversa" });
    await dialog.getByLabel("Pesquisar pessoa").fill(OTHER_USER_NAME);
    await dialog.getByRole("button", { name: OTHER_USER_NAME }).click();

    await expect(dialog.getByRole("alert")).toHaveText(
      "Esta pessoa não está disponível para mensagens.",
    );
    await expect(dialog).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));
    expect(scenario.requests.dmCreates).toEqual([{ otherUserId: OTHER_USER_ID }]);
  });

  /**
   * Issue #721: o outro lado. Quem *recebe* uma DM inédita não está inscrito
   * numa conversa que não existia, então nenhum evento de sala o alcança — nem
   * o message.created da primeira mensagem. O servidor publica
   * conversation.available para ele, e a sidebar se reconcilia sozinha.
   *
   * Sem polling e sem espera fixa: a asserção é o próprio locator do Playwright,
   * e o marcador de reload prova que a conversa não apareceu por recarga.
   */
  test("destinatário: uma DM inédita aparece na sidebar em tempo real, sem recarregar", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();

    // Sobrevive a qualquer navegação/recarga da aba, então some se a sidebar
    // só se corrigir por reload.
    await page.evaluate(() => {
      (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload = true;
    });

    const incomingId = `${targetId}-incoming`;
    const incomingRow = page
      .getByRole("region", { name: "Mensagens diretas" })
      .getByRole("option", { name: new RegExp(`^Mensagem direta com ${SECOND_CANDIDATE_NAME}`) });
    await expect(incomingRow).toHaveCount(0);

    // O servidor já persistiu a DM e a primeira mensagem: a partir daqui
    // GET /api/chat/sidebar devolve a conversa, e um subscribe a ela é aceito.
    scenario.sidebarDMs.push({
      id: incomingId,
      type: "direct",
      name: SECOND_CANDIDATE_NAME,
      unread_count: 1,
      counterpart: { user_id: SECOND_CANDIDATE_ID, display_name: SECOND_CANDIDATE_NAME },
      last_message_at: "2026-06-01T12:00:00.000Z",
    });
    await grantConversationAccess(page, { kind: "dm", targetId: incomingId });

    await emitConversationAvailable(page, { kind: "dm", targetId: incomingId });

    await expect(incomingRow).toBeVisible();
    expect(
      await page.evaluate(
        () => (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload === true,
      ),
    ).toBe(true);
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));
  });
});
