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
    // Since issue #779 the section's own collapse button and "show unread when
    // collapsed" switch are the first two stops, ahead of its rows.
    await page.keyboard.press("Tab");
    await expect(page.getByRole("button", { name: "Canais", exact: true })).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(
      page.getByRole("switch", {
        name: "Mostrar mensagens não lidas quando Canais estiver recolhida",
      }),
    ).toBeFocused();
    for (const expected of [OTHER_CHANNEL_NAME, "Canal Recente"]) {
      await page.keyboard.press("Tab");
      await expect(names(page, "Canais").filter({ hasText: expected })).toBeFocused();
      await page.keyboard.press("Tab");
      // Since issue #527 the row's second tab stop is the actions menu, not a
      // pin: the pin became state and takes no tab stop at all.
      await expect(
        page.getByRole("button", { name: `Mais opções para canal ${expected}` }),
      ).toBeFocused();
    }
  });
});

/**
 * ISSUE #527 — "…" means actions and a pin means pinned. Fixing and unfixing
 * moved into the row's action menu; the pin is no longer clickable.
 */
function rowMenu(page: Page, name: string) {
  return page.getByRole("button", { name: `Mais opções para ${name}` });
}

test.describe("sidebar — conversas fixadas", () => {
  test("fixa e desafixa pelo menu, em sua própria categoria e preservando a seleção", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = await openChatWithAllThreeCategories(page, testInfo);
    const channel = scenario.sidebarChannels[0];
    scenario.sidebarChannels.push({
      id: "e2e-channel-pinned",
      slug: "fixado",
      display_name: "Canal Fixável",
      type: "public",
      can_write: true,
      unread_count: 2,
      last_message_at: "2026-08-13T10:00:00Z",
    });

    await page.reload();
    const channels = optionsIn(page, "Canais");
    await expect(channels).toHaveText([/Canal Fixável/, new RegExp(channel.display_name)]);
    // The pin is not an action any more, on any row.
    await expect(page.getByRole("button", { name: /^Fixar .* no topo$/ })).toHaveCount(0);

    await rowMenu(page, `canal ${channel.display_name}`).click();
    await page.getByRole("menuitem", { name: "Fixar no topo" }).click();
    await expect
      .poll(() => scenario.requests.sidebarPins)
      .toEqual([{ targetId: channel.id, action: "add" }]);
    await expect(optionsIn(page, "Canais")).toHaveText([
      new RegExp(channel.display_name),
      /Canal Fixável/,
    ]);
    await expect(section(page, "Mensagens diretas").getByRole("option")).toHaveCount(1);
    await expect(section(page, "Grupos").getByRole("option")).toHaveCount(1);
    // Neither opening the menu nor running an action changed the conversation.
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));

    await rowMenu(page, `canal ${channel.display_name}`).click();
    await page.getByRole("menuitem", { name: "Desafixar" }).click();
    await expect.poll(() => scenario.requests.sidebarPins).toHaveLength(2);
    await expect(optionsIn(page, "Canais")).toHaveText([
      /Canal Fixável/,
      new RegExp(channel.display_name),
    ]);
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));
  });

  test("abre o menu pelo teclado e fixa sem sair da conversa", async ({ page }, testInfo) => {
    const { scenario, targetId } = await openChatWithAllThreeCategories(page, testInfo);
    const channel = scenario.sidebarChannels[0];

    await rowMenu(page, `canal ${channel.display_name}`).focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("menu")).toBeVisible();
    await page.keyboard.press("Enter");

    await expect
      .poll(() => scenario.requests.sidebarPins)
      .toEqual([{ targetId: channel.id, action: "add" }]);
    await expect(page.getByRole("menu")).toHaveCount(0);
    // Focus came back to the trigger, and nothing navigated.
    await expect(rowMenu(page, `canal ${channel.display_name}`)).toBeFocused();
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));
  });

  // A 1:1 conversation has no membership to leave and no title of its own, so
  // neither action exists for it. A group has both (issue #527).
  test("nunca oferece Sair nem Renomear em uma conversa direta", async ({ page }, testInfo) => {
    await openChatWithAllThreeCategories(page, testInfo);

    await rowMenu(page, `conversa com ${OTHER_USER_NAME}`).click();
    await expect(page.getByRole("menu")).toBeVisible();
    await expect(page.getByRole("menuitem", { name: /sair/i })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: /renomear/i })).toHaveCount(0);
    // What a DM does get.
    await expect(page.getByRole("menuitem", { name: "Silenciar notificações" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Detalhes da conversa" })).toBeVisible();
  });

  test("oferece sair e renomear em um grupo", async ({ page }, testInfo) => {
    await openChatWithAllThreeCategories(page, testInfo);

    await rowMenu(page, `grupo ${GROUP_DM_NAME}`).click();
    await expect(page.getByRole("menuitem", { name: "Renomear grupo" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Sair do grupo" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Detalhes do grupo" })).toBeVisible();
  });

  /**
   * The sidebar's nav is a scrollport (`overflow-y: auto`), so a popup rendered
   * inside it was clipped at its bottom edge: on the last row the menu was cut
   * in half or invisible. The assertion is geometric but not pixel-perfect —
   * every item must have a real box inside the viewport, and the last one must
   * actually be clickable, which is what a clipped menu fails.
   */
  test("mostra o menu inteiro na última linha de uma sidebar rolável", async ({
    page,
  }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    for (let index = 0; index < 24; index += 1) {
      scenario.sidebarChannels.push({
        id: `e2e-channel-scroll-${index}`,
        slug: `rolagem-${index}`,
        display_name: `Canal Rolagem ${index}`,
        type: "public",
        can_write: true,
        unread_count: 0,
        last_message_at: "2026-08-13T10:00:00Z",
      });
    }
    // Short enough that the list cannot fit, which is the whole premise.
    await page.setViewportSize({ width: 1280, height: 600 });
    await page.reload();

    const nav = page.locator(".chat-sidebar__nav");
    await expect(nav).toBeVisible();
    const scrollToBottom = () =>
      nav.evaluate((element) => {
        element.scrollTop = element.scrollHeight;
        return element.scrollTop;
      });
    // Without a real scroll there is nothing to clip and the test proves nothing.
    expect(await scrollToBottom()).toBeGreaterThan(0);

    // The last rows, not just the last one: the row flush with the scrollport's
    // bottom edge and the rows just above it fail differently, and it was one of
    // the latter — far enough from the window's bottom to open downwards, too
    // close to the scrollport's to fit inside it — that used to be cut off.
    const triggers = nav.locator(".chat-sidebar__actions-trigger");
    const total = await triggers.count();
    expect(total).toBeGreaterThan(4);

    const viewport = page.viewportSize()!;
    for (let index = total - 4; index < total; index += 1) {
      await scrollToBottom();
      await triggers.nth(index).click();
      const menu = page.getByRole("menu");
      await expect(menu).toBeVisible();

      // Geometry plus a hit test on every item. The hit test is the part that
      // catches clipping: an ancestor's `overflow` does not change an element's
      // layout box, so a clipped item still reports a bounding box — but the
      // point at its centre belongs to whatever is painted there instead.
      const items = await menu.evaluate((node) =>
        Array.from(node.querySelectorAll('[role="menuitem"]')).map((item) => {
          const box = item.getBoundingClientRect();
          const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
          return {
            width: box.width,
            height: box.height,
            top: box.top,
            bottom: box.bottom,
            left: box.left,
            right: box.right,
            reachable: node.contains(hit),
          };
        }),
      );
      expect(items.length).toBeGreaterThan(0);
      for (const item of items) {
        expect(item.width).toBeGreaterThan(0);
        expect(item.height).toBeGreaterThan(0);
        expect(item.top).toBeGreaterThanOrEqual(0);
        expect(item.bottom).toBeLessThanOrEqual(viewport.height);
        expect(item.left).toBeGreaterThanOrEqual(0);
        expect(item.right).toBeLessThanOrEqual(viewport.width);
        expect(item.reachable).toBe(true);
      }

      await page.keyboard.press("Escape");
      await expect(page.getByRole("menu")).toHaveCount(0);
    }
  });

  // Archiving and hiding are deliberately out of scope for this branch, so they
  // must not appear anywhere — not even disabled.
  test("nunca oferece arquivar ou ocultar", async ({ page }, testInfo) => {
    await openChatWithAllThreeCategories(page, testInfo);

    for (const name of [`conversa com ${OTHER_USER_NAME}`, `grupo ${GROUP_DM_NAME}`]) {
      await rowMenu(page, name).click();
      await expect(page.getByRole("menu")).toBeVisible();
      await expect(page.getByRole("menuitem", { name: /arquivar|ocultar/i })).toHaveCount(0);
      await page.keyboard.press("Escape");
    }
  });
});

