import { expect, type Locator, type Page, test, type TestInfo } from "@playwright/test";

import {
  OTHER_USER_NAME,
  SECOND_CANDIDATE_ID,
  SECOND_CANDIDATE_NAME,
  THIRD_CANDIDATE_ID,
  THIRD_CANDIDATE_NAME,
  createScenario,
  emitConversationAvailable,
  grantConversationAccess,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Issue #1025: canal privado com membros escolhidos na criação.
 *
 * O backend aqui é o mock do repositório, que reproduz o contrato HTTP —
 * initial_member_ids, Idempotency-Key, replay 200. Atomicidade, elegibilidade e
 * concorrência são provadas contra PostgreSQL real em chat-service
 * (channel_create_members_postgres_test.go); este arquivo prova o que só um
 * navegador prova: o fluxo, o teclado, o draft e o que cada lado enxerga.
 */

const CREATED_ID = "e2e-channel-created-1";
const CHANNEL_NAME = "Projeto Sigiloso";

/** The chat with two eligible people, ready for "Nova conversa". */
async function prepareCreator(page: Page, testInfo: TestInfo) {
  const targetId = uniqueId(testInfo, "dm");
  const scenario = createScenario({
    kind: "dm",
    targetId,
    targetName: OTHER_USER_NAME,
    messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
    dmCandidates: [
      { userId: SECOND_CANDIDATE_ID, displayName: SECOND_CANDIDATE_NAME },
      { userId: THIRD_CANDIDATE_ID, displayName: THIRD_CANDIDATE_NAME },
    ],
  });
  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/dm/${targetId}`);
  await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();
  // The channel the mock will create is readable by its creator once it exists.
  await grantConversationAccess(page, { kind: "channel", targetId: CREATED_ID });
  return scenario;
}

async function openChannelFlow(page: Page, testInfo: TestInfo) {
  const scenario = await prepareCreator(page, testInfo);
  await page.getByRole("button", { name: "Nova conversa" }).click();
  const dialog = page.getByRole("dialog", { name: "Nova conversa" });
  await dialog.getByRole("radio", { name: "Canal" }).check();
  await dialog.getByLabel("Nome do canal").fill(CHANNEL_NAME);
  return { scenario, dialog };
}

/** Keyboard focus, as the browser itself reports it — not a programmatic one. */
async function expectKeyboardFocus(locator: Locator) {
  await expect(locator).toBeFocused();
  expect(await locator.evaluate((element) => element.matches(":focus-visible"))).toBe(true);
}

async function invite(dialog: ReturnType<Page["getByRole"]>, name: string) {
  await dialog.getByRole("searchbox", { name: "Pesquisar pessoa" }).fill(name);
  await dialog
    .getByRole("list", { name: "Pessoas encontradas" })
    .getByRole("button", { name })
    .click();
}

test.describe("canal privado — membros iniciais (#1025)", () => {
  test("cria com dois convidados, criador fixo, e a alternância Público/Privado preserva a seleção", async ({
    page,
  }, testInfo) => {
    const { scenario, dialog } = await openChannelFlow(page, testInfo);
    await dialog.getByRole("radio", { name: "Privado" }).check();

    const members = dialog.getByRole("list", { name: "Membros selecionados" });
    const creator = members.getByRole("listitem").filter({ hasText: "Você (criador)" });
    await expect(creator).toBeVisible();
    await expect(creator.getByRole("button")).toHaveCount(0);

    await invite(dialog, SECOND_CANDIDATE_NAME);
    await invite(dialog, THIRD_CANDIDATE_NAME);
    await expect(dialog.getByText("3 membros, incluindo você.")).toBeVisible();
    await expect(
      dialog.getByText("Canal privado: somente você e 2 convidados terão acesso."),
    ).toBeVisible();

    await dialog.getByRole("radio", { name: "Público" }).check();
    await expect(dialog.getByRole("searchbox", { name: "Pesquisar pessoa" })).toHaveCount(0);
    await expect(dialog.getByText("Canal público: todo o workspace poderá entrar.")).toBeVisible();
    await dialog.getByRole("radio", { name: "Privado" }).check();
    await expect(members).toContainText(SECOND_CANDIDATE_NAME);
    await expect(members).toContainText(THIRD_CANDIDATE_NAME);

    await dialog.getByRole("button", { name: "Criar canal" }).click();

    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(new RegExp(`/chat/channel/${CREATED_ID}$`));
    await expect(page.getByRole("option", { name: new RegExp(CHANNEL_NAME) })).toHaveCount(1);

    expect(scenario.requests.channelCreates).toHaveLength(1);
    const [{ body, idempotencyKey }] = scenario.requests.channelCreates;
    expect(idempotencyKey).toEqual(expect.any(String));
    // Only people travel: no role, owner, creator or workspace from the browser.
    expect(body).toEqual({
      slug: "projeto-sigiloso",
      display_name: CHANNEL_NAME,
      type: "private",
      initial_member_ids: [SECOND_CANDIDATE_ID, THIRD_CANDIDATE_ID],
    });
  });

  test("público não envia a seleção mantida", async ({ page }, testInfo) => {
    const { scenario, dialog } = await openChannelFlow(page, testInfo);
    await dialog.getByRole("radio", { name: "Privado" }).check();
    await invite(dialog, SECOND_CANDIDATE_NAME);
    await dialog.getByRole("radio", { name: "Público" }).check();

    await dialog.getByRole("button", { name: "Criar canal" }).click();
    await expect(dialog).toBeHidden();
    expect(scenario.requests.channelCreates[0].body).not.toHaveProperty("initial_member_ids");
    expect(scenario.requests.channelCreates[0].body.type).toBe("public");
  });

  test("clique duplo envia uma única criação", async ({ page }, testInfo) => {
    const { scenario, dialog } = await openChannelFlow(page, testInfo);
    await dialog.getByRole("radio", { name: "Privado" }).check();
    await invite(dialog, SECOND_CANDIDATE_NAME);

    await dialog.getByRole("button", { name: "Criar canal" }).dblclick();

    await expect(page).toHaveURL(new RegExp(`/chat/channel/${CREATED_ID}$`));
    expect(scenario.requests.channelCreates).toHaveLength(1);
    await expect(page.getByRole("option", { name: new RegExp(CHANNEL_NAME) })).toHaveCount(1);
  });

  test("resposta perdida: o draft fica, o retry reusa a chave e não duplica o canal", async ({
    page,
  }, testInfo) => {
    const { scenario, dialog } = await openChannelFlow(page, testInfo);
    // The server commits the first attempt, but its answer never arrives.
    scenario.channelCreateLostResponses = 1;
    await dialog.getByRole("radio", { name: "Privado" }).check();
    await invite(dialog, SECOND_CANDIDATE_NAME);
    const submit = dialog.getByRole("button", { name: "Criar canal" });

    await submit.click();
    await expect(dialog.getByRole("alert")).toHaveText(
      "Sem conexão. Verifique sua rede e tente novamente.",
    );
    await expect(dialog.getByLabel("Nome do canal")).toHaveValue(CHANNEL_NAME);
    await expect(dialog.getByRole("list", { name: "Membros selecionados" })).toContainText(
      SECOND_CANDIDATE_NAME,
    );

    await submit.click();
    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(new RegExp(`/chat/channel/${CREATED_ID}$`));
    await expect(page.getByRole("option", { name: new RegExp(CHANNEL_NAME) })).toHaveCount(1);

    const [first, retry] = scenario.requests.channelCreates;
    expect(retry.idempotencyKey).toBe(first.idempotencyKey);
    expect(scenario.sidebarChannels.filter((channel) => channel.id === CREATED_ID)).toHaveLength(1);
  });
});

/**
 * O seletor de membros inteiro por teclado, num navegador real: Tab percorre o
 * formulário na ordem visível, setas trocam modo e tipo, Enter/Espaço escolhem e
 * removem pessoas — e nenhum Enter dado na busca ou num resultado cria o canal
 * antes do Enter final em "Criar canal".
 */
test.describe("canal privado — seletor de membros por teclado (#1025)", () => {
  test("escolhe, remove e cria só por teclado, sem criação prematura", async ({
    page,
  }, testInfo) => {
    const scenario = await prepareCreator(page, testInfo);
    const trigger = page.getByRole("button", { name: "Nova conversa" });
    await trigger.focus();
    await page.keyboard.press("Enter");
    const dialog = page.getByRole("dialog", { name: "Nova conversa" });
    await expect(dialog).toBeVisible();

    // Pessoa → Canal pelas setas do seletor de modo.
    await page.keyboard.press("Shift+Tab");
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowRight");
    await expectKeyboardFocus(dialog.getByRole("radio", { name: "Canal" }));

    // Tab entra no tipo do canal; a seta escolhe Privado.
    await page.keyboard.press("Tab");
    await expectKeyboardFocus(dialog.getByRole("radio", { name: "Público" }));
    await page.keyboard.press("ArrowRight");
    const privateRadio = dialog.getByRole("radio", { name: "Privado" });
    await expect(privateRadio).toBeChecked();

    await page.keyboard.press("Tab");
    await expectKeyboardFocus(dialog.getByLabel("Nome do canal"));
    await page.keyboard.type(CHANNEL_NAME);
    await page.keyboard.press("Tab");
    await expectKeyboardFocus(dialog.getByLabel("Identificador"));
    await page.keyboard.press("Tab");
    await expectKeyboardFocus(dialog.getByLabel("Categoria"));
    await page.keyboard.press("Tab");
    const search = dialog.getByRole("searchbox", { name: "Pesquisar pessoa" });
    await expectKeyboardFocus(search);

    // Enter na busca procura pessoas; não cria o canal.
    await page.keyboard.type("E2E Candidata");
    await page.keyboard.press("Enter");
    const results = dialog.getByRole("list", { name: "Pessoas encontradas" });
    await expect(results.getByRole("button")).toHaveCount(2);
    await expect(search).toBeFocused();

    // Tab da busca para o primeiro resultado; Enter o escolhe.
    await page.keyboard.press("Tab");
    const second = results.getByRole("button", { name: SECOND_CANDIDATE_NAME });
    await expectKeyboardFocus(second);
    await page.keyboard.press("Enter");
    const third = results.getByRole("button", { name: THIRD_CANDIDATE_NAME });
    await third.focus();
    await page.keyboard.press("Space");

    const members = dialog.getByRole("list", { name: "Membros selecionados" });
    await expect(members).toContainText(SECOND_CANDIDATE_NAME);
    await expect(members).toContainText(THIRD_CANDIDATE_NAME);
    await expect(dialog.getByText("3 membros, incluindo você.")).toBeVisible();
    expect(scenario.requests.channelCreates).toHaveLength(0);

    // Remove uma pessoa pelo próprio botão do chip.
    const removeSecond = members.getByRole("button", { name: `Remover ${SECOND_CANDIDATE_NAME}` });
    await removeSecond.focus();
    await page.keyboard.press("Enter");
    await expect(members).not.toContainText(SECOND_CANDIDATE_NAME);
    await expect(
      dialog.getByText("Canal privado: somente você e 1 convidado terão acesso."),
    ).toBeVisible();
    expect(scenario.requests.channelCreates).toHaveLength(0);

    // O Enter que cria é o do botão "Criar canal", alcançado por Tab a partir
    // da busca limpa: busca → chip restante → Criar canal.
    await search.focus();
    await page.keyboard.press("ControlOrMeta+A");
    await page.keyboard.press("Delete");
    await expect(results).toHaveCount(0);
    await page.keyboard.press("Tab");
    await expectKeyboardFocus(
      members.getByRole("button", { name: `Remover ${THIRD_CANDIDATE_NAME}` }),
    );
    await page.keyboard.press("Tab");
    await expectKeyboardFocus(dialog.getByRole("button", { name: "Criar canal" }));
    await page.keyboard.press("Enter");

    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(new RegExp(`/chat/channel/${CREATED_ID}$`));
    expect(scenario.requests.channelCreates).toHaveLength(1);
    expect(scenario.requests.channelCreates[0].body).toEqual({
      slug: "projeto-sigiloso",
      display_name: CHANNEL_NAME,
      type: "private",
      initial_member_ids: [THIRD_CANDIDATE_ID],
    });
  });
});

/**
 * Os dois lados que o criador não vê: quem foi convidado recebe o canal em
 * tempo real (conversation.available, publicado pelo servidor após o commit);
 * quem não foi não o lista e não o abre.
 */
test.describe("canal privado — visibilidade para convidado e não convidado (#1025)", () => {
  async function viewerPage(page: Page, testInfo: TestInfo, forbidden: boolean) {
    const targetId = uniqueId(testInfo, "dm");
    const channelId = uniqueId(testInfo, "private");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-msg`, body_text: "olá" })],
    });
    scenario.messagesByTarget.set(`channel:${channelId}`, [
      makeMessage({ id: `${channelId}-msg`, body_text: "plano confidencial" }),
    ]);
    await installMessagingMocks(page, scenario, {
      forbiddenTargetIds: forbidden ? [channelId] : [],
    });
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();
    return { scenario, channelId };
  }

  test("convidado: o canal aparece sem recarregar e abre", async ({ page }, testInfo) => {
    const { scenario, channelId } = await viewerPage(page, testInfo, false);
    const row = page.getByRole("option", { name: new RegExp(CHANNEL_NAME) });
    await expect(row).toHaveCount(0);

    scenario.sidebarChannels.push({
      id: channelId,
      slug: "projeto-sigiloso",
      display_name: CHANNEL_NAME,
      type: "private",
      can_write: true,
      unread_count: 0,
    });
    await grantConversationAccess(page, { kind: "channel", targetId: channelId });
    await emitConversationAvailable(page, { kind: "channel", targetId: channelId });

    await expect(row).toBeVisible();
    await row.click();
    await expect(page).toHaveURL(new RegExp(`/chat/channel/${channelId}$`));
    await expect(page.getByText("plano confidencial")).toBeVisible();
  });

  test("não convidado: não lista, não abre e não vê conteúdo", async ({ page }, testInfo) => {
    const { channelId } = await viewerPage(page, testInfo, true);
    await expect(page.getByRole("option", { name: new RegExp(CHANNEL_NAME) })).toHaveCount(0);

    await page.goto(`/chat/channel/${channelId}`);
    await expect(page.getByTestId("chat-msg-access-denied")).toBeVisible();
    await expect(page.getByText("plano confidencial")).toHaveCount(0);
    await expect(page.getByTestId("chat-composer-box")).toHaveCount(0);
  });
});
