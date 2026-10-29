import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import RequireAuth from "../auth/RequireAuth";
import { _resetState } from "../lib/authClient";
import { clearTokens, setTokens } from "../lib/authSession";
import GlobalSearchPage from "../search/GlobalSearchPage";
import FavoritesPage from "./FavoritesPage";
import { useChatSidebar } from "./useChatSidebar";

vi.mock("./useChatWebSocket", () => ({ useChatWebSocket: () => ({ toggleReaction: vi.fn() }) }));

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

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

let user: string;
let workspace: string;
let expire: boolean;
let refreshFails: boolean;
let sidebarFails: boolean;
let holdSidebar: boolean;
let releaseSidebar: (() => void) | undefined;
let releaseLate: (() => void) | undefined;
let refreshed: boolean;
let revoked: boolean;
let requests: Array<{
  path: string;
  cursor: string | null;
  token: string | null;
  signal?: AbortSignal | null;
}>;

beforeEach(() => {
  _resetState();
  clearTokens();
  localStorage.clear();
  setTokens("original");
  user = "u1";
  workspace = "w1";
  expire = refreshFails = sidebarFails = holdSidebar = refreshed = revoked = false;
  releaseSidebar = releaseLate = undefined;
  requests = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit) => {
      const url = new URL(input, "http://localhost");
      const path = url.pathname;
      const body = init.body ? (JSON.parse(String(init.body)) as { cursor?: string }) : {};
      const cursor = url.searchParams.get("before") ?? body.cursor ?? null;
      requests.push({
        path,
        cursor,
        token: new Headers(init.headers).get("authorization"),
        signal: init.signal,
      });
      if (path === "/api/auth/refresh") {
        if (refreshFails) return json({}, 503);
        expire = false;
        refreshed = true;
        holdSidebar = true;
        return json({ access_token: "rotated", token_type: "Bearer", expires_in: 900 });
      }
      if (path === "/api/chat/channel-categories") return json({ data: { groups: [] } });
      if (path === "/api/chat/sidebar") {
        if (holdSidebar)
          await new Promise<void>((resolve) => {
            releaseSidebar = resolve;
          });
        return sidebarFails
          ? json({}, 503)
          : json({ data: { current_user_id: user, workspace: { id: workspace }, channels: [] } });
      }
      if (path === "/api/chat/favorites" || path === "/api/search/groups") {
        if (expire) return json({ error: { code: "token_expired", message: "Expired" } }, 401);
        if (refreshed && revoked)
          return path === "/api/chat/favorites"
            ? json({ data: { favorites: [], next_cursor: "" } })
            : json({ error: { code: "forbidden" } }, 403);
        // The retried old pagination request deliberately ignores abort, and answers last.
        if (cursor === "old-2")
          await new Promise<void>((resolve) => {
            releaseLate = resolve;
          });
        const n = cursor ? 2 : 1;
        const title = `${user}-${n === 1 ? "primeira" : "segunda"}`;
        const next = `${refreshed ? "new" : "old"}-${n}`;
        if (path === "/api/chat/favorites")
          return json({
            data: {
              favorites: [
                {
                  message: {
                    id: `${user}-${n}`,
                    sender_id: user,
                    body_text: title,
                    created_at: "2026-01-01T00:00:00Z",
                  },
                  favorited_at: "2026-01-01T00:00:00Z",
                },
              ],
              next_cursor: next,
            },
          });
        return json({
          data: {
            data: [{ id: `${user}-${n}`, title, participant_count: 2 }],
            pagination: { next_cursor: next, has_more: true },
          },
        });
      }
      throw new Error(`Unexpected local test request: ${path}`);
    }),
  );
});

afterEach(async () => {
  cleanup();
  await act(async () => {
    releaseSidebar?.();
    releaseLate?.();
  });
  clearTokens();
  vi.unstubAllGlobals();
  _resetState();
});

async function open(surface: "favorites" | "search") {
  const view = render(
    <MemoryRouter initialEntries={[`/chat/${surface}`]}>
      <Routes>
        <Route
          element={
            <RequireAuth>
              <Shell />
            </RequireAuth>
          }
        >
          <Route path="/chat/favorites" element={<FavoritesPage />} />
          <Route path="/chat/search" element={<GlobalSearchPage />} />
        </Route>
        <Route path="/login" element={<div>Login</div>} />
      </Routes>
    </MemoryRouter>,
  );
  if (surface === "search") {
    await screen.findByRole("searchbox");
    await userEvent.click(screen.getByRole("tab", { name: "Grupos" }));
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "texto" } });
  }
  await screen.findByText("u1-primeira");
  await userEvent.click(screen.getByRole("button", { name: /carregar mais/i }));
  await screen.findByText("u1-segunda");
  return view;
}