/**
 * ISSUE #527 — renaming a channel. The capability decides only what the menu
 * offers; the endpoint decides whether the write lands.
 */
test.describe("sidebar — renomear canal", () => {
  test("renomeia pelo menu e o novo nome persiste após recarregar", async ({ page }, testInfo) => {
    const { scenario, targetId } = await openChatWithAllThreeCategories(page, testInfo);
    const channel = scenario.sidebarChannels[0];
    channel.can_rename = true;
    await page.reload();

    await rowMenu(page, `canal ${channel.display_name}`).click();
    await page.getByRole("menuitem", { name: "Renomear canal" }).click();

    const dialog = page.getByRole("dialog");
    await expect(dialog.getByLabel("Nome do canal")).toHaveValue(channel.display_name);
    await dialog.getByLabel("Nome do canal").fill("Plataforma");
    await dialog.getByRole("button", { name: "Salvar" }).click();

    await expect
      .poll(() => scenario.requests.channelRenames)
      .toEqual([{ channelId: channel.id, displayName: "Plataforma" }]);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(optionsIn(page, "Canais").filter({ hasText: "Plataforma" })).toHaveCount(1);
    // The rename did not open the renamed channel.
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));

    await page.reload();
    await expect(optionsIn(page, "Canais").filter({ hasText: "Plataforma" })).toHaveCount(1);
  });

  test("não oferece renomear quando o servidor não concede a permissão", async ({
    page,
  }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    const channel = scenario.sidebarChannels[0];

    await rowMenu(page, `canal ${channel.display_name}`).click();
    await expect(page.getByRole("menu")).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Renomear canal" })).toHaveCount(0);
  });

  // The menu is presentation. A caller the server refuses is refused, and the
  // sidebar keeps showing the persisted name rather than the attempted one.
  test("mantém o nome persistido quando o servidor recusa a renomeação", async ({
    page,
  }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    const channel = scenario.sidebarChannels[0];
    channel.can_rename = true;
    await page.reload();

    await rowMenu(page, `canal ${channel.display_name}`).click();
    await page.getByRole("menuitem", { name: "Renomear canal" }).click();
    // The server revokes the capability between the menu opening and the submit.
    channel.can_rename = false;

    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Nome do canal").fill("Plataforma");
    await dialog.getByRole("button", { name: "Salvar" }).click();

    await expect(dialog.getByRole("alert")).toContainText(/permissão/i);
    await expect(dialog).toBeVisible();
    await expect(optionsIn(page, "Canais").filter({ hasText: "Plataforma" })).toHaveCount(0);
    await expect(optionsIn(page, "Canais").filter({ hasText: channel.display_name })).toHaveCount(
      1,
    );
  });
});

