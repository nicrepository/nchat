import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  OTHER_CHANNEL_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  channelDetailsFixture,
  createScenario,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * ISSUE #467 — the shell is one layout with three compositions. These specs
 * drive the behaviour that changes between them; how each one looks is CSS and
 * is not asserted here.
 *
 * The viewport matrix is parametrised rather than written out eight times: the
 * assertions are identical at every size (nothing overflows the viewport, the
 * essential actions are reachable), and only the composition differs.
 */

/** Deliberately hostile content: nothing here may widen the page. */
const LONG_NAME =
  "Canal de Infraestrutura, Redes, Observabilidade e Resposta a Incidentes do NIC-Labs";
const LONG_URL =
  "https://exemplo.nic-labs.com.br/relatorios/2026/infraestrutura/backup-incremental-consolidado-com-um-caminho-absurdamente-longo-sem-espacos?parametro=valor";
const LONG_WORD = "Supercalifragilisticoexpialidosoemumapalavrasemespacosnenhumparaquebrar".repeat(
  2,
);

const WIDE = { width: 1440, height: 900 };
const TABLET = { width: 768, height: 1024 };
const PHONE = { width: 390, height: 844 };

/** Every size the issue's minimum visual matrix names. */
const VIEWPORT_MATRIX = [
  { width: 1920, height: 1080 },
  { width: 1440, height: 900 },
  { width: 1366, height: 768 },
  { width: 1280, height: 720 },
  { width: 1024, height: 768 },
  { width: 768, height: 1024 },
  { width: 390, height: 844 },
  { width: 360, height: 800 },
];

async function openChannel(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
  const targetId = uniqueId(testInfo, "responsive");
  const scenario = createScenario({
    kind: "channel",
    targetId,
    targetName: LONG_NAME,
    messages: [
      makeMessage({ id: `${targetId}-m1`, body_text: `Relatório publicado em ${LONG_URL}` }),
      makeMessage({ id: `${targetId}-m2`, body_text: LONG_WORD }),
    ],
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
          {
            user_id: OTHER_USER_ID,
            display_name: OTHER_USER_NAME,
            role: "member",
            presence: "online",
          },
        ],
        7,
      ),
    );
  }
  scenario.channelAttachments.set(targetId, [
    {
      id: `${targetId}-file`,
      filename: "relatorio-de-capacidade-e-observabilidade-consolidado-2026.pdf",
      contentType: "application/pdf",
      size: 4_194_304,
      status: "clean",
      createdAt: "2026-07-15T12:24:00Z",
    },
  ]);

  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/channel/${targetId}`);
  await expect(page.getByTestId("chat-msg-header")).toBeVisible();
  return { scenario, targetId };
}

/**
 * The same channel, with a conversation list far taller than any viewport.
 *
 * This is the shape that exposed the bug: enough rows that the sidebar's nav has
 * to scroll internally, which is exactly when an unclipped descendant of it can
 * grow the *document's* scroll area instead. The rows are real fixture channels,
 * never a CSS trick — a list that had been shortened, or a nav switched to
 * `overflow: hidden`, would make the assertion pass while proving nothing.
 */
async function openChannelWithLongSidebar(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
  const targetId = uniqueId(testInfo, "long-sidebar");
  const scenario = createScenario({
    kind: "channel",
    targetId,
    targetName: LONG_NAME,
    messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Primeira mensagem" })],
  });
  for (let index = 0; index < 40; index += 1) {
    scenario.sidebarChannels.push({
      id: `${targetId}-extra-${index}`,
      slug: `canal-extra-${index}`,
      display_name: `Canal de acompanhamento ${index}`,
      type: "public",
      can_write: true,
      unread_count: 0,
    });
  }
  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/channel/${targetId}`);
  await expect(page.getByTestId("chat-msg-header")).toBeVisible();
  return { scenario, targetId };
}

/**
 * The acceptance criterion "no global horizontal scrolling", asserted on the
 * document itself rather than on any one element: a bubble, a URL or an
 * attachment card that widened the page shows up here whatever produced it.
 * One pixel of tolerance for sub-pixel layout rounding.
 */
