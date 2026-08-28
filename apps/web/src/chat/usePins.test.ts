/**
 * usePins — RF-05 pin state tests.
 *
 * chatApi is mocked so the hook's fetch/toggle/reload flow is exercised without
 * a network. Covers: initial load, pinnedIds set, idle for empty target,
 * toggle → reload, error surfacing (defensive fallback), and
 * reload not clearing the current list.
 */

import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { usePins } from "./usePins";
import type { PinnedItem } from "./chatTypes";

const { mockFetchPins, mockPin, mockUnpin } = vi.hoisted(() => ({
  mockFetchPins: vi.fn(),
  mockPin: vi.fn(),
  mockUnpin: vi.fn(),
}));

vi.mock("./chatApi", () => ({
  fetchPins: (...a: unknown[]) => mockFetchPins(...a),
  pinMessage: (...a: unknown[]) => mockPin(...a),
  unpinMessage: (...a: unknown[]) => mockUnpin(...a),
}));

function pin(id: string): PinnedItem {
  return {
    message: {
      id,
      senderId: "u1",
      senderDisplayName: "Ana",
      senderEmail: "",
      kind: "user",
      bodyText: "hi",
      bodyFormat: "v3",
      isRemoved: false,
      status: "active",
      createdAt: "2025-01-01T00:00:00Z",
      updatedAt: "2025-01-01T00:00:00Z",
      isEdited: false,
      editCount: 0,
      reactions: [],
      isFavorited: false,
      isForwarded: false,
    },
    pinnedByUserId: "mod-1",
    pinnedAt: "2025-02-01T00:00:00Z",
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  mockFetchPins.mockResolvedValue([]);
  mockPin.mockResolvedValue(undefined);
  mockUnpin.mockResolvedValue(undefined);
});
afterEach(() => vi.clearAllMocks());

describe("usePins", () => {
  it("loads pins for a channel and exposes a pinnedIds set", async () => {
    mockFetchPins.mockResolvedValueOnce([pin("m1"), pin("m2")]);
    const { result } = renderHook(() => usePins({ kind: "channel", id: "ch-1" }));

    await waitFor(() => expect(result.current.pins).toHaveLength(2));
    expect(mockFetchPins).toHaveBeenCalledWith(
      { kind: "channel", id: "ch-1" },
      expect.any(AbortSignal),
    );
    expect(result.current.pinnedIds.has("m1")).toBe(true);
    expect(result.current.pinnedIds.has("m2")).toBe(true);
  });

  it("loads pins for a DM", async () => {
    mockFetchPins.mockResolvedValueOnce([pin("m1")]);
    const { result } = renderHook(() => usePins({ kind: "dm", id: "dm-1" }));
    await waitFor(() => expect(result.current.pins).toHaveLength(1));
    expect(mockFetchPins).toHaveBeenCalledWith({ kind: "dm", id: "dm-1" }, expect.any(AbortSignal));
  });

  it("stays idle for an empty target", async () => {
    const { result } = renderHook(() => usePins(null));
    await Promise.resolve();
    expect(mockFetchPins).not.toHaveBeenCalled();
    expect(result.current.pins).toEqual([]);
  });

  it("ignores AbortError fetch failures", async () => {
    const aborted = Object.assign(new Error("aborted"), { name: "AbortError" });
    mockFetchPins.mockRejectedValueOnce(aborted);
    renderHook(() => usePins({ kind: "channel", id: "ch-1" }));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalled());
  });

  it("hides pins when fetch fails", async () => {
    mockFetchPins.mockRejectedValueOnce(new Error("boom"));
    const { result } = renderHook(() => usePins({ kind: "channel", id: "ch-1" }));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalled());
    expect(result.current.pins).toEqual([]);
    expect(result.current.error).toBeNull();
  });

  it("reloads without clearing the current list first", async () => {
    mockFetchPins.mockResolvedValueOnce([pin("m1")]).mockResolvedValueOnce([pin("m1"), pin("m2")]);
    const { result } = renderHook(() => usePins({ kind: "channel", id: "ch-1" }));
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    act(() => result.current.reload());

    expect(result.current.pins).toHaveLength(1);
    await waitFor(() => expect(result.current.pins).toHaveLength(2));
  });

  it("does not toggle without a target", () => {
    const { result } = renderHook(() => usePins(null));
    act(() => result.current.togglePin("m1", true));
    expect(mockPin).not.toHaveBeenCalled();
    expect(mockUnpin).not.toHaveBeenCalled();
  });

  it("pins then reloads the authoritative list", async () => {
    mockFetchPins.mockResolvedValueOnce([]).mockResolvedValueOnce([pin("m1")]);
    const { result } = renderHook(() => usePins({ kind: "channel", id: "ch-1" }));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalledTimes(1));

    await act(async () => {
      result.current.togglePin("m1", true);
    });

    expect(mockPin).toHaveBeenCalledWith({ kind: "channel", id: "ch-1" }, "m1");
    await waitFor(() => expect(result.current.pinnedIds.has("m1")).toBe(true));
  });

  it("surfaces a defensive error when pinning is rejected", async () => {
    mockPin.mockRejectedValueOnce(new Error("forbidden"));
    const { result } = renderHook(() => usePins({ kind: "channel", id: "ch-1" }));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalled());

    await act(async () => {
      result.current.togglePin("m1", true);
    });

    await waitFor(() => expect(result.current.error).toMatch(/fixar/i));
  });

  it("clears toggle errors after the timeout", async () => {
    vi.useFakeTimers();
    try {
      mockPin.mockRejectedValueOnce(new Error("forbidden"));
      const { result } = renderHook(() => usePins({ kind: "channel", id: "ch-1" }));
      await act(async () => {});

      await act(async () => {
        result.current.togglePin("m1", true);
      });
      expect(result.current.error).toMatch(/fixar/i);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });

      expect(result.current.error).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("unpins via the unpin API", async () => {
    const { result } = renderHook(() => usePins({ kind: "channel", id: "ch-1" }));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalled());

    await act(async () => {
      result.current.togglePin("m1", false);
    });

    expect(mockUnpin).toHaveBeenCalledWith({ kind: "channel", id: "ch-1" }, "m1");
    expect(mockPin).not.toHaveBeenCalled();
  });
});
