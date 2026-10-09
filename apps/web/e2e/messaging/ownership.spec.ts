import { expect, test } from "@playwright/test";
import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  groupDetailsFixture,
  installMessagingMocks,
  emitConversationUpdated,
  makeMessage,
  revealActions,
  uniqueId,
} from "../helpers/messagingApi";

test("ownership transfer converges in two clients and preserves drafts", async ({
  page,
  context,
}, testInfo) => {
  const id = uniqueId(testInfo, "ownership");
  const scenario = createScenario({
    kind: "dm",
    conversationType: "group",
    targetId: id,
    targetName: "Grupo ownership",
    messages: [makeMessage({ id: `${id}-message`, body_text: "Histórico" })],
  });
  const ownership = {
    enabled: true,
    members: [
      {
        user_id: CURRENT_USER_ID,
        display_name: CURRENT_USER_NAME,
        role: "owner",
        actions: { assign_role: true, transfer: false, remove: false },
      },
      {
        user_id: OTHER_USER_ID,
        display_name: OTHER_USER_NAME,
        role: "admin",
        actions: { assign_role: true, transfer: true, remove: true },
      },
    ],
    capabilities: { add_members: true, manage_roles: true, edit_metadata: true, leave: true },
    leave_preview: { last_owner: true, successor_user_id: OTHER_USER_ID, blocked: false },
  };
  const details = Object.assign(
    groupDetailsFixture(
      { id, name: "Grupo ownership" },
      [
        { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME },
        { user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME },
      ],
      2,
      true,
      true,
    ),
    { ownership },
  );
  scenario.groupDetails.set(id, details);
  const observer = await context.newPage();
  for (const client of [page, observer]) {
    await installMessagingMocks(client, scenario);
    await client.goto(`/chat/dm/${id}`);
    const bubble = await revealActions(client, `${id}-message`);
    await bubble.getByRole("button", { name: "Responder" }).click();
    await client.getByTestId("chat-composer-file-input").setInputFiles({
      name: "ownership.pdf",
      mimeType: "application/pdf",
      buffer: Buffer.from("%PDF-1.4 ownership"),
    });
    await expect(client.getByTestId("chat-composer-pending-attachment")).toContainText(
      "Pronto para enviar",
    );
    const composer = client.getByTestId("chat-composer-input");
    await composer.click();
    await client.keyboard.insertText("Rascunho preservado");
    await client.getByRole("button", { name: "Detalhes do grupo", exact: true }).click();
    await expect(client.getByText("Proprietário", { exact: true })).toBeVisible();
  }
  let submitted = 0;
  await page.route(`**/api/chat/dm/${id}/ownership/transfer`, async (route) => {
    submitted++;
    expect(route.request().postDataJSON()).toEqual({
      new_owner_user_id: OTHER_USER_ID,
      actor_new_role: "member",
    });
    expect(route.request().headers()["idempotency-key"]).toBeTruthy();
    ownership.members[0].role = "member";
    ownership.members[1].role = "owner";
    for (const member of ownership.members)
      member.actions = { assign_role: false, transfer: false, remove: false };
    ownership.capabilities.manage_roles = false;
    ownership.capabilities.edit_metadata = false;
    ownership.leave_preview.last_owner = false;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { target_user_id: OTHER_USER_ID, role: "owner", left: false } }),
    });
  });
  await page.getByLabel(`Ações de ${OTHER_USER_NAME}`).click();
  await page.getByRole("menuitem", { name: "Transferir minha propriedade" }).click();
  const dialog = page.getByRole("dialog", { name: "Transferir minha propriedade" });
  await expect(dialog).toBeVisible();
  await expect(
    dialog.getByRole("radio", { name: `${OTHER_USER_NAME}, Administrador` }),
  ).toBeChecked();
  await dialog.getByRole("button", { name: "Transferir propriedade", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await emitConversationUpdated(observer, { kind: "dm", targetId: id });
  for (const client of [page, observer]) {
    const row = client.getByRole("listitem").filter({ hasText: OTHER_USER_NAME });
    await expect(row.getByText("Proprietário", { exact: true })).toBeVisible();
    await expect(client.getByTestId("chat-composer-input")).toContainText("Rascunho preservado");
    await expect(client.getByTestId("chat-composer-quote")).toContainText("Histórico");
    await expect(client.getByTestId("chat-composer-pending-attachment")).toContainText(
      "ownership.pdf",
    );
    await expect(
      client.getByRole("menuitem", { name: "Transferir minha propriedade" }),
    ).toHaveCount(0);
  }
  expect(submitted).toBe(1);
});

