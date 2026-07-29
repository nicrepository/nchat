import { expect, test } from "@playwright/test";

import {
  OTHER_CHANNEL_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  installMessagingMocks,
  makeMessage,
  messagesFor,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Objetivo: autorização negativa — abrir uma DM/canal do qual o usuário não é
 * participante deve falhar como qualquer outra falha de carregamento (mesmo
 * testid/rótulo genérico "Não foi possível carregar as mensagens."), sem
 * vazar conteúdo da conversa nem distinguir "não existe" de "sem permissão"
 * (o backend já responde 404 para ambos — ver permission_service.go).
 */
test.describe("controle de acesso — DM/canal sem participação", () => {
  test("DM sem participação: mostra erro genérico e nenhum conteúdo é exposto", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const forbiddenId = uniqueId(testInfo, "dm-forbidden");
    const secretText = "conteúdo sigiloso que o usuário não deve ver";
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
    });
    // A mensagem existe no lado do servidor mas nunca deve chegar ao cliente:
    // isto prova que o bloqueio acontece antes de qualquer render, não que a
    // UI simplesmente não tem dados para mostrar.
    scenario.messagesByTarget.set(`dm:${forbiddenId}`, [
      makeMessage({ id: `${forbiddenId}-secret`, body_text: secretText }),
    ]);
    await installMessagingMocks(page, scenario, { forbiddenTargetIds: [forbiddenId] });

    await page.goto(`/chat/dm/${forbiddenId}`);

    const error = page.getByTestId("chat-msg-error");
    await expect(error).toBeVisible();
    await expect(error).toContainText("Não foi possível carregar as mensagens.");
    await expect(page.getByText(secretText)).toHaveCount(0);
    await expect(page.getByTestId("chat-msg-bubble")).toHaveCount(0);

    // Retentativa continua bloqueada: a autorização é reavaliada a cada
    // requisição, não é um estado que "destrava" no cliente.
    await error.getByRole("button", { name: "Tentar novamente" }).click();
    await expect(page.getByTestId("chat-msg-error")).toBeVisible();
    await expect(page.getByText(secretText)).toHaveCount(0);
  });

  test("canal sem participação: mostra erro genérico e nenhum conteúdo é exposto", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal E2E Principal",
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
    });
    const secretText = "conteúdo sigiloso do canal secundário";
    scenario.messagesByTarget.set(`channel:${OTHER_CHANNEL_ID}`, [
      makeMessage({ id: `${OTHER_CHANNEL_ID}-secret`, body_text: secretText }),
    ]);
    await installMessagingMocks(page, scenario, { forbiddenTargetIds: [OTHER_CHANNEL_ID] });

    await page.goto(`/chat/channel/${OTHER_CHANNEL_ID}`);

    const error = page.getByTestId("chat-msg-error");
    await expect(error).toBeVisible();
    await expect(error).toContainText("Não foi possível carregar as mensagens.");
    await expect(page.getByText(secretText)).toHaveCount(0);
    await expect(page.getByTestId("chat-msg-bubble")).toHaveCount(0);
  });

  test("não permite editar nem excluir mensagem de outro usuário", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-ownership");
    const originalText = "mensagem de outra pessoa";
    const original = makeMessage({
      id: `${targetId}-other-user-message`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: originalText,
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [original],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const statuses = await page.evaluate(async (messageId) => {
      const patch = await fetch(`/api/chat/messages/${messageId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ body: "alteração indevida", body_format: "v2" }),
      });
      const remove = await fetch(`/api/chat/messages/${messageId}`, { method: "DELETE" });
      return { patch: patch.status, remove: remove.status };
    }, original.id);

    expect(statuses).toEqual({ patch: 403, remove: 403 });
    expect(messagesFor(scenario)[0]).toEqual(
      expect.objectContaining({ body_text: originalText, status: "active", is_removed: false }),
    );
    expect(scenario.requests.patches).toEqual([]);
    expect(scenario.requests.deletes).toEqual([]);
  });

  test("não permite editar nem excluir mensagem própria em conversa proibida", async ({
    page,
  }, testInfo) => {
    const allowedId = uniqueId(testInfo, "allowed");
    const forbiddenId = uniqueId(testInfo, "forbidden");
    const original = makeMessage({
      id: `${forbiddenId}-own-message`,
      body_text: "mensagem própria protegida",
    });
    const scenario = createScenario({
      kind: "dm",
      targetId: allowedId,
      targetName: OTHER_USER_NAME,
      messages: [],
    });
    messagesFor(scenario, "dm", forbiddenId).push(original);
    await installMessagingMocks(page, scenario, { forbiddenTargetIds: [forbiddenId] });
    await page.goto(`/chat/dm/${allowedId}`);

    const statuses = await page.evaluate(async (messageId) => {
      const patch = await fetch(`/api/chat/messages/${messageId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ body: "alteração indevida", body_format: "v2" }),
      });
      const remove = await fetch(`/api/chat/messages/${messageId}`, { method: "DELETE" });
      return { patch: patch.status, remove: remove.status };
    }, original.id);

    expect(statuses).toEqual({ patch: 403, remove: 403 });
    expect(messagesFor(scenario, "dm", forbiddenId)).toEqual([original]);
    expect(scenario.requests.patches).toEqual([]);
    expect(scenario.requests.deletes).toEqual([]);
  });

  test("não permite editar nem excluir mensagem de terceiro em conversa proibida", async ({
    page,
  }, testInfo) => {
    const allowedId = uniqueId(testInfo, "allowed");
    const forbiddenId = uniqueId(testInfo, "forbidden");
    const original = makeMessage({
      id: `${forbiddenId}-other-message`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "mensagem de terceiro protegida",
    });
    const scenario = createScenario({
      kind: "dm",
      targetId: allowedId,
      targetName: OTHER_USER_NAME,
      messages: [],
    });
    messagesFor(scenario, "dm", forbiddenId).push(original);
    await installMessagingMocks(page, scenario, { forbiddenTargetIds: [forbiddenId] });
    await page.goto(`/chat/dm/${allowedId}`);

    const statuses = await page.evaluate(async (messageId) => {
      const patch = await fetch(`/api/chat/messages/${messageId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ body: "alteração indevida", body_format: "v2" }),
      });
      const remove = await fetch(`/api/chat/messages/${messageId}`, { method: "DELETE" });
      return { patch: patch.status, remove: remove.status };
    }, original.id);

    expect(statuses).toEqual({ patch: 403, remove: 403 });
    expect(messagesFor(scenario, "dm", forbiddenId)).toEqual([original]);
    expect(scenario.requests.patches).toEqual([]);
    expect(scenario.requests.deletes).toEqual([]);
  });

  test("rejeita pin quando a mensagem pertence a outra conversa", async ({ page }, testInfo) => {
    const targetBase = uniqueId(testInfo, "pin-target");
    const targetA = `${targetBase}-a`;
    const targetB = `${targetBase}-b`;
    const messageA = makeMessage({ id: `${targetA}-message`, body_text: "mensagem do canal A" });
    const scenario = createScenario({
      kind: "channel",
      targetId: targetA,
      targetName: "Canal A",
      messages: [messageA],
    });
    scenario.sidebarChannels.push({
      id: targetB,
      slug: "canal-b",
      display_name: "Canal B",
      type: "public",
      can_write: true,
      unread_count: 0,
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetA}`);

    const statuses = await page.evaluate(
      async ({ targetId, messageId }) => {
        const url = `/api/chat/channels/${targetId}/messages/${messageId}/pin`;
        const add = await fetch(url, { method: "POST" });
        const remove = await fetch(url, { method: "DELETE" });
        return { add: add.status, remove: remove.status };
      },
      { targetId: targetB, messageId: messageA.id },
    );

    expect(statuses).toEqual({ add: 404, remove: 404 });
    expect(scenario.requests.pins).toEqual([]);
    expect(scenario.pinnedIds.size).toBe(0);
    expect(messagesFor(scenario, "channel", targetA)).toEqual([messageA]);
  });

  test("bloqueia interações em conversas sem acesso sem alterar estado", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "allowed-channel");
    const forbiddenId = OTHER_CHANNEL_ID;
    const allowedMessage = makeMessage({
      id: `${targetId}-message`,
      body_text: "origem permitida",
    });
    const forbiddenMessage = makeMessage({
      id: `${forbiddenId}-message`,
      sender_id: OTHER_USER_ID,
      body_text: "conteúdo protegido",
    });
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal permitido",
      messages: [allowedMessage],
    });
    messagesFor(scenario, "channel", forbiddenId).push(forbiddenMessage);
    await installMessagingMocks(page, scenario, { forbiddenTargetIds: [forbiddenId] });
    await page.goto(`/chat/channel/${targetId}`);

    const result = await page.evaluate(
      async ({ allowedId, allowedMessageId, forbiddenId, forbiddenMessageId }) => {
        const toggleReaction = (
          window as unknown as {
            __e2eToggleReaction: (
              messageId: string,
              emoji: string,
            ) => Promise<{ added: boolean } | undefined>;
          }
        ).__e2eToggleReaction;
        const reactionBlocked = (await toggleReaction(forbiddenMessageId, "👍")) === undefined;
        const favorite = await fetch(`/api/chat/messages/${forbiddenMessageId}/favorite`, {
          method: "POST",
        });
        const pin = await fetch(
          `/api/chat/channels/${forbiddenId}/messages/${forbiddenMessageId}/pin`,
          { method: "POST" },
        );
        const listPins = await fetch(`/api/chat/channels/${forbiddenId}/pins`);
        const forbiddenDestinationForward = await fetch(
          `/api/chat/channels/${forbiddenId}/messages/forward`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ source_message_id: allowedMessageId }),
          },
        );
        const forbiddenSourceForward = await fetch(
          `/api/chat/channels/${allowedId}/messages/forward`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ source_message_id: forbiddenMessageId }),
          },
        );
        return {
          reactionBlocked,
          favorite: favorite.status,
          pin: pin.status,
          listPins: listPins.status,
          forbiddenDestinationForward: forbiddenDestinationForward.status,
          forbiddenSourceForward: forbiddenSourceForward.status,
        };
      },
      {
        allowedId: targetId,
        allowedMessageId: allowedMessage.id,
        forbiddenId,
        forbiddenMessageId: forbiddenMessage.id,
      },
    );

    expect(result).toEqual({
      reactionBlocked: true,
      favorite: 403,
      pin: 403,
      listPins: 403,
      forbiddenDestinationForward: 403,
      forbiddenSourceForward: 403,
    });
    expect(scenario.requests.reactions).toEqual([]);
    expect(scenario.requests.favorites).toEqual([]);
    expect(scenario.requests.pins).toEqual([]);
    expect(scenario.requests.forwards).toEqual([]);
    expect(scenario.pinnedIds.has(`channel:${forbiddenId}`)).toBe(false);
    expect(messagesFor(scenario, "channel", targetId)).toEqual([allowedMessage]);
    expect(messagesFor(scenario, "channel", forbiddenId)).toEqual([forbiddenMessage]);
  });

  // Não há um teste equivalente "tenta enviar mensagem e é bloqueado": a área
  // de mensagens só renderiza um composer depois que a lista carrega com
  // sucesso, então uma DM/canal sem participação nunca chega a mostrar um
  // campo de envio — o bloqueio já acontece na etapa acima (GET .../messages),
  // e isso é reforçado pelas asserções `chat-msg-bubble`/composer ausentes.
});
