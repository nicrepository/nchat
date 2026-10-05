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
        actions: { assign_role: false, transfer: true, remove: false },
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
  await page.getByLabel(`Ações de ${CURRENT_USER_NAME}`).click();
  await page.getByRole("button", { name: "Transferir minha propriedade" }).click();
  const dialog = page.getByRole("dialog", { name: "Transferir minha propriedade" });
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Novo proprietário").selectOption(OTHER_USER_ID);
  await dialog.getByRole("button", { name: "Confirmar", exact: true }).click();
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
    await expect(client.getByRole("button", { name: "Transferir minha propriedade" })).toHaveCount(
      0,
    );
  }
  expect(submitted).toBe(1);
});
