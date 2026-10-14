/**
 * usePins — RF-05 pin state tests.
 *
 * chatApi is mocked so the hook's fetch/toggle/reload flow is exercised without
 * a network. Covers: the loading/ready/error collection (issue #896), the
 * pinnedIds set, idle for an empty target, toggle → reload, stale responses
 * across target switches and overlapping reloads, the per-message mutation
 * lock, and error surfacing without losing the list.
 */

import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { usePins, type PinMutationOutcome, type UsePinsResult } from "./usePins";
import type { PinTarget } from "./chatApi";
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

/** A promise the test settles by hand; it ignores abort, like a response already received. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/**
 * A GET the test answers by hand that, like a real fetch, rejects with an
 * AbortError as soon as its signal is aborted.
 */
function abortableRead() {
  let resolve!: (pins: PinnedItem[]) => void;
  let reject!: (reason: unknown) => void;
  let aborted = false;
  const respond = (_target: PinTarget, signal: AbortSignal) =>
    new Promise<PinnedItem[]>((res, rej) => {
      resolve = res;
      reject = rej;
      signal.addEventListener("abort", () => {
        aborted = true;
        rej(Object.assign(new Error("aborted"), { name: "AbortError" }));
      });
    });
  return {
    respond,
    resolve: (pins: PinnedItem[]) => resolve(pins),
    reject: (reason: unknown) => reject(reason),
    wasAborted: () => aborted,
  };
}

const channel: PinTarget = { kind: "channel", id: "ch-1" };

beforeEach(() => {
  vi.clearAllMocks();
  mockFetchPins.mockResolvedValue([]);
  mockPin.mockResolvedValue(undefined);
  mockUnpin.mockResolvedValue(undefined);
});
afterEach(() => vi.clearAllMocks());

describe("usePins — collection", () => {
  it("starts loading, then exposes the loaded pins and a pinnedIds set", async () => {
    mockFetchPins.mockResolvedValueOnce([pin("m1"), pin("m2")]);
    const { result } = renderHook(() => usePins(channel));

    expect(result.current.collection).toEqual({ status: "loading" });
    await waitFor(() => expect(result.current.pins).toHaveLength(2));
    expect(result.current.collection.status).toBe("ready");
    expect(mockFetchPins).toHaveBeenCalledWith(channel, expect.any(AbortSignal));
    expect(result.current.pinnedIds.has("m1")).toBe(true);
    expect(result.current.pinnedIds.has("m2")).toBe(true);
  });

  it("preserves the server's order", async () => {
    mockFetchPins.mockResolvedValueOnce([pin("m3"), pin("m1"), pin("m2")]);
    const { result } = renderHook(() => usePins(channel));

    await waitFor(() => expect(result.current.pins).toHaveLength(3));
    expect(result.current.pins.map((p) => p.message.id)).toEqual(["m3", "m1", "m2"]);
  });

  it("reports an empty collection as loaded, not as a failure", async () => {
    const { result } = renderHook(() => usePins(channel));

    await waitFor(() => expect(result.current.collection).toEqual({ status: "ready", pins: [] }));
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
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalled());
    await act(async () => {});
    expect(result.current.collection).toEqual({ status: "loading" });
  });

  it("reports a failed load as an error, distinct from empty, without a toggle error", async () => {
    mockFetchPins.mockRejectedValueOnce(new Error("boom"));
    const { result } = renderHook(() => usePins(channel));

    await waitFor(() => expect(result.current.collection).toEqual({ status: "error" }));
    expect(result.current.pins).toEqual([]);
    expect(result.current.error).toBeNull();
  });

  it("retries a failed load visibly through reload", async () => {
    mockFetchPins.mockRejectedValueOnce(new Error("boom"));
    const retry = deferred<PinnedItem[]>();
    mockFetchPins.mockReturnValueOnce(retry.promise);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.collection.status).toBe("error"));

    act(() => result.current.reload());
    expect(result.current.collection).toEqual({ status: "loading" });

    await act(async () => retry.resolve([pin("m1")]));
    expect(result.current.pins.map((p) => p.message.id)).toEqual(["m1"]);
  });

  it("reloads without clearing the current list first", async () => {
    mockFetchPins.mockResolvedValueOnce([pin("m1")]).mockResolvedValueOnce([pin("m1"), pin("m2")]);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    act(() => result.current.reload());

    expect(result.current.pins).toHaveLength(1);
    await waitFor(() => expect(result.current.pins).toHaveLength(2));
  });

  it("keeps a loaded list when a later reload fails", async () => {
    mockFetchPins.mockResolvedValueOnce([pin("m1")]).mockRejectedValueOnce(new Error("boom"));
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    await act(async () => result.current.reload());

    expect(mockFetchPins).toHaveBeenCalledTimes(2);
    expect(result.current.collection).toEqual({
      status: "ready",
      pins: [pin("m1")],
      refreshFailed: true,
    });
    expect(result.current.pins.map((p) => p.message.id)).toEqual(["m1"]);
  });

  it("clears the refresh failure once a later read succeeds", async () => {
    mockFetchPins
      .mockResolvedValueOnce([pin("m1")])
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce([pin("m2")]);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(1));
    await act(async () => result.current.reload());
    expect(result.current.collection).toMatchObject({ refreshFailed: true });

    await act(async () => result.current.reload());

    expect(result.current.collection).toEqual({ status: "ready", pins: [pin("m2")] });
  });

  it("converges on the latest reload when duplicate events overlap", async () => {
    const first = deferred<PinnedItem[]>();
    const second = deferred<PinnedItem[]>();
    mockFetchPins
      .mockResolvedValueOnce([pin("m1")])
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    act(() => result.current.reload());
    act(() => result.current.reload());
    await act(async () => second.resolve([pin("m2")]));
    // The superseded reload answers last, with an older snapshot.
    await act(async () => first.resolve([pin("m1"), pin("m9")]));

    expect(result.current.pins.map((p) => p.message.id)).toEqual(["m2"]);
  });
});

