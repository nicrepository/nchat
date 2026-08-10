import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ChannelCategoriesResult } from "./chatApi";
import type { ChannelCategoryGroup } from "./channelGrouping";
import { useChannelCategories } from "./useChannelCategories";

const { mockFetchChannelCategories } = vi.hoisted(() => ({
  mockFetchChannelCategories: vi.fn(),
}));

vi.mock("./chatApi", () => ({ fetchChannelCategories: mockFetchChannelCategories }));

const groups: ChannelCategoryGroup[] = [
  { kind: "uncategorized", name: "Geral", channelIds: [] },
  { kind: "category", id: "cat-1", name: "Times", channelIds: ["c1"] },
];

const result: ChannelCategoriesResult = { groups, canManage: false };

beforeEach(() => {
  mockFetchChannelCategories.mockReset();
});

describe("useChannelCategories", () => {
  it("starts loading and resolves to ready with the fetched groups and canManage", async () => {
    mockFetchChannelCategories.mockResolvedValue(result);
    const { result: hook } = renderHook(() => useChannelCategories());

    expect(hook.current.state.status).toBe("loading");
    await waitFor(() => expect(hook.current.state.status).toBe("ready"));
    expect(hook.current.state).toEqual({ status: "ready", groups, canManage: false });
  });

  it("surfaces canManage: true when the server grants it", async () => {
    mockFetchChannelCategories.mockResolvedValue({ groups, canManage: true });
    const { result: hook } = renderHook(() => useChannelCategories());

    await waitFor(() => expect(hook.current.state.status).toBe("ready"));
    expect(hook.current.state).toEqual({ status: "ready", groups, canManage: true });
  });

  it("moves to an error state when the fetch rejects, without throwing", async () => {
    mockFetchChannelCategories.mockRejectedValue(new Error("network down"));
    const { result: hook } = renderHook(() => useChannelCategories());

    await waitFor(() => expect(hook.current.state.status).toBe("error"));
  });

  it("coalesces a reload() called while one is already in flight into a single fetch", async () => {
    let resolveFetch!: (value: ChannelCategoriesResult) => void;
    mockFetchChannelCategories.mockReturnValue(
      new Promise((resolve) => {
        resolveFetch = resolve;
      }),
    );
    const { result: hook } = renderHook(() => useChannelCategories());

    // The mount effect already started a load; a second call before it
    // resolves must not start a second fetch.
    act(() => {
      void hook.current.reload();
    });
    expect(mockFetchChannelCategories).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolveFetch(result);
      await Promise.resolve();
    });

    await waitFor(() =>
      expect(hook.current.state).toEqual({ status: "ready", groups, canManage: false }),
    );
  });

  it("does not update state after unmount", async () => {
    let resolveFetch!: (value: ChannelCategoriesResult) => void;
    mockFetchChannelCategories.mockReturnValue(
      new Promise((resolve) => {
        resolveFetch = resolve;
      }),
    );
    const { result: hook, unmount } = renderHook(() => useChannelCategories());
    unmount();

    // Resolving after unmount must not throw or trigger a React warning.
    await act(async () => {
      resolveFetch(result);
      await Promise.resolve();
    });

    expect(hook.current.state).toEqual({ status: "loading" });
  });

  it("reload() refetches and can recover from an error", async () => {
    mockFetchChannelCategories.mockRejectedValueOnce(new Error("network down"));
    mockFetchChannelCategories.mockResolvedValueOnce(result);
    const { result: hook } = renderHook(() => useChannelCategories());

    await waitFor(() => expect(hook.current.state.status).toBe("error"));

    await act(async () => {
      await hook.current.reload();
    });

    expect(hook.current.state).toEqual({ status: "ready", groups, canManage: false });
    expect(mockFetchChannelCategories).toHaveBeenCalledTimes(2);
  });
});
