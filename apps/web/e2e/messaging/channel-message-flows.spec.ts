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
  openMoreActions,
  replaceEditorText,
  revealActions,
  uniqueId,
} from "../helpers/messagingApi";

test.describe("mensagens em canal", () => {
  test("encaminha uma mensagem para outro canal e preserva o indicador após reload", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-source");
    const originalText = `${uniqueId(testInfo, "forward")} conteúdo encaminhado`;
    const original = makeMessage({
      id: `${targetId}-original`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: originalText,
    });
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Origem E2E",
      messages: [original],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const sourceBubble = await revealActions(page, original.id);
    await openMoreActions(sourceBubble);
    await sourceBubble.getByRole("button", { name: "Encaminhar" }).click();
    const dialog = page.getByRole("dialog", { name: "Encaminhar mensagem" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Origem E2E" })).toHaveCount(0);
    await dialog.getByRole("searchbox", { name: "Buscar canal" }).fill("canal e2e");
    await dialog.getByRole("button", { name: "Canal E2E" }).click();

    const forwardResponse = page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/chat/channels/e2e-channel-other/messages/forward") &&
        response.request().method() === "POST",
    );
    await dialog.getByRole("button", { name: "Encaminhar" }).evaluate((button) => {
      (button as HTMLButtonElement).click();
      (button as HTMLButtonElement).click();
    });
    expect((await forwardResponse).status()).toBe(201);
    await expect(dialog).toHaveCount(0);

    expect(scenario.requests.forwards).toEqual([
      {
        destinationChannelId: "e2e-channel-other",
        sourceMessageId: original.id,
        idempotencyKey: expect.any(String),
        raw: { source_message_id: original.id },
      },
    ]);
    expect(scenario.requests.forwards[0].idempotencyKey).toMatch(/^[A-Za-z0-9._:-]{1,128}$/);
    expect(scenario.requests.forwards[0].raw).not.toHaveProperty("body_text");

    const forwarded = messagesFor(scenario, "channel", "e2e-channel-other")[0];
    expect(forwarded).toBeDefined();
    await page.goto("/chat/channel/e2e-channel-other");
    const forwardedBubble = messageBubble(page, forwarded.id);
    await expect(forwardedBubble).toContainText(originalText);
    await expect(forwardedBubble.getByTestId("chat-message-forwarded")).toContainText(
      "Mensagem encaminhada",
    );

    await page.reload();
    await expect(
      messageBubble(page, forwarded.id).getByTestId("chat-message-forwarded"),
    ).toContainText("Mensagem encaminhada");
  });

  test("responde uma mensagem com quote inline e persiste após reload", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const originalText = `${uniqueId(testInfo, "original")} texto citado`;
    const replyText = "resposta canal quote";
    const original = makeMessage({
      id: `${targetId}-original`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: originalText,
    });
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal E2E",
      messages: [original],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

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
        response.url().includes(`/api/chat/channels/${targetId}/messages`) &&
        response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Enviar mensagem" }).click();
    await postResponse;

    expect(scenario.requests.channelPosts).toEqual([
      expect.objectContaining({ body_text: replyText, parent_message_id: original.id }),
    ]);

    const reply = messagesFor(scenario).find((message) => message.body_text === replyText);
    expect(reply).toBeDefined();
    const replyBubble = messageBubble(page, reply!.id);
    await expect(replyBubble).toContainText(replyText);
    const quote = replyBubble.getByTestId("chat-message-quote");
    await expect(quote).toContainText(OTHER_USER_NAME);
    await expect(quote).toContainText(originalText);
    await expect(
      replyBubble.getByRole("button", { name: `Ir para mensagem original de ${OTHER_USER_NAME}` }),
    ).toBeVisible();

    await page.reload();
    await expect(messageBubble(page, reply!.id)).toContainText(replyText);
    await expect(messageBubble(page, reply!.id).getByTestId("chat-message-quote")).toContainText(
      originalText,
    );
  });

  test("edita uma mensagem dentro da janela e mostra histórico", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const originalText = `${uniqueId(testInfo, "original")} texto original`;
    const editedText = "texto editado canal";
    const original = makeMessage({
      id: `${targetId}-edit`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: originalText,
    });
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal E2E",
      messages: [original],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const bubble = await revealActions(page, original.id);
    await openMoreActions(bubble);
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
        body_format: "v3",
      }),
    );
    expect(scenario.requests.patches[0].raw).toEqual(
      expect.objectContaining({ body: editedText, body_format: "v3" }),
    );
    expect(scenario.requests.patches[0].raw).not.toHaveProperty("body_text");
    expect(scenario.requests.patches[0].raw).not.toHaveProperty("text");

    await expect(messageBubble(page, original.id)).toContainText(editedText);
    await expect(messageBubble(page, original.id)).not.toContainText(originalText);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Ver histórico de edições" }),
    ).toBeVisible();

    await messageBubble(page, original.id)
      .getByRole("button", { name: "Ver histórico de edições" })
      .click();
    const history = page.getByRole("dialog", { name: "Histórico de edições" });
    await expect(history).toBeVisible();
    await expect(history).toContainText("Texto original antes da edição");

    await page.reload();
    await expect(messageBubble(page, original.id)).toContainText(editedText);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Ver histórico de edições" }),
    ).toBeVisible();
  });

  test("impede edição fora da janela e mantém conteúdo original", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const originalText = `${uniqueId(testInfo, "original")} texto bloqueado`;
    const editedText = "texto bloqueado canal";
    const original = makeMessage({
      id: `${targetId}-expired`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: originalText,
    });
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal E2E",
      messages: [original],
    });

    await installMessagingMocks(page, scenario, { editWindowExpiredIds: [original.id] });
    await page.goto(`/chat/channel/${targetId}`);

    const bubble = await revealActions(page, original.id);
    await openMoreActions(bubble);
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
    expect(messagesFor(scenario)[0].body_text).toBe(originalText);

    await page.reload();
    await expect(messageBubble(page, original.id)).toContainText(originalText);
    await expect(messageBubble(page, original.id)).not.toContainText(editedText);
  });

  test("exclui mensagem e mantém placeholder sem revelar conteúdo", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const secretText = `${uniqueId(testInfo, "secret")} conteúdo removido`;
    const original = makeMessage({
      id: `${targetId}-delete`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: secretText,
    });
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal E2E",
      messages: [original],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const bubble = await revealActions(page, original.id);
    await openMoreActions(bubble);
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
