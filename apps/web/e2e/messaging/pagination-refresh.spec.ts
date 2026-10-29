import { expect, test, type Page, type Request, type Route } from "@playwright/test";

import {
  createScenario,
  installMessagingMocks,
  makeMessage,
  sidebarResponseBody,
} from "../helpers/messagingApi";

test.use({ screenshot: "only-on-failure" });

type Surface = "search" | "favorites";
const PAGE_SIZE = 8;
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function installPages(page: Page, surface: Surface) {
  const scenario = createScenario({
    kind: "channel",
    targetId: "pagination-origin",
    targetName: "Origem",
    messages: [],
  });
  await installMessagingMocks(page, scenario);
  const identity = deferred();
  const expiry = deferred();
  const heldPage = deferred();
  const state = { fresh: false, expire: false, deny: false, holdAt: 0, owner: "u1", refreshes: 0 };
  type Call = {
    cursor: string | null;
    fresh: boolean;
    owner: string;
    page: number;
    status: number;
    ids: string[];
    aborted: boolean;
  };
  const calls: Call[] = [];
  const requests = new WeakMap<Request, Call>();
  page.on("requestfailed", (request) => {
    const call = requests.get(request);
    if (call) call.aborted = true;
  });
  await page.route("**/api/auth/refresh", async (route) => {
    state.fresh = true;
    state.expire = false;
    state.refreshes += 1;
    await route.fulfill({
      json: { access_token: "pagination-rotated", token_type: "Bearer", expires_in: 900 },
    });
  });
  await page.route("**/api/chat/sidebar", async (route) => {
    if (state.fresh) await identity.promise;
    const body = JSON.parse(sidebarResponseBody(scenario)) as { data: { current_user_id: string } };
    body.data.current_user_id = state.owner;
    await route.fulfill({ json: body });
  });
  const serve = async (route: Route) => {
    const url = new URL(route.request().url());
    const body =
      route.request().method() === "POST"
        ? (route.request().postDataJSON() as { cursor?: string })
        : {};
    const cursor = url.searchParams.get("before") ?? body.cursor ?? null;
    const number = cursor ? Number(cursor.split("-").at(-1)) + 1 : 1;
    const owner = state.owner;
    const fresh = state.fresh;
    const status = state.expire
      ? 401
      : state.deny || (fresh && !!cursor?.startsWith("old-"))
        ? 403
        : 200;
    const prefix = `${owner}-${fresh ? "new" : "old"}`;
    const ids = Array.from({ length: PAGE_SIZE }, (_, index) => `${prefix}-${number}-${index}`);
    const call = {
      cursor,
      fresh,
      owner,
      page: number,
      status,
      ids: status === 200 ? ids : [],
      aborted: false,
    };
    calls.push(call);
    requests.set(route.request(), call);
    if (calls.length > 40)
      throw new Error("Controlled pagination transport exceeded its finite call budget");
    if (status === 401) await expiry.promise;
    if (fresh && number === state.holdAt) await heldPage.promise;
    if (status !== 200) {
      await route.fulfill({
        status,
        json: { error: { code: status === 401 ? "token_expired" : "forbidden" } },
      });
      return;
    }
    const data = ids.map((id) => ({ id, title: `Resultado ${id}`, participant_count: 2 }));
    // A repeated boundary item must not become a duplicate row.
    if (fresh && number > 1)
      data.unshift({
        id: `${prefix}-${number - 1}-${PAGE_SIZE - 1}`,
        title: `Resultado ${prefix}-${number - 1}-${PAGE_SIZE - 1}`,
        participant_count: 2,
      });
    const next = `${fresh ? "new" : "old"}-${number}`;
    await route.fulfill({
      json:
        surface === "favorites"
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
  await page.route("**/api/chat/favorites**", serve);
  await page.route("**/api/search/**", async (route) => {
    if (new URL(route.request().url()).pathname.endsWith("/groups")) await serve(route);
    else
      await route.fulfill({
        json: { data: { data: [], pagination: { next_cursor: null, has_more: false } } },
      });
  });
  const root = page.getByTestId(surface === "search" ? "global-search" : "chat-favorites");
  const rows =
    surface === "search"
      ? root.getByRole("list", { name: "Grupos", exact: true }).getByRole("listitem")
      : root.getByTestId("favorite-item");
  const more = root.getByRole("button", { name: "Carregar mais", exact: true });
  const recover = root.getByRole("button", { name: "Recuperar contexto de leitura", exact: true });
  const result = (id: string) =>
    surface === "search"
      ? root.getByRole("button", { name: `Grupo Resultado ${id} 2 participantes`, exact: true })
      : root.getByText(`Resultado ${id}`, { exact: true });
  const fresh = () => calls.filter((call) => call.fresh && call.status === 200 && !call.aborted);
  const open = async (depth: number) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`/chat/${surface}`);
    if (surface === "search") {
      await page.getByRole("tab", { name: "Grupos", exact: true }).click();
      await page.getByRole("searchbox").fill("resultado");
    }
    for (let number = 1; number <= depth; number += 1) {
      await expect(result(`u1-old-${number}-${PAGE_SIZE - 1}`)).toBeAttached();
      if (number < depth) await more.click();
    }
    const aborted = calls.filter((call) => call.aborted);
    // StrictMode may abort one initial mount. Count it explicitly, never as an authorized page.
    expect(aborted.length).toBeLessThanOrEqual(1);
    expect(aborted.every((call) => call.cursor === null && !call.fresh)).toBe(true);
    expect(calls.filter((call) => !call.aborted).map((call) => call.cursor)).toEqual([
      null,
      ...Array.from({ length: depth - 1 }, (_, i) => `old-${i + 1}`),
    ]);
    await expect(rows).toHaveCount(depth * PAGE_SIZE);
  };
  const rotate = async (preserveExternalFocus = false) => {
    state.expire = true;
    await more.click();
    const top = await root.evaluate((element) => element.scrollTop);
    expect(top).toBeGreaterThan(0);
    expiry.resolve();
    await expect(root.getByText("Confirmando sua sessão…", { exact: true })).toBeVisible();
    if (preserveExternalFocus) {
      await page.getByRole("button", { name: "Buscar no NChat", exact: true }).focus();
    }
    await expect(root.getByText(/Resultado u1-old-/)).toHaveCount(0);
    expect(fresh()).toHaveLength(0);
    identity.resolve();
    return top;
  };
  return { state, calls, fresh, root, rows, more, recover, result, open, rotate, heldPage };
}

for (const movedFocus of [false, true]) {
  test(`L-01: favorites final explicit batch, intentional external focus=${movedFocus}`, async ({
    page,
  }) => {
    const fixture = await installPages(page, "favorites");
    await fixture.open(9);
    const previousTop = await fixture.rotate(true);
    await expect(fixture.result("u1-new-2-7")).toBeAttached();
    // Leave the recovery control for an external control before explicitly resuming.
    await fixture.recover.focus();
    await page.getByRole("button", { name: "Buscar no NChat", exact: true }).focus();
    await expect(page.getByRole("button", { name: "Buscar no NChat", exact: true })).toBeFocused();
    await fixture.recover.click();
    await expect(fixture.rows).toHaveCount(7 * PAGE_SIZE);
    await expect(fixture.recover).toBeFocused();
    fixture.state.holdAt = 8;
    await fixture.recover.click();
    await expect.poll(() => fixture.fresh().length).toBe(8);
    if (movedFocus)
      await page.getByRole("button", { name: "Buscar no NChat", exact: true }).focus();
    fixture.heldPage.resolve();
    await expect(fixture.rows).toHaveCount(9 * PAGE_SIZE);
    await expect(fixture.recover).toHaveCount(0);
    await test.info().attach("final-focus", {
      body: JSON.stringify(await page.evaluate(() => document.activeElement?.outerHTML)),
      contentType: "application/json",
    });
    if (movedFocus) {
      await expect(
        page.getByRole("button", { name: "Buscar no NChat", exact: true }),
      ).toBeFocused();
      await expect(fixture.root).not.toBeFocused();
    } else {
      await expect(fixture.root).toBeVisible();
      await expect(fixture.root).toBeFocused();
    }
    await expect
      .poll(() => fixture.root.evaluate((element) => element.scrollTop))
      .toBeCloseTo(previousTop, 0);
    expect(fixture.fresh().map((call) => call.cursor)).toEqual([
      null,
      ...Array.from({ length: 8 }, (_, i) => `new-${i + 1}`),
    ]);
    await fixture.more.click();
    await expect(fixture.rows).toHaveCount(10 * PAGE_SIZE);
    expect(fixture.fresh().at(-1)?.cursor).toBe("new-9");
  });
}

for (const surface of ["search", "favorites"] as const) {
  for (const depth of [2, 9]) {
    test(`${surface}: R1 depth ${depth}, authorized rebuild 2, explicit batches 5 then 2`, async ({
      page,
    }) => {
      const fixture = await installPages(page, surface);
      const { calls, fresh, rows, root, more, recover, result } = fixture;
      await fixture.open(depth);
      const previousTop = await fixture.rotate();
      await expect(result("u1-new-2-7")).toBeAttached();
      await expect(rows).toHaveCount(2 * PAGE_SIZE);
      expect(fresh().map((call) => call.cursor)).toEqual([null, "new-1"]);
      expect(calls.filter((call) => call.status === 401)).toHaveLength(1);
      expect(fixture.state.refreshes).toBe(1);
      if (depth === 9) {
        await expect(recover).toBeVisible();
        expect(await root.evaluate((element) => element.scrollTop)).toBe(0);
        await expect(root.getByText(/Faltam até 7 páginas/)).toBeVisible();
        await recover.click();
        await expect(result("u1-new-7-7")).toBeAttached();
        await expect(rows).toHaveCount(7 * PAGE_SIZE);
        expect(fresh().map((call) => call.cursor)).toEqual([
          null,
          ...Array.from({ length: 6 }, (_, i) => `new-${i + 1}`),
        ]);
        await expect(root.getByText(/Faltam até 2 páginas/)).toBeVisible();
        await expect(recover).toBeFocused();
        await recover.click();
        await expect(result("u1-new-9-7")).toBeAttached();
        await expect(rows).toHaveCount(9 * PAGE_SIZE);
        await expect(recover).toHaveCount(0);
        expect(fresh()).toHaveLength(9);
      }
      await expect
        .poll(() => root.evaluate((element) => element.scrollTop))
        .toBeCloseTo(previousTop, 0);
      await expect(more).toBeFocused();
      await more.click();
      await expect(result(`u1-new-${depth + 1}-7`)).toBeAttached();
      await expect(rows).toHaveCount((depth + 1) * PAGE_SIZE);
      expect(fresh().map((call) => call.cursor)).toEqual([
        null,
        ...Array.from({ length: depth }, (_, i) => `new-${i + 1}`),
      ]);
      await expect(root.getByText(/Resultado u1-old-/)).toHaveCount(0);
      if (surface === "search") {
        await expect(page.getByRole("searchbox")).toHaveValue("resultado");
        await expect(page.getByRole("tab", { name: "Grupos", exact: true })).toHaveAttribute(
          "aria-selected",
          "true",
        );
      }
      await test
        .info()
        .attach("requests", { body: JSON.stringify(calls), contentType: "application/json" });
    });
  }

  for (const interrupt of ["navigation", "session"] as const) {
    test(`${surface}: R1 interruption by ${interrupt} drops a held old-owner batch`, async ({
      page,
    }) => {
      const fixture = await installPages(page, surface);
      await fixture.open(9);
      await fixture.rotate();
      await expect(fixture.result("u1-new-2-7")).toBeAttached();
      fixture.state.holdAt = 3;
      await fixture.recover.click();
      await expect.poll(() => fixture.fresh().filter((call) => call.owner === "u1").length).toBe(3);
      const cancelled = page.waitForEvent(
        "requestfailed",
        (request) =>
          request.url().includes("/api/chat/favorites?before=new-2") ||
          (request.url().endsWith("/api/search/groups") &&
            request.postData()?.includes('"cursor":"new-2"') === true),
      );
      if (interrupt === "navigation") {
        await page.getByRole("link", { name: "Nchat — Workspace Nic-Labs", exact: true }).click();
        await expect(page).toHaveURL(/\/chat$/);
      } else {
        fixture.state.owner = "u2";
        await page.evaluate(async () => {
          const path = "/src/lib/authSession.ts";
          const session = await import(path);
          session.setTokens("replacement-u2", "session");
        });
        if (surface === "search") {
          await expect(page.getByRole("searchbox")).toHaveValue("");
          await page.getByRole("tab", { name: "Grupos", exact: true }).click();
          await page.getByRole("searchbox").fill("resultado");
        }
        await expect(fixture.result("u2-new-1-0")).toBeAttached();
      }
      await cancelled;
      fixture.heldPage.resolve();
      await expect(fixture.result("u1-new-3-0")).toHaveCount(0);
      await expect(fixture.recover).toHaveCount(0);
      expect(
        fixture.calls
          .filter((call) => call.fresh && call.owner === "u1" && call.status === 200)
          .map((call) => call.page),
      ).toEqual([1, 2, 3]);
      expect(
        fixture.calls.find((call) => call.fresh && call.owner === "u1" && call.page === 3)?.aborted,
      ).toBe(true);
      await test.info().attach("requests", {
        body: JSON.stringify(fixture.calls),
        contentType: "application/json",
      });
    });
  }

  test(`${surface}: revoked access returns 403 and never presents old data`, async ({ page }) => {
    const fixture = await installPages(page, surface);
    await fixture.open(2);
    fixture.state.deny = true;
    // The initial 401 still wins, then all authorized revalidation gets 403.
    await fixture.rotate();
    await expect(fixture.root.getByRole("alert")).toBeVisible();
    await expect(fixture.rows).toHaveCount(0);
    await expect(fixture.root.getByText(/Resultado u1-(old|new)-/)).toHaveCount(0);
    await expect(
      fixture.root.getByRole("button", { name: "Tentar novamente", exact: true }),
    ).toBeVisible();
    expect(
      fixture.calls.filter((call) => call.fresh && call.cursor === null).map((call) => call.status),
    ).toEqual([403]);
    expect(fixture.state.refreshes).toBe(1);
    if (surface === "search") {
      await expect(page.getByRole("searchbox")).toHaveValue("resultado");
      await expect(page.getByRole("tab", { name: "Grupos", exact: true })).toHaveAttribute(
        "aria-selected",
        "true",
      );
    }
    await test
      .info()
      .attach("requests", { body: JSON.stringify(fixture.calls), contentType: "application/json" });
  });
}