/**
 * ISSUE #437 — the footer identifies the *authenticated* user. These cover the
 * two shapes the contract allows (with and without a picture) plus the settings
 * control's reachability, by role and by keyboard — never by pixel.
 */
test.describe("sidebar — rodapé do usuário autenticado", () => {
  const userLink = (page: Page) => page.getByRole("link", { name: /meu perfil/i });

  test("mostra o nome real e as iniciais quando não há foto", async ({ page }, testInfo) => {
    await openChatWithAllThreeCategories(page, testInfo);

    await expect(userLink(page)).toContainText(CURRENT_USER_NAME);
    await expect(userLink(page).locator("img")).toHaveCount(0);
    // Never the placeholder identity this issue removed.
    await expect(page.getByTestId("chat-sidebar")).not.toContainText("Usuário");
  });

  test("mostra a foto configurada quando existe", async ({ page }, testInfo) => {
    await openChatWithAllThreeCategories(page, testInfo);

    // Registered after the helper's default, so it takes precedence; the reload
    // is what makes the sidebar ask again.
    await page.route("**/api/auth/me", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            id: CURRENT_USER_ID,
            display_name: CURRENT_USER_NAME,
            avatar_url: "/assets/nic-labs-icon.png",
          },
        }),
      }),
    );
    await page.reload();

    await expect(userLink(page).locator("img")).toHaveAttribute("src", "/assets/nic-labs-icon.png");
  });

  test("mantém o menu da conta acionável por mouse e por teclado", async ({ page }, testInfo) => {
    await openChatWithAllThreeCategories(page, testInfo);

    const trigger = page.getByRole("button", { name: /menu da conta/i });

    await userLink(page).focus();
    await page.keyboard.press("Tab");
    await expect(trigger).toBeFocused();
    await page.keyboard.press("Enter");
    // ISSUE #672 — SidebarUserMenu deliberately has no "Administração" item:
    // there is no real capability authority on the client to gate it on, so
    // the menu stays honest about what it can actually do (Meu perfil, Sair)
    // rather than pointing at an admin action nothing here can verify.
    const profile = page.getByRole("menuitem", { name: "Meu perfil" });
    await expect(profile).toHaveAttribute("href", "/profile");
    // The menu focuses its first item on open — no extra Tab/ArrowDown needed.
    await expect(profile).toBeFocused();
    await profile.click();
    await expect(page).toHaveURL(/\/profile$/);
  });

  test("mantém o rodapé utilizável em largura reduzida", async ({ page }, testInfo) => {
    await openChatWithAllThreeCategories(page, testInfo);
    // Issue #467: below the drawer breakpoint the navigation is no longer a
    // permanent column, so it is opened before the footer inside it can be
    // asserted on. Narrowing after the load also exercises the resize itself.
    await page.setViewportSize({ width: 360, height: 720 });
    await page.getByTestId("chat-nav-toggle").click();

    const settings = page.getByRole("button", { name: /menu da conta/i });
    await expect(settings).toBeVisible();
    // The drawer slides in over 180ms, and its `visibility` flips on the very
    // first frame — so the link is "visible" while the panel is still moving.
    // boundingBox() does not wait for stability and the two reads below are two
    // separate round-trips, which would sample the sidebar's edge earlier (and
    // therefore further left) than the link's: an apparent overflow produced
    // entirely by the animation. Waiting for the slide to settle is what makes
    // both reads describe the same, final layout. Nothing about the assertion
    // itself changes.
    await page.waitForFunction(() => {
      const drawer = document.querySelector('[data-testid="chat-sidebar"]');
      return drawer !== null && getComputedStyle(drawer).transform === "none";
    });

    // The settings control stays inside the sidebar instead of being pushed out.
    const sidebar = await page.getByTestId("chat-sidebar").boundingBox();
    const box = await settings.boundingBox();
    expect(sidebar).not.toBeNull();
    expect(box).not.toBeNull();
    expect(box!.x + box!.width).toBeLessThanOrEqual(sidebar!.x + sidebar!.width + 1);
  });
});

