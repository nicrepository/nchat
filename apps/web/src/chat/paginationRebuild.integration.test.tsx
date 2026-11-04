import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter, Outlet, Route, Routes, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { _resetState } from "../lib/authClient";
import { clearTokens, setTokens } from "../lib/authSession";
import GlobalSearchPage from "../search/GlobalSearchPage";
import FavoritesPage from "./FavoritesPage";
import { useChatSidebar } from "./useChatSidebar";

vi.mock("./useChatWebSocket", () => ({ useChatWebSocket: () => ({ toggleReaction: vi.fn() }) }));

type Surface = "search" | "favorites";
let refreshed: boolean;
let requests: Array<{
  cursor: string | null;
  fresh: boolean;
  owner: string;
  signal?: AbortSignal | null;
}>;
let releaseIdentity: (() => void) | undefined;
let releasePage: (() => void) | undefined;
let holdAt: number;
let endAt: number;
let failAt: number;
let failStatus: number;
let viewer: string;
let workspace: string;
let revoked: boolean;
let duplicate: boolean;
let cursorPrefix: string;
let refreshes: number;
let sidebarFails: boolean;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function Shell() {
  const { state, confirmingIdentity, retry } = useChatSidebar();
  const navigate = useNavigate();
  return (
    <>
      <button onClick={() => navigate("/elsewhere")}>Outra rota</button>
      <Outlet
        context={{
          sessionIdentity:
            state.status === "ready" && !confirmingIdentity
              ? `${state.currentUserId}:${state.workspaceId}`
              : null,
          sidebarLoading: state.status === "loading",
          currentUserId: state.status === "ready" ? state.currentUserId : "",
          refreshConversations: retry,
        }}
      />
    </>
  );
}

beforeEach(() => {
  vi.useFakeTimers();
  _resetState();
  clearTokens();
  localStorage.clear();
  setTokens("initial");
  refreshed = false;
  requests = [];
  releaseIdentity = undefined;
  releasePage = undefined;
  holdAt = failAt = 0;
  endAt = Infinity;
  failStatus = 500;
  viewer = "u1";
  workspace = "w1";
  revoked = duplicate = false;
  cursorPrefix = "new";
  refreshes = 0;
  sidebarFails = false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit) => {
      const url = new URL(input, "http://localhost");
      if (url.pathname === "/api/auth/refresh") {
        refreshes += 1;
        return json({
          access_token: `rotation-${refreshes}`,
          token_type: "Bearer",
          expires_in: 900,
        });
      }
      if (url.pathname === "/api/chat/channel-categories") return json({ data: { groups: [] } });
      if (url.pathname === "/api/chat/sidebar") {
        if (refreshed)
          await new Promise<void>((resolve) => {
            releaseIdentity = resolve;
          });
        if (sidebarFails) return json({}, 503);
        return json({
          data: { current_user_id: viewer, workspace: { id: workspace }, channels: [] },
        });
      }
      if (url.pathname === "/api/chat/favorites" || url.pathname === "/api/search/groups") {
        const body = init.body ? (JSON.parse(String(init.body)) as { cursor?: string }) : {};
        const cursor = url.searchParams.get("before") ?? body.cursor ?? null;
        requests.push({
          cursor,
          fresh: refreshed,
          owner: `${viewer}:${workspace}`,
          signal: init.signal,
        });
        if (requests.length > 120) throw new Error("Bounded test transport exceeded");
        const page = cursor ? Number(cursor.split("-").at(-1)) + 1 : 1;
        const owner = viewer === "u1" && workspace === "w1" ? "" : `${viewer}:${workspace}-`;
        const title = `${owner}${refreshed ? "fresh" : "old"}-page${page}`;
        const next =
          refreshed && (page >= endAt || revoked)
            ? ""
            : `${refreshed ? cursorPrefix : "old"}-${page}`;
        if (refreshed && page === failAt) return json({ error: { code: "failed" } }, failStatus);
        if (refreshed && page === holdAt)
          await new Promise<void>((resolve) => {
            releasePage = resolve;
          });
        const pages = revoked && refreshed ? [] : [page];
        if (duplicate && refreshed && page > 1) pages.unshift(page - 1);
        if (url.pathname.endsWith("/favorites"))
          return json({
            data: {
              favorites: pages.map((n) => ({
                message: {
                  id: `item-${n}`,
                  sender_id: "u1",
                  body_text: n === page ? title : `fresh-page${n}`,
                  created_at: "2026-01-01T00:00:00Z",
                },
                favorited_at: "2026-01-01T00:00:00Z",
              })),
              next_cursor: next,
            },
          });
        return json({
          data: {
            data: pages.map((n) => ({
              id: `item-${n}`,
              title: n === page ? title : `fresh-page${n}`,
              participant_count: 2,
            })),
            pagination: { next_cursor: next, has_more: !!next },
          },
        });
      }
      if (url.pathname.startsWith("/api/search/"))
        return json({ data: { data: [], pagination: { next_cursor: null, has_more: false } } });
      throw new Error(`Unexpected local test request: ${url.pathname}`);
    }),
  );
});

