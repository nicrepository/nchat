import { expect, test, type Page, type Request, type Route } from "@playwright/test";

import { installPaginatedMessages, TINY_JPEG } from "../helpers/largeConversationFixture";
import {
  GROUP_DM_ID,
  GROUP_DM_NAME,
  OTHER_CHANNEL_ID,
  OTHER_CHANNEL_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Issue #900 — the global search's results. The search-service is mocked here:
 * which rows a caller may see is decided in SQL and proven against PostgreSQL
 * (services/search-service/internal/storage/search_store_postgres_test.go).
 * What only a browser proves is the rest: the overview, every result reaching
 * its real destination — the timeline's MESSAGE_TARGET, the DM flow, the
 * Attachment Viewer — stale answers never winning, and the phone layout.
 * Issue #1081 adds Links: a POST whose query never reaches the URL, opening
 * the message the link was shared in through the same MESSAGE_TARGET.
 */

const PHONE = { width: 390, height: 844 };
const FIELD = "Buscar mensagens, pessoas, canais, grupos e arquivos";

type Category = "messages" | "users" | "channels" | "groups" | "files" | "links";
type Results = Partial<Record<Category, unknown[]>>;

const searchField = (page: Page) => page.getByRole("searchbox", { name: FIELD });
const results = (page: Page) => page.getByRole("tabpanel");
const section = (page: Page, name: string) => results(page).getByRole("region", { name });

/** A page of rows; `next` is the cursor of the following page, if any. */
interface Answer {
  items: unknown[];
  next?: string;
}

function envelope({ items, next }: Answer) {
  return {
    data: {
      data: items,
      pagination: { limit: 20, next_cursor: next ?? null, has_more: next !== undefined },
    },
  };
}

/** One request the client sent: its URL and, for the POST of Links, its body. */
interface SentSearch {
  url: URL;
  body: Record<string, unknown> | null;
}

/**
 * Serves /api/search/** (the V2 message route and the POST of Links
 * included): `answer` picks the rows for a category and query.
 * Every request is recorded so a spec can assert what the client sent.
 */
async function installSearch(
  page: Page,
  answer: (
    category: Category,
    query: string,
    cursor: string | null,
  ) => unknown[] | Answer | Promise<unknown[]>,
  onSettled?: (category: Category, query: string) => void,
) {
  const requests: SentSearch[] = [];
  await page.route("**/api/search/**", async (route: Route) => {
    const request = route.request();
    const url = new URL(request.url());
    const body =
      request.method() === "POST" ? (request.postDataJSON() as Record<string, unknown>) : null;
    requests.push({ url, body });
    const category = url.pathname.split("/").pop() as Category;
    const field = (name: string) =>
      body ? ((body[name] as string | undefined) ?? null) : url.searchParams.get(name);
    const query = field("q") ?? "";
    const rows = await answer(category, query, field("cursor"));
    try {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(envelope(Array.isArray(rows) ? { items: rows } : rows)),
      });
    } finally {
      // Called once this route's answer is out of the handler, delivered or
      // refused because the browser already cancelled the request.
      onSettled?.(category, query);
    }
  });
  return requests;
}

function searchQuery(request: Request): string | null {
  if (!request.url().includes("/api/search/")) return null;
  if (request.method() === "POST") return (request.postDataJSON() as { q?: string }).q ?? null;
  return new URL(request.url()).searchParams.get("q");
}

/**
 * A barrier on one held search request: resolves only when BOTH its route
 * handler has settled (answered after `release`) AND the browser has reported
 * the request's terminal state — `failed` when the client aborted it, which is
 * what a superseded query must do. Assertions made after it see the page after
 * the old request is over, not while it may still be on its way.
 */
