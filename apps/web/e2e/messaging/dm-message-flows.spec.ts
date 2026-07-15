import { expect, test } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  messagesFor,
  replaceEditorText,
  revealActions,
  uniqueId,
} from "../helpers/messagingApi";

test.describe("mensagens diretas 1:1", () => {
  test("responde uma mensagem com quote inline na conversa correta", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const originalText = `${uniqueId(testInfo, "original")} texto citado DM`;
    const replyText = "resposta dm quote";
    const original = makeMessage({
      id: `${targetId}-original`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: originalText,
      body_format: "v2",
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [original],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const originalBubble = messageBubble(page, original.id);
    await expect(originalBubble).toContainText(originalText);
    await revealActions(page, original.id);
    await originalBubble.getByRole("button", { name: "Responder" }).click();

    const composerQuote = page.getByTestId("chat-composer-quote");
    await expect(composerQuote).toContainText(OTHER_USER_NAME);
    await expect(composerQuote).toContainText(originalText);

    await fillComposer(page, replyText);
    const postResponse = page.waitForResponse(
      (response) =>
        response.url().includes(`/api/chat/dm/${targetId}/messages`) &&
        response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Enviar mensagem" }).click();
    await postResponse;

    expect(scenario.requests.dmPosts).toEqual([
      expect.objectContaining({ body_text: replyText, parent_message_id: original.id }),
    ]);
    expect(scenario.requests.channelPosts).toEqual([]);

    const reply = messagesFor(scenario).find((message) => message.body_text === replyText);
    expect(reply).toBeDefined();
    const replyBubble = messageBubble(page, reply!.id);
    await expect(replyBubble).toContainText(replyText);
    await expect(replyBubble.getByTestId("chat-message-quote")).toContainText(OTHER_USER_NAME);
    await expect(replyBubble.getByTestId("chat-message-quote")).toContainText(originalText);

    const channelLoad = page.waitForResponse(
      (response) =>
        response.url().includes("/api/chat/channels/e2e-channel-other/messages") &&
        response.request().method() === "GET",
    );
    await page.goto("/chat/channel/e2e-channel-other");
    const channelResponse = await channelLoad;
    expect(channelResponse.status()).toBe(200);
    await expect(page).toHaveURL(/\/chat\/channel\/e2e-channel-other$/);
    await expect(page.getByTestId("chat-msg-empty")).toBeVisible();
    await expect(page.getByRole("log", { name: "Mensagens" })).toHaveCount(0);
    await expect(messageBubble(page, reply!.id)).toHaveCount(0);
    await expect(page.getByText(replyText)).toHaveCount(0);

    await page.goto(`/chat/dm/${targetId}`);
    await expect(messageBubble(page, reply!.id)).toContainText(replyText);
    await page.reload();
    await expect(messageBubble(page, reply!.id).getByTestId("chat-message-quote")).toContainText(
      originalText,
    );
  });

  test("edita uma mensagem dentro da janela e mantém a alteração após reload", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const originalText = `${uniqueId(testInfo, "original")} texto original DM`;
    const editedText = "texto editado dm";
    const original = makeMessage({
      id: `${targetId}-edit`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: originalText,
      body_format: "v2",
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [original],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const bubble = await revealActions(page, original.id);
    await bubble.getByRole("button", { name: "Editar mensagem" }).click();
    await replaceEditorText(page, page.getByTestId(`chat-edit-input-${original.id}`), editedText);

    const patchResponse = page.waitForResponse(
      (response) =>
        response.url().includes(`/api/chat/messages/${original.id}`) &&
        response.request().method() === "PATCH",
    );
    await page.getByRole("button", { name: "Salvar" }).click();
    const response = await patchResponse;
    expect(response.status()).toBe(200);
    expect(response.request().method()).toBe("PATCH");
    expect(new URL(response.request().url()).pathname).toBe(`/api/chat/messages/${original.id}`);

    expect(scenario.requests.patches).toHaveLength(1);
    expect(scenario.requests.patches[0]).toEqual(
      expect.objectContaining({
        messageId: original.id,
        method: "PATCH",
        endpoint: `/api/chat/messages/${original.id}`,
        body: editedText,
        body_format: "v2",
      }),
    );
    expect(scenario.requests.patches[0].raw).toEqual(
      expect.objectContaining({ body: editedText, body_format: "v2" }),
    );
    expect(scenario.requests.patches[0].raw).not.toHaveProperty("body_text");
    expect(scenario.requests.patches[0].raw).not.toHaveProperty("text");

    await expect(messageBubble(page, original.id)).toContainText(editedText);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Ver histórico de edições" }),
    ).toBeVisible();

    await page.reload();
    await expect(messageBubble(page, original.id)).toContainText(editedText);
  });

  test("impede edição fora da janela na DM e mantém conteúdo original", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const originalText = `${uniqueId(testInfo, "original")} texto bloqueado DM`;
    const editedText = "texto bloqueado dm";
    const original = makeMessage({
      id: `${targetId}-expired`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: originalText,
      body_format: "v2",
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [original],
    });

    await installMessagingMocks(page, scenario, { editWindowExpiredIds: [original.id] });
    await page.goto(`/chat/dm/${targetId}`);

    const bubble = await revealActions(page, original.id);
    await bubble.getByRole("button", { name: "Editar mensagem" }).click();
    await replaceEditorText(page, page.getByTestId(`chat-edit-input-${original.id}`), editedText);

    const patchResponse = page.waitForResponse(
      (response) =>
        response.url().includes(`/api/chat/messages/${original.id}`) &&
        response.request().method() === "PATCH",
    );
    await page.getByRole("button", { name: "Salvar" }).click();
    const response = await patchResponse;
    expect(response.status()).toBe(409);
    await expect(page.getByRole("alert")).toContainText("Janela de edição expirada.");

    await page.getByRole("button", { name: "Cancelar" }).click();
    await expect(messageBubble(page, original.id)).toContainText(originalText);
    await expect(messageBubble(page, original.id)).not.toContainText(editedText);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Ver histórico de edições" }),
    ).toHaveCount(0);
    expect(messagesFor(scenario)[0].body_text).toBe(originalText);

    const reloadMessages = page.waitForResponse(
      (reloadResponse) =>
        reloadResponse.url().includes(`/api/chat/dm/${targetId}/messages`) &&
        reloadResponse.request().method() === "GET",
    );
    await page.reload();
    const reloadResponse = await reloadMessages;
    expect(reloadResponse.status()).toBe(200);
    await expect(messageBubble(page, original.id)).toContainText(originalText);
    await expect(messageBubble(page, original.id)).not.toContainText(editedText);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Ver histórico de edições" }),
    ).toHaveCount(0);
  });

  test("exclui mensagem na DM e mantém placeholder persistente", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const secretText = `${uniqueId(testInfo, "secret")} conteúdo removido DM`;
    const original = makeMessage({
      id: `${targetId}-delete`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: secretText,
      body_format: "v2",
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [original],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const bubble = await revealActions(page, original.id);
    page.once("dialog", (dialog) => dialog.accept());
    const deleteResponse = page.waitForResponse(
      (response) =>
        response.url().includes(`/api/chat/messages/${original.id}`) &&
        response.request().method() === "DELETE",
    );
    await bubble.getByRole("button", { name: "Excluir mensagem" }).click();
    await deleteResponse;

    const removedBubble = messageBubble(page, original.id);
    await expect(removedBubble).toContainText("Mensagem removida.");
    await expect(removedBubble).not.toContainText(secretText);
    expect(await removedBubble.innerHTML()).not.toContain(secretText);
    await removedBubble.hover();
    await expect(removedBubble.getByRole("button", { name: "Editar mensagem" })).toHaveCount(0);
    await expect(removedBubble.getByRole("button", { name: "Excluir mensagem" })).toHaveCount(0);
    await expect(removedBubble.getByRole("button", { name: /Reagir/ })).toHaveCount(0);

    await page.reload();
    await expect(messageBubble(page, original.id)).toContainText("Mensagem removida.");
    await expect(messageBubble(page, original.id)).not.toContainText(secretText);
  });
});