async function flush() {
  for (let tick = 0; tick < 6; tick += 1)
    await act(async () => {
      await Promise.resolve();
    });
}

afterEach(async () => {
  cleanup();
  await act(async () => {
    releaseIdentity?.();
    releasePage?.();
  });
  clearTokens();
  _resetState();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

function root(surface: Surface) {
  return screen.getByTestId(surface === "search" ? "global-search" : "chat-favorites");
}

async function loadPages(surface: Surface, pages: number, strict = false) {
  const content = (
    <MemoryRouter initialEntries={["/test"]}>
      <Routes>
        <Route element={<Shell />}>
          <Route
            path="/test"
            element={surface === "search" ? <GlobalSearchPage /> : <FavoritesPage />}
          />
        </Route>
        <Route path="/elsewhere" element={<div>Outra página</div>} />
      </Routes>
    </MemoryRouter>
  );
  render(strict ? <StrictMode>{content}</StrictMode> : content);
  await flush();
  if (surface === "search") {
    fireEvent.click(screen.getByRole("tab", { name: "Grupos" }));
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "texto" } });
    await act(async () => {
      vi.advanceTimersByTime(300);
    });
    await flush();
  }
  for (let page = 1; page < pages; page += 1) {
    fireEvent.click(within(root(surface)).getByRole("button", { name: /carregar mais/i }));
    await flush();
  }
  expect(within(root(surface)).getByText(`old-page${pages}`)).toBeInTheDocument();
  if (!strict) expect(requests).toHaveLength(pages);
}

async function rotate(surface: Surface) {
  refreshed = true;
  act(() => setTokens("rotated", "refresh"));
  await flush();
  expect(within(root(surface)).queryByText(/old-page/)).not.toBeInTheDocument();
  expect(releaseIdentity).toBeTypeOf("function");
  await act(async () => {
    releaseIdentity!();
  });
  await flush();
}

