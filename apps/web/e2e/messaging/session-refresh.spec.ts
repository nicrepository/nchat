import { expect, test, type Route } from "@playwright/test";

import {
  createScenario,
  installMessagingMocks,
  makeMessage,
  sidebarResponseBody,
} from "../helpers/messagingApi";

test.use({ screenshot: "only-on-failure" });

for (const width of [390, 1440]) {
  for (const surface of ["search", "favorites"] as const) {
    for (const moveFocus of [false, true]) {
      test(`${surface}, ${width}px: refresh preserves reading position${moveFocus ? " and intentional external focus" : " and pagination focus"}`, async ({
        page,
      }) => {
        await page.setViewportSize({ width, height: 844 });
        const scenario = createScenario({
          kind: "channel",
          targetId: "refresh-test",
          targetName: "Refresh",
          messages: [],
        });
        await installMessagingMocks(page, scenario);
        let expire = false;
        let refreshed = false;
        const calls: Array<{
          cursor: string | null;
          fresh: boolean;
          status: number;
          ids: string[];
        }> = [];
        let releaseExpiry!: () => void;
        let releaseIdentity!: () => void;
        const expiry = new Promise<void>((resolve) => {
          releaseExpiry = resolve;
        });
        const identity = new Promise<void>((resolve) => {
          releaseIdentity = resolve;
        });
        await page.route("**/api/auth/refresh", async (route) => {
          refreshed = true;
          expire = false;
          await route.fulfill({
            json: { access_token: "e2e-refresh-token", token_type: "Bearer", expires_in: 900 },
          });
        });
        await page.route("**/api/chat/sidebar", async (route) => {
          if (refreshed) await identity;
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: sidebarResponseBody(scenario),
          });
        });
        const servePage = async (route: Route) => {
          const url = new URL(route.request().url());
          const body =
            route.request().method() === "POST"
              ? (route.request().postDataJSON() as { cursor?: string })
              : {};
          const cursor = url.searchParams.get("before") ?? body.cursor;
          if (expire) {
            calls.push({ cursor: cursor ?? null, fresh: refreshed, status: 401, ids: [] });
            await expiry;
            await route.fulfill({ status: 401, json: { error: { code: "token_expired" } } });
            return;
          }
          const offset = cursor ? 30 : 0;
          const next = `${refreshed ? "new" : "old"}-${cursor ? 2 : 1}`;
          const data = Array.from({ length: 30 }, (_, index) => ({
            id: `refresh-${refreshed ? "new" : "old"}-${offset + index}`,
            title: `Resultado ${refreshed ? "novo " : ""}${offset + index}`,
            participant_count: 2,
          }));
          calls.push({
            cursor: cursor ?? null,
            fresh: refreshed,
            status: 200,
            ids: data.map((item) => item.id),
          });
          await route.fulfill({
            json: url.pathname.endsWith("/favorites")
              ? {
                  data: {
                    favorites: data.map(({ id, title }) => ({
                      message: makeMessage({ id, body_text: title }),
                      favorited_at: "2026-01-01T00:00:00Z",
                    })),
                    next_cursor: next,
                  },
                }
              : { data: { data, pagination: { next_cursor: next, has_more: true } } },
          });
        };
        await page.route("**/api/chat/favorites**", servePage);
        await page.route("**/api/search/**", async (route) => {
          if (new URL(route.request().url()).pathname.endsWith("/groups")) await servePage(route);
          else
            await route.fulfill({
              json: { data: { data: [], pagination: { next_cursor: null, has_more: false } } },
            });
        });
        await page.goto(`/chat/${surface}`);
        if (surface === "search") {
          await page.getByRole("tab", { name: "Grupos" }).click();
          await page.getByRole("searchbox").fill("resultado");
        }
        const root = page.getByTestId(surface === "search" ? "global-search" : "chat-favorites");
        const more = page.getByRole("button", { name: "Carregar mais", exact: true });
        const result = (index: number, fresh = false) =>
          surface === "search"
            ? root.getByRole("button", {
                name: `Grupo Resultado ${fresh ? "novo " : ""}${index} 2 participantes`,
                exact: true,
              })
            : root.getByText(`Resultado ${fresh ? "novo " : ""}${index}`, { exact: true });
        await expect(result(0)).toBeVisible();
        await more.click();
        await expect(result(59)).toBeAttached();
        expire = true;
        await more.click();
        const before = await root.evaluate((element) => element.scrollTop);
        expect(before).toBeGreaterThan(0);
        releaseExpiry();
        await expect(page.getByText("Confirmando sua sessão…", { exact: true })).toBeVisible();
        await expect(result(0)).toHaveCount(0);
        await expect(result(0, true)).toHaveCount(0);
        expect(calls.filter((call) => call.fresh && call.cursor === null)).toEqual([]);
        const outside =
          width < 1024
            ? page.getByTestId("chat-nav-toggle")
            : page.getByRole("button", { name: "Buscar no NChat", exact: true });
        if (moveFocus) await outside.focus();
        releaseIdentity();
        await expect(result(59, true)).toBeAttached();
        await expect(result(0)).toHaveCount(0);
        await expect(result(59)).toHaveCount(0);
        await expect(moveFocus ? outside : more).toBeFocused();
        await expect
          .poll(() => root.evaluate((element) => element.scrollTop))
          .toBeCloseTo(before, 0);
        if (surface === "search") {
          await expect(page.getByRole("searchbox")).toHaveValue("resultado");
          await expect(page.getByRole("tab", { name: "Grupos" })).toHaveAttribute(
            "aria-selected",
            "true",
          );
        }
        const rebuilt = calls.filter(
          (call) => call.fresh && (call.cursor === null || call.cursor.startsWith("new-")),
        );
        expect(rebuilt.map((call) => call.cursor)).toEqual([null, "new-1"]);
        expect(rebuilt.flatMap((call) => call.ids)).toEqual(
          Array.from({ length: 60 }, (_, index) => `refresh-new-${index}`),
        );
        await test.info().attach("refresh-requests-and-ids", {
          contentType: "application/json",
          body: JSON.stringify(calls),
        });
      });
    }
  }
}
