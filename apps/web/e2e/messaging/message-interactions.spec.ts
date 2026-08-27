import { expect, test } from "@playwright/test";
import type { Locator } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
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

  test("escolhe um emoji no picker completo por busca e por teclado", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-picker");
    const original = makeMessage({
      id: `${targetId}-msg`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "mensagem para o picker",
    });
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "picker",
      messages: [original],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await revealActions(page, original.id);
    const opener = page.getByRole("button", { name: "Mais reações" });
    await opener.click();

    const picker = page.getByRole("dialog", { name: "Escolher reação" });
    await expect(picker).toBeVisible();
    // A barra de categorias é compacta: dez ícones numa linha só, sem rótulo
    // truncado e sem scroll horizontal.
    const tabs = picker.getByRole("tab");
    await expect(tabs).toHaveCount(9);
    await expect(picker.getByRole("tab", { name: "Bandeiras" })).toBeVisible();
    const overflow = await picker
      .locator(".chat-emoji-picker__tabs")
      .evaluate((el) => el.scrollWidth > el.clientWidth);
    expect(overflow).toBe(false);

    const search = picker.getByRole("searchbox", { name: "Buscar emoji" });
    await expect(search).toBeFocused();
    await search.fill("zzzzzz");
    await expect(picker.getByText("Nenhum emoji encontrado.")).toBeVisible();

    await search.fill("foguete");
    const rocket = picker.getByRole("button", { name: "foguete", exact: true });
    await expect(rocket).toBeVisible();
    // Teclado: a seta entra no grid e Enter seleciona.
    await rocket.focus();
    await page.keyboard.press("Enter");

    await expect(picker).toBeHidden();
    await expect(opener).toBeFocused();
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Remover reação 🚀" }),
    ).toBeVisible();
    expect(scenario.requests.reactions).toEqual([
      { messageId: original.id, emoji: "🚀", added: true },
    ]);
  });

  test("escolhe o tom de pele na palette contextual do próprio emoji", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-tone");
    const original = makeMessage({
      id: `${targetId}-msg`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "mensagem para tom de pele",
    });
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "tone",
      messages: [original],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await revealActions(page, original.id);
    await page.getByRole("button", { name: "Mais reações" }).click();
    const search = page.getByRole("searchbox", { name: "Buscar emoji" });
    await search.fill("mao acenando");

    const wave = page.getByRole("button", { name: "mão acenando" }).first();
    await wave.click();

    // A escolha é feita contra o emoji, não num seletor global no cabeçalho.
    const palette = page.getByRole("dialog", { name: "Tom de pele para mão acenando" });
    await expect(palette).toBeVisible();
    await expect(palette.getByRole("button")).toHaveCount(6);
    const paletteBox = await palette.boundingBox();
    const viewport = page.viewportSize();
    expect(
      paletteBox && paletteBox.x >= 0 && paletteBox.x + paletteBox.width <= viewport.width,
    ).toBe(true);

    await palette.getByRole("button", { name: "mão acenando — Escura" }).click();

    await expect(palette).toBeHidden();
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Remover reação 👋🏿" }),
    ).toBeVisible();
    expect(scenario.requests.reactions).toEqual([
      { messageId: original.id, emoji: "👋🏿", added: true },
    ]);
  });

  test("anima a saída da reação antes de remover o badge", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-exit");
    const original = makeMessage({
      id: `${targetId}-msg`,
      body_text: "mensagem com uma reação só",
      reactions: [
        {
          emoji: "🎉",
          count: 1,
          reacted_by_me: true,
          users: [{ user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME }],
        },
      ],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [original],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const bubble = messageBubble(page, original.id);
    const slot = bubble.locator(".chat-msg-area__reaction-slot");
    await bubble.getByRole("button", { name: "Remover reação 🎉" }).click();

    // A última reação não some entre dois frames: ela sai, e só então é removida.
    await expect(slot).toHaveAttribute("data-exiting", "true");
    await expect(slot).toHaveCSS("pointer-events", "none");
    await expect(slot).toHaveCount(0);
  });

  test("fecha o picker com Escape e devolve o foco ao acionador", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-escape");
    const original = makeMessage({ id: `${targetId}-msg`, body_text: "mensagem" });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [original],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await revealActions(page, original.id);
    const opener = page.getByRole("button", { name: "Mais reações" });
    await opener.click();
    await expect(page.getByRole("searchbox", { name: "Buscar emoji" })).toBeFocused();

    await page.keyboard.press("Escape");

    await expect(page.getByRole("dialog", { name: "Escolher reação" })).toBeHidden();
    await expect(opener).toBeFocused();
    expect(scenario.requests.reactions).toEqual([]);
  });

  test("mostra quem reagiu no hover e no foco do badge, no grupo", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "group-authors");
    const original = makeMessage({
      id: `${targetId}-msg`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "mensagem com reações",
      reactions: [
        {
          emoji: "🎉",
          count: 3,
          reacted_by_me: false,
          users: [
            { user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME },
            { user_id: "e2e-third", display_name: "Terceira Pessoa" },
          ],
        },
      ],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: GROUP_DM_NAME,
      messages: [original],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const badge = messageBubble(page, original.id).getByRole("button", {
      name: "Adicionar reação 🎉",
    });
    const authors = messageBubble(page, original.id).getByTestId("reaction-authors");

    // A informação é acessível sempre — a descrição do próprio botão — e não
    // depende do tooltip visual.
    await expect(badge).toHaveAccessibleDescription(
      `🎉: ${OTHER_USER_NAME}, Terceira Pessoa e mais 1`,
    );
    await expect(authors).toHaveCSS("opacity", "0");

    await badge.hover();
    await expect(authors).toHaveCSS("opacity", "1");

    await page.mouse.move(0, 0);
    await expect(authors).toHaveCSS("opacity", "0");

    // O mesmo conteúdo aparece por foco de teclado, sem mouse.
    await badge.focus();
    await expect(authors).toHaveCSS("opacity", "1");

    // Reagir junto passa a nomear o leitor como "Você", sem reload.
    await badge.click();
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Remover reação 🎉" }),
    ).toHaveAccessibleDescription(`🎉: Você, ${OTHER_USER_NAME} e mais 2`);
  });

  test("respeita prefers-reduced-motion nas microinterações da reação", async ({
    page,
  }, testInfo) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    const targetId = uniqueId(testInfo, "dm-reduced-motion");
    const original = makeMessage({
      id: `${targetId}-msg`,
      body_text: "mensagem",
      reactions: [
        {
          emoji: "👍",
          count: 1,
          reacted_by_me: false,
          users: [{ user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME }],
        },
      ],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [original],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const badge = messageBubble(page, original.id).getByRole("button", {
      name: "Adicionar reação 👍",
    });
    await expect(badge.locator(".chat-msg-area__reaction-emoji")).toHaveCSS(
      "animation-name",
      "none",
    );
    await badge.hover();
    // Sem movimento: o destaque continua sendo dado por fundo e contorno.
    await expect(badge).toHaveCSS("transform", "none");
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

    await revealActions(page, original.id);
    const favoriteBtn = page.getByRole("button", { name: "Favoritar mensagem" });
    await expect(favoriteBtn).toHaveAttribute("aria-pressed", "false");
    await favoriteBtn.click();

    const activeFavoriteBtn = messageBubble(page, original.id).getByRole("button", {
      name: "Remover dos favoritos",
    });
    await expect(activeFavoriteBtn).toHaveAttribute("aria-pressed", "true");
    expect(scenario.requests.favorites).toEqual([{ messageId: original.id, action: "add" }]);

    await page.reload();
    await expect(messageBubble(page, original.id)).toContainText("mensagem para favoritar");
    await revealActions(page, original.id);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Remover dos favoritos" }),
    ).toHaveAttribute("aria-pressed", "true");

    await messageBubble(page, original.id)
      .getByRole("button", { name: "Remover dos favoritos" })
      .click();
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

    await revealActions(page, original.id);
    await page.getByRole("button", { name: "Fixar mensagem" }).click();

    // The bar previews the pinned message itself (issue #435), so what is
    // asserted is the author and the body of the message just pinned. The
    // section's aggregate textContent is deliberately not used: it also carries
    // the icon glyphs and the close button's label.
    const pinsBar = page.getByTestId("chat-pins");
    await expect(pinsBar).toBeVisible();
    await expect(pinsBar).toHaveAttribute("aria-label", "Mensagem fixada");
    await expectPinPreview(pinsBar, pinnedText);
    await expect(
      messageBubble(page, original.id).getByRole("button", { name: "Desafixar mensagem" }),
    ).toHaveAttribute("aria-pressed", "true");
    expect(scenario.requests.pins).toEqual([{ messageId: original.id, targetId, action: "add" }]);

    await page.reload();
    // Same message after a reload — the pin survived, not merely some bar.
    const pinsBarAfterReload = page.getByTestId("chat-pins");
    await expect(pinsBarAfterReload).toBeVisible();
    await expectPinPreview(pinsBarAfterReload, pinnedText);
    await revealActions(page, original.id);
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

/**
 * The composer's emoji button (issue #496).
 *
 * It used to open a hard-coded panel of twenty emoji beside a searchable
 * catalog of thousands over a message — one product, two emoji experiences.
 * This runs in a real browser because what is under test is a caret in a
 * contenteditable, which is exactly what jsdom cannot express.
 */
test.describe("emoji no composer", () => {
  test("insere no cursor pelo picker completo e mantém o picker aberto", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-composer-emoji");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "composer-emoji",
      messages: [],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await fillComposer(page, "bom dia");
    const input = page.getByTestId("chat-composer-input");
    // Cursor entre "bom" e " dia".
    await page.keyboard.press("ArrowLeft");
    await page.keyboard.press("ArrowLeft");
    await page.keyboard.press("ArrowLeft");
    await page.keyboard.press("ArrowLeft");

    await page.getByTestId("toolbar-emoji-btn").click();
    const picker = page.getByRole("dialog", { name: "Inserir emoji" });
    await expect(picker).toBeVisible();
    // É o picker completo — busca, categorias e catálogo —, não uma lista fixa.
    await expect(picker.getByRole("searchbox", { name: "Buscar emoji" })).toBeFocused();
    await expect(picker.getByRole("tab")).toHaveCount(9);
    // E fica dentro da viewport, acima do botão que o abriu.
    const box = (await picker.boundingBox())!;
    const viewport = page.viewportSize()!;
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.y).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
    expect(box.y + box.height).toBeLessThanOrEqual(viewport.height);

    await picker.getByRole("button", { name: "rosto risonho", exact: true }).click();
    await expect(input).toHaveText("bom😀 dia");

    // Escolher não fecha: dá para pegar vários seguidos.
    await expect(picker).toBeVisible();
    await picker.getByRole("searchbox", { name: "Buscar emoji" }).fill("foguete");
    await picker.getByRole("button", { name: "foguete", exact: true }).click();
    await expect(input).toHaveText("bom😀🚀 dia");

    // Escape fecha e devolve o foco ao editor, com o cursor onde estava (#493).
    await page.keyboard.press("Escape");
    await expect(picker).toBeHidden();
    await expect(input).toBeFocused();
    await page.keyboard.insertText("!");
    await expect(input).toHaveText("bom😀🚀! dia");
  });

  test("fecha o picker quando a mensagem é enviada", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-composer-emoji-send");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "composer-emoji-send",
      messages: [],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await fillComposer(page, "oi");
    await page.getByTestId("toolbar-emoji-btn").click();
    const picker = page.getByRole("dialog", { name: "Inserir emoji" });
    await picker.getByRole("button", { name: "rosto risonho", exact: true }).click();
    await expect(page.getByTestId("chat-composer-input")).toHaveText("oi😀");

    await page.getByTestId("chat-send-btn").click();

    await expect(picker).toBeHidden();
    await expect(page.getByText("oi😀")).toBeVisible();
  });
});