async function expectNoHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(() => {
    const root = document.documentElement;
    return Math.max(
      root.scrollWidth - root.clientWidth,
      document.body.scrollWidth - root.clientWidth,
    );
  });
  expect(overflow).toBeLessThanOrEqual(1);
}

/**
 * The conversation column, and the message list inside it, must not scroll
 * sideways either (issue #496 regression).
 *
 * The document-level check above is not enough on its own, and that is exactly
 * how this regression shipped: the list has `overflow-y: auto`, which makes its
 * computed `overflow-x` `auto` too, so it can grow its own horizontal scrollbar
 * while the page around it stays exactly the right width.
 */
async function expectNoConversationScroll(page: Page, moment: string) {
  const overflow = await page.evaluate(() => {
    const measure = (selector: string) => {
      const element = document.querySelector(selector);
      return element ? element.scrollWidth - element.clientWidth : 0;
    };
    return {
      conversation: measure(".chat-msg-area__conversation"),
      list: measure(".chat-msg-area__list"),
    };
  });
  expect(overflow, `rolagem horizontal na conversa (${moment})`).toEqual({
    conversation: 0,
    list: 0,
  });
}

/**
 * The document's own scroll geometry, plus where the shell actually sits in it.
 *
 * Reported as numbers rather than asserted in place because the failure this
 * guards against is a band of empty page below a correctly sized shell — and
 * the only way to tell that apart from a shell that is simply too short is to
 * read both at once.
 */
async function readRootGeometry(page: Page) {
  return page.evaluate(() => {
    const root = document.documentElement;
    const element = document.querySelector('[data-testid="chat-shell"]');
    const rect = element?.getBoundingClientRect();
    return {
      scrollY: window.scrollY,
      scrollHeight: root.scrollHeight,
      clientHeight: root.clientHeight,
      shell: rect ? { top: rect.top, bottom: rect.bottom, height: rect.height } : null,
    };
  });
}

/**
 * A chat route is a full-screen surface: the document under it never scrolls
 * (issue #467 follow-up).
 *
 * Three facts, and all three are needed. `scrollY === 0` alone passes on a page
 * that *can* scroll and merely happens to be at the top; `scrollHeight` alone
 * passes on a shell that has drifted off the top of a document that fits. The
 * shell's own edges are what prove the fix is geometry and not a hidden
 * overflow painted over the symptom. One pixel of tolerance for sub-pixel
 * layout rounding, nothing more.
 */
async function expectNoRootScroll(page: Page, label: string) {
  const geometry = await readRootGeometry(page);
  expect(geometry, label).toMatchObject({ scrollY: 0 });
  expect(geometry.scrollHeight, `${label}: altura de rolagem do documento`).toBeLessThanOrEqual(
    geometry.clientHeight + 1,
  );
  expect(geometry.shell, `${label}: shell ausente`).not.toBeNull();
  expect(Math.abs(geometry.shell!.top), `${label}: topo do shell`).toBeLessThanOrEqual(1);
  expect(
    Math.abs(geometry.shell!.bottom - geometry.clientHeight),
    `${label}: base do shell`,
  ).toBeLessThanOrEqual(1);
  return geometry;
}

/** The sidebar's scrollport — the container long conversation lists belong in. */
async function readSidebarNav(page: Page) {
  return page.evaluate(() => {
    const nav = document.querySelector(".chat-sidebar__nav");
    if (!nav) return null;
    return {
      clientHeight: nav.clientHeight,
      scrollHeight: nav.scrollHeight,
      scrollTop: nav.scrollTop,
    };
  });
}

const navToggle = (page: Page) => page.getByTestId("chat-nav-toggle");
const detailsToggle = (page: Page) => page.getByTestId("chat-details-toggle");
const composer = (page: Page) => page.getByTestId("chat-composer-input");

