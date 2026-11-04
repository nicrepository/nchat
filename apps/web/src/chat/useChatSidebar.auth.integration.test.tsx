import { act, cleanup, renderHook } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { _resetState } from "../lib/authClient";
import { clearTokens, setTokens } from "../lib/authSession";
import { useChatSidebar } from "./useChatSidebar";

const realtime = vi.hoisted(() => ({ invalidate: () => {} }));
vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: ({ onConversationAvailable }: { onConversationAvailable: () => void }) => {
    realtime.invalidate = onConversationAvailable;
    return { toggleReaction: vi.fn() };
  },
}));

interface SidebarRequest {
  authorization: string | null;
  resolve: (response: Response) => void;
}

let requests: SidebarRequest[];
let refreshes: number;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const unauthorized = () => json({ error: { code: "token_expired", message: "Expired" } }, 401);
const sidebar = (user = "user-a") =>
  json({ data: { current_user_id: user, workspace: { id: "workspace-1" }, channels: [] } });

function Wrapper({ children }: PropsWithChildren) {
  return <MemoryRouter initialEntries={["/chat"]}>{children}</MemoryRouter>;
}

// Only fetch and realtime are replaced: the helper, chatApi, authClient,
// authSession and parsing run together. Held responses make every race explicit.
beforeEach(() => {
  _resetState();
  clearTokens();
  localStorage.clear();
  setTokens("token-a");
  requests = [];
  refreshes = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string, init: RequestInit) => {
      if (url === "/api/auth/refresh") {
        refreshes += 1;
        return Promise.resolve(
          json({ access_token: `rotated-${refreshes}`, token_type: "Bearer", expires_in: 900 }),
        );
      }
      if (url === "/api/chat/channel-categories")
        return Promise.resolve(json({ data: { groups: [] } }));
      if (url === "/api/chat/sidebar")
        return new Promise<Response>((resolve) =>
          requests.push({ authorization: new Headers(init.headers).get("authorization"), resolve }),
        );
      throw new Error(`Unexpected request: ${url}`);
    }),
  );
});

afterEach(async () => {
  cleanup();
  // A failing assertion may leave a request held: end its operation, then let
  // its response settle without starting work in the next test.
  await act(async () => {
    for (const request of requests) request.resolve(json({}, 503));
  });
  clearTokens();
  vi.unstubAllGlobals();
  _resetState();
});

async function answer(index: number, response: Response) {
  expect(requests[index]).toBeDefined();
  await act(async () => requests[index].resolve(response));
}

async function exhaust(start = 0) {
  for (let index = start; index < start + 4; index += 1) {
    expect(requests).toHaveLength(index + 1);
    await answer(index, unauthorized());
  }
}

