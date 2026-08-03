import { expect, test } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  SECOND_CANDIDATE_ID,
  SECOND_CANDIDATE_NAME,
  channelDetailsFixture,
  createScenario,
  groupDetailsFixture,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Issue #398 — adding members from the channel-details panel.
 *
 * These cover what the unit tests structurally cannot: the panel, the picker,
 * the API call and the refetch working together in a real browser, with the
 * conversation and its composer surviving the whole flow.
 */
test.describe("adicionar membros pelo painel de detalhes", () => {
  /** Builds a scenario whose channels all carry the given permission. */
  function scenarioWith(
    testInfo: Parameters<Parameters<typeof test>[1]>[1],
    label: string,
    options: { canManage: boolean; type?: "public" | "private" },
  ) {
    const targetId = uniqueId(testInfo, label);
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Infraestrutura E2E",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no canal" })],
      dmCandidates: [
        { userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME },
        { userId: OTHER_USER_ID, displayName: OTHER_USER_NAME },
      ],
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
          8,
          options.canManage,
        ),
      );
    }
    return { scenario, targetId };
  }

  test("adiciona um membro a um canal público e atualiza lista e contador sem reload", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = scenarioWith(testInfo, "add-members-public", {
      canManage: true,
      type: "public",
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const composer = page.getByTestId("chat-composer-input");
    await expect(composer).toBeVisible();
    await expect(page.getByText("8 membros")).toBeHidden();

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-channel-details");
    await expect(panel).toBeVisible();
    await expect(panel).toContainText("8 membros");

    await panel.getByTestId("chat-details-add-members").click();
    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });
    await expect(dialog).toBeVisible();

    await dialog.getByLabel("Pesquisar pessoa").fill("e2e");
    await dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) }).click();
    await dialog.getByRole("button", { name: "Adicionar", exact: true }).click();

    // The dialog closes and the panel refetches — the counter comes back from
    // the server, not from a local increment.
    await expect(dialog).toBeHidden();
    await expect(panel).toContainText("9 membros");
    await expect(panel.getByRole("status")).toContainText("1 pessoa adicionada");

    // Only the picked user ID crossed the wire.
    expect(scenario.addMembersRequests).toEqual([
      { channelId: targetId, userIds: [SECOND_CANDIDATE_ID] },
    ]);

    // The conversation was never remounted: the composer is the same element and
    // the message is still on screen.
    await expect(composer).toBeVisible();
    await expect(page.getByText("Mensagem no canal")).toBeVisible();
  });

  test("adiciona um membro a um canal privado", async ({ page }, testInfo) => {
    const { scenario, targetId } = scenarioWith(testInfo, "add-members-private", {
      canManage: true,
      type: "private",
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-channel-details");
    await expect(panel).toContainText("Canal privado");

    await panel.getByTestId("chat-details-add-members").click();
    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });
    await dialog.getByLabel("Pesquisar pessoa").fill("e2e");
    await dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) }).click();
    await dialog.getByRole("button", { name: "Adicionar", exact: true }).click();

    await expect(dialog).toBeHidden();
    expect(scenario.addMembersRequests).toHaveLength(1);
  });

  test("adiciona várias pessoas de uma vez", async ({ page }, testInfo) => {
    const { scenario, targetId } = scenarioWith(testInfo, "add-members-many", {
      canManage: true,
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    await page.getByTestId("chat-details-add-members").click();
    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });

    await dialog.getByLabel("Pesquisar pessoa").fill("e2e");
    await dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) }).click();
    await dialog.getByRole("button", { name: new RegExp(OTHER_USER_NAME) }).click();
    await dialog.getByRole("button", { name: "Adicionar", exact: true }).click();

    await expect(dialog).toBeHidden();
    expect(scenario.addMembersRequests[0]?.userIds).toHaveLength(2);
  });

  // The button is not the security control — the server is — but it must reflect
  // the server's answer, and a caller without permission must not see it.
  test("não oferece a ação quando o servidor nega a permissão", async ({ page }, testInfo) => {
    const { scenario, targetId } = scenarioWith(testInfo, "add-members-denied", {
      canManage: false,
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-channel-details");
    await expect(panel).toBeVisible();
    await expect(panel.getByTestId("chat-details-add-members")).toHaveCount(0);
  });

  test("cancelar fecha o seletor sem chamar a API", async ({ page }, testInfo) => {
    const { scenario, targetId } = scenarioWith(testInfo, "add-members-cancel", {
      canManage: true,
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    await page.getByTestId("chat-details-add-members").click();
    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });

    await dialog.getByLabel("Pesquisar pessoa").fill("e2e");
    await dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) }).click();
    await dialog.getByRole("button", { name: "Cancelar" }).click();

    await expect(dialog).toBeHidden();
    expect(scenario.addMembersRequests).toHaveLength(0);
  });

  test("uma recusa do servidor mantém o seletor aberto e a seleção intacta", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = scenarioWith(testInfo, "add-members-403", { canManage: true });
    scenario.addMembersStatus = 403;
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    await page.getByTestId("chat-details-add-members").click();
    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });

    await dialog.getByLabel("Pesquisar pessoa").fill("e2e");
    await dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) }).click();
    await dialog.getByRole("button", { name: "Adicionar", exact: true }).click();

    await expect(dialog.getByRole("alert")).toContainText("não tem permissão");
    await expect(dialog).toBeVisible();
    // The selection survives, so a retry costs one click rather than a re-pick.
    await expect(dialog.getByRole("list", { name: "Pessoas selecionadas" })).toContainText(
      SECOND_CANDIDATE_NAME,
    );
  });

  test("todo o fluxo funciona por teclado e o foco volta para a ação", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = scenarioWith(testInfo, "add-members-keyboard", {
      canManage: true,
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const action = page.getByTestId("chat-details-add-members");
    await action.focus();
    await page.keyboard.press("Enter");

    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });
    await expect(dialog).toBeVisible();
    // Focus opens on the search field, so typing works without a click.
    await page.keyboard.type("e2e");
    const candidate = dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) });
    await candidate.focus();
    await page.keyboard.press("Enter");

    await dialog.getByRole("button", { name: "Adicionar", exact: true }).focus();
    await page.keyboard.press("Enter");

    await expect(dialog).toBeHidden();
    await expect(action).toBeFocused();
    expect(scenario.addMembersRequests).toHaveLength(1);
  });

  test("Escape fecha o seletor e devolve o foco sem mutação", async ({ page }, testInfo) => {
    const { scenario, targetId } = scenarioWith(testInfo, "add-members-escape", {
      canManage: true,
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const action = page.getByTestId("chat-details-add-members");
    await action.click();
    await expect(page.getByRole("dialog", { name: "Adicionar membros" })).toBeVisible();

    await page.keyboard.press("Escape");

    await expect(page.getByRole("dialog", { name: "Adicionar membros" })).toBeHidden();
    await expect(action).toBeFocused();
    expect(scenario.addMembersRequests).toHaveLength(0);
  });
});

