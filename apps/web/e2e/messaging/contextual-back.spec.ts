import { expect, test, type Page, type Route, type TestInfo } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  GROUP_DM_ID,
  GROUP_DM_NAME,
  OTHER_USER_NAME,
  createScenario,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  sidebarResponseBody,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Issue #1124 — the conversation header's Back. Where the navigation is a
 * drawer (below 1024px) the header starts with a "Voltar" that returns to the
 * previous conversation the app itself navigated from, and otherwise opens the
 * conversation list — never history.back() into the sign-in or another site.
 * Above it the sidebar is a column on screen and there is no Back.
 *
 * Every scenario's sidebar has the three kinds: the channel under test (A), a
 * DM with OTHER_USER_NAME (B) and the group GROUP_DM_NAME (C).
 */

const PHONE = { width: 390, height: 844 };
const DRAWER_VIEWPORTS = [
  { width: 360, height: 800 },
  { width: 390, height: 844 },
  { width: 768, height: 1024 },
];
const COLUMN_VIEWPORTS = [
  { width: 1024, height: 768 },
  { width: 1440, height: 900 },
];
const CHANNEL_NAME = "Canal de origem";
const MENTIONED_USER_ID = "22222222-2222-4222-8222-222222222222";
const MENTIONED_USER_NAME = "Marina Costa";

const back = (page: Page) => page.getByRole("button", { name: "Voltar" });
const navToggle = (page: Page) => page.getByTestId("chat-nav-toggle");
const header = (page: Page) => page.getByTestId("chat-msg-header");
const sidebar = (page: Page) => page.getByTestId("chat-sidebar");
const historyLength = (page: Page) => page.evaluate(() => window.history.length);
/** Every `a` paired with every `b`, in the order two nested loops would visit them. */
const pairs = <A, B>(a: readonly A[], b: readonly B[]) =>
  a.flatMap((x) => b.map((y) => [x, y] as const));

async function expectNoHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
}

type ScenarioOptions = Parameters<typeof createScenario>[0];

async function installChannel(
  page: Page,
  testInfo: TestInfo,
  extra: (targetId: string) => Partial<ScenarioOptions> = () => ({}),
) {
  const targetId = uniqueId(testInfo, "back");
  const scenario = createScenario({
    kind: "channel",
    targetId,
    targetName: CHANNEL_NAME,
    messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Primeira mensagem" })],
    ...extra(targetId),
  });
  await installMessagingMocks(page, scenario);
  return { scenario, targetId, url: `/chat/channel/${targetId}` };
}

/** Picks a conversation from the sidebar, opening the drawer first when it is a closed one. */
async function pick(page: Page, name: string) {
  const closedDrawer =
    (await navToggle(page).isVisible()) &&
    (await navToggle(page).getAttribute("aria-expanded")) !== "true";
  if (closedDrawer) {
    await navToggle(page).click();
  }
  await page.getByRole("option", { name: new RegExp(name) }).click();
  await expect(header(page)).toContainText(name);
}

/** A → B → C: channel, DM, group, each picked from the drawer. */
async function walkThroughThreeKinds(page: Page) {
  await pick(page, OTHER_USER_NAME);
  await expect(page).toHaveURL(/\/chat\/dm\/e2e-dm-other$/);
  await pick(page, GROUP_DM_NAME);
  await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
}

/** The list on screen over the conversation, the conversation still `url`. */
async function expectListOpenOver(page: Page, url: string) {
  await expect(page).toHaveURL(url);
  await expect(sidebar(page)).toBeVisible();
  await expect(navToggle(page)).toHaveAttribute("aria-expanded", "true");
  await expect(navToggle(page)).toBeFocused();
  await expect(page.getByRole("main")).toHaveAttribute("inert", "");
}

async function expectBackStartsTheHeader(page: Page) {
  await expect(back(page)).toBeVisible();
  const backBox = await back(page).boundingBox();
  const titleBox = await header(page).getByRole("heading").boundingBox();
  expect(backBox && titleBox && backBox.x + backBox.width <= titleBox.x).toBe(true);
  expect(backBox!.width).toBeGreaterThanOrEqual(44);
  expect(backBox!.height).toBeGreaterThanOrEqual(44);
}