function heldSearch(page: Page, category: Category, query: string) {
  let started!: () => void;
  let release!: () => void;
  let handlerSettled!: () => void;
  const requested = new Promise<void>((resolve) => (started = resolve));
  const released = new Promise<void>((resolve) => (release = resolve));
  const settledInHandler = new Promise<void>((resolve) => (handlerSettled = resolve));
  const isHeld = (request: Request) =>
    new URL(request.url()).pathname.endsWith(`/${category}`) && searchQuery(request) === query;
  const terminal = new Promise<"finished" | "failed">((resolve) => {
    page.on("requestfinished", (request) => isHeld(request) && resolve("finished"));
    page.on("requestfailed", (request) => isHeld(request) && resolve("failed"));
  });
  return {
    requested,
    release,
    /** For installSearch's answer: holds this request until `release`. */
    async hold() {
      started();
      await released;
    },
    /** For installSearch's onSettled. */
    onSettled(settledCategory: Category, settledQuery: string) {
      if (settledCategory === category && settledQuery === query) handlerSettled();
    },
    /** The old request is over, in the handler and in the browser. */
    async settled() {
      await settledInHandler;
      return terminal;
    },
  };
}

function conversation(kind: "channel" | "dm", id: string, type: string, name: string) {
  return {
    conversation_kind: kind,
    conversation_id: id,
    conversation_type: type,
    conversation_name: name,
  };
}

async function openChannelWithHistory(page: Page, targetId: string, count = 1) {
  const history = Array.from({ length: count }, (_, index) =>
    makeMessage({
      id: `${targetId}-m-${index}`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text:
        index === 3
          ? "checklist do backup antigo"
          : `Mensagem ${index} ${"palavra ".repeat(index % 5)}`,
      created_at: `2026-07-10T12:${String(index % 60).padStart(2, "0")}:00.000Z`,
    }),
  );
  const scenario = createScenario({
    kind: "channel",
    targetId,
    targetName: "infraestrutura",
    messages: history,
  });
  await installMessagingMocks(page, scenario);
  return { scenario, history };
}

async function openSearch(page: Page, query: string) {
  await page.getByRole("button", { name: "Buscar no NChat" }).click();
  await expect(searchField(page)).toBeFocused();
  await searchField(page).fill(query);
}

function everyCategory(targetId: string): Results {
  return {
    messages: [
      {
        id: `${targetId}-m-3`,
        ...conversation("channel", targetId, "public", "infraestrutura"),
        sender_id: OTHER_USER_ID,
        sender_display_name: OTHER_USER_NAME,
        body_text: "checklist do backup antigo",
        created_at: "2026-07-10T12:03:00.000Z",
        score: 1,
      },
    ],
    users: [{ id: OTHER_USER_ID, display_name: OTHER_USER_NAME }],
    channels: [
      {
        id: OTHER_CHANNEL_ID,
        slug: "e2e-canal",
        display_name: OTHER_CHANNEL_NAME,
        type: "public",
        description: "Rotina de backup",
        member_count: 4,
        is_general: false,
      },
    ],
    groups: [{ id: GROUP_DM_ID, title: GROUP_DM_NAME, participant_count: 3 }],
    files: [
      {
        id: "e2e-file-backup",
        filename: "relatorio-backup.pdf",
        content_type: "application/pdf",
        size: 2_516_582,
        status: "clean",
        preview_status: "ready",
        message_id: `${targetId}-m-3`,
        ...conversation("channel", targetId, "public", "infraestrutura"),
        created_at: "2026-07-10T12:03:00.000Z",
      },
    ],
    links: [
      linkRow(`${targetId}-m-3`, conversation("channel", targetId, "public", "infraestrutura")),
    ],
  };
}

function linkRow(
  messageId: string,
  where: ReturnType<typeof conversation>,
  url = "https://docs.example.com/runbook",
) {
  return {
    message_id: messageId,
    target_key: "abababababababababababababababab",
    url,
    hostname: new URL(url).host,
    ...where,
    sender_id: OTHER_USER_ID,
    sender_display_name: OTHER_USER_NAME,
    created_at: "2026-07-10T12:03:00.000Z",
  };
}

