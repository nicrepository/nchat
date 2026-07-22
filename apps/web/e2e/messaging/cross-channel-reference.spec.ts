import { expect, test } from "@playwright/test";

import {
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  messagesFor,
  revealActions,
  uniqueId,
} from "../helpers/messagingApi";

test.describe("RF-09 — citação entre canais", () => {
  test("cria a referência e navega até a mensagem de origem", async ({ page }) => {
    const sourceChannelId = "11111111-1111-4111-8111-111111111111";
    const source = makeMessage({
      id: "22222222-2222-4222-8222-222222222222",
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "conteúdo autorizado RF-09",
    });
    const scenario = createScenario({
      kind: "channel",
      targetId: sourceChannelId,
      targetName: "Origem E2E",
      messages: [source],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${sourceChannelId}`);

    const sourceBubble = await revealActions(page, source.id);
    await sourceBubble.getByRole("button", { name: "Citar em outra conversa" }).click();
    await page.getByRole("button", { name: /Canal E2E/ }).click();
    await expect(page.getByTestId("chat-composer-reference")).toContainText(
      "conteúdo autorizado RF-09",
    );
    await fillComposer(page, "veja a origem");
    await page.getByRole("button", { name: "Enviar mensagem" }).click();

    await expect.poll(() => scenario.requests.channelPosts.length).toBe(1);
    expect(scenario.requests.channelPosts[0]).toEqual(
      expect.objectContaining({ referenced_message_id: source.id }),
    );
    const created = messagesFor(scenario, "channel", "e2e-channel-other").at(-1)!;
    const reference = messageBubble(page, created.id).getByTestId("chat-message-reference");
    await expect(reference).toContainText("conteúdo autorizado RF-09");
    await reference.click();
    await expect(page).toHaveURL(
      `/chat/channel/${sourceChannelId}?message=${encodeURIComponent(source.id)}`,
    );
    await expect(messageBubble(page, source.id)).toBeVisible();
  });

  test("revogação após reload remove conteúdo, metadados e navegação", async ({
    page,
  }, testInfo) => {
    const destinationId = uniqueId(testInfo, "destination-channel");
    const source = makeMessage({
      id: uniqueId(testInfo, "private-source"),
      sender_id: OTHER_USER_ID,
      sender_display_name: "Autora Protegida",
      body_text: "segredo RF-09 que não pode vazar",
    });
    const destination = makeMessage({
      id: uniqueId(testInfo, "destination-message"),
      body_text: "mensagem de destino",
      reference: {
        available: true,
        message_id: source.id,
        target_type: "channel",
        target_id: "e2e-channel-other",
        target_label: "Canal Privado Protegido",
        author_display_name: source.sender_display_name,
        body: source.body_text,
        body_format: source.body_format,
        created_at: source.created_at,
      },
    });
    const scenario = createScenario({
      kind: "channel",
      targetId: destinationId,
      targetName: "Destino E2E",
      messages: [destination],
    });
    messagesFor(scenario, "channel", "e2e-channel-other").push(source);
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${destinationId}`);
    await expect(page.getByTestId("chat-message-reference")).toContainText(source.body_text!);

    destination.reference = { available: false };
    await page.reload();

    const unavailable = page.getByTestId("chat-message-reference");
    await expect(unavailable).toHaveText("citação indisponível");
    await expect(unavailable).toHaveAttribute("aria-disabled", "true");
    await expect(page.getByRole("link", { name: /Ir para mensagem citada/ })).toHaveCount(0);
    const html = await unavailable.evaluate((element) => element.outerHTML);
    expect(html).not.toContain(source.id);
    expect(html).not.toContain(source.body_text!);
    expect(html).not.toContain(source.sender_display_name);
    expect(html).not.toContain("Canal Privado Protegido");

    messagesFor(scenario, "channel", "e2e-channel-other").splice(0);
    await page.goto(`/chat/channel/e2e-channel-other?message=${encodeURIComponent(source.id)}`);
    await expect(page.getByTestId("chat-msg-empty")).toBeVisible();
    await expect(page.locator("body")).not.toContainText(source.body_text!);
  });
});