describe.each(["search", "favorites"] as const)(
  "%s: automatic pagination reconstruction (R1)",
  (surface) => {
    it("server end during an explicit batch bounds the next refresh to the depth actually reached", async () => {
      await loadPages(surface, 8);
      await rotate(surface);
      endAt = 4;
      fireEvent.click(
        within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
      );
      await flush();
      expect(within(root(surface)).getByText("fresh-page4")).toBeInTheDocument();
      expect(
        within(root(surface)).queryByRole("button", { name: /carregar mais|recuperar contexto/i }),
      ).not.toBeInTheDocument();
      endAt = Infinity;
      act(() => setTokens("after-server-end", "refresh"));
      await flush();
      await act(async () => {
        releaseIdentity!();
      });
      await flush();
      const count = requests.length;
      fireEvent.click(
        within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
      );
      await flush();
      expect(requests).toHaveLength(count + 2);
      expect(within(root(surface)).queryByText("fresh-page5")).not.toBeInTheDocument();
    });

    if (surface === "favorites")
      it("retrying identity confirmation preserves the pending reading recovery", async () => {
        await loadPages(surface, 8);
        sidebarFails = true;
        await rotate(surface);
        await act(async () => {
          releaseIdentity!();
        });
        await flush();
        const retry = within(root(surface)).getByRole("button", { name: "Tentar novamente" });
        retry.focus();
        fireEvent.click(retry);
        await flush();
        sidebarFails = false;
        await act(async () => {
          releaseIdentity!();
        });
        await flush();
        expect(requests.filter((r) => r.fresh)).toHaveLength(2);
        expect(
          within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
        ).toBeInTheDocument();
      });

    if (surface === "search")
      it("switching tabs also aborts the initial automatic rebuild", async () => {
        await loadPages(surface, 8);
        holdAt = 1;
        await rotate(surface);
        const late = releasePage!;
        const pending = requests.at(-1)!;
        fireEvent.click(screen.getByRole("tab", { name: "Pessoas" }));
        await flush();
        expect(pending.signal?.aborted).toBe(true);
        const count = requests.length;
        await act(async () => {
          late();
        });
        await flush();
        expect(requests).toHaveLength(count);
      });

    it("keeps scroll and focus pending until the explicit recovery reaches the old depth", async () => {
      vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(() =>
        !refreshed || requests.filter((r) => r.fresh).length >= 8 ? 1200 : 400,
      );
      vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(200);
      await loadPages(surface, 8);
      root(surface).scrollTop = 600;
      fireEvent.scroll(root(surface));
      within(root(surface))
        .getByRole("button", { name: /carregar mais/i })
        .focus();
      await rotate(surface);
      expect(root(surface).scrollTop).toBe(0);
      const recover = within(root(surface)).getByRole("button", {
        name: "Recuperar contexto de leitura",
      });
      recover.focus();
      fireEvent.click(recover);
      await flush();
      expect(root(surface).scrollTop).toBe(0);
      fireEvent.click(recover);
      await flush();
      expect(root(surface).scrollTop).toBe(600);
      expect(within(root(surface)).getByRole("button", { name: /carregar mais/i })).toHaveFocus();
    });

    if (surface === "search") {
      it.each(["query", "tab"])("changing %s cancels an in-flight recovery", async (change) => {
        await loadPages(surface, 8);
        await rotate(surface);
        holdAt = 3;
        fireEvent.click(
          within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
        );
        await flush();
        const late = releasePage!;
        const pending = requests.at(-1)!;
        if (change === "tab") fireEvent.click(screen.getByRole("tab", { name: "Pessoas" }));
        else {
          fireEvent.change(screen.getByRole("searchbox"), { target: { value: "outra consulta" } });
          await act(async () => {
            vi.advanceTimersByTime(300);
          });
        }
        await flush();
        expect(pending.signal?.aborted).toBe(true);
        const count = requests.length;
        await act(async () => {
          late();
        });
        await flush();
        expect(requests).toHaveLength(count);
        expect(within(root(surface)).queryByText("fresh-page3")).not.toBeInTheDocument();
        expect(
          within(root(surface)).queryByRole("button", { name: "Recuperar contexto de leitura" }),
        ).not.toBeInTheDocument();
      });
    }

    it("StrictMode and double activation still request one five-page batch", async () => {
      await loadPages(surface, 8, true);
      duplicate = true;
      await rotate(surface);
      expect(requests.filter((r) => r.fresh)).toHaveLength(2);
      const recover = within(root(surface)).getByRole("button", {
        name: "Recuperar contexto de leitura",
      });
      act(() => {
        fireEvent.click(recover);
        fireEvent.click(recover);
      });
      await flush();
      expect(requests.filter((r) => r.fresh)).toHaveLength(7);
      expect(within(root(surface)).getAllByText(/fresh-page/)).toHaveLength(7);
    });

    it.each(["cancel", "route"])(
      "%s stops a batch and rejects its late response",
      async (action) => {
        await loadPages(surface, 8);
        await rotate(surface);
        holdAt = 3;
        fireEvent.click(
          within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
        );
        await flush();
        expect(releasePage).toBeTypeOf("function");
        const pending = requests.at(-1)!;
        if (action === "cancel")
          fireEvent.click(
            within(root(surface)).getByRole("button", { name: "Continuar sem restaurar posição" }),
          );
        else fireEvent.click(screen.getByRole("button", { name: "Outra rota" }));
        expect(pending.signal?.aborted).toBe(true);
        const count = requests.length;
        await act(async () => {
          releasePage!();
        });
        await flush();
        expect(requests).toHaveLength(count);
        expect(screen.queryByText("fresh-page3")).not.toBeInTheDocument();
        if (action === "route") expect(screen.getByText("Outra página")).toBeInTheDocument();
        else
          expect(
            within(root(surface)).queryByRole("button", { name: "Recuperar contexto de leitura" }),
          ).not.toBeInTheDocument();
      },
    );

    it.each(["user", "workspace", "session"])(
      "a new %s during a batch discards old cursors and data",
      async (change) => {
        await loadPages(surface, 8);
        await rotate(surface);
        holdAt = 3;
        fireEvent.click(
          within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
        );
        await flush();
        const late = releasePage!;
        const pending = requests.at(-1)!;
        holdAt = 0;
        if (change === "user") viewer = "u2";
        if (change === "workspace") workspace = "w2";
        act(() => setTokens("another-session"));
        await flush();
        await act(async () => {
          releaseIdentity!();
        });
        await flush();
        expect(pending.signal?.aborted).toBe(true);
        await act(async () => {
          late();
        });
        await flush();
        expect(within(root(surface)).queryByText("fresh-page3")).not.toBeInTheDocument();
        expect(
          within(root(surface)).queryByRole("button", { name: "Recuperar contexto de leitura" }),
        ).not.toBeInTheDocument();
        if (surface === "search") expect(screen.getByRole("searchbox")).toHaveValue("");
        else expect(requests.at(-1)?.cursor).toBeNull();
      },
    );

    it("a concurrent refresh cancels the batch and rebuilds only two with the new cursor chain", async () => {
      await loadPages(surface, 8);
      await rotate(surface);
      holdAt = 3;
      fireEvent.click(
        within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
      );
      await flush();
      const late = releasePage!;
      const pending = requests.at(-1)!;
      holdAt = 0;
      cursorPrefix = "next";
      const count = requests.length;
      act(() => setTokens("another-rotation", "refresh"));
      await flush();
      await act(async () => {
        releaseIdentity!();
      });
      await flush();
      expect(requests.slice(count).map((r) => r.cursor)).toEqual([null, "next-1"]);
      expect(pending.signal?.aborted).toBe(true);
      await act(async () => {
        late();
      });
      await flush();
      expect(requests).toHaveLength(count + 2);
      expect(within(root(surface)).queryByText("fresh-page3")).not.toBeInTheDocument();
    });

    it.each(["end", "revoked"])(
      "server %s ends reconstruction immediately without exposing old results",
      async (kind) => {
        await loadPages(surface, 8);
        endAt = 1;
        revoked = kind === "revoked";
        within(root(surface))
          .getByRole("button", { name: /carregar mais/i })
          .focus();
        await rotate(surface);
        expect(requests.filter((r) => r.fresh)).toHaveLength(1);
        expect(
          within(root(surface)).queryByRole("button", {
            name: /carregar mais|recuperar contexto/i,
          }),
        ).not.toBeInTheDocument();
        expect(within(root(surface)).getByText(/A lista terminou antes/)).toBeInTheDocument();
        expect(within(root(surface)).queryByText(/old-page/)).not.toBeInTheDocument();
        if (revoked)
          expect(within(root(surface)).queryByText(/fresh-page/)).not.toBeInTheDocument();
        expect(root(surface).scrollTop).toBe(0);
        expect(root(surface)).toHaveFocus();
        const count = requests.length;
        endAt = Infinity;
        revoked = false;
        act(() => setTokens("another-refresh", "refresh"));
        await flush();
        await act(async () => {
          releaseIdentity!();
        });
        await flush();
        expect(requests).toHaveLength(count + 1);
        expect(
          within(root(surface)).queryByRole("button", { name: "Recuperar contexto de leitura" }),
        ).not.toBeInTheDocument();
      },
    );

    it("an error stops the batch; an explicit retry continues from the last committed cursor", async () => {
      await loadPages(surface, 8);
      await rotate(surface);
      failAt = 4;
      fireEvent.click(
        within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
      );
      await flush();
      expect(requests.filter((r) => r.fresh).map((r) => r.cursor)).toEqual([
        null,
        "new-1",
        "new-2",
        "new-3",
      ]);
      expect(within(root(surface)).queryByText("fresh-page3")).not.toBeInTheDocument();
      const count = requests.length;
      await flush();
      expect(requests).toHaveLength(count);
      failAt = 0;
      fireEvent.click(
        within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
      );
      await flush();
      expect(requests.slice(count).map((r) => r.cursor)).toEqual([
        "new-2",
        "new-3",
        "new-4",
        "new-5",
        "new-6",
      ]);
      expect(within(root(surface)).getAllByText(/fresh-page/)).toHaveLength(7);
    });

    it.each([3, 7])(
      "401 on page %s during explicit recovery does not automatically resume that batch",
      async (page) => {
        await loadPages(surface, 8);
        await rotate(surface);
        failAt = page;
        failStatus = 401;
        fireEvent.click(
          within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
        );
        await flush();
        await act(async () => {
          releaseIdentity!();
        });
        await flush();
        expect(refreshes).toBe(1);
        // Includes the HTTP retry and the two-page rebuild after identity confirmation.
        expect(requests.filter((r) => r.fresh)).toHaveLength(page === 3 ? 6 : 10);
        const count = requests.length;
        await flush();
        expect(requests).toHaveLength(count);
        expect(within(root(surface)).queryByText("fresh-page3")).not.toBeInTheDocument();
        expect(
          within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
        ).toBeInTheDocument();
      },
    );

    it("recovers only five additional pages per explicit action and stops between batches", async () => {
      await loadPages(surface, 8);
      await rotate(surface);
      expect(requests.filter((request) => request.fresh)).toHaveLength(2);
      fireEvent.click(
        within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
      );
      await flush();
      expect(requests.filter((request) => request.fresh).map((request) => request.cursor)).toEqual([
        null,
        "new-1",
        "new-2",
        "new-3",
        "new-4",
        "new-5",
        "new-6",
      ]);
      const count = requests.length;
      await flush();
      expect(requests).toHaveLength(count);
      expect(within(root(surface)).queryByText("fresh-page8")).not.toBeInTheDocument();
      fireEvent.click(
        within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
      );
      await flush();
      expect(requests.filter((request) => request.fresh)).toHaveLength(8);
      expect(within(root(surface)).getByText("fresh-page8")).toBeInTheDocument();
      expect(
        within(root(surface)).queryByRole("button", { name: "Recuperar contexto de leitura" }),
      ).not.toBeInTheDocument();
    });

    it("normal load-more uses the new cursor and abandons recovery without duplicates", async () => {
      await loadPages(surface, 8);
      await rotate(surface);
      expect(requests.filter((request) => request.fresh)).toHaveLength(2);
      fireEvent.click(within(root(surface)).getByRole("button", { name: /carregar mais/i }));
      await flush();
      expect(requests.filter((request) => request.fresh).map((request) => request.cursor)).toEqual([
        null,
        "new-1",
        "new-2",
      ]);
      expect(within(root(surface)).getAllByText(/fresh-page/)).toHaveLength(3);
      expect(
        within(root(surface)).queryByRole("button", { name: "Recuperar contexto de leitura" }),
      ).not.toBeInTheDocument();
    });

    it.each([1, 2, 50])(
      "limits automatic work after %s loaded pages",
      async (pages) => {
        await loadPages(surface, pages);
        await rotate(surface);
        expect(requests.filter((request) => request.fresh)).toHaveLength(Math.min(pages, 2));
        expect(
          requests.filter((request) => request.fresh).map((request) => request.cursor),
        ).toEqual(pages === 1 ? [null] : [null, "new-1"]);
        if (pages === 50) {
          for (let recovered = 2; recovered < 50; recovered += 5) {
            fireEvent.click(
              within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
            );
            await flush();
            expect(requests.filter((r) => r.fresh)).toHaveLength(Math.min(50, recovered + 5));
          }
          expect(within(root(surface)).getAllByText(/fresh-page/)).toHaveLength(50);
          expect(requests.filter((r) => r.fresh).at(-1)?.cursor).toBe("new-49");
          expect(
            within(root(surface)).queryByRole("button", { name: "Recuperar contexto de leitura" }),
          ).not.toBeInTheDocument();
        }
      },
      60_000,
    );
  },
);
