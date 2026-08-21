import { expect, test } from "@playwright/test";
import type { Locator } from "@playwright/test";

import {
  CURRENT_USER_ID,
  GROUP_DM_ID,
  GROUP_DM_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  emitMessageCreated,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  messagesFor,
  openMoreActions,
  revealActions,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Asserts that the pinned bar previews one specific message.
 *
 * The bar renders the author and the body in two separate spans, so each is
 * matched on its own dynamic text rather than on the section's aggregate
 * textContent — that aggregate also contains the "keep" and "close" icon
 * glyphs, which are presentation and not part of any contract.
 */
async function expectPinPreview(pinsBar: Locator, bodyText: string) {
  await expect(pinsBar.getByText(`${OTHER_USER_NAME}:`, { exact: true })).toBeVisible();
  await expect(pinsBar.getByText(`${OTHER_USER_NAME}: ${bodyText}`, { exact: true })).toBeVisible();
}

/**
 * Objetivo: reação (WS round-trip), favorito e pin (REST) — cada um com
 * persistência após reload — mais um envio/quote no grupo ad-hoc (RF-08),
 * que as suítes existentes de DM 1:1/canal não cobrem.
 */
test.describe("interações de mensagem — reação, favorito e pin", () => {
  test("recebe message.created de outro participante uma única vez na DM correta", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-realtime");
    const initial = makeMessage({ id: `${targetId}-initial`, body_text: "estado inicial da DM" });
    const incoming = makeMessage({
      id: `${targetId}-incoming`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "mensagem recebida sem reload",
      created_at: "2026-07-15T12:01:00.000Z",
      updated_at: "2026-07-15T12:01:00.000Z",
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [initial],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await expect(page.getByTestId("chat-msg-bubble")).toHaveCount(1);
    await expect(messageBubble(page, initial.id)).toContainText("estado inicial da DM");

    await emitMessageCreated(page, scenario, { kind: "dm", targetId, message: incoming });

    const incomingBubble = messageBubble(page, incoming.id);
    await expect(incomingBubble).toContainText("mensagem recebida sem reload");
    await expect(incomingBubble.getByTestId("chat-msg-sender")).toHaveText(OTHER_USER_NAME);

    await emitMessageCreated(page, scenario, { kind: "dm", targetId, message: incoming });
    await expect(messageBubble(page, incoming.id)).toHaveCount(1);

    await page
      .getByRole("region", { name: "Grupos" })
      .getByRole("option", { name: `Grupo ${GROUP_DM_NAME}` })
      .click();
    await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
    await expect(messageBubble(page, incoming.id)).toHaveCount(0);
  });

  test("atualiza unread de grupo não selecionado e limpa ao abrir", async ({ page }, testInfo) => {
    const activeTargetId = uniqueId(testInfo, "dm-active");
    const activeMessage = makeMessage({
      id: `${activeTargetId}-message`,
      body_text: "conversa atualmente aberta",
    });
    const incoming = makeMessage({
      id: `${GROUP_DM_ID}-incoming`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "mensagem nova no grupo",
      created_at: "2026-07-15T12:02:00.000Z",
      updated_at: "2026-07-15T12:02:00.000Z",
    });
    const scenario = createScenario({
      kind: "dm",
      targetId: activeTargetId,
      targetName: OTHER_USER_NAME,
      messages: [activeMessage],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${activeTargetId}`);

    const groupOption = page
      .getByRole("region", { name: "Grupos" })
      .getByRole("option", { name: `Grupo ${GROUP_DM_NAME}` });
    await expect(groupOption.getByLabel("1 não lidas")).toHaveCount(0);

    await emitMessageCreated(page, scenario, {
      kind: "dm",
      targetId: GROUP_DM_ID,
      message: incoming,
    });

    await expect(groupOption.getByLabel("1 não lidas")).toBeVisible();
    await expect(messageBubble(page, incoming.id)).toHaveCount(0);
    await expect(messageBubble(page, activeMessage.id)).toContainText("conversa atualmente aberta");

    await groupOption.click();
    await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
    await expect(messageBubble(page, incoming.id)).toContainText("mensagem nova no grupo");
    await expect(messageBubble(page, incoming.id).getByTestId("chat-msg-sender")).toHaveText(
      OTHER_USER_NAME,
    );
    await expect(groupOption.getByLabel("1 não lidas")).toHaveCount(0);
  });

  test("reage a uma mensagem via WebSocket e a reação persiste após reload", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const original = makeMessage({
      id: `${targetId}-msg`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "mensagem para reagir",
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [original],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await revealActions(page, original.id);
    await page.getByRole("button", { name: "Reagir rapidamente com 👍" }).click();

    const pill = messageBubble(page, original.id).getByRole("button", {
      name: "Remover reação 👍",
    });
    await expect(pill).toBeVisible();
    await expect(pill).toHaveAttribute("aria-pressed", "true");
    await expect(pill).toContainText("1");
    expect(scenario.requests.reactions).toEqual([
      { messageId: original.id, emoji: "👍", added: true },
    ]);

    await page.reload();
    await expect(messageBubble(page, original.id)).toContainText("mensagem para reagir");
    const pillAfterReload = messageBubble(page, original.id).getByRole("button", {
      name: "Remover reação 👍",
    });
    await expect(pillAfterReload).toBeVisible();
    await expect(pillAfterReload).toHaveAttribute("aria-pressed", "true");

    // Remove a única reação: era a minha e a única, então a barra de reações
    // some por completo (nenhum pill "Adicionar" fica para trás).
    await pillAfterReload.click();
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Remover reação 👍" }),
    ).toHaveCount(0);
    expect(scenario.requests.reactions).toEqual([
      { messageId: original.id, emoji: "👍", added: true },
      { messageId: original.id, emoji: "👍", added: false },
    ]);
  });

  test("favorita e remove dos favoritos via REST, com persistência após reload", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const original = makeMessage({
      id: `${targetId}-msg`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "mensagem para favoritar",
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
    await openMoreActions(bubble);
    const favoriteBtn = bubble.getByRole("button", { name: "Favoritar mensagem" });
    await expect(favoriteBtn).toHaveAttribute("aria-pressed", "false");
    await favoriteBtn.click();

    await openMoreActions(bubble);
    const activeFavoriteBtn = messageBubble(page, original.id).getByRole("button", {
      name: "Remover dos favoritos",
    });
    await expect(activeFavoriteBtn).toHaveAttribute("aria-pressed", "true");
    expect(scenario.requests.favorites).toEqual([{ messageId: original.id, action: "add" }]);

    await page.reload();
    await expect(messageBubble(page, original.id)).toContainText("mensagem para favoritar");
    const reloadedBubble = await revealActions(page, original.id);
    await openMoreActions(reloadedBubble);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Remover dos favoritos" }),
    ).toHaveAttribute("aria-pressed", "true");

    await messageBubble(page, original.id)
      .getByRole("button", { name: "Remover dos favoritos" })
      .click();
    await openMoreActions(reloadedBubble);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Favoritar mensagem" }),
    ).toHaveAttribute("aria-pressed", "false");
    expect(scenario.requests.favorites).toEqual([
      { messageId: original.id, action: "add" },
      { messageId: original.id, action: "remove" },
    ]);
  });

  test("fixa uma mensagem via REST, exibe a barra de fixados e persiste após reload", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    // Unique per run, so the assertions below can only be satisfied by the very
    // message this test pinned — never by fixture text that happens to match.
    const pinnedText = `${uniqueId(testInfo, "pin")} mensagem para fixar`;
    const original = makeMessage({
      id: `${targetId}-msg`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: pinnedText,
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
    await openMoreActions(bubble);
    await bubble.getByRole("button", { name: "Fixar mensagem" }).click();

    // The bar previews the pinned message itself (issue #435), so what is
    // asserted is the author and the body of the message just pinned. The
    // section's aggregate textContent is deliberately not used: it also carries
    // the icon glyphs and the close button's label.
    const pinsBar = page.getByTestId("chat-pins");
    await expect(pinsBar).toBeVisible();
    await expect(pinsBar).toHaveAttribute("aria-label", "Mensagem fixada");
    await expectPinPreview(pinsBar, pinnedText);
    await openMoreActions(bubble);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Desafixar mensagem" }),
    ).toHaveAttribute("aria-pressed", "true");
    expect(scenario.requests.pins).toEqual([{ messageId: original.id, targetId, action: "add" }]);

    await page.reload();
    // Same message after a reload — the pin survived, not merely some bar.
    const pinsBarAfterReload = page.getByTestId("chat-pins");
    await expect(pinsBarAfterReload).toBeVisible();
    await expectPinPreview(pinsBarAfterReload, pinnedText);
    const reloadedBubble = await revealActions(page, original.id);
    await openMoreActions(reloadedBubble);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Desafixar mensagem" }),
    ).toHaveAttribute("aria-pressed", "true");
    await messageBubble(page, original.id)
      .getByRole("button", { name: "Desafixar mensagem" })
      .click();
    await expect(page.getByTestId("chat-pins")).toHaveCount(0);
    expect(scenario.requests.pins).toEqual([
      { messageId: original.id, targetId, action: "add" },
      { messageId: original.id, targetId, action: "remove" },
    ]);
  });

  test("envia mensagem e responde com quote em um grupo ad-hoc", async ({ page }, testInfo) => {
    const originalText = `${uniqueId(testInfo, "original")} texto no grupo`;
    const replyText = "resposta no grupo";
    const original = makeMessage({
      id: `${GROUP_DM_ID}-original`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: originalText,
    });
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId: GROUP_DM_ID,
      targetName: GROUP_DM_NAME,
      messages: [original],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${GROUP_DM_ID}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();
    await expect(
      page
        .getByRole("region", { name: "Grupos" })
        .getByRole("option", { name: `Grupo ${GROUP_DM_NAME}` }),
    ).toHaveAttribute("aria-selected", "true");
    await expect(
      page
        .getByRole("region", { name: "Mensagens diretas" })
        .getByRole("option", { name: `Mensagem direta com ${GROUP_DM_NAME}` }),
    ).toHaveCount(0);

    const originalBubble = messageBubble(page, original.id);
    await expect(originalBubble).toContainText(originalText);
    await revealActions(page, original.id);
    await originalBubble.getByRole("button", { name: "Responder" }).click();

    await fillComposer(page, replyText);
    const postResponse = page.waitForResponse(
      (response) =>
        response.url().includes(`/api/chat/dm/${GROUP_DM_ID}/messages`) &&
        response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Enviar mensagem" }).click();
    await postResponse;

    expect(scenario.requests.dmPosts).toEqual([
      expect.objectContaining({ body_text: replyText, parent_message_id: original.id }),
    ]);

    const reply = messagesFor(scenario, "dm", GROUP_DM_ID).find(
      (message) => message.body_text === replyText,
    );
    expect(reply).toBeDefined();
    await expect(messageBubble(page, reply!.id)).toContainText(replyText);
    await expect(messageBubble(page, reply!.id).getByTestId("chat-message-quote")).toContainText(
      originalText,
    );
    expect(reply!.sender_id).toBe(CURRENT_USER_ID);
  });
});
