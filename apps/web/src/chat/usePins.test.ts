/**
 * usePins — RF-05 channel pin state tests.
 *
 * chatApi is mocked so the hook's fetch/toggle/reload flow is exercised without
 * a network. Covers: initial load, pinnedIds set, idle for empty channelId
 * (DMs), toggle → reload, error surfacing (e.g. 403 for non-moderators), and
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
  fetchChannelPins: (...a: unknown[]) => mockFetchPins(...a),
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
      reactions: [],
      isFavorited: false,
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
    const { result } = renderHook(() => usePins("ch-1"));

    await waitFor(() => expect(result.current.pins).toHaveLength(2));
    expect(mockFetchPins).toHaveBeenCalledWith("ch-1", expect.any(AbortSignal));
    expect(result.current.pinnedIds.has("m1")).toBe(true);
    expect(result.current.pinnedIds.has("m2")).toBe(true);
  });

  it("stays idle for an empty channelId (DMs)", async () => {
    const { result } = renderHook(() => usePins(""));
    await Promise.resolve();
    expect(mockFetchPins).not.toHaveBeenCalled();
    expect(result.current.pins).toEqual([]);
  });

  it("pins then reloads the authoritative list", async () => {
    mockFetchPins.mockResolvedValueOnce([]).mockResolvedValueOnce([pin("m1")]);
    const { result } = renderHook(() => usePins("ch-1"));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalledTimes(1));

    await act(async () => {
      result.current.togglePin("m1", true);
    });

    expect(mockPin).toHaveBeenCalledWith("ch-1", "m1");
    await waitFor(() => expect(result.current.pinnedIds.has("m1")).toBe(true));
  });

  it("surfaces a permission error when pinning is rejected", async () => {
    mockPin.mockRejectedValueOnce(new Error("forbidden"));
    const { result } = renderHook(() => usePins("ch-1"));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalled());

    await act(async () => {
      result.current.togglePin("m1", true);
    });

    await waitFor(() => expect(result.current.error).toMatch(/permissão de moderador/i));
  });

  it("unpins via the unpin API", async () => {
    const { result } = renderHook(() => usePins("ch-1"));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalled());

    await act(async () => {
      result.current.togglePin("m1", false);
    });

    expect(mockUnpin).toHaveBeenCalledWith("ch-1", "m1");
    expect(mockPin).not.toHaveBeenCalled();
  });
});