test.describe("Voltar contextual do cabeçalho (#1124)", () => {
  for (const viewport of DRAWER_VIEWPORTS) {
    test(`${viewport.width}x${viewport.height}: A → B → C → B → A → lista`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize(viewport);
      const { url } = await installChannel(page, testInfo);
      await page.goto(url);
      await expect(header(page)).toContainText(CHANNEL_NAME);
      await walkThroughThreeKinds(page);
      await expectBackStartsTheHeader(page);
      await expectNoHorizontalScroll(page);

      await back(page).click();
      await expect(page).toHaveURL(/\/chat\/dm\/e2e-dm-other$/);
      await expect(header(page)).toContainText(OTHER_USER_NAME);
      await expect(sidebar(page)).toBeHidden();

      await back(page).click();
      await expect(page).toHaveURL(url);
      await expect(header(page)).toContainText(CHANNEL_NAME);

      await back(page).click();
      await expectListOpenOver(page, url);

      // From the list, another conversation opens normally.
      await page.getByRole("option", { name: new RegExp(GROUP_DM_NAME) }).click();
      await expect(header(page)).toContainText(GROUP_DM_NAME);
    });
  }

  for (const viewport of COLUMN_VIEWPORTS) {
    test(`${viewport.width}x${viewport.height}: com a sidebar em coluna não há Voltar`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize(viewport);
      const { url } = await installChannel(page, testInfo);
      await page.goto(url);
      await pick(page, OTHER_USER_NAME);

      await expect(sidebar(page)).toBeVisible();
      await expect(back(page)).toBeHidden();
      await expect(header(page).getByRole("heading")).toContainText(OTHER_USER_NAME);
    });
  }

  test("login → lista → A → B → C → B → A → lista, sem voltar ao login", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const targetId = uniqueId(testInfo, "back-login");
    await installMessagingMocks(
      page,
      createScenario({ kind: "channel", targetId, targetName: CHANNEL_NAME, messages: [] }),
      { startSignedOut: true },
    );
    const session = {
      access_token: "e2e-login-at",
      token_type: "Bearer",
      expires_in: 3600,
      user: {
        id: CURRENT_USER_ID,
        email: "e2e@example.test",
        display_name: CURRENT_USER_NAME,
        must_change_password: false,
      },
    };
    await page.route("**/api/auth/refresh", (route) => route.fulfill({ status: 401 }));
    await page.route("**/api/auth/login", (route) => route.fulfill({ status: 200, json: session }));
    await page.goto("/login");
    await page.getByLabel("E-mail corporativo").fill("e2e@example.test");
    await page.getByLabel("Senha").fill("senha-e2e");
    await page.getByRole("button", { name: /^Entrar$/ }).click();

    // #1123: the list is the first thing on screen after signing in.
    await expectListOpenOver(page, "/chat");
    await pick(page, CHANNEL_NAME);
    await walkThroughThreeKinds(page);

    await back(page).click();
    await expect(header(page)).toContainText(OTHER_USER_NAME);
    await back(page).click();
    await expect(header(page)).toContainText(CHANNEL_NAME);
    await back(page).click();
    await expectListOpenOver(page, `/chat/channel/${targetId}`);
    // The entry before the list was the sign-in; nothing here ever went there.
    await expect(page).not.toHaveURL(/\/login/);
  });

  test("deep link em nova aba: Voltar abre a lista sem sair do NChat", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { url } = await installChannel(page, testInfo);
    await page.goto("about:blank");
    await page.goto(url);
    await expect(header(page)).toContainText(CHANNEL_NAME);
    const entries = await historyLength(page);

    await back(page).click();

    await expectListOpenOver(page, url);
    expect(await historyLength(page)).toBe(entries);
    await page.keyboard.press("Escape");
    await expect(header(page)).toContainText(CHANNEL_NAME);
  });

  test("reload com a conversa aberta: a lista reabre (#1130) e Voltar é seguro", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { url } = await installChannel(page, testInfo);
    await page.goto(url);
    await pick(page, OTHER_USER_NAME);
    const dmUrl = page.url();

    await page.reload();
    await expectListOpenOver(page, new URL(dmUrl).pathname);
    await navToggle(page).click();
    await expect(sidebar(page)).toBeHidden();

    // The trail before the reload is not this document's: no previous conversation.
    await back(page).click();
    await expectListOpenOver(page, new URL(dmUrl).pathname);
  });

  // CQ-04: the panel covers the header and its Back; its own close stands in
  // the Back's place, is the Back's first step, and is pressed for real here —
  // tap, mouse, Enter and Space — never replaced by Escape.
  for (const press of ["tap", "mouse", "Enter", "Space"] as const) {
    test(`detalhes abertos (${press}): o Voltar visível no painel fecha, depois o do cabeçalho sai`, async ({
      browser,
    }, testInfo) => {
      const context = await browser.newContext({ viewport: PHONE, hasTouch: press === "tap" });
      const page = await context.newPage();
      const { url } = await installChannel(page, testInfo);
      await page.goto(url);
      await expect(header(page)).toContainText(CHANNEL_NAME);
      await pick(page, GROUP_DM_NAME);
      const headerBackBox = await back(page).boundingBox();
      const toggle = page.getByTestId("chat-details-toggle");

      await toggle.click();
      const panel = page.getByTestId("chat-conversation-details");
      const panelBack = panel.getByRole("button", { name: "Fechar detalhes do grupo" });
      await expect(panel).toBeVisible();
      await expect(back(page)).toBeHidden();
      await expect(panelBack).toBeVisible();
      await expect(panelBack).toBeFocused();
      await expect(panelBack).toContainText("arrow_back");
      // Where the header's Back was: the leading edge, the same 44px target.
      const panelBackBox = await panelBack.boundingBox();
      const titleBox = await panel
        .getByRole("heading", { name: "Detalhes do grupo" })
        .boundingBox();
      expect(panelBackBox!.width).toBeGreaterThanOrEqual(44);
      expect(panelBackBox!.height).toBeGreaterThanOrEqual(44);
      expect(panelBackBox!.x + panelBackBox!.width).toBeLessThanOrEqual(titleBox!.x);
      expect(Math.abs(panelBackBox!.x - headerBackBox!.x)).toBeLessThanOrEqual(8);

      if (press === "tap") await panelBack.tap();
      else if (press === "mouse") await panelBack.click();
      else await page.keyboard.press(press);

      await expect(panel).toBeHidden();
      await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
      await expect(toggle).toBeFocused();
      await expect(page.getByTestId("chat-composer-input")).toBeVisible();

      await back(page).click();
      await expect(page).toHaveURL(url);
      await context.close();
    });
  }

  test("detalhes no desktop: o fechar segue um X acessível no fim da faixa, sem Voltar", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    const { url } = await installChannel(page, testInfo);
    await page.goto(url);
    await pick(page, GROUP_DM_NAME);
    await page.getByTestId("chat-details-toggle").click();
    const panel = page.getByTestId("chat-conversation-details");
    const close = panel.getByRole("button", { name: "Fechar detalhes do grupo" });

    await expect(close).toBeVisible();
    await expect(close).toContainText("close");
    const closeBox = await close.boundingBox();
    const titleBox = await panel.getByRole("heading", { name: "Detalhes do grupo" }).boundingBox();
    expect(closeBox!.x).toBeGreaterThanOrEqual(titleBox!.x + titleBox!.width);
    await expect(back(page)).toBeHidden();
    await close.click();
    await expect(panel).toBeHidden();
    await expect(page.getByTestId("chat-details-toggle")).toBeFocused();
  });

  test("título longo a 360px: o Voltar continua inteiro, primeiro e acionável", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 360, height: 800 });
    const longName =
      "Canal de origem com um nome muito, muito comprido para caber no cabeçalho do celular";
    const { url } = await installChannel(page, testInfo, () => ({ targetName: longName }));
    await page.goto(url);
    await pick(page, OTHER_USER_NAME);
    await back(page).click();
    await expect(page).toHaveURL(url);
    await expect(header(page).getByRole("heading")).toContainText(longName);

    // #467's header lets a title too long for the line wrap below the controls
    // and ellipsise there; the Back stays first, whole, and clear of it.
    await expect(back(page)).toBeVisible();
    const backBox = (await back(page).boundingBox())!;
    const titleBox = (await header(page).getByRole("heading").boundingBox())!;
    expect(backBox.width).toBeGreaterThanOrEqual(44);
    expect(backBox.height).toBeGreaterThanOrEqual(44);
    const overlaps =
      backBox.x < titleBox.x + titleBox.width &&
      titleBox.x < backBox.x + backBox.width &&
      backBox.y < titleBox.y + titleBox.height &&
      titleBox.y < backBox.y + backBox.height;
    expect(overlaps).toBe(false);
    expect(await header(page).getByRole("button").first().getAttribute("aria-label")).toBe(
      "Voltar",
    );
    await expectNoHorizontalScroll(page);
    // Nothing paints over it: the button is what is hit at its own centre.
    const hit = await page.evaluate(
      ([x, y]) => document.elementFromPoint(x, y)?.closest("button")?.getAttribute("aria-label"),
      [backBox.x + backBox.width / 2, backBox.y + backBox.height / 2],
    );
    expect(hit).toBe("Voltar");
  });

  test("busca → resultado → Voltar restaura a busca", async ({ page }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { url } = await installChannel(page, testInfo);
    await page.route("**/api/search/**", async (route) => {
      const category = new URL(route.request().url()).pathname.split("/").pop();
      const data =
        category === "groups"
          ? [{ id: GROUP_DM_ID, title: GROUP_DM_NAME, participant_count: 3 }]
          : [];
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: { data, pagination: { limit: 20, next_cursor: null, has_more: false } },
        }),
      });
    });
    await page.goto(url);
    await navToggle(page).click();
    await page.getByRole("button", { name: "Buscar no NChat" }).click();
    const field = page.getByRole("searchbox", {
      name: "Buscar mensagens, pessoas, canais, grupos e arquivos",
    });
    await field.fill("projeto");
    await page.getByRole("tab", { name: "Grupos" }).click();
    await page
      .getByRole("tabpanel")
      .getByRole("button", { name: new RegExp(GROUP_DM_NAME) })
      .click();
    await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);

    await back(page).click();

    await expect(page).toHaveURL(/\/chat\/search$/);
    await expect(field).toHaveValue("projeto");
    await expect(page.getByRole("tab", { name: "Grupos" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  test("menção → DM → Voltar retorna ao canal com o rascunho", async ({ page }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { url } = await installChannel(page, testInfo, (targetId) => ({
      dmCandidates: [{ userId: MENTIONED_USER_ID, displayName: MENTIONED_USER_NAME }],
      messages: [
        makeMessage({
          id: `${targetId}-msg`,
          sender_id: "e2e-teammate",
          sender_display_name: "Ana Souza",
          body_text: `@[${MENTIONED_USER_NAME}](mention:user:${MENTIONED_USER_ID}) pode revisar?`,
          body_format: "v3",
        }),
      ],
    }));
    await page.goto(url);
    await fillComposer(page, "rascunho do canal");
    await page.getByRole("button", { name: `Abrir conversa com ${MENTIONED_USER_NAME}` }).click();
    await expect(page).toHaveURL(new RegExp(`/chat/dm/e2e-dm-with-${MENTIONED_USER_ID}$`));

    await back(page).click();

    await expect(page).toHaveURL(url);
    await expect(page.getByTestId("chat-composer-input")).toHaveText("rascunho do canal");
  });

  test("Voltar/Avançar nativos e o Voltar do cabeçalho não criam ciclos", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { url } = await installChannel(page, testInfo);
    await page.goto(url);
    await walkThroughThreeKinds(page);

    await page.goBack();
    await expect(header(page)).toContainText(OTHER_USER_NAME);
    await page.goForward();
    await expect(header(page)).toContainText(GROUP_DM_NAME);

    await back(page).click();
    await expect(header(page)).toContainText(OTHER_USER_NAME);
    await page.goForward();
    await expect(header(page)).toContainText(GROUP_DM_NAME);
    await back(page).click();
    await back(page).click();
    await expect(page).toHaveURL(url);
    await back(page).click();
    await expectListOpenOver(page, url);
  });

  test("teclado e zoom 200%: Enter e Espaço, foco visível, sem rolagem lateral", async ({
    page,
  }, testInfo) => {
    // 200% over 1280×720 hands the page 640×360 CSS pixels.
    await page.setViewportSize({ width: 640, height: 360 });
    const { url } = await installChannel(page, testInfo);
    await page.goto(url);
    await walkThroughThreeKinds(page);
    await expectNoHorizontalScroll(page);

    await back(page).focus();
    await page.keyboard.press("Enter");
    await expect(header(page)).toContainText(OTHER_USER_NAME);

    await back(page).focus();
    await expect(back(page)).toBeFocused();
    const outline = await back(page).evaluate((element) => getComputedStyle(element).outlineStyle);
    expect(outline).not.toBe("none");
    await page.keyboard.press(" ");
    await expect(page).toHaveURL(url);
    // DM → channel swaps the header; focus lands on the new Back, not <body>.
    await expect(back(page)).toBeFocused();
  });

  test("resize entre celular e desktop não navega nem cria entradas", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { url } = await installChannel(page, testInfo);
    await page.goto(url);
    await pick(page, OTHER_USER_NAME);
    const dmUrl = page.url();
    const entries = await historyLength(page);

    await page.setViewportSize({ width: 1440, height: 900 });
    await expect(back(page)).toBeHidden();
    await page.setViewportSize(PHONE);
    await expect(back(page)).toBeVisible();

    await expect(page).toHaveURL(dmUrl);
    expect(await historyLength(page)).toBe(entries);
    await expect(navToggle(page)).toHaveAttribute("aria-expanded", "false");
    await back(page).click();
    await expect(page).toHaveURL(url);
  });

  /**
   * CQ-05: a token rotation puts the sidebar's identity in doubt until the
   * server says whose the new token is. The group's first messages request is
   * answered 401, so the app's own client rotates the token and the sidebar
   * asks the server whose it is — and that answer is held by the test, released
   * only when the test says so: no timer decides what is pending.
   */
  async function rotateWhileOpening(page: Page, scenario: ReturnType<typeof createScenario>) {
    let viewer = CURRENT_USER_ID;
    let hold = false;
    let release!: () => void;
    const released = new Promise<void>((resolve) => {
      release = resolve;
    });
    let served = 0;
    await page.route("**/api/chat/sidebar", async (route: Route) => {
      if (hold) {
        hold = false;
        await released;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: sidebarResponseBody(scenario, viewer),
      });
      served += 1;
    });
    let expire = false;
    await page.route(
      (url) => url.pathname === `/api/chat/dm/${GROUP_DM_ID}/messages`,
      async (route: Route) => {
        // Until the client has rotated: the first request may be aborted by the
        // view before its answer arrives.
        if (!expire) return route.fallback();
        await route.fulfill({ status: 401, json: { error: { code: "unauthorized" } } });
      },
    );
    let rotations = 0;
    await page.route("**/api/auth/refresh", async (route: Route) => {
      rotations += 1;
      expire = false;
      // The sidebar's next request is the one asking whose the rotated token is.
      hold = true;
      await route.fulfill({
        status: 200,
        json: {
          access_token: `e2e-rotated-at-${rotations}`,
          token_type: "Bearer",
          expires_in: 900,
        },
      });
    });
    return {
      expireOnNextOpen: () => {
        expire = true;
      },
      /** The sidebar's next request is held: for a rotation the test installs itself. */
      holdNext: () => {
        hold = true;
      },
      /** Whose the sidebar says the token is from now on, nothing held or released. */
      serveAs: (as: string) => {
        viewer = as;
      },
      /** Resolves once a held sidebar request has arrived and is waiting. */
      held: () => expect.poll(() => hold).toBe(false),
      /** Resolves once the sidebar is waiting on the held answer. */
      pending: () => expect.poll(() => rotations === 1 && !hold).toBe(true),
      /** Lets the held answer through, for `as`; resolves once the sidebar has heard it. */
      confirm: async (as: string) => {
        viewer = as;
        const before = served;
        release();
        // Another user's answer is a session change: a second load follows it.
        await expect
          .poll(() => served)
          .toBeGreaterThanOrEqual(before + (as === CURRENT_USER_ID ? 1 : 2));
      },
    };
  }

  async function openSearchWithQuery(page: Page) {
    await page.route("**/api/search/**", async (route) => {
      const category = new URL(route.request().url()).pathname.split("/").pop();
      const data =
        category === "groups"
          ? [{ id: GROUP_DM_ID, title: GROUP_DM_NAME, participant_count: 3 }]
          : [];
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: { data, pagination: { limit: 20, next_cursor: null, has_more: false } },
        }),
      });
    });
    await navToggle(page).click();
    await page.getByRole("button", { name: "Buscar no NChat" }).click();
    const field = page.getByRole("searchbox", {
      name: "Buscar mensagens, pessoas, canais, grupos e arquivos",
    });
    await field.fill("segredo-u1");
    await page.getByRole("tab", { name: "Grupos" }).click();
    return field;
  }

  async function openFavorites(page: Page) {
    await page.route("**/api/chat/favorites**", (route) =>
      route.fulfill({ status: 200, json: { data: { favorites: [] } } }),
    );
    await navToggle(page).click();
    await page.getByRole("link", { name: "Meus favoritos" }).click();
    await expect(page).toHaveURL("/chat/favorites");
    await expect(page.getByRole("heading", { name: "Meus favoritos" })).toBeVisible();
  }

  /** Back pressed: the list opens over the group, nothing travelled, no entry added. */
  async function expectBackHolds(page: Page) {
    const entries = await historyLength(page);
    await back(page).click();
    await expectListOpenOver(page, `/chat/dm/${GROUP_DM_ID}`);
    expect(await historyLength(page)).toBe(entries);
    await navToggle(page).click();
    await expect(back(page)).toBeVisible();
  }

  for (const [surface, confirmed] of pairs(
    ["busca", "favoritos"] as const,
    ["outro usuário", "mesmo usuário"] as const,
  )) {
    test(`CQ-05 ${surface} → conversa → refresh pendente → Voltar → confirma ${confirmed}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize(PHONE);
      const { scenario, url } = await installChannel(page, testInfo);
      const identity = await rotateWhileOpening(page, scenario);
      await page.goto(url);
      await expect(header(page)).toContainText(CHANNEL_NAME);

      const field = surface === "busca" ? await openSearchWithQuery(page) : null;
      if (surface === "favoritos") await openFavorites(page);
      identity.expireOnNextOpen();
      // From the search, by its result — which records the query on its entry.
      if (field) {
        await page
          .getByRole("tabpanel")
          .getByRole("button", { name: new RegExp(GROUP_DM_NAME) })
          .click();
      } else {
        await pick(page, GROUP_DM_NAME);
      }
      await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
      await identity.pending();

      // Pending: whatever is behind is U1's, and U1 may no longer be here.
      await expectBackHolds(page);
      await expect(page.getByText("segredo-u1")).toHaveCount(0);

      if (confirmed === "outro usuário") {
        await identity.confirm("e2e-other-identity");
        await expectBackHolds(page);
        await expect(page).not.toHaveURL(/\/chat\/(search|favorites)/);
        await expect(page.getByRole("searchbox")).toHaveCount(0);
        return;
      }

      await identity.confirm(CURRENT_USER_ID);
      await back(page).click();
      if (field) {
        await expect(page).toHaveURL(/\/chat\/search$/);
        await expect(field).toHaveValue("segredo-u1");
        await expect(page.getByRole("tab", { name: "Grupos" })).toHaveAttribute(
          "aria-selected",
          "true",
        );
      } else {
        await expect(page).toHaveURL("/chat/favorites");
      }
    });
  }

  /**
   * CQ-07: the Back was pressed while the identity was settled, and the
   * rotation starts in the very same turn — before the POP lands. The page
   * imports the app's own auth module (the dev server serves the one instance
   * the app runs on) and installs the rotated token right after the press, so
   * the order is the test's, not a timer's.
   */
  const pressThenRotate = (press: string) => `(async () => {
    const auth = await import("/src/lib/authSession.ts");
    ${press};
    auth.setTokens("e2e-rotated-same-turn", "refresh");
  })()`;
  const CONTEXTUAL_BACK = `document.querySelector('[aria-label="Voltar"]').click()`;
  const NATIVE_BACK = "history.back()";

  /**
   * Records every frame and every DOM change in which any of U1's `secrets` is
   * on screen — in the search field or as text — so a transient exposure fails
   * the test, not only one that is still there at the end.
   */
  const watchFor = (...secrets: string[]) => `(() => {
    const secrets = ${JSON.stringify(secrets)};
    window.__cq07 = { seen: [], stop: false };
    const look = () => {
      if (window.__cq07.stop) return;
      const field = document.getElementById("global-search-input");
      const text = (field ? field.value : "") + " " + document.body.innerText;
      for (const secret of secrets)
        if (text.includes(secret)) window.__cq07.seen.push(secret + " @ " + location.pathname);
    };
    new MutationObserver(look).observe(document.body, {
      subtree: true, childList: true, attributes: true, characterData: true,
    });
    const frame = () => { look(); if (!window.__cq07.stop) requestAnimationFrame(frame); };
    frame();
  })()`;
  const WATCH_FOR_QUERY = watchFor("segredo-u1");
  const exposures = (page: Page) =>
    page.evaluate(() => (window as unknown as { __cq07: { seen: string[] } }).__cq07.seen);
  const stopWatching = (page: Page) =>
    page.evaluate(() => {
      (window as unknown as { __cq07: { stop: boolean } }).__cq07.stop = true;
    });

  for (const [[how, press], confirmed] of pairs(
    [
      ["Voltar contextual", CONTEXTUAL_BACK],
      ["Voltar nativo", NATIVE_BACK],
    ] as const,
    ["outro usuário", "mesmo usuário"] as const,
  )) {
    test(`CQ-07 ${how} pedido e refresh no mesmo turno → busca → confirma ${confirmed}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize(PHONE);
      const { scenario, url } = await installChannel(page, testInfo);
      const identity = await rotateWhileOpening(page, scenario);
      await page.goto(url);
      await expect(header(page)).toContainText(CHANNEL_NAME);
      const field = await openSearchWithQuery(page);
      await page
        .getByRole("tabpanel")
        .getByRole("button", { name: new RegExp(GROUP_DM_NAME) })
        .click();
      await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
      await expect(header(page)).toContainText(GROUP_DM_NAME);

      identity.holdNext();
      await page.evaluate(WATCH_FOR_QUERY);
      await page.evaluate(pressThenRotate(press));
      await identity.held();

      // The POP landed on the search while nobody knows whose session it is:
      // no field to type into, nothing of U1's restored (CQ-10, CQ-11).
      await expect(page).toHaveURL(/\/chat\/search$/);
      await expect(page.getByText("Confirmando sua sessão…")).toBeVisible();
      await expect(page.getByRole("searchbox")).toHaveCount(0);

      if (confirmed === "outro usuário") {
        await identity.confirm("e2e-other-identity");
        // U2's search: usable, empty, and U1's query never drawn — not even for a frame.
        await expect(field).toBeVisible();
        await expect(field).toHaveValue("");
        await expect(page.getByRole("tab", { name: "Tudo" })).toHaveAttribute(
          "aria-selected",
          "true",
        );
        await field.fill("nova");
        await expect(field).toHaveValue("nova");
        expect(await exposures(page)).toEqual([]);
        return;
      }

      expect(await exposures(page)).toEqual([]);
      await stopWatching(page);
      await identity.confirm(CURRENT_USER_ID);
      await expect(field).toHaveValue("segredo-u1");
      await expect(page.getByRole("tab", { name: "Grupos" })).toHaveAttribute(
        "aria-selected",
        "true",
      );
      await expect(
        page.getByRole("tabpanel").getByRole("button", { name: new RegExp(GROUP_DM_NAME) }),
      ).toBeVisible();
    });
  }

  test("CQ-07 busca já aberta: a troca de usuário apaga a consulta e a busca segue utilizável", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { scenario, url } = await installChannel(page, testInfo);
    const identity = await rotateWhileOpening(page, scenario);
    await page.goto(url);
    await expect(header(page)).toContainText(CHANNEL_NAME);
    const field = await openSearchWithQuery(page);
    await expect(
      page.getByRole("tabpanel").getByRole("button", { name: new RegExp(GROUP_DM_NAME) }),
    ).toBeVisible();

    identity.holdNext();
    await page.evaluate(pressThenRotate("void 0"));
    await identity.held();
    await expect(page.getByRole("searchbox")).toHaveCount(0);
    await page.evaluate(WATCH_FOR_QUERY);

    await identity.confirm("e2e-other-identity");
    await expect(field).toHaveValue("");
    await expect(page.getByRole("tabpanel").getByRole("button")).toHaveCount(0);
    expect(await exposures(page)).toEqual([]);
    await field.fill("nova");
    await expect(field).toHaveValue("nova");
  });

  // ── CQ-09, CQ-10, CQ-11: what Favoritos and the search show across identities ──

  const OTHER_IDENTITY = "e2e-other-identity";
  const ROTATE = pressThenRotate("void 0");

  /**
   * Favourites answered by whose token asked — U1's private DM message, or
   * U2's — each answer fixed when its request arrives, so a held one is U1's
   * however late it is let through. U1's first page has a next one. Holding
   * holds every U1 request, StrictMode's discarded first mount's included, so
   * the live one is among them.
   */
  async function serveFavorites(page: Page) {
    let owner = "u1";
    let holding = false;
    const held: Array<() => Promise<void>> = [];
    const favorite = (id: string, sender: string, body: string) => ({
      message: makeMessage({ id, sender_id: id, sender_display_name: sender, body_text: body }),
      dm_conversation_id: `${id}-dm`,
      favorited_at: "2026-07-15T12:00:00.000Z",
    });
    await page.route("**/api/chat/favorites**", async (route: Route) => {
      const later = new URL(route.request().url()).searchParams.has("before");
      const favorites =
        owner === "u2"
          ? [favorite("fav-u2", "Remetente U2", "favorito-u2")]
          : [
              later
                ? favorite("fav-u1-p2", "Remetente U1", "segredo-u1-pagina-2")
                : favorite("fav-u1", "Remetente U1", "segredo-u1"),
            ];
      const next = owner === "u1" && !later ? "c1" : "";
      const answer = () =>
        route
          .fulfill({ status: 200, json: { data: { favorites, next_cursor: next } } })
          // Aborted by the page meanwhile: there is nobody left to answer.
          .catch(() => {});
      if (!holding || owner !== "u1") return answer();
      held.push(answer);
    });
    return {
      serveAs: (who: "u1" | "u2") => {
        owner = who;
      },
      holdU1: () => {
        holding = true;
      },
      held: () => expect.poll(() => held.length).toBeGreaterThan(0),
      release: () => Promise.all(held.splice(0).map((answer) => answer())),
    };
  }

  async function openFavoritesPage(page: Page) {
    await navToggle(page).click();
    await page.getByRole("link", { name: "Meus favoritos" }).click();
    await expect(page).toHaveURL("/chat/favorites");
  }

  /** The rotation, its confirming answer held: the identity is in doubt. */
  async function rotateAndHold(
    page: Page,
    identity: Awaited<ReturnType<typeof rotateWhileOpening>>,
  ) {
    identity.holdNext();
    await page.evaluate(ROTATE);
    await identity.held();
  }

  test("CQ-09 favoritos carregados: confirmado U2, nada de U1 aparece e U2 vê os seus", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { scenario, url } = await installChannel(page, testInfo);
    const identity = await rotateWhileOpening(page, scenario);
    const favorites = await serveFavorites(page);
    await page.goto(url);
    await expect(header(page)).toContainText(CHANNEL_NAME);
    await openFavoritesPage(page);
    await expect(page.getByText("segredo-u1")).toBeVisible();
    await expect(page.getByText("Remetente U1")).toBeVisible();

    await rotateAndHold(page, identity);
    await expect(page.getByText("Confirmando sua sessão…")).toBeVisible();
    await expect(page.getByTestId("favorite-item")).toHaveCount(0);
    await page.evaluate(watchFor("segredo-u1", "Remetente U1"));

    favorites.serveAs("u2");
    await identity.confirm(OTHER_IDENTITY);
    await expect(page.getByText("favorito-u2")).toBeVisible();
    await expect(page.getByText("Remetente U2")).toBeVisible();
    await expect(page.getByTestId("favorite-item")).toHaveCount(1);
    expect(await exposures(page)).toEqual([]);
    await stopWatching(page);
  });

  for (const which of ["primeira página", "Carregar mais"] as const) {
    test(`CQ-09 favoritos: resposta de U1 retida (${which}) chega depois de confirmado U2 e não aparece`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize(PHONE);
      const { scenario, url } = await installChannel(page, testInfo);
      const identity = await rotateWhileOpening(page, scenario);
      const favorites = await serveFavorites(page);
      await page.goto(url);
      await expect(header(page)).toContainText(CHANNEL_NAME);
      if (which === "primeira página") favorites.holdU1();
      await openFavoritesPage(page);
      if (which === "Carregar mais") {
        await expect(page.getByText("segredo-u1")).toBeVisible();
        favorites.holdU1();
        await page.getByRole("button", { name: "Carregar mais" }).click();
      }
      await favorites.held();
      // The request on screen is the held one: nothing of it answered yet.
      await expect(
        page.getByText(which === "primeira página" ? "Carregando favoritos…" : "Carregando…"),
      ).toBeVisible();

      await rotateAndHold(page, identity);
      await page.evaluate(watchFor("segredo-u1", "Remetente U1"));
      favorites.serveAs("u2");
      await identity.confirm(OTHER_IDENTITY);
      await expect(page.getByText("favorito-u2")).toBeVisible();

      // U1's answer, at last: nobody asked for it under U2.
      await favorites.release();
      await expect(page.getByText("favorito-u2")).toBeVisible();
      await expect(page.getByTestId("favorite-item")).toHaveCount(1);
      expect(await exposures(page)).toEqual([]);
      await stopWatching(page);
    });
  }

  for (const [how, press] of [
    ["Voltar contextual", CONTEXTUAL_BACK],
    ["Voltar nativo", NATIVE_BACK],
  ] as const) {
    test(`CQ-09 ${how} para favoritos com refresh no mesmo turno → confirma U2: só os de U2`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize(PHONE);
      const { scenario, url } = await installChannel(page, testInfo);
      const identity = await rotateWhileOpening(page, scenario);
      const favorites = await serveFavorites(page);
      await page.goto(url);
      await expect(header(page)).toContainText(CHANNEL_NAME);
      await openFavoritesPage(page);
      await expect(page.getByText("segredo-u1")).toBeVisible();
      await pick(page, GROUP_DM_NAME);

      identity.holdNext();
      await page.evaluate(watchFor("segredo-u1", "Remetente U1"));
      await page.evaluate(pressThenRotate(press));
      await identity.held();
      await expect(page).toHaveURL("/chat/favorites");
      await expect(page.getByText("Confirmando sua sessão…")).toBeVisible();

      favorites.serveAs("u2");
      await identity.confirm(OTHER_IDENTITY);
      await expect(page.getByText("favorito-u2")).toBeVisible();
      expect(await exposures(page)).toEqual([]);
      await stopWatching(page);

      // Forward to the group and Back again, natively: still only U2's.
      await page.goForward();
      await expect(header(page)).toContainText(GROUP_DM_NAME);
      await page.goBack();
      await expect(page.getByText("favorito-u2")).toBeVisible();
      await expect(page.getByText("segredo-u1")).toHaveCount(0);
    });
  }

  test("CQ-10 sidebar falha: a busca não consulta nem aceita texto; recuperada como U2, nada de antes", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { scenario } = await installChannel(page, testInfo);
    const identity = await rotateWhileOpening(page, scenario);
    let failFirst = true;
    await page.route("**/api/chat/sidebar", (route: Route) => {
      if (!failFirst) return route.fallback();
      failFirst = false;
      return route.fulfill({ status: 500, json: { error: { code: "internal" } } });
    });
    const searched: string[] = [];
    await page.route("**/api/search/**", async (route: Route) => {
      const q = String((route.request().postDataJSON() as { q?: unknown }).q);
      searched.push(q);
      const data =
        q === "consulta-sem-owner-u1"
          ? [{ id: GROUP_DM_ID, title: "resultado-privado-u1", participant_count: 3 }]
          : [];
      await route.fulfill({
        status: 200,
        json: { data: { data, pagination: { limit: 20, next_cursor: null, has_more: false } } },
      });
    });
    await page.goto("/chat/search");

    const search = page.getByTestId("global-search");
    await expect(search.getByRole("alert")).toContainText("Não foi possível confirmar sua sessão.");
    await expect(page.getByRole("searchbox")).toHaveCount(0);
    await page.keyboard.type("consulta-sem-owner-u1");
    expect(searched).toEqual([]);

    // The session moves on to U2, and the retry is what confirms it.
    await page.evaluate(watchFor("consulta-sem-owner-u1", "resultado-privado-u1"));
    await page.evaluate(ROTATE);
    identity.serveAs(OTHER_IDENTITY);
    await search.getByRole("button", { name: "Tentar novamente" }).click();
    const field = page.getByRole("searchbox", {
      name: "Buscar mensagens, pessoas, canais, grupos e arquivos",
    });
    await expect(field).toBeVisible();
    await expect(field).toHaveValue("");
    await expect(field).toBeFocused();
    expect(searched).toEqual([]);

    await field.fill("nova");
    await expect.poll(() => searched.includes("nova")).toBe(true);
    expect(searched.every((q) => q === "nova")).toBe(true);
    expect(await exposures(page)).toEqual([]);
    await stopWatching(page);
  });

  test("CQ-11 texto digitado sobre a busca restaurada sobrevive à confirmação do mesmo usuário", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { scenario, url } = await installChannel(page, testInfo);
    const identity = await rotateWhileOpening(page, scenario);
    await page.goto(url);
    await expect(header(page)).toContainText(CHANNEL_NAME);
    const field = await openSearchWithQuery(page);
    await page
      .getByRole("tabpanel")
      .getByRole("button", { name: new RegExp(GROUP_DM_NAME) })
      .click();
    await expect(header(page)).toContainText(GROUP_DM_NAME);
    await back(page).click();
    await expect(field).toHaveValue("segredo-u1");
    await field.fill("texto-novo-digitado");

    await rotateAndHold(page, identity);
    // Nothing to type into that the confirmation could then overwrite.
    await expect(page.getByText("Confirmando sua sessão…")).toBeVisible();
    await expect(page.getByRole("searchbox")).toHaveCount(0);
    await page.keyboard.type("perdido");

    await identity.confirm(CURRENT_USER_ID);
    await expect(field).toHaveValue("texto-novo-digitado");
    await expect(field).toBeFocused();
    await expect(page.getByRole("tab", { name: "Grupos" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await page.keyboard.type("!");
    await expect(field).toHaveValue("texto-novo-digitado!");
  });

  // ── CQ-13: an answer speaks for the token that asked, not the one installed when it lands ──

  const U2_TOKEN = "cq13-u2";
  const U1_TOKEN = "cq13-u1";
  const U1_OWNER = `${CURRENT_USER_ID}:e2e-workspace`;
  const U2_OWNER = `${OTHER_IDENTITY}:e2e-workspace`;
  /** Whom the app's own history trail belongs to, as its module says. */
  const trailOwner = (page: Page) =>
    page.evaluate(`import("/src/lib/historyTrail.ts").then((trail) => trail.historyTrailOwner())`);
  /** Installs `token` through the app's own auth module, as a rotation or a new session. */
  const install = (page: Page, token: string, change: "refresh" | "session") =>
    page.evaluate(
      `import("/src/lib/authSession.ts").then((auth) => auth.setTokens("${token}", "${change}"))`,
    );

  /**
   * The sidebar as a server answers it: for whoever's token asked, the answer
   * fixed when the request arrives. The first request — U1's, before any
   * identity was confirmed — is held, and so is every one made with `heldToken`
   * until `releaseHeld`.
   */
  async function serveSidebarByToken(
    page: Page,
    scenario: ReturnType<typeof createScenario>,
    heldToken = U2_TOKEN,
  ) {
    const asked: string[] = [];
    const held: Array<() => Promise<void>> = [];
    let holding = true;
    await page.route("**/api/chat/sidebar", async (route: Route) => {
      const token = route.request().headers()["authorization"] ?? "";
      asked.push(token);
      const viewer = token === `Bearer ${U2_TOKEN}` ? OTHER_IDENTITY : CURRENT_USER_ID;
      const body = sidebarResponseBody(scenario, viewer);
      const answer = () =>
        route.fulfill({ status: 200, contentType: "application/json", body }).catch(() => {});
      if (asked.length === 1 || (token === `Bearer ${heldToken}` && holding)) held.push(answer);
      else await answer();
    });
    return {
      asked,
      /** Lets the oldest held answer through: U1's first one. */
      releaseFirst: () => held.shift()?.(),
      releaseHeld: () => {
        holding = false;
        return Promise.all(held.splice(0).map((answer) => answer()));
      },
    };
  }

  /** The search, answered by whoever's token asked; every request recorded. */
  async function serveSearchByToken(page: Page) {
    const searched: Array<{ token: string; q: string }> = [];
    await page.route("**/api/search/**", async (route: Route) => {
      const token = route.request().headers()["authorization"] ?? "";
      const q = String((route.request().postDataJSON() as { q?: unknown }).q);
      searched.push({ token, q });
      const groups = new URL(route.request().url()).pathname.endsWith("/groups");
      const data = groups ? [{ id: GROUP_DM_ID, title: GROUP_DM_NAME, participant_count: 3 }] : [];
      await route.fulfill({
        status: 200,
        json: { data: { data, pagination: { limit: 20, next_cursor: null, has_more: false } } },
      });
    });
    return searched;
  }

  /** Waits until the sidebar's first request is out — U1's, held. */
  async function firstSidebarHeld(sidebarRoute: { asked: string[] }) {
    await expect.poll(() => sidebarRoute.asked.length).toBeGreaterThan(0);
  }

  /**
   * R6: a refresh installing U2's token `microtasks` microtasks after the first
   * sidebar answer was put together — `fetchSidebarData` reads the categories'
   * `groups` synchronously and returns — so it lands just before, inside or just
   * after the step between checking that answer's generation and taking it. A
   * refresh ends in a promise continuation of its own, so any of those orders
   * is one a real rotation can take; sweeping them needs no knowledge of where
   * the step is. `restore` leaves a search of U1's in history, as U1 leaving the
   * search and reloading would.
   */
  async function rotateAfterFirstAnswer(page: Page, microtasks: number, restore: boolean) {
    await page.addInitScript(
      ({ owner, restore }) => {
        if (restore && location.pathname === "/chat/search" && !history.state) {
          const globalSearch = { query: "segredo-u1", tab: "groups", owner };
          history.replaceState({ usr: { globalSearch }, key: "r6", idx: 0 }, "", location.href);
        }
        const r6 = { armed: false, rotated: false, rotate: () => {} };
        Object.assign(window, { __r6: r6 });
        const json = Response.prototype.json;
        Response.prototype.json = function (this: Response) {
          const body = json.call(this) as Promise<{ data?: { groups?: unknown } }>;
          if (!this.url.endsWith("/api/chat/channel-categories")) return body;
          return body.then((parsed) => {
            const data = parsed.data;
            if (!data) return parsed;
            const groups = data.groups;
            Object.defineProperty(data, "groups", {
              get() {
                if (r6.armed) {
                  r6.armed = false;
                  r6.rotate();
                }
                return groups;
              },
            });
            return parsed;
          });
        };
      },
      { owner: U1_OWNER, restore },
    );
    return {
      /** Arms the rotation, once the app's auth module is loaded. */
      arm: () =>
        page.evaluate(`import("/src/lib/authSession.ts").then((auth) => {
          const r6 = window.__r6;
          r6.rotate = () => {
            let left = ${microtasks};
            const step = () => {
              if (--left > 0) return queueMicrotask(step);
              auth.setTokens("${U2_TOKEN}", "refresh");
              r6.rotated = true;
            };
            queueMicrotask(step);
          };
          r6.armed = true;
        })`),
      rotated: () =>
        page.evaluate(() => (window as unknown as { __r6: { rotated: boolean } }).__r6.rotated),
    };
  }

  test("CQ-13 busca: a primeira sidebar de U1, chegada depois do token de U2, não confirma ninguém", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { scenario } = await installChannel(page, testInfo);
    const identity = await serveSidebarByToken(page, scenario);
    const searched = await serveSearchByToken(page);
    await page.goto("/chat/search");
    await firstSidebarHeld(identity);

    // U2's token is installed; then U1's answer, asked before it, lands.
    await install(page, U2_TOKEN, "refresh");
    await identity.releaseFirst();
    await expect.poll(() => identity.asked.at(-1)).toBe(`Bearer ${U2_TOKEN}`);

    // Nobody is confirmed: no field, nothing asked, no owner for the trail.
    await expect(page.getByRole("searchbox")).toHaveCount(0);
    expect(await trailOwner(page)).not.toBe(U1_OWNER);
    expect(searched).toEqual([]);

    await identity.releaseHeld();
    const field = page.getByRole("searchbox", {
      name: "Buscar mensagens, pessoas, canais, grupos e arquivos",
    });
    await expect(field).toBeVisible();
    await expect.poll(() => trailOwner(page)).toBe(U2_OWNER);
    await field.fill("segredo-u2");
    await page.getByRole("tab", { name: "Grupos" }).click();
    const result = page
      .getByRole("tabpanel")
      .getByRole("button", { name: new RegExp(GROUP_DM_NAME) });
    await expect(result).toBeVisible();
    await result.click();
    await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
    await expect(header(page)).toContainText(GROUP_DM_NAME);

    // The query was asked under U2's token and recorded as U2's.
    expect(searched.length).toBeGreaterThan(0);
    expect(searched.every(({ token }) => token === `Bearer ${U2_TOKEN}`)).toBe(true);
    await page.goBack();
    await expect(field).toHaveValue("segredo-u2");
    const recorded = await page.evaluate(
      () => (history.state as { usr?: { globalSearch?: { owner?: string } } })?.usr?.globalSearch,
    );
    expect(recorded?.owner).toBe(U2_OWNER);
    await page.goForward();
    await expect(header(page)).toContainText(GROUP_DM_NAME);

    // U1 signs in: U2's search is never restored for, nor sent by, U1.
    await install(page, U1_TOKEN, "session");
    await expect.poll(() => trailOwner(page)).toBe(U1_OWNER);
    await page.goBack();
    await expect(field).toBeVisible();
    await expect(field).toHaveValue("");
    expect(
      searched.some(({ token, q }) => token === `Bearer ${U1_TOKEN}` && q === "segredo-u2"),
    ).toBe(false);
  });

  test("CQ-13 busca, mesmo usuário: a resposta antiga não confirma; a do token atual sim", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { scenario } = await installChannel(page, testInfo);
    const identity = await serveSidebarByToken(page, scenario, U1_TOKEN);
    await serveSearchByToken(page);
    await page.goto("/chat/search");
    await firstSidebarHeld(identity);

    await install(page, U1_TOKEN, "refresh");
    await identity.releaseFirst();
    // The same user, but the answer is the previous token's: asked again, and
    // until the current token's answer is in, nobody is confirmed.
    await expect.poll(() => identity.asked.at(-1)).toBe(`Bearer ${U1_TOKEN}`);
    await expect(page.getByRole("searchbox")).toHaveCount(0);
    expect(await trailOwner(page)).not.toBe(U1_OWNER);

    await identity.releaseHeld();
    const field = page.getByRole("searchbox", {
      name: "Buscar mensagens, pessoas, canais, grupos e arquivos",
    });
    await expect(field).toBeVisible();
    await expect.poll(() => trailOwner(page)).toBe(U1_OWNER);
    // Everything after the first answer was asked with the current token.
    expect(identity.asked.slice(1).every((token) => token === `Bearer ${U1_TOKEN}`)).toBe(true);
    await field.fill("consulta-u1");
    await expect(field).toHaveValue("consulta-u1");
  });

  test("CQ-13 favoritos: a primeira sidebar de U1, chegada depois do token de U2, não libera os favoritos de U2 sob U1", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const { scenario } = await installChannel(page, testInfo);
    const identity = await serveSidebarByToken(page, scenario);
    const asked: string[] = [];
    await page.route("**/api/chat/favorites**", (route: Route) => {
      const token = route.request().headers()["authorization"] ?? "";
      asked.push(token);
      const u2 = token === `Bearer ${U2_TOKEN}`;
      const message = makeMessage({
        id: u2 ? "fav-private-u2" : "fav-u1",
        sender_id: u2 ? "sender-u2" : "sender-u1",
        sender_display_name: u2 ? "Remetente privado U2" : "Remetente U1",
        body_text: u2 ? "segredo-favorito-u2" : "favorito-u1",
      });
      const favorites = [
        {
          message,
          dm_conversation_id: u2 ? "private-dm-u2" : "dm-u1",
          favorited_at: "2026-07-15T12:00:00.000Z",
        },
      ];
      return route.fulfill({ status: 200, json: { data: { favorites, next_cursor: "" } } });
    });
    await page.goto("/chat/favorites");
    await firstSidebarHeld(identity);

    await install(page, U2_TOKEN, "refresh");
    await page.evaluate(watchFor("segredo-favorito-u2", "Remetente privado U2", "private-dm-u2"));
    await identity.releaseFirst();
    await expect.poll(() => identity.asked.at(-1)).toBe(`Bearer ${U2_TOKEN}`);

    // Nobody is confirmed: nothing of U2's is asked for, let alone shown under U1.
    await expect(page.getByTestId("favorite-item")).toHaveCount(0);
    expect(asked).toEqual([]);
    expect(await trailOwner(page)).not.toBe(U1_OWNER);
    expect(await exposures(page)).toEqual([]);
    await stopWatching(page);

    // Confirmed U2, for U2's own token: U2's favourites, under U2.
    await identity.releaseHeld();
    await expect(page.getByText("segredo-favorito-u2", { exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "Ver na conversa" })).toHaveAttribute(
      "href",
      "/chat/dm/private-dm-u2",
    );
    expect(await trailOwner(page)).toBe(U2_OWNER);
    expect(asked.every((token) => token === `Bearer ${U2_TOKEN}`)).toBe(true);
  });

  for (const microtasks of [1, 2, 3, 4]) {
    test(`CQ-13 (R6) busca: U2 instalado ${microtasks} microtask(s) depois da primeira resposta — segredo-u1 nunca volta sob U2`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize(PHONE);
      const { scenario } = await installChannel(page, testInfo);
      const identity = await serveSidebarByToken(page, scenario);
      const searched = await serveSearchByToken(page);
      const rotation = await rotateAfterFirstAnswer(page, microtasks, true);
      await page.goto("/chat/search");
      await firstSidebarHeld(identity);
      await rotation.arm();
      await identity.releaseFirst();
      await expect.poll(rotation.rotated).toBe(true);

      // Under U2's token, U2's token is asked whose it is, and until it
      // answers nobody is confirmed: no field, nothing of U1's restored.
      await expect.poll(() => identity.asked.at(-1)).toBe(`Bearer ${U2_TOKEN}`);
      await expect(page.getByText("Confirmando sua sessão…")).toBeVisible();
      await expect(page.getByRole("searchbox")).toHaveCount(0);
      await expect(page.getByText("segredo-u1")).toHaveCount(0);
      expect(await trailOwner(page)).not.toBe(U1_OWNER);
      expect(searched.filter(({ token }) => token === `Bearer ${U2_TOKEN}`)).toEqual([]);

      await page.evaluate(WATCH_FOR_QUERY);
      await identity.releaseHeld();
      const field = page.getByRole("searchbox", {
        name: "Buscar mensagens, pessoas, canais, grupos e arquivos",
      });
      await expect(field).toBeVisible();
      await expect.poll(() => trailOwner(page)).toBe(U2_OWNER);
      await expect(field).toHaveValue("");
      expect(await page.evaluate(() => sessionStorage.getItem("nchat_at"))).toBe(U2_TOKEN);
      expect(await exposures(page)).toEqual([]);
      await stopWatching(page);
      expect(searched.some(({ q }) => q === "segredo-u1")).toBe(false);
    });

    test(`CQ-13 (R6) favoritos: U2 instalado ${microtasks} microtask(s) depois da primeira resposta — nada de U2 sob U1`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize(PHONE);
      const { scenario } = await installChannel(page, testInfo);
      const identity = await serveSidebarByToken(page, scenario);
      const asked: string[] = [];
      await page.route("**/api/chat/favorites**", (route: Route) => {
        const token = route.request().headers()["authorization"] ?? "";
        asked.push(token);
        const u2 = token === `Bearer ${U2_TOKEN}`;
        const message = makeMessage({
          id: u2 ? "fav-private-u2" : "fav-u1",
          body_text: u2 ? "segredo-favorito-u2" : "favorito-u1",
        });
        const favorites = [
          {
            message,
            dm_conversation_id: u2 ? "private-dm-u2" : "dm-u1",
            favorited_at: "2026-07-15T12:00:00.000Z",
          },
        ];
        return route.fulfill({ status: 200, json: { data: { favorites, next_cursor: "" } } });
      });
      const rotation = await rotateAfterFirstAnswer(page, microtasks, false);
      await page.goto("/chat/favorites");
      await firstSidebarHeld(identity);
      await rotation.arm();
      await page.evaluate(watchFor("segredo-favorito-u2", "private-dm-u2"));
      await identity.releaseFirst();
      await expect.poll(rotation.rotated).toBe(true);

      await expect.poll(() => identity.asked.at(-1)).toBe(`Bearer ${U2_TOKEN}`);
      await expect(page.getByText("Confirmando sua sessão…")).toBeVisible();
      await expect(page.getByTestId("favorite-item")).toHaveCount(0);
      expect(asked.filter((token) => token === `Bearer ${U2_TOKEN}`)).toEqual([]);
      expect(await trailOwner(page)).not.toBe(U1_OWNER);
      expect(await exposures(page)).toEqual([]);
      await stopWatching(page);

      await identity.releaseHeld();
      await expect(page.getByText("segredo-favorito-u2", { exact: true })).toBeVisible();
      expect(await trailOwner(page)).toBe(U2_OWNER);
    });
  }
});