describe("sidebar authentication revalidation budget", () => {
  it("ends persistent 401s, and gives an explicit retry its own finite budget", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
    await exhaust();

    expect(requests).toHaveLength(4);
    expect(refreshes).toBe(2);
    expect(result.current.state.status).toBe("error");

    let retry!: Promise<void>;
    await act(async () => {
      retry = result.current.retry();
    });
    await exhaust(4);
    expect(requests).toHaveLength(8);
    expect(result.current.state.status).toBe("error");
    await expect(retry).resolves.toBeUndefined();

    await act(async () => {
      retry = result.current.retry();
    });
    await answer(8, sidebar());
    await expect(retry).resolves.toBeUndefined();
    expect(result.current.state).toMatchObject({ status: "ready", currentUserId: "user-a" });
    expect(requests).toHaveLength(9);
  });

  it("loads the identity confirmed under the recovered token after a successful refresh", async () => {
    const seen: string[] = [];
    const { result } = renderHook(
      () => {
        const api = useChatSidebar();
        if (api.state.status === "ready") seen.push(api.state.currentUserId);
        return api;
      },
      { wrapper: Wrapper },
    );
    await answer(0, unauthorized());
    await answer(1, sidebar("old-response"));
    expect(seen).toEqual([]);
    await answer(2, sidebar());

    expect(requests.map(({ authorization }) => authorization)).toEqual([
      "Bearer token-a",
      "Bearer rotated-1",
      "Bearer rotated-1",
    ]);
    expect(refreshes).toBe(1);
    expect(seen).not.toContain("old-response");
    expect(result.current.state).toMatchObject({ status: "ready", currentUserId: "user-a" });
  });

  it("rejects an old identity even when the last allowed revalidation succeeds", async () => {
    const seen: string[] = [];
    const { result } = renderHook(
      () => {
        const api = useChatSidebar();
        if (api.state.status === "ready") seen.push(api.state.currentUserId);
        return api;
      },
      { wrapper: Wrapper },
    );
    act(() => setTokens("token-b", "refresh"));
    await answer(0, sidebar("user-a"));
    act(() => setTokens("token-c", "refresh"));
    await answer(1, sidebar("user-b"));

    expect(requests).toHaveLength(2);
    expect(result.current.state.status).toBe("error");
    expect(seen).toEqual([]);
    await act(async () => {
      void result.current.retry();
    });
    await answer(2, sidebar("user-c"));
    expect(result.current.state).toMatchObject({ status: "ready", currentUserId: "user-c" });
    expect(seen).toEqual(["user-c"]);
  });

  it("shares one failing operation between simultaneous retry callers and settles both", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
    const first = result.current.retry();
    const second = result.current.retry();
    expect(second).toBe(first);
    expect(requests).toHaveLength(1);

    await exhaust();
    expect(requests).toHaveLength(4);
    expect(result.current.state.status).toBe("error");
    await expect(Promise.all([first, second])).resolves.toEqual([undefined, undefined]);
  });

  it("coalesces concurrent background updates, settles failures and accepts a later update", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
    await answer(0, sidebar());
    act(() => {
      realtime.invalidate();
      realtime.invalidate();
    });
    expect(requests).toHaveLength(2);
    await answer(1, json({}, 503));
    expect(requests).toHaveLength(3);
    await answer(2, json({}, 503));
    expect(requests).toHaveLength(3);
    expect(result.current.state).toMatchObject({ status: "ready", currentUserId: "user-a" });
    expect(result.current.identityPending()).toBe(false);

    act(() => realtime.invalidate());
    await answer(3, sidebar());
    expect(requests).toHaveLength(4);
    expect(result.current.state.status).toBe("ready");
  });

  it("ends a failing identity confirmation and its safe reload instead of restarting forever", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
    await answer(0, sidebar());
    act(() => setTokens("rotated-before-check", "refresh"));
    expect(result.current.identityPending()).toBe(true);

    await exhaust(1);
    expect(result.current.state.status).toBe("loading");
    await exhaust(5);
    expect(requests).toHaveLength(9);
    expect(result.current.state.status).toBe("error");
    expect(result.current.identityPending()).toBe(false);
    expect(result.current.confirmingIdentity).toBe(false);
  });

  it.each(["logout", "unmount", "new session"] as const)(
    "stops an old revalidation after %s",
    async (ending) => {
      const { result, unmount } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
      act(() => setTokens("rotated-before-ending", "refresh"));
      act(() => {
        if (ending === "unmount") unmount();
        else if (ending === "logout") clearTokens();
        else setTokens("token-b");
      });
      await answer(0, sidebar("user-a"));

      if (ending === "new session") {
        expect(requests).toHaveLength(2);
        expect(result.current.state.status).toBe("loading");
        await answer(1, sidebar("user-b"));
        expect(result.current.state).toMatchObject({ status: "ready", currentUserId: "user-b" });
        expect(requests[1].authorization).toBe("Bearer token-b");
      } else {
        expect(requests).toHaveLength(1);
        expect(result.current.state.status).toBe("loading");
      }
    },
  );
});