test.describe("busca global — resultados categorizados (#900)", () => {
  test("Tudo mostra cada categoria encontrada, sem avatar no cabeçalho", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId);
    const fixture = everyCategory(targetId);
    const requests = await installSearch(page, (category) => fixture[category] ?? []);
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "backup");

    for (const name of ["Mensagens", "Pessoas", "Canais", "Grupos", "Arquivos", "Links"]) {
      await expect(section(page, name)).toBeVisible();
    }
    await expect(page.getByRole("tab", { name: "Tudo" })).toHaveAttribute("aria-selected", "true");
    // The search header holds the field and nothing about the signed-in user;
    // the result cards below do carry avatars, so the scope matters.
    const header = page.getByTestId("global-search-header");
    await expect(header.getByRole("searchbox", { name: FIELD })).toBeVisible();
    await expect(header.getByRole("img")).toHaveCount(0);
    await expect(header.locator("img, [class*='avatar']")).toHaveCount(0);
    await expect(section(page, "Pessoas").locator(".global-search__avatar")).toHaveCount(1);
    await expect(section(page, "Pessoas").locator(".global-search__avatar img")).toHaveAttribute(
      "src",
      /^data:image\/svg\+xml/,
    );
    // The client sends the query and a limit — never who is asking. Links
    // carries them in a POST body, so its URL has no query string at all.
    for (const { url, body } of requests) {
      const sent = body ?? Object.fromEntries(url.searchParams);
      expect(Object.keys(sent).sort()).toEqual(["limit", "q"]);
      expect(String(sent.limit)).toBe("5");
    }
    const links = requests.filter(({ url }) => url.pathname === "/api/search/links");
    expect(links).toHaveLength(1);
    expect(links[0].url.search).toBe("");
  });

  test("mensagem antiga abre na mensagem exata, com destaque, fora da primeira página", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const { history } = await openChannelWithHistory(page, targetId, 120);
    await installPaginatedMessages(page, targetId, history);
    const fixture = everyCategory(targetId);
    await installSearch(page, (category) => (category === "messages" ? fixture.messages! : []));
    await page.goto(`/chat/channel/${targetId}`);
    await expect(messageBubble(page, history.at(-1)!.id)).toBeVisible();

    await openSearch(page, "backup");
    const target = history[3];
    const highlighted = page.waitForFunction(
      (id) =>
        document
          .querySelector(`[data-message-id="${id}"]`)
          ?.classList.contains("chat-msg-area__msg--highlight") === true,
      target.id,
    );
    await section(page, "Mensagens")
      .getByRole("button", { name: /checklist do backup antigo/ })
      .click();

    await expect(page).toHaveURL(
      `/chat/channel/${targetId}?message=${encodeURIComponent(target.id)}`,
    );
    await highlighted;
    await expect(messageBubble(page, target.id)).toBeInViewport();
    await expect(messageBubble(page, history.at(-1)!.id)).not.toBeInViewport();
  });

  test("pessoa abre a DM", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const { scenario } = await openChannelWithHistory(page, targetId);
    await installSearch(page, (category) => everyCategory(targetId)[category] ?? []);
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "backup");
    await section(page, "Pessoas")
      .getByRole("button", { name: new RegExp(OTHER_USER_NAME) })
      .click();

    await expect(page).toHaveURL(/\/chat\/dm\/[^/?]+$/);
    expect(scenario.requests.dmCreates).toEqual([{ otherUserId: OTHER_USER_ID }]);
  });

  test("canal e grupo abrem a conversa; Voltar restaura a busca", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId);
    await installSearch(page, (category) => everyCategory(targetId)[category] ?? []);
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "backup");
    await section(page, "Canais")
      .getByRole("button", { name: new RegExp(OTHER_CHANNEL_NAME) })
      .click();
    await expect(page).toHaveURL(`/chat/channel/${OTHER_CHANNEL_ID}`);

    await page.goBack();
    await expect(searchField(page)).toHaveValue("backup");
    await expect(page).toHaveURL(/\/chat\/search$/);
    await section(page, "Grupos")
      .getByRole("button", { name: new RegExp(GROUP_DM_NAME) })
      .click();
    await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
  });

  test("arquivo abre no Attachment Viewer e fecha de volta para a busca", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId);
    await installSearch(page, (category) =>
      category === "files" ? everyCategory(targetId).files! : [],
    );
    const previewRequests: string[] = [];
    await page.route("**/api/files/attachments/*/document-preview**", async (route) => {
      previewRequests.push(new URL(route.request().url()).pathname);
      if (route.request().url().includes("/pages/")) {
        await route.fulfill({ status: 200, contentType: "image/jpeg", body: TINY_JPEG });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: { attachmentId: "e2e-file-backup", kind: "pages", pageCount: 1, labels: ["1"] },
        }),
      });
    });
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "backup");
    const file = section(page, "Arquivos").getByRole("button", { name: /relatorio-backup\.pdf/ });
    await expect(file).toContainText("Verificado");
    await file.click();

    const viewer = page.getByRole("dialog", { name: "relatorio-backup.pdf" });
    await expect(viewer).toBeVisible();
    // The viewer asked file-service, which re-authorizes every request.
    await expect
      .poll(() => previewRequests[0])
      .toBe("/api/files/attachments/e2e-file-backup/document-preview");

    await page.keyboard.press("Escape");
    await expect(viewer).toBeHidden();
    await expect(page).toHaveURL(/\/chat\/search$/);
    await expect(searchField(page)).toHaveValue("backup");
    await expect(file).toBeFocused();
  });

  test("resposta atrasada de uma busca antiga nunca substitui a atual", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId);
    const old = heldSearch(page, "users", "bac");
    await installSearch(
      page,
      async (category, query) => {
        if (category !== "users") return [];
        if (query === "bac") {
          await old.hold();
          return [{ id: "stale-user", display_name: "Resultado Antigo" }];
        }
        return [{ id: OTHER_USER_ID, display_name: OTHER_USER_NAME }];
      },
      old.onSettled,
    );
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "bac");
    await old.requested;
    await searchField(page).fill("backup");
    await expect(section(page, "Pessoas")).toContainText(OTHER_USER_NAME);

    old.release();
    // The superseded request was cancelled by the client, and its answer has
    // left the handler: only now can "it did not land" be asserted.
    expect(await old.settled()).toBe("failed");
    await expect(page.getByText("Resultado Antigo")).toHaveCount(0);
    await expect(section(page, "Pessoas")).toContainText(OTHER_USER_NAME);
  });

  test("web novo com search-service sem POST: cada categoria indisponível, nenhum GET, retry repete o POST", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId, 1);
    const requests: Array<{ method: string; url: URL }> = [];
    // A search-service from before #1081: its modern routes take GET only (a
    // POST is 405) and it has no /links (404). It would answer a GET — the web
    // must never send one, whatever the query.
    await page.route("**/api/search/**", async (route: Route) => {
      const url = new URL(route.request().url());
      const method = route.request().method();
      requests.push({ method, url });
      const answer =
        method === "GET" && !url.pathname.endsWith("/links")
          ? { status: 200, body: envelope({ items: [] }) }
          : url.pathname.endsWith("/links")
            ? { status: 404, body: { error: { code: "not_found", message: "not found" } } }
            : {
                status: 405,
                body: { error: { code: "bad_request", message: "method not allowed" } },
              };
      await route.fulfill({
        status: answer.status,
        contentType: "application/json",
        body: JSON.stringify(answer.body),
      });
    });
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "ABCDEF1234567890");
    for (const name of ["Mensagens", "Pessoas", "Canais", "Grupos", "Arquivos", "Links"]) {
      await expect(section(page, name).getByRole("alert")).toContainText(
        "Esta busca ainda não está disponível.",
      );
    }
    await expect(page.getByTestId("global-search-empty")).toHaveCount(0);
    expect(requests).toHaveLength(6);

    await section(page, "Pessoas").getByRole("button", { name: "Tentar novamente" }).click();
    // The retry is a request of its own: wait for it, then for its answer.
    await expect.poll(() => requests.length).toBe(7);
    await expect(section(page, "Pessoas").getByRole("alert")).toContainText(
      "Esta busca ainda não está disponível.",
    );
    expect(requests.at(-1)!.url.pathname).toBe("/api/search/users");
    for (const { method, url } of requests) {
      expect(method).toBe("POST");
      expect(url.search).toBe("");
    }
  });

  test("aba paginada: Carregar mais envia o cursor, anexa sem duplicar e some no fim", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId);
    const person = (id: string, name: string) => ({ id, display_name: name });
    const requests = await installSearch(page, (category, _query, cursor) => {
      if (category !== "users") return [];
      if (cursor === null) {
        return {
          items: [person("u-a", "Ana Backup"), person("u-b", "Bruno Backup")],
          next: "page-2",
        };
      }
      // The server repeats a row across the page boundary; the list must not.
      return { items: [person("u-b", "Bruno Backup"), person("u-c", "Carla Backup")] };
    });
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "backup");
    await page.getByRole("tab", { name: "Pessoas" }).click();
    const list = results(page).getByRole("list", { name: "Pessoas" });
    await expect(list.getByRole("listitem")).toHaveCount(2);

    await results(page).getByRole("button", { name: "Carregar mais" }).click();

    await expect(list.getByRole("listitem")).toHaveCount(3);
    await expect(list.getByRole("listitem")).toHaveText([
      /Ana Backup/,
      /Bruno Backup/,
      /Carla Backup/,
    ]);
    await expect(results(page).getByRole("button", { name: "Carregar mais" })).toHaveCount(0);
    await expect(results(page).getByText("3 resultados para “backup”")).toBeVisible();
    const pages = requests
      .filter(({ url, body }) => url.pathname.endsWith("/users") && body?.limit === 20)
      .map(({ body }) => body!);
    expect(pages.map((body) => body.cursor ?? null)).toEqual([null, "page-2"]);
    expect(pages.every((body) => body.q === "backup")).toBe(true);
  });

  test("teclado: setas, Home e End trocam de aba; Tab segue para o painel", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId);
    await installSearch(page, (category) => everyCategory(targetId)[category] ?? []);
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "backup");
    await expect(section(page, "Pessoas")).toBeVisible();
    await page.keyboard.press("Tab");
    await expect(page.getByRole("button", { name: "Limpar busca" })).toBeFocused();
    await page.keyboard.press("Tab");
    const tab = (name: string) => page.getByRole("tab", { name });
    await expect(tab("Tudo")).toBeFocused();

    await page.keyboard.press("ArrowRight");
    await expect(tab("Mensagens")).toBeFocused();
    await expect(tab("Mensagens")).toHaveAttribute("aria-selected", "true");
    await expect(results(page)).toHaveAccessibleName("Mensagens");
    await page.keyboard.press("End");
    await expect(tab("Links")).toBeFocused();
    await expect(results(page).getByRole("list", { name: "Links" })).toBeVisible();
    await page.keyboard.press("ArrowRight");
    await expect(tab("Tudo")).toBeFocused();
    await page.keyboard.press("ArrowLeft");
    await expect(tab("Links")).toBeFocused();
    await page.keyboard.press("Home");
    await expect(tab("Tudo")).toHaveAttribute("aria-selected", "true");

    await page.keyboard.press("Tab");
    await expect(results(page)).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(
      section(page, "Mensagens").getByRole("button", { name: /checklist do backup antigo/ }),
    ).toBeFocused();
  });

  test("celular: abas roláveis, resultados e seleção sem hover", async ({ page }, testInfo) => {
    await page.setViewportSize(PHONE);
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId);
    await installSearch(page, (category) => everyCategory(targetId)[category] ?? []);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-nav-toggle").click();
    await openSearch(page, "backup");
    await expect(section(page, "Mensagens")).toBeVisible();

    // Tabs scroll inside their own row; the page itself never scrolls sideways.
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBe(true);
    const files = page.getByRole("tab", { name: "Arquivos" });
    await files.click();
    await expect(files).toHaveAttribute("aria-selected", "true");
    await expect(files).toBeInViewport();

    await page.getByRole("tab", { name: "Grupos" }).click();
    await results(page)
      .getByRole("button", { name: new RegExp(GROUP_DM_NAME) })
      .click();
    await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}`);
  });
});

test.describe("busca global — links (#1081)", () => {
  test("link em canal: Tudo, aba Links e a mensagem exata, com destaque", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const { history } = await openChannelWithHistory(page, targetId, 120);
    await installPaginatedMessages(page, targetId, history);
    const target = history[3];
    const where = conversation("channel", targetId, "public", "infraestrutura");
    await installSearch(page, (category) =>
      category === "links" ? [linkRow(target.id, where)] : [],
    );
    await page.goto(`/chat/channel/${targetId}`);
    await expect(messageBubble(page, history.at(-1)!.id)).toBeVisible();

    await openSearch(page, "docs.example.com");
    await expect(
      section(page, "Links").getByRole("button", { name: /docs\.example\.com/ }),
    ).toBeVisible();
    await page.getByRole("tab", { name: "Links" }).click();
    const card = results(page)
      .getByRole("list", { name: "Links" })
      .getByRole("button", { name: /https:\/\/docs\.example\.com\/runbook/ });
    await expect(card).toContainText("#infraestrutura");
    await expect(card).toContainText(OTHER_USER_NAME);

    const highlighted = page.waitForFunction(
      (id) =>
        document
          .querySelector(`[data-message-id="${id}"]`)
          ?.classList.contains("chat-msg-area__msg--highlight") === true,
      target.id,
    );
    await card.click();
    await expect(page).toHaveURL(
      `/chat/channel/${targetId}?message=${encodeURIComponent(target.id)}`,
    );
    await highlighted;
    await expect(messageBubble(page, target.id)).toBeInViewport();
  });

  test("link em grupo e em DM abrem a mensagem na conversa certa", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "infraestrutura",
      messages: [makeMessage({ id: `${targetId}-m-0` })],
    });
    const groupMessage = makeMessage({ id: "e2e-group-link", body_text: "runbook do grupo" });
    const directMessage = makeMessage({ id: "e2e-direct-link", body_text: "runbook da DM" });
    scenario.messagesByTarget.set(`dm:${GROUP_DM_ID}`, [groupMessage]);
    scenario.messagesByTarget.set("dm:e2e-dm-other", [directMessage]);
    await installMessagingMocks(page, scenario);
    await installSearch(page, (category) =>
      category === "links"
        ? [
            linkRow(groupMessage.id, conversation("dm", GROUP_DM_ID, "group", GROUP_DM_NAME)),
            linkRow(
              directMessage.id,
              conversation("dm", "e2e-dm-other", "direct", OTHER_USER_NAME),
            ),
          ]
        : [],
    );
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "runbook");
    await section(page, "Links")
      .getByRole("button", { name: new RegExp(GROUP_DM_NAME) })
      .click();
    await expect(page).toHaveURL(`/chat/dm/${GROUP_DM_ID}?message=${groupMessage.id}`);
    await expect(messageBubble(page, groupMessage.id)).toBeInViewport();

    await page.goBack();
    await expect(searchField(page)).toHaveValue("runbook");
    await section(page, "Links")
      .getByRole("button", { name: new RegExp(`Conversa com ${OTHER_USER_NAME}`) })
      .click();
    await expect(page).toHaveURL(`/chat/dm/e2e-dm-other?message=${directMessage.id}`);
    await expect(messageBubble(page, directMessage.id)).toBeInViewport();
  });

  test("search-service sem a rota: erro explícito em Links, o resto segue e o retry recupera", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId);
    const fixture = everyCategory(targetId);
    let linksDeployed = false;
    await page.route("**/api/search/**", async (route: Route) => {
      const category = new URL(route.request().url()).pathname.split("/").pop() as Category;
      if (category === "links" && !linksDeployed) {
        await route.fulfill({
          status: 404,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "not_found", message: "not found" } }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(envelope({ items: fixture[category] ?? [] })),
      });
    });
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "backup");
    const links = section(page, "Links");
    await expect(links.getByRole("alert")).toContainText("Esta busca ainda não está disponível.");
    await expect(section(page, "Mensagens")).toBeVisible();
    await expect(page.getByTestId("global-search-empty")).toHaveCount(0);

    linksDeployed = true;
    await links.getByRole("button", { name: "Tentar novamente" }).click();
    await expect(links.getByRole("button", { name: /docs\.example\.com/ })).toBeVisible();
    await expect(links.getByRole("alert")).toHaveCount(0);
  });

  test("resposta atrasada de Links para a busca antiga nunca substitui a atual", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId);
    const where = conversation("channel", targetId, "public", "infraestrutura");
    const old = heldSearch(page, "links", "docs");
    await installSearch(
      page,
      async (category, query) => {
        if (category !== "links") return [];
        if (query === "docs") {
          await old.hold();
          return [linkRow("stale-link", where, "https://antigo.example.com/velho")];
        }
        return [linkRow(`${targetId}-m-0`, where)];
      },
      old.onSettled,
    );
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "docs");
    await old.requested;
    await searchField(page).fill("docs.example.com");
    await expect(section(page, "Links")).toContainText("https://docs.example.com/runbook");

    old.release();
    // The superseded POST was cancelled by the client, and its answer has left
    // the handler: only now can "it did not land" be asserted.
    expect(await old.settled()).toBe("failed");
    await expect(page.getByText("antigo.example.com")).toHaveCount(0);
    await expect(section(page, "Links")).toContainText("https://docs.example.com/runbook");
  });

  for (const secretQuery of [
    "https://example.test/reset/token?signature=SECRET123",
    "ABCDEF1234567890",
  ]) {
    test(`${secretQuery} em Tudo e em cada aba: só POST, nenhuma request com query string`, async ({
      page,
    }, testInfo) => {
      const targetId = uniqueId(testInfo, "channel");
      await openChannelWithHistory(page, targetId);
      const requests = await installSearch(page, () => []);
      const methods: string[] = [];
      page.on("request", (request) => {
        if (request.url().includes("/api/search/")) methods.push(request.method());
      });
      await page.goto(`/chat/channel/${targetId}`);

      await openSearch(page, secretQuery);
      await expect(page.getByTestId("global-search-empty")).toBeVisible();
      for (const name of ["Mensagens", "Pessoas", "Canais", "Grupos", "Arquivos", "Links"]) {
        await page.getByRole("tab", { name }).click();
        await expect(page.getByTestId("global-search-empty")).toBeVisible();
      }

      // 6 overview sections + 6 tabs, every one a POST carrying it in its body.
      expect(requests).toHaveLength(12);
      expect(methods).toEqual(Array(12).fill("POST"));
      for (const { url, body } of requests) {
        expect(url.search).toBe("");
        expect(body?.q).toBe(secretQuery);
      }
    });
  }

  test("teclado: aba Links pelas setas e Enter abre a mensagem", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const { history } = await openChannelWithHistory(page, targetId, 1);
    const where = conversation("channel", targetId, "public", "infraestrutura");
    await installSearch(page, (category) =>
      category === "links" ? [linkRow(history[0].id, where)] : [],
    );
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "docs");
    await expect(section(page, "Links")).toBeVisible();
    await page.getByRole("tab", { name: "Tudo" }).focus();
    await page.keyboard.press("End");
    await expect(page.getByRole("tab", { name: "Links" })).toHaveAttribute("aria-selected", "true");
    // The tab loads its own list: until it renders, the panel holds only a
    // skeleton and there is no card for the second Tab to reach.
    await expect(results(page).getByRole("list", { name: "Links" })).toBeVisible();
    await page.keyboard.press("Tab");
    await expect(results(page)).toBeFocused();
    await page.keyboard.press("Tab");
    const card = results(page).getByRole("button", { name: /docs\.example\.com/ });
    await expect(card).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(
      `/chat/channel/${targetId}?message=${encodeURIComponent(history[0].id)}`,
    );
  });

  test("celular: URL longa recortada sem rolagem lateral, seleção sem hover", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(PHONE);
    const targetId = uniqueId(testInfo, "channel");
    const { history } = await openChannelWithHistory(page, targetId, 1);
    const longUrl = `https://docs.example.com/${"segmento-muito-longo/".repeat(40)}fim?ref=abc`;
    const where = conversation("channel", targetId, "public", "infraestrutura");
    await installSearch(page, (category) =>
      category === "links" ? [linkRow(history[0].id, where, longUrl)] : [],
    );
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByTestId("chat-nav-toggle").click();
    await openSearch(page, "docs");
    await page.getByRole("tab", { name: "Links" }).click();
    const card = results(page).getByRole("button", { name: /docs\.example\.com/ });
    await expect(card).toBeVisible();
    await expect(card).toHaveAccessibleName(/fim\?ref=abc/);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBe(true);
    const box = await card.boundingBox();
    expect(box!.x + box!.width).toBeLessThanOrEqual(PHONE.width);

    await card.click();
    await expect(page).toHaveURL(
      `/chat/channel/${targetId}?message=${encodeURIComponent(history[0].id)}`,
    );
  });
});