test.describe("layout responsivo", () => {
  test("celular: navega da lista para a conversa, volta, abre detalhes e envia", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { scenario } = await openChannel(page, testInfo);

    // Uma área principal por vez: a navegação começa fora da tela.
    await expect(page.getByTestId("chat-sidebar")).toBeHidden();
    await expect(navToggle(page)).toBeVisible();
    await expectNoHorizontalScroll(page);

    // Lista → conversa.
    await navToggle(page).click();
    await expect(page.getByTestId("chat-sidebar")).toBeVisible();
    await expect(page.getByRole("main")).toHaveAttribute("inert", "");
    await page.getByRole("option", { name: new RegExp(OTHER_CHANNEL_NAME) }).click();
    await expect(page.getByTestId("chat-sidebar")).toBeHidden();
    await expect(page.getByRole("main")).not.toHaveAttribute("inert", "");
    await expect(page.getByTestId("chat-msg-header")).toContainText(OTHER_CHANNEL_NAME);

    // Retorno à lista, e de volta para a conversa original.
    await navToggle(page).click();
    await expect(page.getByTestId("chat-sidebar")).toBeVisible();
    await page.getByRole("option", { name: new RegExp(LONG_NAME.slice(0, 20)) }).click();
    await expect(page.getByTestId("chat-msg-header")).toContainText(LONG_NAME.slice(0, 20));

    // Detalhes ocupam a tela; a conversa fica atrás, intacta e fora de alcance.
    await detailsToggle(page).click();
    const details = page.getByTestId("chat-conversation-details");
    await expect(details).toBeVisible();
    await expect(composer(page)).toBeHidden();
    await expectNoHorizontalScroll(page);

    // Retorno claro ao chat, com o foco de volta no acionador.
    await page.getByRole("button", { name: "Fechar detalhes do canal" }).click();
    await expect(details).toBeHidden();
    await expect(detailsToggle(page)).toBeFocused();

    // Envio continua funcionando na viewport móvel.
    await fillComposer(page, "mensagem enviada do celular");
    await page.getByTestId("chat-send-btn").click();
    await expect
      .poll(() => scenario.requests.channelPosts.length, { timeout: 5_000 })
      .toBeGreaterThan(0);
    expect(scenario.requests.channelPosts.at(-1)?.body_text).toContain(
      "mensagem enviada do celular",
    );
    await expectNoHorizontalScroll(page);
  });

  test("celular: Escape fecha a navegação e devolve o foco ao acionador", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    await openChannel(page, testInfo);

    await navToggle(page).click();
    await expect(page.getByTestId("chat-sidebar")).toBeVisible();

    await page.keyboard.press("Escape");

    await expect(page.getByTestId("chat-sidebar")).toBeHidden();
    await expect(navToggle(page)).toBeFocused();
  });

  /**
   * The mobile half of the focus-return fix (issue #467, code quality review).
   * Only a real browser can produce it: the row that opened the panel is still
   * mounted and still connected, and cannot hold focus purely because the drawer
   * around it is hidden — a CSS fact jsdom has no way to reproduce.
   */
  test("celular: fechar os detalhes abertos pela sidebar devolve o foco ao toggle de navegação", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    await openChannel(page, testInfo);

    await navToggle(page).click();
    const rowTrigger = page.getByRole("button", {
      name: `Mais opções para canal ${OTHER_CHANNEL_NAME}`,
    });
    await rowTrigger.click();
    await page.getByRole("menuitem", { name: "Detalhes do canal" }).click();

    // Opening from the row closes the drawer, so the trigger is now inside a
    // hidden sidebar: mounted, but unable to take focus back.
    const details = page.getByTestId("chat-conversation-details");
    await expect(details).toBeVisible();
    await expect(page.getByTestId("chat-sidebar")).toBeHidden();
    await expect(rowTrigger).toBeHidden();

    await page.getByRole("button", { name: "Fechar detalhes do canal" }).click();

    await expect(details).toBeHidden();
    await expect(navToggle(page)).toBeFocused();
  });

  test("redimensionar de desktop para tablet preserva conversa, rascunho e detalhes", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(WIDE);
    await openChannel(page, testInfo);

    // Desktop amplo: a navegação é coluna, não há disclosure a acionar.
    await expect(page.getByTestId("chat-sidebar")).toBeVisible();
    await expect(navToggle(page)).toBeHidden();

    await fillComposer(page, "rascunho que precisa sobreviver ao resize");
    await detailsToggle(page).click();
    await expect(page.getByTestId("chat-conversation-details")).toBeVisible();

    await page.setViewportSize(TABLET);

    // Sem reload: a conversa, o rascunho e o painel continuam de pé; só a
    // composição mudou — a navegação virou drawer.
    await expect(page.getByTestId("chat-msg-header")).toContainText(LONG_NAME.slice(0, 20));
    await expect(page.getByTestId("chat-conversation-details")).toBeVisible();
    await expect(navToggle(page)).toBeVisible();
    await expect(page.getByTestId("chat-sidebar")).toBeHidden();
    await expectNoHorizontalScroll(page);

    await page.getByRole("button", { name: "Fechar detalhes do canal" }).click();
    await expect(composer(page)).toContainText("rascunho que precisa sobreviver ao resize");

    await page.setViewportSize(WIDE);
    await expect(page.getByTestId("chat-sidebar")).toBeVisible();
    await expect(composer(page)).toContainText("rascunho que precisa sobreviver ao resize");
  });

  /**
   * 200% de zoom sobre 1280×720: o navegador entrega metade da largura e da
   * altura em pixels CSS, que é exatamente o que a página vê. As ações
   * essenciais continuam alcançáveis e nada transborda.
   */
  test("zoom elevado mantém as ações essenciais e não gera rolagem horizontal", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 640, height: 360 });
    await openChannel(page, testInfo);

    await expect(navToggle(page)).toBeVisible();
    await expect(detailsToggle(page)).toBeVisible();
    await expect(page.getByTestId("chat-send-btn")).toBeVisible();
    await expectNoHorizontalScroll(page);

    await detailsToggle(page).click();
    await expect(page.getByTestId("chat-conversation-details")).toBeVisible();
    await expectNoHorizontalScroll(page);
  });

  /**
   * ISSUE #467 follow-up — the document under a chat route must not scroll.
   *
   * The defect these cover was a full-height shell sitting inside a document
   * that was taller than the viewport, so the page scrolled the shell off the
   * top and left a band of empty background below it.
   */
  test("lista longa de conversas rola na sidebar, nunca no documento", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1366, height: 768 });
    await openChannelWithLongSidebar(page, testInfo);

    const geometry = await expectNoRootScroll(page, "sidebar longa");
    const nav = await readSidebarNav(page);

    // The point of the scenario: there IS more content than fits, and it is in
    // the sidebar's own scrollport rather than in the document.
    expect(nav).not.toBeNull();
    expect(nav!.scrollHeight, "a nav precisa ter conteúdo excedente").toBeGreaterThan(
      nav!.clientHeight,
    );

    // Scrolling the nav to its end moves the nav and nothing else.
    await page.evaluate(() => {
      const el = document.querySelector(".chat-sidebar__nav")!;
      el.scrollTop = el.scrollHeight;
    });
    const scrolled = await readSidebarNav(page);
    expect(scrolled!.scrollTop, "a nav precisa ter rolado").toBeGreaterThan(0);
    await expectNoRootScroll(page, "sidebar rolada até o fim");

    // Reported so a failure carries the real numbers, not just a boolean.
    console.log(
      `[#467] root scrollY=${geometry.scrollY} scrollHeight=${geometry.scrollHeight} ` +
        `clientHeight=${geometry.clientHeight} shell=[${geometry.shell!.top}, ${geometry.shell!.bottom}] ` +
        `nav client=${scrolled!.clientHeight} scroll=${scrolled!.scrollHeight} top=${scrolled!.scrollTop}`,
    );
  });

  test("navegar, abrir detalhes e usar o composer não introduz rolagem no documento", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1366, height: 768 });
    await openChannelWithLongSidebar(page, testInfo);
    await expectNoRootScroll(page, "carregado");

    // A row near the end of a list taller than the viewport: reaching it is
    // what used to scroll the document rather than the nav.
    const lastRow = page.getByRole("option", { name: "Canal de acompanhamento 39" });
    await lastRow.scrollIntoViewIfNeeded();
    await expectNoRootScroll(page, "última linha da sidebar visível");

    await lastRow.click();
    await expect(page.getByTestId("chat-msg-header")).toContainText("Canal de acompanhamento 39");
    await expectNoRootScroll(page, "conversa selecionada");

    await detailsToggle(page).click();
    await expect(page.getByTestId("chat-conversation-details")).toBeVisible();
    await expectNoRootScroll(page, "detalhes abertos");

    await page.getByRole("button", { name: "Fechar detalhes do canal" }).click();
    await expect(page.getByTestId("chat-conversation-details")).toBeHidden();
    await expectNoRootScroll(page, "detalhes fechados");

    // The footer is the bottom-most focusable thing in the sidebar; focusing it
    // is the gesture most likely to ask the browser to scroll an ancestor.
    await page.getByRole("link", { name: /Meu perfil/ }).focus();
    await expectNoRootScroll(page, "rodapé focado");

    await fillComposer(page, "mensagem digitada sem mover o documento");
    await expectNoRootScroll(page, "composer em uso");
  });

  test("desktop → tablet → celular → desktop sem reload mantém o documento sem rolagem", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(WIDE);
    await openChannelWithLongSidebar(page, testInfo);
    await expectNoRootScroll(page, "desktop");

    for (const [label, viewport] of [
      ["tablet", TABLET],
      ["celular", PHONE],
      ["desktop de volta", WIDE],
    ] as const) {
      await page.setViewportSize(viewport);
      await expect(page.getByTestId("chat-msg-header")).toBeVisible();
      await expectNoRootScroll(page, label);
    }
  });

  test("rolar o histórico move a lista de mensagens, não a janela", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 1366, height: 768 });
    const targetId = uniqueId(testInfo, "long-history");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: LONG_NAME,
      messages: Array.from({ length: 60 }, (_, index) =>
        makeMessage({
          id: `${targetId}-m${index}`,
          body_text: `Mensagem número ${index} do histórico longo desta conversa.`,
        }),
      ),
    });
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await expect(page.getByTestId("chat-msg-header")).toBeVisible();

    const list = page.locator(".chat-msg-area__list");
    await expect
      .poll(async () => list.evaluate((el) => el.scrollHeight - el.clientHeight))
      .toBeGreaterThan(0);

    await list.evaluate((el) => {
      el.scrollTop = 0;
    });
    await list.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    expect(await list.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
    await expectNoRootScroll(page, "histórico rolado");
  });

  /**
   * ISSUE #672 — AppShell is a layout route shared by /chat and /profile, kept
   * mounted (and the sidebar's WebSocket-backed state with it) across
   * navigation between them. The root lock is scoped to AppShell's lifetime,
   * not to any one child route, so it stays applied on /profile too — Profile
   * scrolls inside its own `.profile-settings` scrollport instead of the
   * document ever regaining ordinary scrolling.
   */
  test("perfil mantém o shell compartilhado e usa seu próprio scrollport", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1366, height: 768 });
    await openChannelWithLongSidebar(page, testInfo);
    await expectNoRootScroll(page, "no chat");

    await page.getByRole("link", { name: /Meu perfil/ }).click();
    await expect(page).toHaveURL(/\/profile$/);

    await expect(page.getByTestId("chat-shell")).toBeVisible();
    await expect(page.getByTestId("profile-settings-shell")).toBeVisible();

    const locked = await page.evaluate(() => ({
      html: document.documentElement.classList.contains("chat-root-locked"),
      body: document.body.classList.contains("chat-root-locked"),
    }));
    expect(locked).toEqual({ html: true, body: true });

    const profileOverflowY = await page
      .getByTestId("profile-settings-shell")
      .evaluate((element) => getComputedStyle(element).overflowY);
    expect(profileOverflowY).toBe("auto");
  });

  /**
   * The regression this file exists to catch, in the shape it actually shipped
   * (issue #496).
   *
   * A reaction on an own message sits against the right edge of the
   * conversation, and the tooltip naming who reacted used to be drawn inside the
   * badge and centred on it — so half of it hung past the message. An absolutely
   * positioned box still counts towards the scrollable overflow of its scroll
   * container, and the message list is one, so the conversation grew a
   * horizontal scrollbar while the page around it stayed exactly the right
   * width. That is why it has to be measured on the list, and in a real browser:
   * nothing in jsdom has a width.
   */
  /** An own message — right-aligned, so its reactions hug the right edge. */
  function messageWithReactions(targetId: string) {
    return makeMessage({
      id: `${targetId}-mine`,
      sender_id: CURRENT_USER_ID,
      sender_display_name: CURRENT_USER_NAME,
      body_text: "reações na borda direita",
      reactions: ["👍", "❤️", "😂", "🎉", "🚀", "🔥"].map((emoji) => ({
        emoji,
        count: 3,
        reacted_by_me: true,
        users: [
          { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME },
          { user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME },
        ],
      })),
    });
  }

  async function openReactedMessage(page: Page, testInfo: Parameters<typeof uniqueId>[0]) {
    const targetId = uniqueId(testInfo, "reaction-overflow");
    const mine = messageWithReactions(targetId);
    // History on both sides of it, so the list has somewhere to scroll in either
    // direction: a badge pinned to the end of the conversation can only ever be
    // scrolled out of sight, which would prove nothing about following it.
    const filler = (prefix: string, hour: number) =>
      Array.from({ length: 14 }, (_, index) =>
        makeMessage({
          id: `${targetId}-${prefix}${index}`,
          body_text: `histórico ${prefix}${index}`,
          created_at: new Date(Date.UTC(2026, 0, 1, hour, index)).toISOString(),
        }),
      );
    await installMessagingMocks(
      page,
      createScenario({
        kind: "channel",
        targetId,
        targetName: OTHER_CHANNEL_NAME,
        messages: [...filler("a", 8), mine, ...filler("b", 10)],
      }),
    );
    await page.goto(`/chat/channel/${targetId}`);
    const bubble = page.locator(`[data-message-id="${mine.id}"]`);
    await expect(bubble).toBeAttached();
    // The list opens on the newest message, so bring the reacted one into view.
    await bubble.scrollIntoViewIfNeeded();
    await expect(bubble).toBeVisible();
    return bubble;
  }

  /** The tooltip lives on the body, which is the whole point of the fix. */
  const authorsTooltip = (page: Page) => page.locator("body > [data-testid=reaction-authors]");

  /**
   * The tooltip's own gap is 6px; this leaves room for the sub-pixel rounding of
   * a re-measured box without tolerating a tooltip that stayed behind.
   */
  const maxAuthorsGap = 16;

  /**
   * Where the tooltip sits relative to its badge.
   *
   * Reported as an offset rather than as absolute coordinates because that is
   * the thing that must not change when the badge moves: the tooltip is clamped
   * inside the viewport, so a badge near an edge legitimately has its tooltip
   * off-centre, and only the badge's own position may change it.
   */
  async function anchorOffset(badge: Locator, tooltip: Locator) {
    const anchor = (await badge.boundingBox())!;
    const box = (await tooltip.boundingBox())!;
    return {
      dx: Math.round(box.x + box.width / 2 - (anchor.x + anchor.width / 2)),
      // Positive: the tooltip's bottom sits above the badge's top.
      gap: Math.round(anchor.y - (box.y + box.height)),
    };
  }

  for (const viewport of [
    { width: 1280, height: 900 },
    { width: 1920, height: 1080 },
    { width: 390, height: 780 },
    { width: 640, height: 450 },
    { width: 320, height: 700 },
  ]) {
    test(`${viewport.width}x${viewport.height}: reações e tooltip de autores não rolam a conversa`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize(viewport);
      const bubble = await openReactedMessage(page, testInfo);
      await expectNoHorizontalScroll(page);
      await expectNoConversationScroll(page, "reações renderizadas");

      // The floating toolbar appears on hover; it must not widen anything.
      await page.mouse.move(0, 0);
      await bubble.hover();
      await expect(page.getByRole("button", { name: "Mais reações" })).toBeVisible();
      await expectNoHorizontalScroll(page);
      await expectNoConversationScroll(page, "toolbar visível");

      // Every badge's tooltip, one at a time: shown, whole inside the viewport,
      // and never the reason the conversation scrolls.
      const badges = bubble.locator(".chat-msg-area__reaction-slot");
      const tooltip = authorsTooltip(page);
      const count = await badges.count();
      expect(count).toBe(6);
      for (let index = 0; index < count; index += 1) {
        await page.mouse.move(0, 0);
        await badges.nth(index).hover();
        await expect(tooltip).toBeVisible();
        const inside = await tooltip.evaluate((element) => {
          const box = element.getBoundingClientRect();
          return box.left >= 0 && box.right <= document.documentElement.clientWidth;
        });
        expect(inside, `tooltip ${index} inteiro na viewport`).toBe(true);
        await expectNoHorizontalScroll(page);
        await expectNoConversationScroll(page, `tooltip do badge ${index}`);
      }
    });
  }

  /**
   * The tooltip is `position: fixed`, so it does not travel with its badge on
   * its own. These drive the two ways the badge can move under an open tooltip.
   */
  test("o tooltip acompanha o badge quando a conversa rola", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    const bubble = await openReactedMessage(page, testInfo);
    const badge = bubble.locator(".chat-msg-area__reaction-slot").last();
    const tooltip = authorsTooltip(page);

    // Focus rather than hover: the pointer would follow the page, the caret does
    // not, so this is the case where the tooltip really can be left behind.
    await badge.getByRole("button").focus();
    await expect(tooltip).toBeVisible();
    // Settled just above the badge. Polled rather than read once: the list
    // scrolls itself to the newest message on open, and the tooltip is placed
    // against wherever the badge has come to rest.
    await expect
      .poll(async () => {
        const now = await anchorOffset(badge, tooltip);
        return now.gap >= 0 && now.gap <= maxAuthorsGap;
      })
      .toBe(true);
    const placed = await anchorOffset(badge, tooltip);
    const before = (await badge.boundingBox())!.y;

    // A nudge, not a jump: the badge has to move while staying on screen, which
    // is the only state in which following it means anything.
    await page.locator(".chat-msg-area__list").evaluate((list) => {
      list.scrollTop += 60;
    });
    // Wait for the badge to have actually moved rather than for a duration.
    await expect.poll(async () => (await badge.boundingBox())!.y).not.toBe(before);

    // Still just above the badge, and still the same distance from its centre:
    // it followed. Before the listener existed the tooltip stayed where the
    // badge used to be, which this measures as a gap of roughly the scroll.
    await expect
      .poll(async () => {
        const now = await anchorOffset(badge, tooltip);
        return { dx: now.dx, follows: now.gap >= 0 && now.gap <= maxAuthorsGap };
      })
      .toEqual({ dx: placed.dx, follows: true });
    await expect(tooltip).toBeVisible();
    await expectNoConversationScroll(page, "após rolar a lista");
  });

  test("o tooltip volta para dentro da viewport quando ela encolhe", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    const bubble = await openReactedMessage(page, testInfo);
    const badge = bubble.locator(".chat-msg-area__reaction-slot").last();
    const tooltip = authorsTooltip(page);

    await badge.getByRole("button").focus();
    await expect(tooltip).toBeVisible();

    // The same page, resized: this is what exercises the resize listener.
    await page.setViewportSize({ width: 420, height: 700 });
    // A narrower shell reflows the conversation, which can carry the badge out
    // of the list's visible band — and a tooltip with nothing to point at is
    // correctly hidden. Bring it back so what is under test is the clamp.
    await badge.scrollIntoViewIfNeeded();

    await expect
      .poll(async () => {
        const box = await tooltip.boundingBox();
        return box !== null && box.x >= 0 && box.x + box.width <= 420;
      })
      .toBe(true);
    await expect(tooltip).toBeVisible();
    await expectNoHorizontalScroll(page);
    await expectNoConversationScroll(page, "após encolher a viewport");
  });

  /**
   * A tooltip may only be drawn against a badge the reader can actually see.
   *
   * The badge lives inside `.chat-msg-area__list`, which clips vertically, so
   * "visible" is the list's own band and not the window: a badge scrolled past
   * the list's edge is hidden even though the window still has room for it. The
   * tooltip is `position: fixed` and followed its badge anywhere, so a focused
   * badge scrolled out of the list left its names floating over the page, or off
   * it entirely.
   */
  test("o tooltip some quando o badge sai da área visível da lista e volta com ele", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    const bubble = await openReactedMessage(page, testInfo);
    const badge = bubble.locator(".chat-msg-area__reaction-slot").last();
    const tooltip = authorsTooltip(page);
    const list = page.locator(".chat-msg-area__list");

    await badge.getByRole("button").focus();
    await expect(tooltip).toBeVisible();

    // Reads whether any part of the badge is inside the band the list actually
    // paints — the intersection of the list's box and the window.
    const badgeIsOnScreen = () =>
      badge.evaluate((slot) => {
        const clip = slot.closest(".chat-msg-area__list")!.getBoundingClientRect();
        const rect = slot.getBoundingClientRect();
        return (
          rect.bottom > Math.max(0, clip.top) &&
          rect.top < Math.min(window.innerHeight, clip.bottom) &&
          rect.right > Math.max(0, clip.left) &&
          rect.left < Math.min(window.innerWidth, clip.right)
        );
      });

    expect(await badgeIsOnScreen()).toBe(true);
    const scrolledDown = await list.evaluate((element) => element.scrollTop);

    // All the way up: the reacted message is far below now, out of the band.
    await list.evaluate((element) => {
      element.scrollTop = 0;
    });
    await expect.poll(badgeIsOnScreen).toBe(false);

    // The badge is gone from view, so its names are too — without the reader
    // having touched the keyboard or the mouse.
    await expect(tooltip).toBeHidden();
    await expectNoConversationScroll(page, "âncora fora da lista");
    await expectNoHorizontalScroll(page);

    // Scrolling back brings it straight back, still against its badge.
    await list.evaluate((element, top) => {
      element.scrollTop = top;
    }, scrolledDown);
    await expect.poll(badgeIsOnScreen).toBe(true);

    await expect(tooltip).toBeVisible();
    await expect
      .poll(async () => {
        const now = await anchorOffset(badge, tooltip);
        return now.gap >= 0 && now.gap <= maxAuthorsGap;
      })
      .toBe(true);
    await expectNoConversationScroll(page, "âncora de volta");
  });

  // One real combination, to prove in a browser what the unit tests prove in
  // isolation: whichever channel is still asking keeps the names on screen.
  test("o tooltip permanece enquanto o badge segue focado e o ponteiro sai", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    const bubble = await openReactedMessage(page, testInfo);
    const badge = bubble.locator(".chat-msg-area__reaction-slot").last();
    const tooltip = authorsTooltip(page);

    await badge.hover();
    await expect(tooltip).toBeVisible();
    await badge.getByRole("button").focus();

    // The pointer leaves; the keyboard has not.
    await page.mouse.move(0, 0);
    await expect(tooltip).toBeVisible();

    await badge.getByRole("button").blur();
    await expect(tooltip).toHaveCount(0);
  });

  for (const viewport of VIEWPORT_MATRIX) {
    test(`${viewport.width}x${viewport.height}: sem rolagem horizontal com conteúdo longo, detalhes abertos e fechados`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize(viewport);
      await openChannel(page, testInfo);

      // Nome longo, URL sem espaços, palavra sem quebra e anexo, todos na tela.
      await expect(page.getByTestId("chat-msg-header")).toBeVisible();
      await expect(composer(page)).toBeVisible();
      await expectNoHorizontalScroll(page);
      await expectNoConversationScroll(page, "inicial");
      await expectNoRootScroll(page, "inicial");

      await detailsToggle(page).click();
      await expect(page.getByTestId("chat-conversation-details")).toBeVisible();
      await expectNoHorizontalScroll(page);
      await expectNoConversationScroll(page, "detalhes abertos");
      await expectNoRootScroll(page, "detalhes abertos");

      await page.getByRole("button", { name: "Fechar detalhes do canal" }).click();
      await expect(page.getByTestId("chat-conversation-details")).toBeHidden();
      await expectNoHorizontalScroll(page);
      await expectNoConversationScroll(page, "detalhes fechados");
      await expectNoRootScroll(page, "detalhes fechados");
    });
  }
});
