import { expect, test } from "@playwright/test";

import {
  GROUP_DM_ID,
  createScenario,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Objetivo: comportamento estilo Discord ao abrir o chat sem seleção
 * explícita — priorizar a última conversa não lida e, na ausência de
 * qualquer não lida, cair para a última conversa lida (ambas pelo mesmo
 * critério de atividade já usado em toda a sidebar). Cobre também que a
 * seleção manual do usuário nunca é sobrescrita pela navegação automática.
 *
 * `createScenario({ kind: "dm", ... })` já semeia três linhas de sidebar por
 * padrão — um canal (OTHER_CHANNEL_ID), a DM direta alvo do cenário e o
 * grupo padrão (GROUP_DM_ID) — o suficiente para variar `unread_count`/
 * `last_message_at` entre as três sem precisar de fixtures extras.
 */
test.describe("navegação padrão — última não lida, senão última lida", () => {
  test("ao abrir /chat, seleciona a conversa não lida mais recentemente ativa", async ({
    page,
  }, testInfo) => {
    const directTargetId = uniqueId(testInfo, "unread-direct");
    const scenario = createScenario({
      kind: "dm",
      targetId: directTargetId,
      targetName: "Direta Alvo",
      messages: Array.from({ length: 20 }, (_, i) =>
        makeMessage({ id: `${directTargetId}-m${i}`, body_text: `Histórico ${i}` }),
      ),
    });
    // Canal: não lido, mas o mais antigo dos três.
    scenario.sidebarChannels[0].unread_count = 1;
    scenario.sidebarChannels[0].last_message_at = "2026-08-04T10:00:00.000Z";
    // Grupo padrão: não lido, intermediário.
    const groupDM = scenario.sidebarDMs.find((dm) => dm.id === GROUP_DM_ID)!;
    groupDM.unread_count = 2;
    groupDM.last_message_at = "2026-08-04T11:00:00.000Z";
    // DM direta alvo: não lida e a mais recente — deve vencer.
    const directDM = scenario.sidebarDMs.find((dm) => dm.id === directTargetId)!;
    directDM.unread_count = 3;
    directDM.last_message_at = "2026-08-04T12:00:00.000Z";

    await installMessagingMocks(page, scenario);
    await page.goto("/chat");

    await expect(page).toHaveURL(`/chat/dm/${directTargetId}`);

    // A navegação automática cai no mesmo mecanismo de foco/scroll de
    // qualquer outra navegação (issue do composer/scroll) — sem novo código.
    const input = page.getByTestId("chat-composer-input");
    await expect(input).toBeFocused();
    const list = page.locator(".chat-msg-area__list");
    await expect
      .poll(async () =>
        list.evaluate((el) => Math.abs(el.scrollHeight - el.clientHeight - el.scrollTop)),
      )
      .toBeLessThanOrEqual(1);
  });

  test("sem nenhuma não lida, seleciona a conversa lida mais recentemente ativa", async ({
    page,
  }, testInfo) => {
    const directTargetId = uniqueId(testInfo, "read-direct");
    const scenario = createScenario({
      kind: "dm",
      targetId: directTargetId,
      targetName: "Direta Lida",
      messages: [makeMessage({ id: `${directTargetId}-m0`, body_text: "última mensagem" })],
    });
    scenario.sidebarChannels[0].unread_count = 0;
    scenario.sidebarChannels[0].last_message_at = "2026-08-04T09:00:00.000Z";
    const groupDM = scenario.sidebarDMs.find((dm) => dm.id === GROUP_DM_ID)!;
    groupDM.unread_count = 0;
    groupDM.last_message_at = "2026-08-04T13:00:00.000Z"; // o mais recente dos três
    const directDM = scenario.sidebarDMs.find((dm) => dm.id === directTargetId)!;
    directDM.unread_count = 0;
    directDM.last_message_at = "2026-08-04T11:00:00.000Z";

    await installMessagingMocks(page, scenario);
    await page.goto("/chat");

    await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
  });

  test("a seleção manual do usuário nunca é sobrescrita pela navegação automática", async ({
    page,
  }, testInfo) => {
    const directTargetId = uniqueId(testInfo, "manual-pick");
    const scenario = createScenario({
      kind: "dm",
      targetId: directTargetId,
      targetName: "Escolhida Manualmente",
      messages: [makeMessage({ id: `${directTargetId}-m0`, body_text: "conversa escolhida" })],
    });
    // O grupo é o mais recentemente ativo e não lido — venceria a seleção
    // automática se o usuário não tivesse navegado explicitamente.
    const groupDM = scenario.sidebarDMs.find((dm) => dm.id === GROUP_DM_ID)!;
    groupDM.unread_count = 5;
    groupDM.last_message_at = "2026-08-04T23:00:00.000Z";

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${directTargetId}`);

    await expect(page).toHaveURL(`/chat/dm/${directTargetId}`);
    await expect(page.getByText("conversa escolhida")).toBeVisible();
  });

  test("separa visualmente não lidas e lidas na sidebar", async ({ page }, testInfo) => {
    // Abre um canal como conversa ativa — não uma DM — para que marcar uma DM
    // como não lida no fixture não seja imediatamente zerada pela regra
    // existente de "abrir marca como lida", que só se aplica à conversa
    // efetivamente aberta.
    const channelTargetId = uniqueId(testInfo, "grouping-channel");
    const unreadDMId = uniqueId(testInfo, "grouping-unread-dm");
    const scenario = createScenario({
      kind: "channel",
      targetId: channelTargetId,
      targetName: "Canal Ativo",
      messages: [],
    });
    // A DM direta padrão do fixture fica lida; uma segunda DM direta,
    // adicionada aqui, fica não lida — as duas caem na mesma seção
    // "Mensagens diretas", garantindo a mistura que o agrupamento cobre.
    scenario.sidebarDMs.push({
      id: unreadDMId,
      type: "direct",
      name: "Direta Não Lida",
      unread_count: 1,
      last_message_at: "2026-08-04T12:00:00.000Z",
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${channelTargetId}`);

    const dmSection = page.getByRole("region", { name: "Mensagens diretas" });
    await expect(dmSection.getByRole("heading", { level: 3, name: "Não lidas" })).toBeVisible();
    await expect(
      dmSection.getByRole("heading", { level: 3, name: "Lidas", exact: true }),
    ).toBeVisible();
  });
});
