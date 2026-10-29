import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import RequireAuth from "../auth/RequireAuth";
import { _resetState } from "../lib/authClient";
import { clearTokens, setTokens } from "../lib/authSession";
import GlobalSearchPage from "../search/GlobalSearchPage";
import FavoritesPage from "./FavoritesPage";
import { useChatSidebar } from "./useChatSidebar";

vi.mock("./useChatWebSocket", () => ({ useChatWebSocket: () => ({ toggleReaction: vi.fn() }) }));

type Surface = "search" | "favorites";
interface Request {
  path: string;
  page: number;
  token: string | null;
  signal?: AbortSignal | null;
}
let requests: Request[];
let confirmations: Array<() => void>;
let rotations: number;
let denyFrom: number;
let viewer: string;
let holdSidebar: boolean;
let deniedPath: string | null;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function Shell() {
  const { state, confirmingIdentity, retry } = useChatSidebar();
  return (
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
  );
}

beforeEach(() => {
  vi.useFakeTimers();
  _resetState();
  clearTokens();
  localStorage.clear();
  setTokens("initial");
  requests = [];
  confirmations = [];
  rotations = 0;
  denyFrom = Infinity;
  viewer = "u1";
  holdSidebar = false;
  deniedPath = null;
  // Every post-refresh sidebar answer is held. The test can advance only a
  // fixed number of rounds, even if production would otherwise refetch forever.
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit) => {
      const url = new URL(input, "http://localhost");
      const path = url.pathname;
      const body = init.body
        ? (JSON.parse(String(init.body)) as { cursor?: string; limit?: number })
        : {};
      const cursor = url.searchParams.get("before") ?? body.cursor;
      const page = cursor ? Number(cursor.split("-").at(-1)) + 1 : 1;
      requests.push({
        path,
        page,
        token: new Headers(init.headers).get("authorization"),
        signal: init.signal,
      });
      if (requests.length > 100) throw new Error("Test transport call ceiling exceeded");
      if (path === "/api/auth/refresh") {
        rotations += 1;
        holdSidebar = true;
        return json({
          access_token: `rotated-${rotations}`,
          token_type: "Bearer",
          expires_in: 900,
        });
      }
      if (path === "/api/chat/channel-categories") return json({ data: { groups: [] } });
      if (path === "/api/chat/sidebar") {
        if (holdSidebar)
          await new Promise<void>((resolve) => {
            confirmations.push(resolve);
          });
        return json({ data: { current_user_id: viewer, workspace: { id: "w1" }, channels: [] } });
      }
      if (path === "/api/chat/favorites" || path.startsWith("/api/search/")) {
        if (page >= denyFrom && (deniedPath === null || path === deniedPath)) {
          return json({ error: { code: "token_expired", message: "Persistent 401" } }, 401);
        }
        const title = `${viewer}-${path.endsWith("/favorites") ? "favorites" : "search"}-page${page}`;
        const next = page < 4 ? `cursor-${rotations}-${page}` : null;
        if (path === "/api/chat/favorites")
          return json({
            data: {
              favorites: [
                {
                  message: {
                    id: title,
                    sender_id: viewer,
                    body_text: title,
                    created_at: "2026-01-01T00:00:00Z",
                  },
                  favorited_at: "2026-01-01T00:00:00Z",
                },
              ],
              next_cursor: next ?? "",
            },
          });
        const data = path.endsWith("/groups") ? [{ id: title, title, participant_count: 2 }] : [];
        return json({ data: { data, pagination: { next_cursor: next, has_more: next !== null } } });
      }
      throw new Error(`Unexpected local request: ${path}`);
    }),
  );
});

async function flush() {
  for (let tick = 0; tick < 6; tick += 1) {
    await act(async () => {
      await Promise.resolve();
    });
  }
}

