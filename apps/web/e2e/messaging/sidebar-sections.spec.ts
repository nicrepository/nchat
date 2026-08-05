import { expect, test, type Page } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  GROUP_DM_ID,
  GROUP_DM_NAME,
  OTHER_CHANNEL_ID,
  OTHER_CHANNEL_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  emitMessageCreated,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * ISSUE #396 — Canais, Mensagens Diretas and Grupos are three distinct product
 * categories. The fixture always carries one of each, so these tests assert
 * placement rather than the incidental order of a single list.
 */

/** Each section is a landmark named by its own heading. */
function section(page: Page, name: string) {
  return page.getByRole("region", { name });
}

function optionsIn(page: Page, name: string) {
  return section(page, name).getByRole("option");
}

async function openChatWithAllThreeCategories(
  page: Page,
  testInfo: Parameters<typeof uniqueId>[0],
) {
  const targetId = uniqueId(testInfo, "dm");
  const scenario = createScenario({
    kind: "dm",
    targetId,
    targetName: OTHER_USER_NAME,
    messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
  });
  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/dm/${targetId}`);
  await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();
  return { scenario, targetId };
}

test.describe("sidebar — seções de conversas", () => {
  test("apresenta as três seções como cabeçalhos", async ({ page }, testInfo) => {
    await openChatWithAllThreeCategories(page, testInfo);

    for (const name of ["Canais", "Mensagens diretas", "Grupos"]) {
      await expect(page.getByRole("heading", { name })).toBeVisible();
      await expect(section(page, name)).toBeVisible();
    }
  });

  test("posiciona canal, DM 1:1 e grupo cada um em sua própria seção", async ({
    page,
  }, testInfo) => {
    const { targetId } = await openChatWithAllThreeCategories(page, testInfo);

    await expect(optionsIn(page, "Canais")).toHaveText([new RegExp(OTHER_CHANNEL_NAME)]);
    await expect(optionsIn(page, "Mensagens diretas")).toHaveText([new RegExp(OTHER_USER_NAME)]);
    await expect(optionsIn(page, "Grupos")).toHaveText([new RegExp(GROUP_DM_NAME)]);

    // Exclusividade: nenhum item aparece em duas seções, nem duplicado.
    await expect(page.getByRole("option")).toHaveCount(3);
    await expect(
      page.getByRole("option", { name: `Mensagem direta com ${OTHER_USER_NAME}` }),
    ).toHaveCount(1);
    await expect(page.getByRole("option", { name: `Grupo ${GROUP_DM_NAME}` })).toHaveCount(1);
    await expect(
      section(page, "Grupos").getByRole("option", { name: /Mensagem direta/ }),
    ).toHaveCount(0);
    await expect(
      section(page, "Mensagens diretas").getByRole("option", { name: /^Grupo / }),
    ).toHaveCount(0);
    await expect(
      section(page, "Canais").getByRole("option", { name: /Grupo|Mensagem direta/ }),
    ).toHaveCount(0);

    // A conversa aberta segue selecionada em sua própria seção.
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));
  });

  test("navega de um grupo para um canal preservando a seleção", async ({ page }, testInfo) => {
    await openChatWithAllThreeCategories(page, testInfo);

    const group = page.getByRole("option", { name: `Grupo ${GROUP_DM_NAME}` });
    await group.click();
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${GROUP_DM_ID}$`));
    await expect(group).toHaveAttribute("aria-selected", "true");
    // O grupo permanece na seção Grupos depois de selecionado.
    await expect(optionsIn(page, "Grupos")).toHaveCount(1);
    await expect(
      section(page, "Mensagens diretas").getByRole("option", { selected: true }),
    ).toHaveCount(0);

    const channel = optionsIn(page, "Canais").first();
    await channel.click();
    await expect(page).toHaveURL(/\/chat\/channel\//);
    await expect(channel).toHaveAttribute("aria-selected", "true");
    await expect(group).toHaveAttribute("aria-selected", "false");
  });
});

/**
 * ISSUE #414 — each section is ordered by its own most recent activity. The
 * fixture below gives every section two rows and states, per row, when it was
 * last written in, so what is asserted is the order the app computed and not
 * the order the payload happened to arrive in.
 */