/**
 * ISSUE #527 — the actions the row menu gained: silencing, renaming a group and
 * leaving. Each drives the real endpoints through the mocked contract, so the
 * state a scenario builds survives a reload the way it would in production.
 */
test.describe("sidebar — silenciar notificações", () => {
  test("silencia e reativa um canal, mantendo o estado", async ({ page }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    const channel = scenario.sidebarChannels[0];

    await rowMenu(page, `canal ${channel.display_name}`).click();
    await page.getByRole("menuitem", { name: "Silenciar notificações" }).click();

    await expect
      .poll(() => scenario.requests.mutes)
      .toEqual([{ targetType: "channel", targetId: channel.id, muted: true }]);
    // The menu closed, and reopening reflects the persisted preference.
    await expect(page.getByRole("menu")).toHaveCount(0);
    await rowMenu(page, `canal ${channel.display_name}`).click();
    await expect(page.getByRole("menuitem", { name: "Ativar notificações" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Silenciar notificações" })).toHaveCount(0);

    await page.getByRole("menuitem", { name: "Ativar notificações" }).click();
    await expect.poll(() => scenario.requests.mutes).toHaveLength(2);

    // It survives a reload, because the preference is the server's and not the
    // client's.
    await page.reload();
    await rowMenu(page, `canal ${channel.display_name}`).click();
    await expect(page.getByRole("menuitem", { name: "Silenciar notificações" })).toBeVisible();
  });

  test("silencia uma conversa direta", async ({ page }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);

    await rowMenu(page, `conversa com ${OTHER_USER_NAME}`).click();
    await page.getByRole("menuitem", { name: "Silenciar notificações" }).click();

    await expect.poll(() => scenario.requests.mutes.map((m) => m.targetType)).toEqual(["dm"]);
  });

  test("não oferece silenciar no canal Geral", async ({ page }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    const channel = scenario.sidebarChannels[0];
    channel.is_general = true;
    await page.reload();

    await rowMenu(page, `canal ${channel.display_name}`).click();
    await expect(page.getByRole("menu")).toBeVisible();
    await expect(page.getByRole("menuitem", { name: /notificações/i })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: /renomear/i })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: /sair/i })).toHaveCount(0);
    // What the general channel does keep.
    await expect(page.getByRole("menuitem", { name: "Fixar no topo" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Detalhes do canal" })).toBeVisible();
  });
});

test.describe("sidebar — renomear grupo", () => {
  test("renomeia pelo menu e o novo nome persiste", async ({ page }, testInfo) => {
    const { scenario, targetId } = await openChatWithAllThreeCategories(page, testInfo);
    const group = scenario.sidebarDMs.find((dm) => dm.type === "group");
    if (!group) throw new Error("fixture must carry a group");

    await rowMenu(page, `grupo ${group.name}`).click();
    await page.getByRole("menuitem", { name: "Renomear grupo" }).click();

    const dialog = page.getByRole("dialog");
    await expect(dialog.getByLabel("Nome do grupo")).toHaveValue(group.name);
    await dialog.getByLabel("Nome do grupo").fill("Piloto MVP");
    await dialog.getByRole("button", { name: "Salvar" }).click();

    await expect
      .poll(() => scenario.requests.groupRenames)
      .toEqual([{ conversationId: group.id, title: "Piloto MVP" }]);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(section(page, "Grupos").getByRole("option")).toHaveText([/Piloto MVP/]);
    // The rename did not open the renamed group.
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));

    await page.reload();
    await expect(section(page, "Grupos").getByRole("option")).toHaveText([/Piloto MVP/]);
  });

  test("mostra o evento de renomeação na timeline do grupo", async ({ page }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    const group = scenario.sidebarDMs.find((dm) => dm.type === "group");
    if (!group) throw new Error("fixture must carry a group");
    const previousName = group.name;

    await rowMenu(page, `grupo ${previousName}`).click();
    await page.getByRole("menuitem", { name: "Renomear grupo" }).click();
    await page.getByRole("dialog").getByLabel("Nome do grupo").fill("Piloto MVP");
    await page.getByRole("dialog").getByRole("button", { name: "Salvar" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);

    // Open the group and read the event the server persisted. It is a discrete
    // timeline line, never a message bubble.
    await section(page, "Grupos").getByRole("option").first().click();
    const event = page.getByTestId("chat-system-message");
    await expect(event).toContainText(
      `${CURRENT_USER_NAME} renomeou o grupo de ${previousName} para Piloto MVP`,
    );
    await expect(event.locator("button")).toHaveCount(0);
  });
});