afterEach(async () => {
  cleanup();
  holdSidebar = false;
  await act(async () => {
    for (const resolve of confirmations.splice(0)) resolve();
  });
  clearTokens();
  vi.unstubAllGlobals();
  _resetState();
  vi.useRealTimers();
});

function root(surface: Surface) {
  return screen.getByTestId(surface === "search" ? "global-search" : "chat-favorites");
}

async function mount(surface: Surface | "both") {
  render(
    <MemoryRouter initialEntries={["/test"]}>
      <Routes>
        <Route
          element={
            <RequireAuth>
              <Shell />
            </RequireAuth>
          }
        >
          <Route
            path="/test"
            element={
              <>
                {surface !== "favorites" && <GlobalSearchPage />}
                {surface !== "search" && <FavoritesPage />}
              </>
            }
          />
        </Route>
        <Route path="/login" element={<div>Login</div>} />
      </Routes>
    </MemoryRouter>,
  );
  await flush();
  if (surface !== "favorites") {
    fireEvent.click(screen.getByRole("tab", { name: "Grupos" }));
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "texto" } });
    await act(async () => {
      vi.advanceTimersByTime(300);
    });
    await flush();
  }
}

async function more(surface: Surface) {
  fireEvent.click(within(root(surface)).getByRole("button", { name: /carregar mais/i }));
  await flush();
}

async function confirmOne() {
  expect(confirmations.length).toBeGreaterThan(0);
  await act(async () => {
    confirmations.shift()!();
  });
  await flush();
}

async function stopWithinTwoConfirmations() {
  // Do not release a third round: a regression fails without running a real loop.
  for (let round = 0; round < 2 && confirmations.length; round += 1) await confirmOne();
  expect(confirmations).toHaveLength(0);
}

function assertStopped(surface: Surface) {
  expect(
    within(root(surface)).getByRole("button", { name: /tentar novamente/i }),
  ).toBeInTheDocument();
  expect(within(root(surface)).queryByText(/u1-.*-page/)).not.toBeInTheDocument();
}