test.describe("sidebar — ordenação por atividade", () => {
  const SECOND_GROUP_ID = "e2e-dm-group-2";
  const SECOND_GROUP_NAME = "E2E Grupo Antigo";
  const SECOND_DM_ID = "e2e-dm-other-2";
  const SECOND_DM_NAME = "E2E Participante Dois";

  async function openOrderedSidebar(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
    const primaryChannelId = uniqueId(testInfo, "channel");
    const scenario = createScenario({
      kind: "channel",
      targetId: primaryChannelId,
      targetName: "Canal Recente",
      messages: [makeMessage({ id: `${primaryChannelId}-msg`, body_text: "olá" })],
    });

    // The primary channel is the *less* recently active of the two, so a sidebar
    // that simply kept payload order would fail every assertion below.
    const activity: Record<string, string | null> = {
      [primaryChannelId]: "2026-07-20T10:00:00Z",
      [OTHER_CHANNEL_ID]: "2026-07-28T10:00:00Z",
      "e2e-dm-other": "2026-07-21T10:00:00Z",
      [SECOND_DM_ID]: "2026-07-27T10:00:00Z",
      [GROUP_DM_ID]: "2026-07-22T10:00:00Z",
      [SECOND_GROUP_ID]: "2026-07-26T10:00:00Z",
    };

    scenario.sidebarDMs.push(
      {
        id: SECOND_DM_ID,
        type: "direct",
        name: SECOND_DM_NAME,
        unread_count: 0,
        counterpart: { user_id: `${OTHER_USER_ID}-2`, display_name: SECOND_DM_NAME },
      },
      { id: SECOND_GROUP_ID, type: "group", name: SECOND_GROUP_NAME, unread_count: 0 },
    );
    for (const item of [...scenario.sidebarChannels, ...scenario.sidebarDMs]) {
      item.created_at = "2026-01-01T00:00:00Z";
      item.last_message_at = activity[item.id] ?? null;
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${primaryChannelId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();
    return { scenario, primaryChannelId };
  }

  const names = (page: Page, sectionName: string) => section(page, sectionName).getByRole("option");

  test("ordena cada seção pela atividade mais recente, de forma independente", async ({
    page,
  }, testInfo) => {
    const { primaryChannelId } = await openOrderedSidebar(page, testInfo);

    await expect(names(page, "Canais")).toHaveText([
      new RegExp(OTHER_CHANNEL_NAME),
      /Canal Recente/,
    ]);
    await expect(names(page, "Mensagens diretas")).toHaveText([
      new RegExp(SECOND_DM_NAME),
      new RegExp(OTHER_USER_NAME),
    ]);
    await expect(names(page, "Grupos")).toHaveText([
      new RegExp(SECOND_GROUP_NAME),
      new RegExp(GROUP_DM_NAME),
    ]);

    // A reload recomputes the same order from the same persisted state.
    await page.reload();
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();
    await expect(names(page, "Canais")).toHaveText([
      new RegExp(OTHER_CHANNEL_NAME),
      /Canal Recente/,
    ]);
    await expect(page).toHaveURL(new RegExp(`/chat/channel/${primaryChannelId}$`));
  });

  test("mensagem recebida move a conversa ao topo sem tocar nas outras seções", async ({
    page,
  }, testInfo) => {
    const { scenario, primaryChannelId } = await openOrderedSidebar(page, testInfo);

    await emitMessageCreated(page, scenario, {
      kind: "channel",
      targetId: primaryChannelId,
      message: makeMessage({
        id: `${primaryChannelId}-incoming`,
        sender_id: OTHER_USER_ID,
        sender_display_name: OTHER_USER_NAME,
        body_text: "chegou agora",
        created_at: "2026-07-30T10:00:00Z",
        updated_at: "2026-07-30T10:00:00Z",
      }),
    });

    await expect(names(page, "Canais")).toHaveText([
      /Canal Recente/,
      new RegExp(OTHER_CHANNEL_NAME),
    ]);
    // The other two sections did not move: a channel event is a channel event.
    await expect(names(page, "Mensagens diretas")).toHaveText([
      new RegExp(SECOND_DM_NAME),
      new RegExp(OTHER_USER_NAME),
    ]);
    await expect(names(page, "Grupos")).toHaveText([
      new RegExp(SECOND_GROUP_NAME),
      new RegExp(GROUP_DM_NAME),
    ]);
    // The conversation on screen stays open and stays selected where it landed.
    await expect(page).toHaveURL(new RegExp(`/chat/channel/${primaryChannelId}$`));
    await expect(names(page, "Canais").first()).toHaveAttribute("aria-selected", "true");
  });

  test("mensagem enviada move a DM ao topo quando o servidor confirma", async ({
    page,
  }, testInfo) => {
    const { scenario } = await openOrderedSidebar(page, testInfo);

    await expect(names(page, "Mensagens diretas")).toHaveText([
      new RegExp(SECOND_DM_NAME),
      new RegExp(OTHER_USER_NAME),
    ]);

    // What the server broadcasts after persisting the user's own message: the
    // sidebar promotes on that confirmation, never on a local clock.
    await emitMessageCreated(page, scenario, {
      kind: "dm",
      targetId: "e2e-dm-other",
      message: makeMessage({
        id: "e2e-dm-other-sent",
        sender_id: CURRENT_USER_ID,
        sender_display_name: CURRENT_USER_NAME,
        body_text: "enviada por mim",
        created_at: "2026-07-31T09:00:00Z",
        updated_at: "2026-07-31T09:00:00Z",
      }),
    });

    await expect(names(page, "Mensagens diretas")).toHaveText([
      new RegExp(OTHER_USER_NAME),
      new RegExp(SECOND_DM_NAME),
    ]);
    await expect(names(page, "Canais")).toHaveText([
      new RegExp(OTHER_CHANNEL_NAME),
      /Canal Recente/,
    ]);
  });

  test("mantém a navegação por teclado na ordem renderizada", async ({ page }, testInfo) => {
    await openOrderedSidebar(page, testInfo);

    await page.getByRole("button", { name: "Nova conversa" }).focus();
    for (const expected of [OTHER_CHANNEL_NAME, "Canal Recente"]) {
      await page.keyboard.press("Tab");
      await expect(names(page, "Canais").filter({ hasText: expected })).toBeFocused();
    }
  });
});
