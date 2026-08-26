import { expect, test, type Page } from "@playwright/test";

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
   * The lock is scoped to the shell, so leaving the chat has to give the
   * document its ordinary scrolling back — otherwise a taller route silently
   * loses its bottom half.
   */
  test("sair do chat devolve a rolagem normal ao documento", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 1366, height: 768 });
    await openChannelWithLongSidebar(page, testInfo);
    await expectNoRootScroll(page, "no chat");

    await page.getByRole("link", { name: /Meu perfil/ }).click();
    await expect(page).toHaveURL(/\/profile$/);
    await expect(page.getByTestId("chat-shell")).toHaveCount(0);

    const unlocked = await page.evaluate(() => ({
      html: document.documentElement.classList.contains("chat-root-locked"),
      body: document.body.classList.contains("chat-root-locked"),
      overflowY: getComputedStyle(document.documentElement).overflowY,
    }));
    expect(unlocked).toEqual({ html: false, body: false, overflowY: "visible" });
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
      await expectNoRootScroll(page, "inicial");

      await detailsToggle(page).click();
      await expect(page.getByTestId("chat-conversation-details")).toBeVisible();
      await expectNoHorizontalScroll(page);
      await expectNoRootScroll(page, "detalhes abertos");

      await page.getByRole("button", { name: "Fechar detalhes do canal" }).click();
      await expect(page.getByTestId("chat-conversation-details")).toBeHidden();
      await expectNoHorizontalScroll(page);
      await expectNoRootScroll(page, "detalhes fechados");
    });
  }
});