describe("usePins — target switch", () => {
  it("never shows the previous target's pins, even for the render before the refetch", async () => {
    mockFetchPins.mockResolvedValueOnce([pin("a1")]).mockReturnValueOnce(new Promise(() => {}));
    const { result, rerender } = renderHook(({ target }) => usePins(target), {
      initialProps: { target: channel },
    });
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    rerender({ target: { kind: "channel", id: "ch-2" } });

    expect(result.current.collection).toEqual({ status: "loading" });
    expect(result.current.pinnedIds.size).toBe(0);
  });

  it("drops a late answer for the target the reader left", async () => {
    const forA = deferred<PinnedItem[]>();
    mockFetchPins.mockReturnValueOnce(forA.promise).mockResolvedValueOnce([pin("b1")]);
    const { result, rerender } = renderHook(({ target }) => usePins(target), {
      initialProps: { target: channel },
    });

    rerender({ target: { kind: "channel", id: "ch-2" } });
    await waitFor(() => expect(result.current.pins.map((p) => p.message.id)).toEqual(["b1"]));
    await act(async () => forA.resolve([pin("a1")]));

    expect(result.current.pins.map((p) => p.message.id)).toEqual(["b1"]);
    const [, firstSignal] = mockFetchPins.mock.calls[0] as [PinTarget, AbortSignal];
    expect(firstSignal.aborted).toBe(true);
  });

  it("does not reload the target the reader left when its unpin succeeds after a switch", async () => {
    const unpin = deferred<void>();
    mockUnpin.mockReturnValueOnce(unpin.promise);
    mockFetchPins.mockResolvedValueOnce([pin("a1")]).mockResolvedValueOnce([pin("b1")]);
    const { result, rerender } = renderHook(({ target }) => usePins(target), {
      initialProps: { target: channel },
    });
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    act(() => {
      void result.current.togglePin("a1", false);
    });
    rerender({ target: { kind: "channel", id: "ch-2" } });
    await waitFor(() => expect(result.current.pins.map((p) => p.message.id)).toEqual(["b1"]));
    await act(async () => unpin.resolve());

    expect(mockFetchPins).toHaveBeenCalledTimes(2);
    expect(mockFetchPins).toHaveBeenLastCalledWith(
      { kind: "channel", id: "ch-2" },
      expect.any(AbortSignal),
    );
    expect(result.current.pins.map((p) => p.message.id)).toEqual(["b1"]);
    expect(result.current.pendingIds.size).toBe(0);
  });

  it("does not let a mutation that finishes after a switch reload or report into the new target", async () => {
    const unpin = deferred<void>();
    mockUnpin.mockReturnValueOnce(unpin.promise);
    mockFetchPins.mockResolvedValueOnce([pin("a1")]).mockResolvedValueOnce([pin("b1")]);
    const { result, rerender } = renderHook(({ target }) => usePins(target), {
      initialProps: { target: channel },
    });
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    act(() => {
      void result.current.togglePin("a1", false);
    });
    rerender({ target: { kind: "channel", id: "ch-2" } });
    await waitFor(() => expect(result.current.pins.map((p) => p.message.id)).toEqual(["b1"]));
    await act(async () => unpin.reject(new Error("forbidden")));

    expect(mockFetchPins).toHaveBeenCalledTimes(2);
    expect(result.current.error).toBeNull();
    expect(result.current.pins.map((p) => p.message.id)).toEqual(["b1"]);
    expect(result.current.pendingIds.size).toBe(0);
  });
});