async function startRefresh() {
  expire = true;
  await userEvent.click(screen.getByRole("button", { name: /carregar mais/i }));
  await waitFor(() => expect(releaseSidebar).toBeTypeOf("function"));
  expect(screen.queryByText("u1-primeira")).not.toBeInTheDocument();
  expect(screen.queryByText("u1-segunda")).not.toBeInTheDocument();
}

async function confirm() {
  holdSidebar = false;
  await act(async () => {
    releaseSidebar?.();
  });
}

describe.each(["favorites", "search"] as const)("%s: real HTTP refresh chain", (surface) => {
  it("respects revoked results after confirming the same owner", async () => {
    await open(surface);
    await startRefresh();
    revoked = true;
    await confirm();
    if (surface === "favorites") await screen.findByTestId("chat-favorites-empty");
    else await screen.findByText("Você não tem permissão para ver esses resultados.");
    const count = requests.length;
    await act(async () => {
      releaseLate?.();
    });
    expect(screen.queryByText("u1-primeira")).not.toBeInTheDocument();
    expect(screen.queryByText("u1-segunda")).not.toBeInTheDocument();
    expect(requests).toHaveLength(count);
  });

  it("rebuilds two authorized pages after a real 401/refresh and ignores old pagination", async () => {
    await open(surface);
    await startRefresh();
    await confirm();
    await screen.findByText("u1-segunda");
    await act(async () => {
      releaseLate?.();
    });
    expect(screen.getAllByText("u1-segunda")).toHaveLength(1);
    const fresh = requests.filter(
      (r) =>
        r.token === "Bearer rotated" &&
        r.cursor !== "old-2" &&
        (r.path.endsWith("/favorites") || r.path.endsWith("/groups")),
    );
    expect(fresh.map((r) => r.cursor)).toEqual([null, "new-1"]);
    expect(requests.filter((r) => r.path === "/api/auth/refresh")).toHaveLength(1);
    expect(requests.find((r) => r.cursor === "old-2")?.signal?.aborted).toBe(true);
  });

  it.each(["user", "workspace"])(
    "does not preserve U1 context when the %s changes",
    async (change) => {
      await open(surface);
      await startRefresh();
      if (change === "user") user = "u2";
      else workspace = "w2";
      await confirm();
      if (surface === "search") {
        expect(await screen.findByRole("searchbox")).toHaveValue("");
        expect(screen.queryByText("u1-segunda")).not.toBeInTheDocument();
      } else {
        await screen.findByText(`${user}-primeira`);
        expect(screen.queryByText("u1-segunda")).not.toBeInTheDocument();
      }
      await act(async () => {
        releaseLate?.();
      });
      expect(screen.queryByText("u1-segunda")).not.toBeInTheDocument();
    },
  );

  it("a new login for the same owner discards pagination and query", async () => {
    await open(surface);
    act(() => setTokens("new-login"));
    if (surface === "search") expect(await screen.findByRole("searchbox")).toHaveValue("");
    else await screen.findByText("u1-primeira");
    expect(screen.queryByText("u1-segunda")).not.toBeInTheDocument();
  });

  it("a failed refresh ends at login without old private data", async () => {
    await open(surface);
    expire = refreshFails = true;
    await userEvent.click(screen.getByRole("button", { name: /carregar mais/i }));
    expect(await screen.findByText("Login")).toBeInTheDocument();
    expect(screen.queryByText("u1-primeira")).not.toBeInTheDocument();
  });

  it("an unavailable sidebar keeps data hidden and stops automatically retrying", async () => {
    await open(surface);
    await startRefresh();
    sidebarFails = true;
    await confirm();
    await screen.findByText(/Não foi possível confirmar sua sessão/);
    const count = requests.length;
    await act(async () => {
      releaseLate?.();
    });
    expect(requests).toHaveLength(count);
    expect(screen.queryByText("u1-primeira")).not.toBeInTheDocument();
  });

  it.each(["logout", "unmount"])(
    "%s cancels pending pagination without restarting it",
    async (end) => {
      const view = await open(surface);
      await startRefresh();
      if (end === "logout") act(() => clearTokens());
      else view.unmount();
      const count = requests.length;
      await confirm();
      await act(async () => {
        releaseLate?.();
      });
      expect(requests).toHaveLength(count);
      expect(screen.queryByText("u1-primeira")).not.toBeInTheDocument();
    },
  );
});