/**
 * Group flow and conversation-switch invalidation (issue #398).
 *
 * The group panel is a distinct component with its own endpoint, so these do not
 * duplicate the channel specs above; they cover what only a real browser shows —
 * that the right panel mounts, that the picker posts to the group route, and
 * that navigating away with the picker open cannot post into the new
 * conversation.
 */
test.describe("adicionar membros a um grupo", () => {
  function groupScenario(
    testInfo: Parameters<Parameters<typeof test>[1]>[1],
    label: string,
    canManage: boolean,
  ) {
    const targetId = uniqueId(testInfo, label);
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: "Time de Infra",
      conversationType: "group",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no grupo" })],
      dmCandidates: [{ userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME }],
    });
    scenario.groupDetails.set(
      targetId,
      groupDetailsFixture(
        targetId,
        "Time de Infra",
        [{ user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" }],
        4,
        canManage,
      ),
    );
    return { scenario, targetId };
  }

  test("abre o painel do grupo e adiciona um participante sem reload", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = groupScenario(testInfo, "group-add", true);
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const composer = page.getByTestId("chat-composer-input");
    await expect(composer).toBeVisible();

    const toggle = page.getByTestId("chat-details-toggle");
    await expect(toggle).toHaveAccessibleName("Detalhes do grupo");
    await toggle.click();

    const panel = page.getByTestId("chat-group-details");
    await expect(panel).toBeVisible();
    await expect(panel).toContainText("4 participantes");
    // The group panel, never the channel one.
    await expect(page.getByTestId("chat-channel-details")).toHaveCount(0);

    await panel.getByTestId("chat-group-add-members").click();
    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });
    await dialog.getByLabel("Pesquisar pessoa").fill("e2e");
    await dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) }).click();
    await dialog.getByRole("button", { name: "Adicionar", exact: true }).click();

    await expect(dialog).toBeHidden();
    await expect(panel).toContainText("5 participantes");
    await expect(panel.getByRole("status")).toContainText("1 pessoa adicionada");

    expect(scenario.addMembersRequests).toEqual([
      { channelId: targetId, userIds: [SECOND_CANDIDATE_ID] },
    ]);
    // The conversation survived: same composer element, message still rendered.
    await expect(composer).toBeVisible();
    await expect(page.getByText("Mensagem no grupo")).toBeVisible();
  });

  test("não oferece a ação de grupo quando o servidor nega a permissão", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = groupScenario(testInfo, "group-denied", false);
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-group-details");
    await expect(panel).toBeVisible();
    await expect(panel.getByTestId("chat-group-add-members")).toHaveCount(0);
  });

  // A 1:1 has no panel at all: adding a third person would convert it.
  test("DM 1:1 não oferece painel nem ação de detalhes", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "direct-no-details");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: "Juliane",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Oi" })],
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await expect(page.getByTestId("chat-msg-header")).toBeVisible();
    await expect(page.getByTestId("chat-details-toggle")).toHaveCount(0);
    await expect(page.getByTestId("chat-group-details")).toHaveCount(0);
  });
});