describe("usePins — pin/unpin", () => {
  it("tells the caller how each write ended, per message", async () => {
    mockUnpin.mockRejectedValueOnce(new Error("forbidden"));
    mockFetchPins
      .mockResolvedValueOnce([pin("m1"), pin("m2")])
      .mockRejectedValueOnce(new Error("pins indisponível"));
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(2));

    let refused: PinMutationOutcome | undefined;
    await act(async () => {
      refused = await result.current.togglePin("m1", false);
    });
    expect(refused).toBe("rejected");

    // Written, but the read meant to confirm it failed: still a persisted write.
    let accepted: PinMutationOutcome | undefined;
    await act(async () => {
      accepted = await result.current.togglePin("m2", false);
    });
    expect(accepted).toBe("persisted");
    expect(result.current.collection).toMatchObject({ refreshFailed: true });
  });

  it("reports a press that sent nothing as skipped", async () => {
    const unpin = deferred<void>();
    mockUnpin.mockReturnValueOnce(unpin.promise);
    mockFetchPins.mockResolvedValue([pin("m1")]);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    let first!: Promise<PinMutationOutcome>;
    act(() => {
      first = result.current.togglePin("m1", false);
    });
    let repeat: PinMutationOutcome | undefined;
    await act(async () => {
      repeat = await result.current.togglePin("m1", false);
    });
    expect(repeat).toBe("skipped");

    await act(async () => unpin.resolve());
    await expect(first).resolves.toBe("persisted");
    expect(mockUnpin).toHaveBeenCalledTimes(1);
  });

  it("does not toggle without a target", () => {
    const { result } = renderHook(() => usePins(null));
    act(() => {
      void result.current.togglePin("m1", true);
    });
    expect(mockPin).not.toHaveBeenCalled();
    expect(mockUnpin).not.toHaveBeenCalled();
  });

  it("pins then reloads the authoritative list", async () => {
    mockFetchPins.mockResolvedValueOnce([]).mockResolvedValueOnce([pin("m1")]);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalledTimes(1));

    await act(async () => {
      void result.current.togglePin("m1", true);
    });

    expect(mockPin).toHaveBeenCalledWith(channel, "m1");
    await waitFor(() => expect(result.current.pinnedIds.has("m1")).toBe(true));
  });

  it("unpins via the unpin API", async () => {
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalled());

    await act(async () => {
      void result.current.togglePin("m1", false);
    });

    expect(mockUnpin).toHaveBeenCalledWith(channel, "m1");
    expect(mockPin).not.toHaveBeenCalled();
  });

  it("keeps a message pending, and its pin listed, until the authoritative reload lands", async () => {
    const unpin = deferred<void>();
    const reload = deferred<PinnedItem[]>();
    mockUnpin.mockReturnValueOnce(unpin.promise);
    mockFetchPins.mockResolvedValueOnce([pin("m1"), pin("m2")]).mockReturnValueOnce(reload.promise);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(2));

    act(() => {
      void result.current.togglePin("m1", false);
    });
    expect([...result.current.pendingIds]).toEqual(["m1"]);

    await act(async () => unpin.resolve());
    expect(result.current.pendingIds.has("m1")).toBe(true);
    expect(result.current.pinnedIds.has("m1")).toBe(true);

    await act(async () => reload.resolve([pin("m2")]));
    expect(result.current.pendingIds.size).toBe(0);
    expect(result.current.pins.map((p) => p.message.id)).toEqual(["m2"]);
  });

  it("holds the lock through a reconciliation superseded by a realtime reload", async () => {
    const first = abortableRead();
    const second = abortableRead();
    mockFetchPins
      .mockResolvedValueOnce([pin("m1"), pin("m2")])
      .mockImplementationOnce(first.respond)
      .mockImplementationOnce(second.respond);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(2));

    // DELETE succeeds and the mutation's own reconciliation (GET #1) starts.
    await act(async () => {
      void result.current.togglePin("m1", false);
    });
    expect(mockFetchPins).toHaveBeenCalledTimes(2);

    // pin.updated arrives: GET #2 replaces GET #1, which is aborted.
    await act(async () => result.current.reload());
    expect(first.wasAborted()).toBe(true);

    // GET #2 has not answered: the list has not converged, so m1 stays locked.
    act(() => {
      void result.current.togglePin("m1", false);
    });
    expect(result.current.pendingIds.has("m1")).toBe(true);
    expect(mockUnpin).toHaveBeenCalledTimes(1);

    await act(async () => second.resolve([pin("m2")]));

    expect(result.current.pendingIds.has("m1")).toBe(false);
    expect(result.current.pins.map((p) => p.message.id)).toEqual(["m2"]);
  });

  it("does not tie a mutation's lock to another conversation's read after a switch", async () => {
    const readA1 = abortableRead();
    const readB1 = abortableRead();
    mockFetchPins
      .mockResolvedValueOnce([pin("a1"), pin("a2")])
      .mockImplementationOnce(readA1.respond)
      .mockImplementationOnce(readB1.respond);
    const { result, rerender } = renderHook(({ target }) => usePins(target), {
      initialProps: { target: channel },
    });
    await waitFor(() => expect(result.current.pins).toHaveLength(2));

    // DELETE A/a1 succeeds; its reconciliation, GET A1, stays pending.
    await act(async () => {
      void result.current.togglePin("a1", false);
    });
    expect(result.current.pendingIds.has("a1")).toBe(true);

    // The reader switches to B: GET B1 starts and A1 is aborted.
    await act(async () => rerender({ target: { kind: "channel", id: "ch-2" } }));
    expect(readA1.wasAborted()).toBe(true);
    expect(mockFetchPins).toHaveBeenLastCalledWith(
      { kind: "channel", id: "ch-2" },
      expect.any(AbortSignal),
    );

    // B1 has not answered, and A's mutation is no longer waiting on it.
    expect(result.current.pendingIds.size).toBe(0);
    expect(result.current.collection).toEqual({ status: "loading" });
    expect(result.current.error).toBeNull();

    await act(async () => readB1.resolve([pin("b1")]));

    expect(result.current.collection).toEqual({ status: "ready", pins: [pin("b1")] });
    expect(mockUnpin).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["succeeds", "ready"],
    ["fails", "refreshFailed"],
  ])("follows a chain of same-target reads to the last one, which %s", async (_how, outcome) => {
    const reads = [abortableRead(), abortableRead(), abortableRead()];
    mockFetchPins
      .mockResolvedValueOnce([pin("m1"), pin("m2")])
      .mockImplementationOnce(reads[0].respond)
      .mockImplementationOnce(reads[1].respond)
      .mockImplementationOnce(reads[2].respond);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(2));

    await act(async () => {
      void result.current.togglePin("m1", false);
    }); // A1
    await act(async () => result.current.reload()); // A2 replaces A1
    await act(async () => result.current.reload()); // A3 replaces A2
    expect(reads[0].wasAborted()).toBe(true);
    expect(reads[1].wasAborted()).toBe(true);
    expect(result.current.pendingIds.has("m1")).toBe(true);

    if (outcome === "ready") {
      await act(async () => reads[2].resolve([pin("m2")]));
      expect(result.current.collection).toEqual({ status: "ready", pins: [pin("m2")] });
    } else {
      await act(async () => reads[2].reject(new Error("boom")));
      expect(result.current.collection).toMatchObject({ refreshFailed: true });
    }
    expect(result.current.pendingIds.size).toBe(0);
  });

  it("ends the wait when the target goes away while the reconciliation is pending", async () => {
    const readA1 = abortableRead();
    mockFetchPins.mockResolvedValueOnce([pin("a1")]).mockImplementationOnce(readA1.respond);
    const { result, rerender } = renderHook<UsePinsResult, { target: PinTarget | null }>(
      ({ target }) => usePins(target),
      { initialProps: { target: channel } },
    );
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    await act(async () => {
      void result.current.togglePin("a1", false);
    });
    expect(result.current.pendingIds.has("a1")).toBe(true);

    await act(async () => rerender({ target: null }));

    expect(readA1.wasAborted()).toBe(true);
    expect(result.current.pendingIds.size).toBe(0);
  });

  it("ends the wait, without touching anything, when the hook unmounts mid-reconciliation", async () => {
    const readA1 = abortableRead();
    mockFetchPins.mockResolvedValueOnce([pin("a1")]).mockImplementationOnce(readA1.respond);
    const { result, unmount } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    await act(async () => {
      void result.current.togglePin("a1", false);
    });
    unmount();
    // A chain that never settled, or settled into a cycle, would surface here
    // as an unhandled rejection; a read issued after unmount as a third call.
    await act(async () => {});

    expect(readA1.wasAborted()).toBe(true);
    expect(mockFetchPins).toHaveBeenCalledTimes(2);
  });

  it("releases the lock and reports a stale list when the reconciliation after an unpin fails", async () => {
    mockFetchPins
      .mockResolvedValueOnce([pin("m1")])
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce([]);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    await act(async () => {
      void result.current.togglePin("m1", false);
    });

    // Persisted, but not confirmed: the list is kept and marked, the mutation
    // is not reported as failed, and the lock does not outlive the attempt.
    expect(result.current.collection).toEqual({
      status: "ready",
      pins: [pin("m1")],
      refreshFailed: true,
    });
    expect(result.current.error).toBeNull();
    expect(result.current.pendingIds.size).toBe(0);

    await act(async () => result.current.reload());

    expect(result.current.collection).toEqual({ status: "ready", pins: [] });
  });

  it("refuses a second mutation for a message already in flight, but not for another", async () => {
    const unpin = deferred<void>();
    mockUnpin.mockReturnValueOnce(unpin.promise);
    mockFetchPins.mockResolvedValue([pin("m1"), pin("m2")]);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(2));

    act(() => {
      void result.current.togglePin("m1", false);
    });
    act(() => {
      void result.current.togglePin("m1", false);
    });
    act(() => {
      void result.current.togglePin("m1", true);
    });
    act(() => {
      void result.current.togglePin("m2", false);
    });

    expect(mockUnpin).toHaveBeenCalledTimes(2);
    expect(mockUnpin).toHaveBeenNthCalledWith(1, channel, "m1");
    expect(mockUnpin).toHaveBeenNthCalledWith(2, channel, "m2");
    expect(mockPin).not.toHaveBeenCalled();

    await act(async () => unpin.resolve());
    await waitFor(() => expect(result.current.pendingIds.size).toBe(0));

    // Released: the same message can be acted on again.
    act(() => {
      void result.current.togglePin("m1", false);
    });
    expect(mockUnpin).toHaveBeenCalledTimes(3);
  });

  it("keeps the pin, releases the lock and reports a defensive error when unpinning fails", async () => {
    mockUnpin.mockRejectedValueOnce(new Error("internal: pq relation"));
    mockFetchPins.mockResolvedValueOnce([pin("m1")]);
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(result.current.pins).toHaveLength(1));

    await act(async () => {
      void result.current.togglePin("m1", false);
    });

    expect(result.current.error).toBe("Não foi possível desafixar a mensagem.");
    expect(result.current.pins.map((p) => p.message.id)).toEqual(["m1"]);
    expect(result.current.pendingIds.size).toBe(0);
    expect(mockFetchPins).toHaveBeenCalledTimes(1);
  });

  it("surfaces a defensive error when pinning is rejected", async () => {
    mockPin.mockRejectedValueOnce(new Error("forbidden"));
    const { result } = renderHook(() => usePins(channel));
    await waitFor(() => expect(mockFetchPins).toHaveBeenCalled());

    await act(async () => {
      void result.current.togglePin("m1", true);
    });

    await waitFor(() => expect(result.current.error).toMatch(/fixar/i));
  });

  it("clears toggle errors after the timeout", async () => {
    vi.useFakeTimers();
    try {
      mockPin.mockRejectedValueOnce(new Error("forbidden"));
      const { result } = renderHook(() => usePins(channel));
      await act(async () => {});

      await act(async () => {
        void result.current.togglePin("m1", true);
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
});
