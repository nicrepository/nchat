import { expect, test, type Page } from "@playwright/test";

import {
  GROUP_DM_ID,
  GROUP_DM_NAME,
  OTHER_CHANNEL_NAME,
  OTHER_USER_NAME,
  createScenario,
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