test("participant controls preserve the conversation and use an accessible mobile sheet", async ({
  page,
}, testInfo) => {
  const id = uniqueId(testInfo, "participants-ui");
  const messages = Array.from({ length: 40 }, (_, index) =>
    makeMessage({
      id: `${id}-message-${index}`,
      body_text: `Histórico ${index}`,
      created_at: new Date(Date.UTC(2026, 0, 1, 12, index)).toISOString(),
    }),
  );
  const scenario = createScenario({
    kind: "dm",
    conversationType: "group",
    targetId: id,
    targetName: "Grupo participantes",
    messages,
  });
  const ownership = {
    enabled: true,
    members: [
      {
        user_id: CURRENT_USER_ID,
        display_name: CURRENT_USER_NAME,
        role: "owner",
        actions: { assign_role: true, remove: false, transfer: false },
      },
      {
        user_id: OTHER_USER_ID,
        display_name: OTHER_USER_NAME,
        role: "admin",
        actions: { assign_role: true, remove: true, transfer: true },
      },
      {
        user_id: `${id}-member`,
        display_name: "Dai Member",
        role: "member",
        actions: { assign_role: true, remove: true, transfer: true },
      },
    ],
    capabilities: { add_members: true, manage_roles: true, edit_metadata: true, leave: true },
    leave_preview: { last_owner: true, successor_user_id: OTHER_USER_ID, blocked: false },
  };
  scenario.groupDetails.set(
    id,
    Object.assign(
      groupDetailsFixture({ id, name: "Grupo participantes" }, ownership.members, 3, true, true),
      { ownership },
    ),
  );
  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/dm/${id}`);
  const bubble = await revealActions(page, messages.at(-1)!.id);
  await bubble.getByRole("button", { name: "Responder" }).click();
  await page.getByTestId("chat-composer-file-input").setInputFiles({
    name: "participants.pdf",
    mimeType: "application/pdf",
    buffer: Buffer.from("%PDF-1.4 participants"),
  });
  await expect(page.getByTestId("chat-composer-pending-attachment")).toContainText(
    "Pronto para enviar",
  );
  await page.getByTestId("chat-composer-input").click();
  await page.keyboard.insertText("Rascunho preservado");
  const composer = await page.getByTestId("chat-composer-input").elementHandle();
  const timeline = page.locator(".chat-msg-area__list");
  const timelineNode = await timeline.elementHandle();
  const frames = () =>
    page.evaluate(() => {
      const scope = window as unknown as {
        __e2eWebSocketMessages: () => Array<{ type: string }>;
        __e2eWebSocketLifecycle: () => { created: number; closed: number; active: number };
      };
      return {
        lifecycle: scope.__e2eWebSocketLifecycle(),
        subscriptions: scope
          .__e2eWebSocketMessages()
          .filter((frame) => ["subscribe", "unsubscribe"].includes(frame.type)),
      };
    });
  const originalFrames = await frames();
  // Compare against the mounted app: media sockets and StrictMode also use the mock.
  expect(originalFrames.lifecycle.active).toBeGreaterThan(0);

  for (const viewport of [
    { width: 1280, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    await page.getByRole("button", { name: "Detalhes do grupo", exact: true }).click();
    const panel = page.getByTestId("chat-conversation-details");
    await expect(panel.getByText("Proprietário", { exact: true })).toBeVisible();
    const originalScroll = await timeline.evaluate((node) => {
      node.scrollTop = Math.max(0, node.scrollHeight - node.clientHeight - 120);
      return node.scrollTop;
    });
    expect(originalScroll).toBeGreaterThan(0);
    const trigger = panel.getByLabel(`Ações de ${OTHER_USER_NAME}`);
    await trigger.focus();
    await page.keyboard.press("Enter");
    const menu = page.getByRole("menu", { name: `Ações de ${OTHER_USER_NAME}` });
    await expect(menu.getByRole("menuitem", { name: "Tornar proprietário" })).toBeFocused();
    const surface = page.locator(".ownership-member-menu");
    const bounds = await surface.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.y).toBeGreaterThanOrEqual(0);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(viewport.width);
    expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(viewport.height);
    if (viewport.width < 640) {
      expect(bounds!.width).toBe(viewport.width);
      expect(bounds!.y + bounds!.height).toBe(viewport.height);
      await surface.getByRole("button", { name: "Fechar menu" }).click();
    } else await page.keyboard.press("Escape");
    await expect(trigger).toBeFocused();
    await expect(menu).toHaveCount(0);
    ownership.members[2].actions.transfer = true;
    await trigger.click();
    await page.getByRole("menuitem", { name: "Transferir minha propriedade" }).click();
    const transferDialog = page.getByRole("dialog", { name: "Transferir minha propriedade" });
    const cancel = transferDialog.getByRole("button", { name: "Cancelar" });
    await expect(cancel).toBeFocused();
    const confirmTransfer = transferDialog.getByRole("button", {
      name: "Transferir propriedade",
      exact: true,
    });
    await confirmTransfer.focus();
    await page.keyboard.press("Tab");
    await expect(transferDialog.getByLabel("Buscar participante")).toBeFocused();
    await page.keyboard.press("Shift+Tab");
    await expect(confirmTransfer).toBeFocused();
    const targetRadio = transferDialog.getByRole("radio", {
      name: `${OTHER_USER_NAME}, Administrador`,
    });
    await targetRadio.focus();
    await page.keyboard.press("ArrowDown");
    await expect(transferDialog.getByRole("radio", { name: "Dai Member, Membro" })).toBeChecked();
    const dialogBounds = await transferDialog.boundingBox();
    expect(dialogBounds!.x).toBeGreaterThanOrEqual(0);
    expect(dialogBounds!.x + dialogBounds!.width).toBeLessThanOrEqual(viewport.width);
    expect(await transferDialog.evaluate((node) => node.scrollWidth <= node.clientWidth)).toBe(
      true,
    );
    await expect(
      transferDialog.getByRole("button", { name: "Transferir propriedade", exact: true }),
    ).toBeInViewport();
    await page.keyboard.press("Escape");
    await expect(transferDialog).toHaveCount(0);
    await expect(trigger).toBeFocused();

    await page.route(`**/api/chat/dm/${id}/ownership/transfer-and-leave`, async (route) => {
      expect(route.request().postDataJSON()).toEqual({
        new_owner_user_id: `${id}-member`,
        actor_new_role: "member",
      });
      expect(route.request().headers()["idempotency-key"]).toBeTruthy();
      ownership.members[2].actions.transfer = false;
      await route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "conflict", message: "changed" } }),
      });
    });
    await panel.getByRole("button", { name: "Sair da conversa" }).click();
    const leaveDialog = page.getByRole("dialog", { name: "Sair da conversa" });
    await expect(leaveDialog).toContainText(`${OTHER_USER_NAME} será promovido automaticamente`);
    await leaveDialog.getByRole("button", { name: "Escolher outro proprietário" }).click();
    await leaveDialog.getByRole("radio", { name: "Dai Member, Membro" }).check();
    await leaveDialog.getByRole("button", { name: "Sair e transferir" }).click();
    await expect(leaveDialog.getByRole("alert")).toContainText("A propriedade mudou");
    await expect(leaveDialog.getByRole("radio", { name: "Dai Member, Membro" })).toHaveCount(0);
    await expect(leaveDialog.getByRole("button", { name: "Sair e transferir" })).toBeDisabled();
    await expect(leaveDialog.locator("form")).toHaveAttribute("aria-describedby");
    await expect(page.getByTestId("chat-composer-input")).toContainText("Rascunho preservado");
    await leaveDialog.getByRole("button", { name: "Cancelar" }).click();
    await page.unroute(`**/api/chat/dm/${id}/ownership/transfer-and-leave`);
    ownership.members[2].actions.transfer = true;
    await emitConversationUpdated(page, { kind: "dm", targetId: id });
    await panel.getByRole("button", { name: "Ver todos" }).click();
    await panel.getByLabel("Buscar participante").fill("  DAI  ");
    await panel.getByLabel("Papel").selectOption("member");
    await expect(panel.getByText("Dai Member", { exact: true })).toBeVisible();
    await expect(panel.getByRole("listitem")).toHaveCount(1);
    await panel.getByLabel("Buscar participante").fill("");
    await panel.getByLabel("Papel").selectOption("");
    await expect(panel.getByRole("listitem")).toHaveCount(3);
    expect(await timeline.evaluate((node, original) => node === original, timelineNode)).toBe(true);
    expect(
      await page
        .getByTestId("chat-composer-input")
        .evaluate((node, original) => node === original, composer),
    ).toBe(true);
    expect(await timeline.evaluate((node) => node.scrollTop)).toBe(originalScroll);
    expect(await frames()).toEqual(originalFrames);
    await page.getByRole("button", { name: "Fechar detalhes do grupo" }).click();
    await expect(page.getByTestId("chat-composer-input")).toContainText("Rascunho preservado");
    await expect(page.getByTestId("chat-composer-quote")).toContainText("Histórico 39");
    await expect(page.getByTestId("chat-composer-pending-attachment")).toContainText(
      "participants.pdf",
    );
  }
});