test.describe("sidebar — sair da conversa", () => {
  test("sai de um grupo após confirmação e a linha desaparece", async ({ page }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    const group = scenario.sidebarDMs.find((dm) => dm.type === "group");
    if (!group) throw new Error("fixture must carry a group");

    await rowMenu(page, `grupo ${group.name}`).click();
    await page.getByRole("menuitem", { name: "Sair do grupo" }).click();

    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText(`Sair de ${group.name}?`);
    // The safe action holds focus, so a stray Enter cannot make someone leave.
    await expect(dialog.getByRole("button", { name: "Cancelar" })).toBeFocused();

    await dialog.getByRole("button", { name: "Sair do grupo" }).click();

    await expect
      .poll(() => scenario.requests.leaves)
      .toEqual([{ targetType: "dm", targetId: group.id }]);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(section(page, "Grupos").getByRole("option")).toHaveCount(0);
  });

  test("cancela a saída sem chamar o servidor", async ({ page }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    const group = scenario.sidebarDMs.find((dm) => dm.type === "group");
    if (!group) throw new Error("fixture must carry a group");

    await rowMenu(page, `grupo ${group.name}`).click();
    await page.getByRole("menuitem", { name: "Sair do grupo" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Cancelar" }).click();

    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(scenario.requests.leaves).toEqual([]);
    await expect(section(page, "Grupos").getByRole("option")).toHaveCount(1);
  });

  /**
   * Leaving the conversation on screen (issue #527, code review). The row going
   * away is not enough: the route still names a channel this user is no longer
   * in, and the message area would keep asking for it. The reader is put back on
   * the neutral route instead.
   */
  test("volta para a rota neutra ao sair do canal aberto", async ({ page }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    const channel = scenario.sidebarChannels[0];

    await optionsIn(page, "Canais").filter({ hasText: channel.display_name }).click();
    await expect(page).toHaveURL(new RegExp(`/chat/channel/${channel.id}$`));

    await rowMenu(page, `canal ${channel.display_name}`).click();
    await page.getByRole("menuitem", { name: "Sair do canal" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Sair do canal" }).click();

    await expect
      .poll(() => scenario.requests.leaves)
      .toEqual([{ targetType: "channel", targetId: channel.id }]);
    await expect(page).toHaveURL(/\/chat$/);
    await expect(
      section(page, "Canais").getByRole("option").filter({ hasText: channel.display_name }),
    ).toHaveCount(0);
  });

  test("sai de um canal normal e a linha desaparece", async ({ page }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    const channel = scenario.sidebarChannels[0];

    await rowMenu(page, `canal ${channel.display_name}`).click();
    await page.getByRole("menuitem", { name: "Sair do canal" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Sair do canal" }).click();

    await expect
      .poll(() => scenario.requests.leaves)
      .toEqual([{ targetType: "channel", targetId: channel.id }]);
    await expect(
      section(page, "Canais").getByRole("option").filter({ hasText: channel.display_name }),
    ).toHaveCount(0);
  });
});

/**
 * ISSUE #779 — each section can be collapsed independently, and a collapsed
 * section can be told to show only conversations with unread. Expanded, the
 * preference has no filtering effect at all.
 */
test.describe("sidebar — seções recolhíveis e filtro de não lidas", () => {
  function collapseButton(page: Page, title: string) {
    return page.getByRole("button", { name: title, exact: true });
  }
  function unreadSwitch(page: Page, title: string) {
    return page.getByRole("switch", {
      name: `Mostrar mensagens não lidas quando ${title} estiver recolhida`,
    });
  }

  test("recolhe, filtra por não lidas, atualiza em tempo real e restaura ao expandir", async ({
    page,
  }, testInfo) => {
    const { scenario } = await openChatWithAllThreeCategories(page, testInfo);
    const readChannel = scenario.sidebarChannels[0];
    const unreadChannelId = "e2e-channel-unread";
    scenario.sidebarChannels.push({
      id: unreadChannelId,
      slug: "com-nao-lidas",
      display_name: "Canal Com Não Lidas",
      type: "public",
      can_write: true,
      unread_count: 3,
    });
    await page.reload();
    await expect(page.getByRole("heading", { name: "Canais" })).toBeVisible();

    const collapse = collapseButton(page, "Canais");
    const toggle = unreadSwitch(page, "Canais");
    await expect(collapse).toHaveAttribute("aria-expanded", "true");
    await expect(toggle).toHaveAttribute("aria-checked", "false");

    // Expandida + unread ligado: a opção não filtra nada.
    await toggle.click();
    // O clique realmente alternou o estado — se o toggle voltar a ser no-op
    // (por exemplo, workspaceId vazio no fixture), o teste falha aqui, no
    // ponto exato da causa, em vez de mais adiante numa asserção de conteúdo.
    await expect(toggle).toHaveAttribute("aria-checked", "true");
    await expect(optionsIn(page, "Canais")).toHaveCount(2);

    // Recolhida + unread ligado: só o canal com não lidas aparece.
    await collapse.click();
    await expect(collapse).toHaveAttribute("aria-expanded", "false");
    await expect(optionsIn(page, "Canais")).toHaveCount(1);
    await expect(optionsIn(page, "Canais")).toHaveText([/Canal Com Não Lidas/]);

    // Abrir a conversa não lida funciona normalmente e ela some da seção
    // recolhida assim que fica lida — sem navegação nem remontagem indevida.
    await optionsIn(page, "Canais").first().click();
    await expect(page).toHaveURL(new RegExp(`/chat/channel/${unreadChannelId}$`));
    await expect(section(page, "Canais").getByRole("option")).toHaveCount(0);

    // Mensagem em tempo real em outro canal o traz de volta à seção recolhida.
    await emitMessageCreated(page, scenario, {
      kind: "channel",
      targetId: readChannel.id,
      message: makeMessage({
        id: `${readChannel.id}-incoming`,
        sender_id: OTHER_USER_ID,
        sender_display_name: OTHER_USER_NAME,
        body_text: "chegou agora",
        created_at: "2026-08-20T10:00:00Z",
        updated_at: "2026-08-20T10:00:00Z",
      }),
    });
    await expect(section(page, "Canais").getByRole("option")).toHaveText([
      new RegExp(readChannel.display_name),
    ]);

    // Recolhida + unread desligado: nenhum item, e nenhuma mensagem de vazio.
    await toggle.click();
    await expect(section(page, "Canais").getByRole("option")).toHaveCount(0);
    await expect(section(page, "Canais")).not.toContainText("Nenhum canal disponível");

    // Expandir de novo restaura a visão completa, sem perder nenhum canal.
    await collapse.click();
    await expect(optionsIn(page, "Canais")).toHaveCount(2);
  });

  test("mantém o estado de cada seção independente e não navega ao recolher a conversa aberta", async ({
    page,
  }, testInfo) => {
    const { targetId } = await openChatWithAllThreeCategories(page, testInfo);

    // A conversa aberta é a DM do cenário — recolher Canais não deve mudar a rota.
    await collapseButton(page, "Canais").click();
    await expect(page).toHaveURL(new RegExp(`/chat/dm/${targetId}$`));
    await expect(collapseButton(page, "Mensagens diretas")).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    await expect(collapseButton(page, "Grupos")).toHaveAttribute("aria-expanded", "true");

    // Mensagens diretas continua mostrando sua conversa normalmente.
    await expect(optionsIn(page, "Mensagens diretas")).toHaveCount(1);
  });
});
