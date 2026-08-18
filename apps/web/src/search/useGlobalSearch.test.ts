import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { mockSearchMessages, mockSearchUsers, mockSearchChannels } = vi.hoisted(() => ({
  mockSearchMessages: vi.fn(),
  mockSearchUsers: vi.fn(),
  mockSearchChannels: vi.fn(),
}));

vi.mock("./searchApi", async () => {
  const actual = await vi.importActual<typeof import("./searchApi")>("./searchApi");
  return {
    ...actual,
    searchMessages: (...args: unknown[]) => mockSearchMessages(...args),
    searchUsers: (...args: unknown[]) => mockSearchUsers(...args),
    searchChannels: (...args: unknown[]) => mockSearchChannels(...args),
  };
});

import { useGlobalSearch } from "./useGlobalSearch";

function page<T>(items: T[], nextCursor: string | null = null, hasMore = false) {
  return { items, nextCursor, hasMore };
}

beforeEach(() => {
  vi.useFakeTimers();
  mockSearchMessages.mockReset().mockResolvedValue(page([]));
  mockSearchUsers.mockReset().mockResolvedValue(page([]));
  mockSearchChannels.mockReset().mockResolvedValue(page([]));
});

afterEach(() => {
  vi.useRealTimers();
  vi.clearAllMocks();
});

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function commit(
  result: ReturnType<typeof renderHook<ReturnType<typeof useGlobalSearch>, unknown>>["result"],
  query: string,
) {
  act(() => result.current.setQuery(query));
  await act(async () => {
    vi.advanceTimersByTime(300);
    await Promise.resolve();
  });
  await flush();
}

describe("useGlobalSearch", () => {
  it("does not fetch for an empty query", async () => {
    renderHook(() => useGlobalSearch());
    await act(async () => {
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });
    expect(mockSearchMessages).not.toHaveBeenCalled();
  });

  it("debounces query commits and fetches only the active tab", async () => {
    const { result } = renderHook(() => useGlobalSearch());

    act(() => result.current.setQuery("or"));
    act(() => vi.advanceTimersByTime(100));
    act(() => result.current.setQuery("orion"));
    await act(async () => {
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });
    await flush();

    expect(mockSearchMessages).toHaveBeenCalledTimes(1);
    expect(mockSearchMessages).toHaveBeenCalledWith(
      "orion",
      expect.objectContaining({ limit: 20 }),
    );
    expect(mockSearchUsers).not.toHaveBeenCalled();
    expect(mockSearchChannels).not.toHaveBeenCalled();
    expect(result.current.state.messages.status).toBe("ready");
  });

  it("switching to an already-ready tab issues no new fetch and keeps other tabs' results", async () => {
    mockSearchMessages.mockResolvedValue(page([{ id: "m1" }]));
    mockSearchUsers.mockResolvedValue(page([{ id: "u1" }]));
    const { result } = renderHook(() => useGlobalSearch());

    await commit(result, "orion");
    expect(result.current.state.messages.items).toEqual([{ id: "m1" }]);

    act(() => result.current.setActiveTab("users"));
    await flush();
    expect(mockSearchUsers).toHaveBeenCalledTimes(1);
    expect(result.current.state.users.items).toEqual([{ id: "u1" }]);

    act(() => result.current.setActiveTab("messages"));
    await flush();
    // Switching back must not refetch, and must not have cleared users' results.
    expect(mockSearchMessages).toHaveBeenCalledTimes(1);
    expect(result.current.state.messages.items).toEqual([{ id: "m1" }]);
    expect(result.current.state.users.items).toEqual([{ id: "u1" }]);
  });

  it("a new committed query resets all three tabs atomically", async () => {
    mockSearchMessages.mockResolvedValue(page([{ id: "m1" }]));
    mockSearchUsers.mockResolvedValue(page([{ id: "u1" }]));
    const { result } = renderHook(() => useGlobalSearch());

    await commit(result, "orion");
    act(() => result.current.setActiveTab("users"));
    await flush();
    expect(result.current.state.users.items).toEqual([{ id: "u1" }]);

    mockSearchUsers.mockResolvedValue(page([{ id: "u2" }]));
    await commit(result, "nova");

    expect(result.current.state.messages).toMatchObject({ status: "idle", items: [] });
    expect(result.current.state.channels).toMatchObject({ status: "idle", items: [] });
    // The active tab (users) refetches under the new query once its effect runs.
    await flush();
    expect(mockSearchUsers).toHaveBeenLastCalledWith("nova", expect.anything());
  });

  it("loadMore appends items without duplication and updates the cursor", async () => {
    mockSearchMessages.mockResolvedValueOnce(page([{ id: "m1" }], "cursor-1", true));
    const { result } = renderHook(() => useGlobalSearch());
    await commit(result, "orion");
    expect(result.current.state.messages.items).toEqual([{ id: "m1" }]);
    expect(result.current.state.messages.hasMore).toBe(true);

    mockSearchMessages.mockResolvedValueOnce(page([{ id: "m2" }], null, false));
    await act(async () => {
      result.current.loadMore("messages");
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mockSearchMessages).toHaveBeenLastCalledWith(
      "orion",
      expect.objectContaining({ cursor: "cursor-1" }),
    );
    expect(result.current.state.messages.items).toEqual([{ id: "m1" }, { id: "m2" }]);
    expect(result.current.state.messages.hasMore).toBe(false);
  });

  it("a failed loadMore preserves the already-loaded items", async () => {
    mockSearchMessages.mockResolvedValueOnce(page([{ id: "m1" }], "cursor-1", true));
    const { result } = renderHook(() => useGlobalSearch());
    await commit(result, "orion");

    mockSearchMessages.mockRejectedValueOnce(new Error("network"));
    await act(async () => {
      result.current.loadMore("messages");
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(result.current.state.messages.items).toEqual([{ id: "m1" }]);
    expect(result.current.state.messages.loadMoreError).toBe("unknown");
    expect(result.current.state.messages.loadingMore).toBe(false);
  });

  it("does not loadMore when hasMore is false", async () => {
    mockSearchMessages.mockResolvedValueOnce(page([{ id: "m1" }], null, false));
    const { result } = renderHook(() => useGlobalSearch());
    await commit(result, "orion");

    act(() => result.current.loadMore("messages"));
    expect(mockSearchMessages).toHaveBeenCalledTimes(1);
  });

  it("retryTab resets the tab to idle so the fetch effect runs again", async () => {
    mockSearchMessages.mockRejectedValueOnce(new Error("boom"));
    const { result } = renderHook(() => useGlobalSearch());
    await commit(result, "orion");
    expect(result.current.state.messages.status).toBe("error");

    mockSearchMessages.mockResolvedValueOnce(page([{ id: "m1" }]));
    await act(async () => {
      result.current.retryTab("messages");
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(result.current.state.messages.status).toBe("ready");
    expect(result.current.state.messages.items).toEqual([{ id: "m1" }]);
  });

  it("aborts the previous in-flight request when the query changes again", async () => {
    const { result } = renderHook(() => useGlobalSearch());
    let firstSignal: AbortSignal | undefined;
    mockSearchMessages.mockImplementationOnce((_q: string, opts: { signal?: AbortSignal }) => {
      firstSignal = opts.signal;
      return new Promise(() => {});
    });

    act(() => result.current.setQuery("or"));
    await act(async () => {
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });
    expect(firstSignal?.aborted).toBe(false);

    mockSearchMessages.mockResolvedValueOnce(page([]));
    act(() => result.current.setQuery("orion"));
    await act(async () => {
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });

    expect(firstSignal?.aborted).toBe(true);
  });
});
