import { expect, test, type Page, type Route } from "@playwright/test";

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
 */

const PHONE = { width: 390, height: 844 };
const FIELD = "Buscar mensagens, pessoas, canais, grupos e arquivos";

type Category = "messages" | "users" | "channels" | "groups" | "files";
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

/**
 * Serves /api/search/** (the V2 message route included): `answer` picks the
 * rows for a category and query.
 * Every request URL is recorded so a spec can assert what the client sent.
 */
async function installSearch(
  page: Page,
  answer: (
    category: Category,
    query: string,
    cursor: string | null,
  ) => unknown[] | Answer | Promise<unknown[]>,
) {
  const requests: URL[] = [];
  await page.route("**/api/search/**", async (route: Route) => {
    const url = new URL(route.request().url());
    requests.push(url);
    const category = url.pathname.split("/").pop() as Category;
    const rows = await answer(
      category,
      url.searchParams.get("q") ?? "",
      url.searchParams.get("cursor"),
    );
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(envelope(Array.isArray(rows) ? { items: rows } : rows)),
    });
  });
  return requests;
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

    for (const name of ["Mensagens", "Pessoas", "Canais", "Grupos", "Arquivos"]) {
      await expect(section(page, name)).toBeVisible();
    }
    await expect(page.getByRole("tab", { name: "Tudo" })).toHaveAttribute("aria-selected", "true");
    // The search header holds the field and nothing about the signed-in user;
    // the result cards below do carry avatars, so the scope matters.
    const header = page.getByTestId("global-search-header");
    await expect(header.getByRole("searchbox", { name: FIELD })).toBeVisible();
    await expect(header.getByRole("img")).toHaveCount(0);
    await expect(header.locator("img, [class*='avatar']")).toHaveCount(0);
    await expect(section(page, "Pessoas").locator("[class*='avatar']")).toHaveCount(1);
    // The client sends the query and a limit — never who is asking.
    for (const url of requests) {
      expect([...url.searchParams.keys()].sort()).toEqual(["limit", "q"]);
      expect(url.searchParams.get("limit")).toBe("5");
    }
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
    let releaseOld!: () => void;
    const oldAnswered = new Promise<void>((resolve) => (releaseOld = resolve));
    let oldAsked!: () => void;
    const oldRequested = new Promise<void>((resolve) => (oldAsked = resolve));
    await installSearch(page, async (category, query) => {
      if (category !== "users") return [];
      if (query === "bac") {
        oldAsked();
        await oldAnswered;
        return [{ id: "stale-user", display_name: "Resultado Antigo" }];
      }
      return [{ id: OTHER_USER_ID, display_name: OTHER_USER_NAME }];
    });
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "bac");
    await oldRequested;
    await searchField(page).fill("backup");
    await expect(section(page, "Pessoas")).toContainText(OTHER_USER_NAME);

    releaseOld();
    await expect(page.getByText("Resultado Antigo")).toHaveCount(0);
    await expect(section(page, "Pessoas")).toContainText(OTHER_USER_NAME);
  });

  test("web novo com search-service anterior: V2 ausente cai uma vez no legado e navega ao canal", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    await openChannelWithHistory(page, targetId, 1);
    const target = `${targetId}-m-0`;
    const requests: string[] = [];
    // A search-service from before #900: no /v2/messages (its catch-all 404),
    // and the channel-only legacy shape on /messages.
    await page.route("**/api/search/**", async (route: Route) => {
      const url = new URL(route.request().url());
      requests.push(url.pathname);
      if (url.pathname.endsWith("/v2/messages")) {
        await route.fulfill({
          status: 404,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "not_found", message: "not found" } }),
        });
        return;
      }
      const legacy = url.pathname.endsWith("/messages")
        ? [
            {
              id: target,
              channel_id: targetId,
              channel_name: "infraestrutura",
              sender_id: OTHER_USER_ID,
              sender_display_name: OTHER_USER_NAME,
              body_text: "Mensagem do backup legado",
              created_at: "2026-07-10T12:00:00.000Z",
              score: 1,
            },
          ]
        : [];
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(envelope({ items: legacy })),
      });
    });
    await page.goto(`/chat/channel/${targetId}`);

    await openSearch(page, "backup");
    const card = section(page, "Mensagens").getByRole("button", { name: /backup legado/ });
    await expect(card).toContainText("em #infraestrutura");
    await expect(card).not.toContainText("undefined");
    const messageRequests = requests.filter((path) => path.endsWith("/messages"));
    expect(messageRequests).toEqual(["/api/search/v2/messages", "/api/search/messages"]);

    await card.click();
    await expect(page).toHaveURL(`/chat/channel/${targetId}?message=${encodeURIComponent(target)}`);
    await expect(messageBubble(page, target)).toBeInViewport();
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
    const pages = requests.filter(
      (url) => url.pathname.endsWith("/users") && url.searchParams.get("limit") === "20",
    );
    expect(pages.map((url) => url.searchParams.get("cursor"))).toEqual([null, "page-2"]);
    expect(pages.every((url) => url.searchParams.get("q") === "backup")).toBe(true);
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
    await expect(tab("Arquivos")).toBeFocused();
    await expect(results(page).getByRole("list", { name: "Arquivos" })).toBeVisible();
    await page.keyboard.press("ArrowRight");
    await expect(tab("Tudo")).toBeFocused();
    await page.keyboard.press("ArrowLeft");
    await expect(tab("Arquivos")).toBeFocused();
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
