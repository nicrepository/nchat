import { expect, test } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  SECOND_CANDIDATE_ID,
  SECOND_CANDIDATE_NAME,
  channelDetailsFixture,
  channelRosterFixture,
  createScenario,
  groupDetailsFixture,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Issue #469 — removendo membros pelo painel lateral de detalhes.
 *
 * O que estes testes cobrem e os unitários não: o painel, o roster
 * administrável, a confirmação, o DELETE e o refetch funcionando juntos num
 * navegador real — com a conversa, o compositor e o painel sobrevivendo ao
 * fluxo inteiro.
 *
 * Duas distinções do domínio aparecem aqui de propósito. A lista que um gestor
 * administra num canal é a membership (`GET /channels/{id}/members`), não a
 * prévia de presença; e num grupo quem remove é o criador, não qualquer
 * participante que já pode adicionar.
 */
test.describe("remover membros pelo painel de detalhes", () => {
  /**
   * Um canal com prévia de presença, roster administrável e a capacidade que o
   * servidor informaria — os três independentes, como no serviço real.
   */
  function channelScenario(
    testInfo: Parameters<Parameters<typeof test>[1]>[1],
    label: string,
    options: { canRemove: boolean; type?: "public" | "private"; withRoster?: boolean },
  ) {
    const targetId = uniqueId(testInfo, label);
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Infraestrutura E2E",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no canal" })],
    });
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(
          { ...channel, type: options.type ?? channel.type },
          [
            {
              user_id: CURRENT_USER_ID,
              display_name: CURRENT_USER_NAME,
              role: "moderator",
              presence: "online",
            },
          ],
          3,
          options.canRemove,
          options.canRemove,
        ),
      );
    }
    if (options.withRoster ?? options.canRemove) {
      scenario.channelRosters.set(
        targetId,
        channelRosterFixture([
          { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, role: "moderator" },
          { user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME, role: "member" },
          // Offline e fora da prévia de presença: é exatamente esta pessoa que
          // a prévia esconderia e que o roster torna administrável.
          { user_id: SECOND_CANDIDATE_ID, display_name: SECOND_CANDIDATE_NAME, role: "member" },
        ]),
      );
    }
    return { scenario, targetId };
  }

  function groupScenario(
    testInfo: Parameters<Parameters<typeof test>[1]>[1],
    label: string,
    options: { canRemove: boolean },
  ) {
    const targetId = uniqueId(testInfo, label);
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
          { user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME, presence: "offline" },
        ],
        2,
        // Adicionar é de qualquer participante; remover é só do criador.
        true,
        options.canRemove,
      ),
    );
    return { scenario, targetId };
  }

  test("remove um membro de canal privado e reconcilia lista e contador sem reload", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = channelScenario(testInfo, "remove-private", {
      canRemove: true,
      type: "private",
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const composer = page.getByTestId("chat-composer-input");
    await expect(composer).toBeVisible();

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");
    await expect(panel).toBeVisible();
    await expect(panel).toContainText("3 membros");

    // A seção passa a mostrar a membership: a pessoa offline está nela.
    const target = panel.getByRole("button", {
      name: `Remover ${SECOND_CANDIDATE_NAME} do canal`,
    });
    await expect(target).toBeVisible();
    await expect(target).toHaveAttribute("title", "Remover membro");

    await target.click();
    const dialog = page.getByRole("dialog", { name: "Remover membro?" });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText(SECOND_CANDIDATE_NAME);
    await expect(dialog).toContainText("Infraestrutura E2E");
    // Canal privado: aqui a remoção realmente revoga o acesso.
    await expect(dialog).toContainText("canal privado");

    await dialog.getByRole("button", { name: "Remover membro" }).click();

    await expect(dialog).toBeHidden();
    expect(scenario.removeMemberRequests).toEqual([
      { kind: "channel", targetId, userId: SECOND_CANDIDATE_ID },
    ]);
    // Reconciliação pelo servidor: a linha some e o contador vem do refetch.
    // A ausência é asserida pela ação da linha, e não pelo nome — o anúncio
    // da região viva também contém o nome, de propósito.
    await expect(
      panel.getByRole("button", { name: `Remover ${SECOND_CANDIDATE_NAME} do canal` }),
    ).toHaveCount(0);
    await expect(panel).toContainText("2 membros");
    await expect(panel.getByRole("status")).toContainText(
      `${SECOND_CANDIDATE_NAME} foi removido do canal`,
    );

    // O painel continua aberto, a conversa é a mesma e nada foi remontado.
    await expect(panel).toBeVisible();
    await expect(composer).toBeVisible();
    await expect(page.getByText("Mensagem no canal")).toBeVisible();
  });

  test("em canal público a confirmação não promete revogar a leitura", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = channelScenario(testInfo, "remove-public", {
      canRemove: true,
      type: "public",
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");
    await panel.getByRole("button", { name: `Remover ${OTHER_USER_NAME} do canal` }).click();

    const dialog = page.getByRole("dialog", { name: "Remover membro?" });
    await expect(dialog).toContainText("continua visível");
    await expect(dialog).not.toContainText("perderá o acesso");

    await dialog.getByRole("button", { name: "Remover membro" }).click();
    await expect(dialog).toBeHidden();
    expect(scenario.removeMemberRequests).toEqual([
      { kind: "channel", targetId, userId: OTHER_USER_ID },
    ]);
    // A membership sai; a visibilidade do canal não muda.
    await expect(panel).toContainText("Canal público");
  });

  test("remove um participante de grupo pelo endpoint do grupo", async ({ page }, testInfo) => {
    const { scenario, targetId } = groupScenario(testInfo, "remove-group", { canRemove: true });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");
    await expect(panel).toContainText("2 participantes");

    await panel.getByRole("button", { name: `Remover ${OTHER_USER_NAME} do grupo` }).click();
    const dialog = page.getByRole("dialog", { name: "Remover membro?" });
    await expect(dialog).toContainText("Time de Infra E2E");
    await dialog.getByRole("button", { name: "Remover membro" }).click();

    await expect(dialog).toBeHidden();
    expect(scenario.removeMemberRequests).toEqual([
      { kind: "dm", targetId, userId: OTHER_USER_ID },
    ]);
    await expect(
      panel.getByRole("button", { name: `Remover ${OTHER_USER_NAME} do grupo` }),
    ).toHaveCount(0);
    await expect(panel).toContainText("1 participante");
  });

  test("sem permissão o painel não oferece remoção alguma", async ({ page }, testInfo) => {
    const { scenario, targetId } = channelScenario(testInfo, "remove-forbidden", {
      canRemove: false,
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");
    await expect(panel).toBeVisible();

    await expect(panel.getByTestId("chat-details-participant-remove")).toHaveCount(0);
    expect(scenario.removeMemberRequests).toEqual([]);
  });

  test("num grupo, poder adicionar não é poder remover", async ({ page }, testInfo) => {
    const { scenario, targetId } = groupScenario(testInfo, "remove-group-not-creator", {
      canRemove: false,
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");

    await expect(panel.getByTestId("chat-details-add-members")).toBeVisible();
    await expect(panel.getByTestId("chat-details-participant-remove")).toHaveCount(0);
  });

  test("a própria pessoa não recebe a ação de remoção", async ({ page }, testInfo) => {
    const { scenario, targetId } = channelScenario(testInfo, "remove-self", { canRemove: true });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");

    await expect(
      panel.getByRole("button", { name: `Remover ${CURRENT_USER_NAME} do canal` }),
    ).toHaveCount(0);
    // As outras duas linhas continuam administráveis.
    await expect(panel.getByTestId("chat-details-participant-remove")).toHaveCount(2);
  });

  test("cancelar não remove ninguém e devolve o foco ao botão de origem", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = channelScenario(testInfo, "remove-cancel", { canRemove: true });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");
    const trigger = panel.getByRole("button", { name: `Remover ${OTHER_USER_NAME} do canal` });
    await trigger.click();

    const dialog = page.getByRole("dialog", { name: "Remover membro?" });
    await dialog.getByRole("button", { name: "Cancelar" }).click();

    await expect(dialog).toBeHidden();
    expect(scenario.removeMemberRequests).toEqual([]);
    await expect(trigger).toBeFocused();
    await expect(panel.getByText(OTHER_USER_NAME)).toBeVisible();
  });

  test("o fluxo inteiro funciona pelo teclado", async ({ page }, testInfo) => {
    const { scenario, targetId } = channelScenario(testInfo, "remove-keyboard", {
      canRemove: true,
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");
    const trigger = panel.getByRole("button", { name: `Remover ${OTHER_USER_NAME} do canal` });

    // Alcançável e acionável sem mouse.
    await trigger.focus();
    await expect(trigger).toBeFocused();
    await page.keyboard.press("Enter");

    const dialog = page.getByRole("dialog", { name: "Remover membro?" });
    // O foco entra no diálogo, na ação segura.
    await expect(dialog.getByRole("button", { name: "Cancelar" })).toBeFocused();
    // Escape cancela enquanto nada está em voo, e o foco volta para a origem.
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();
    expect(scenario.removeMemberRequests).toEqual([]);

    // Confirmando pelo teclado: Tab para a ação destrutiva e Enter.
    await page.keyboard.press("Enter");
    await expect(dialog).toBeVisible();
    await page.keyboard.press("Tab");
    await expect(dialog.getByRole("button", { name: "Remover membro" })).toBeFocused();
    await page.keyboard.press("Enter");

    await expect(dialog).toBeHidden();
    expect(scenario.removeMemberRequests).toHaveLength(1);
  });

  test("uma recusa do servidor mantém o diálogo, a lista e permite tentar de novo", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = channelScenario(testInfo, "remove-error", { canRemove: true });
    scenario.removeMemberStatus = 403;
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");
    await panel.getByRole("button", { name: `Remover ${OTHER_USER_NAME} do canal` }).click();

    const dialog = page.getByRole("dialog", { name: "Remover membro?" });
    await dialog.getByRole("button", { name: "Remover membro" }).click();

    await expect(dialog.getByRole("alert")).toHaveText(
      "Você não tem permissão para remover esta pessoa.",
    );
    await expect(dialog).toBeVisible();
    // Nada mudou na conversa nem na lista.
    await expect(panel).toContainText("3 membros");
    await expect(panel.getByText(OTHER_USER_NAME)).toBeVisible();

    // A segunda tentativa, agora aceita, parte do mesmo diálogo.
    scenario.removeMemberStatus = 204;
    await dialog.getByRole("button", { name: "Remover membro" }).click();
    await expect(dialog).toBeHidden();
    expect(scenario.removeMemberRequests).toHaveLength(2);
    await expect(panel).toContainText("2 membros");
  });

  test("dois cliques na confirmação enviam uma única remoção", async ({ page }, testInfo) => {
    const { scenario, targetId } = channelScenario(testInfo, "remove-double", { canRemove: true });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");
    await panel.getByRole("button", { name: `Remover ${OTHER_USER_NAME} do canal` }).click();

    const dialog = page.getByRole("dialog", { name: "Remover membro?" });
    const confirm = dialog.getByRole("button", { name: "Remover membro" });
    await confirm.dblclick();

    await expect(dialog).toBeHidden();
    expect(scenario.removeMemberRequests).toEqual([
      { kind: "channel", targetId, userId: OTHER_USER_ID },
    ]);
  });
});