test.describe("troca de conversa com o seletor aberto", () => {
  // Navigating away with people selected must not post them into the
  // conversation the user landed on. The panel is not remounted by the switch,
  // so this is the case a boolean "picker is open" would get wrong.
  test("navegar do canal A para o canal B fecha o seletor e não envia nada", async ({
    page,
  }, testInfo) => {
    const channelA = uniqueId(testInfo, "switch-a");
    const scenario = createScenario({
      kind: "channel",
      targetId: channelA,
      targetName: "Canal A",
      messages: [makeMessage({ id: `${channelA}-m1`, body_text: "Mensagem A" })],
      dmCandidates: [{ userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME }],
    });
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(
          channel,
          [
            {
              user_id: CURRENT_USER_ID,
              display_name: CURRENT_USER_NAME,
              role: "moderator",
              presence: "online",
            },
          ],
          8,
          true,
        ),
      );
    }
    const channelB = scenario.sidebarChannels.find((c) => c.id !== channelA)?.id;
    expect(channelB, "fixture needs a second channel").toBeTruthy();

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${channelA}`);

    await page.getByTestId("chat-details-toggle").click();
    await page.getByTestId("chat-details-add-members").click();
    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });
    await dialog.getByLabel("Pesquisar pessoa").fill("e2e");
    await dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) }).click();
    await expect(dialog.getByRole("list", { name: "Pessoas selecionadas" })).toBeVisible();

    // Navigate while the picker holds a selection made for channel A.
    await page.goto(`/chat/channel/${channelB as string}`);

    await expect(page.getByRole("dialog", { name: "Adicionar membros" })).toBeHidden();
    expect(scenario.addMembersRequests).toHaveLength(0);

    // Reopening in channel B starts empty.
    await page.getByTestId("chat-details-toggle").click();
    await page.getByTestId("chat-details-add-members").click();
    const reopened = page.getByRole("dialog", { name: "Adicionar membros" });
    await expect(reopened.getByRole("list", { name: "Pessoas selecionadas" })).toHaveCount(0);
    await expect(reopened.getByRole("button", { name: "Adicionar", exact: true })).toBeDisabled();
  });

  test("navegar do canal para um grupo fecha o seletor e não envia nada", async ({
    page,
  }, testInfo) => {
    const channelId = uniqueId(testInfo, "switch-ch-to-group");
    const scenario = createScenario({
      kind: "channel",
      targetId: channelId,
      targetName: "Canal A",
      messages: [makeMessage({ id: `${channelId}-m1`, body_text: "Mensagem A" })],
      dmCandidates: [{ userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME }],
    });
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(
          channel,
          [
            {
              user_id: CURRENT_USER_ID,
              display_name: CURRENT_USER_NAME,
              role: "moderator",
              presence: "online",
            },
          ],
          8,
          true,
        ),
      );
    }
    const groupId = scenario.sidebarDMs.find((dm) => dm.type === "group")?.id;
    expect(groupId, "fixture needs a group DM").toBeTruthy();
    scenario.groupDetails.set(
      groupId as string,
      groupDetailsFixture(
        groupId as string,
        "E2E Grupo",
        [{ user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" }],
        3,
        true,
      ),
    );

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${channelId}`);

    await page.getByTestId("chat-details-toggle").click();
    await page.getByTestId("chat-details-add-members").click();
    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });
    await dialog.getByLabel("Pesquisar pessoa").fill("e2e");
    await dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) }).click();

    await page.goto(`/chat/dm/${groupId as string}`);

    await expect(page.getByRole("dialog", { name: "Adicionar membros" })).toBeHidden();
    // Nothing was posted — not to the channel, and above all not to the group.
    expect(scenario.addMembersRequests).toHaveLength(0);
  });
});