describe.each(["search", "favorites"] as const)(
  "%s: bounded successive 401 operations (R2)",
  (surface) => {
    it("persistent 401 terminates despite successful refresh and same-owner sidebar", async () => {
      denyFrom = 1;
      await mount(surface);
      await stopWithinTwoConfirmations();
      assertStopped(surface);
      expect(rotations).toBe(2);
      const endpoint = surface === "search" ? "/api/search/groups" : "/api/chat/favorites";
      expect(requests.filter((r) => r.path === endpoint)).toHaveLength(4);
      const count = requests.length;
      await flush();
      expect(requests).toHaveLength(count);
    });

    it.each([2, 3])(
      "a persistent 401 on recovery page %s terminates without exposing unvalidated data",
      async (page) => {
        await mount(surface);
        await more(surface);
        await more(surface);
        expect(within(root(surface)).getByText(`u1-${surface}-page3`)).toBeInTheDocument();
        denyFrom = page;
        holdSidebar = true;
        act(() => setTokens("external-rotation", "refresh"));
        await flush();
        if (page === 3) {
          // R1 stops the automatic rebuild at two. Only explicit recovery asks page 3.
          await confirmOne();
          expect(rotations).toBe(0);
          fireEvent.click(
            within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
          );
          await flush();
        }
        await stopWithinTwoConfirmations();
        if (page === 2) assertStopped(surface);
        else {
          expect(within(root(surface)).queryByText(`u1-${surface}-page3`)).not.toBeInTheDocument();
          expect(
            within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
          ).toBeInTheDocument();
          const stopped = requests.length;
          await flush();
          expect(requests).toHaveLength(stopped);
        }
        expect(rotations).toBe(1);
        denyFrom = Infinity;
        if (page === 2) {
          fireEvent.click(within(root(surface)).getByRole("button", { name: /tentar novamente/i }));
          await flush();
          expect(within(root(surface)).queryByText(`u1-${surface}-page3`)).not.toBeInTheDocument();
        }
        fireEvent.click(
          within(root(surface)).getByRole("button", { name: "Recuperar contexto de leitura" }),
        );
        await flush();
        expect(within(root(surface)).getByText(`u1-${surface}-page3`)).toBeInTheDocument();
        expect(within(root(surface)).getAllByText(new RegExp(`u1-${surface}-page`))).toHaveLength(
          3,
        );
      },
    );

    it("an explicit retry gets its own finite budget and can later recover", async () => {
      denyFrom = 1;
      await mount(surface);
      await stopWithinTwoConfirmations();
      assertStopped(surface);
      const before = rotations;
      fireEvent.click(within(root(surface)).getByRole("button", { name: /tentar novamente/i }));
      await flush();
      await stopWithinTwoConfirmations();
      assertStopped(surface);
      expect(rotations - before).toBeLessThanOrEqual(2);
      denyFrom = Infinity;
      fireEvent.click(within(root(surface)).getByRole("button", { name: /tentar novamente/i }));
      await flush();
      expect(within(root(surface)).getByText(`u1-${surface}-page1`)).toBeInTheDocument();
      const count = requests.length;
      await flush();
      expect(requests).toHaveLength(count);
    });

    it.each(["logout", "session"])("%s invalidates old operations", async (end) => {
      denyFrom = 1;
      await mount(surface);
      const old = requests.filter((r) =>
        r.path.includes(surface === "search" ? "/api/search/" : "/favorites"),
      );
      denyFrom = Infinity;
      if (end === "logout") act(() => clearTokens());
      else {
        viewer = "u2";
        act(() => setTokens("new-session"));
      }
      await flush();
      holdSidebar = false;
      await act(async () => {
        for (const resolve of confirmations.splice(0)) resolve();
      });
      await flush();
      expect(screen.queryByText(/u1-.*-page/)).not.toBeInTheDocument();
      expect(old.every((r) => r.signal?.aborted)).toBe(true);
      if (end === "logout") expect(screen.getByText("Login")).toBeInTheDocument();
      else if (surface === "search") expect(screen.getByRole("searchbox")).toHaveValue("");
      else expect(screen.getByText("u2-favorites-page1")).toBeInTheDocument();
    });

    it("successful revalidation allows later ordinary refreshes of the same session", async () => {
      await mount(surface);
      await more(surface);
      for (let rotation = 0; rotation < 3; rotation += 1) {
        holdSidebar = true;
        act(() => setTokens(`ordinary-${rotation}`, "refresh"));
        await flush();
        await confirmOne();
        expect(within(root(surface)).getByText(`u1-${surface}-page2`)).toBeInTheDocument();
        expect(
          within(root(surface)).queryByRole("button", { name: /tentar novamente/i }),
        ).not.toBeInTheDocument();
      }
      expect(rotations).toBe(0);
    });
  },
);

it("healthy overview categories cannot renew the budget of a category with persistent 401", async () => {
  await mount("search");
  denyFrom = 1;
  deniedPath = "/api/search/groups";
  fireEvent.click(screen.getByRole("tab", { name: "Tudo" }));
  await flush();
  await stopWithinTwoConfirmations();
  expect(rotations).toBe(2);
  expect(
    within(root("search")).getAllByRole("button", { name: /tentar novamente/i }).length,
  ).toBeGreaterThan(0);
  const count = requests.length;
  await flush();
  expect(requests).toHaveLength(count);
});

it("concurrent search and favorites 401s share refresh and both stop", async () => {
  await mount("both");
  denyFrom = 1;
  act(() => {
    fireEvent.click(within(root("favorites")).getByRole("button", { name: /carregar mais/i }));
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "nova" } });
    vi.advanceTimersByTime(300);
  });
  await flush();
  expect(rotations).toBe(1);
  await stopWithinTwoConfirmations();
  assertStopped("search");
  assertStopped("favorites");
  expect(rotations).toBe(2);
});
