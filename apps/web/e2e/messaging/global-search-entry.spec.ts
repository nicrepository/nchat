import { expect, test, type Page } from "@playwright/test";

import {
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Issue #550 — the global search entry lives at the top of the sidebar and
 * the button and Ctrl+K open the same search. Placement, naming and the
 * command wiring are proven by component tests; what only a browser proves is
 * the round trip: the search takes focus, Escape leaves it, and the
 * conversation — draft included — is still there, with focus back on the
 * control that opened it.
 */

const PHONE = { width: 390, height: 844 };

const searchButton = (page: Page) => page.getByRole("button", { name: "Buscar no NChat" });
const searchField = (page: Page) =>
  page.getByRole("searchbox", { name: "Buscar mensagens, pessoas, canais, grupos e arquivos" });
const composerInput = (page: Page) => page.getByTestId("chat-composer-input");

async function openConversation(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
  const targetId = uniqueId(testInfo, "dm");
  const original = makeMessage({
    id: `${targetId}-original`,
    sender_id: OTHER_USER_ID,
    sender_display_name: OTHER_USER_NAME,
    body_text: "mensagem que já estava aqui",
  });
  const scenario = createScenario({
    kind: "dm",
    targetId,
    targetName: OTHER_USER_NAME,
    messages: [original],
  });
  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/dm/${targetId}`);
  await expect(messageBubble(page, original.id)).toBeVisible();
  return { targetId, original };
}

test.describe("busca global — ponto de entrada na sidebar (#550)", () => {
  test("botão e Ctrl+K abrem a busca; Escape volta com a conversa e o rascunho intactos", async ({
    page,
  }, testInfo) => {
    const { targetId, original } = await openConversation(page, testInfo);
    await fillComposer(page, "rascunho que não pode sumir");

    await searchButton(page).click();
    await expect(page).toHaveURL(/\/chat\/search$/);
    await expect(searchField(page)).toBeFocused();

    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));
    await expect(messageBubble(page, original.id)).toBeVisible();
    await expect(composerInput(page)).toHaveText("rascunho que não pode sumir");
    await expect(searchButton(page)).toBeFocused();

    // Focus is on the button, not in an editor, so the shortcut applies.
    await page.keyboard.press("Control+k");
    await expect(searchField(page)).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(composerInput(page)).toHaveText("rascunho que não pode sumir");
  });

  test("celular: a busca fica no topo do drawer e Escape devolve o foco ao toggle", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    await openConversation(page, testInfo);

    await page.getByTestId("chat-nav-toggle").click();
    await expect(searchButton(page)).toBeVisible();
    await searchButton(page).click();
    await expect(searchField(page)).toBeFocused();

    // Immediately — while the drawer may still be mid-transition. Opening the
    // search closed it, so the button inside it is not where focus returns:
    // the toggle is, the one control on screen in every state.
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("chat-nav-toggle")).toBeFocused();
    await expect(page.getByTestId("chat-sidebar")).toBeHidden();
    // Still there once the transition has ended, not only at its start.
    await expect(page.getByTestId("chat-nav-toggle")).toBeFocused();
  });
});