/**
 * The membership the picker must respect is the server's, not the panel's
 * (issue #398).
 *
 * These two are the cases the preview structurally cannot cover: a channel
 * member who is offline (absent from a presence-filtered preview) and a group
 * participant past the preview's 30-row cap. Both used to be offered as
 * selectable.
 */
test.describe("candidatos excluem membros fora da prévia", () => {
  test("membro offline de um canal não é ofertado", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "offline-member");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Infraestrutura E2E",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem" })],
      dmCandidates: [
        { userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME },
        { userId: OTHER_USER_ID, displayName: OTHER_USER_NAME },
      ],
    });
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(
          channel,
          // The preview shows only the viewer: OTHER_USER is a member but
          // offline, so it cannot appear here at all.
          [
            {
              user_id: CURRENT_USER_ID,
              display_name: CURRENT_USER_NAME,
              role: "moderator",
              presence: "online",
            },
          ],
          9,
          true,
        ),
      );
      // The real membership, which only the server knows.
      scenario.channelMemberships.set(channel.id, new Set([CURRENT_USER_ID, OTHER_USER_ID]));
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    await page.getByTestId("chat-details-add-members").click();
    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });
    await dialog.getByLabel("Pesquisar pessoa").fill("e2e");

    // The eligible non-member is offered …
    await expect(
      dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) }),
    ).toBeVisible();
    // … and the offline current member is not, despite being absent from the
    // panel's online preview.
    await expect(dialog.getByRole("button", { name: new RegExp(OTHER_USER_NAME) })).toHaveCount(0);
  });

  test("participante além da prévia do grupo não é ofertado", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "beyond-preview");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: "Time de Infra",
      conversationType: "group",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem" })],
      dmCandidates: [
        { userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME },
        { userId: OTHER_USER_ID, displayName: OTHER_USER_NAME },
      ],
    });
    // A group larger than the preview: the panel shows the cap, the membership
    // includes OTHER_USER beyond it.
    scenario.groupDetails.set(
      targetId,
      groupDetailsFixture(
        targetId,
        "Time de Infra",
        [{ user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" }],
        35,
        true,
      ),
    );
    scenario.groupMemberships.set(targetId, new Set([CURRENT_USER_ID, OTHER_USER_ID]));

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-group-details");
    await expect(panel).toContainText("35 participantes");

    await panel.getByTestId("chat-group-add-members").click();
    const dialog = page.getByRole("dialog", { name: "Adicionar membros" });
    await dialog.getByLabel("Pesquisar pessoa").fill("e2e");

    await expect(
      dialog.getByRole("button", { name: new RegExp(SECOND_CANDIDATE_NAME) }),
    ).toBeVisible();
    await expect(dialog.getByRole("button", { name: new RegExp(OTHER_USER_NAME) })).toHaveCount(0);
  });
});
