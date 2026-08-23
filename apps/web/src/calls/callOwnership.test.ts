import { describe, expect, it, vi } from "vitest";

import {
  compareOwnershipClaims,
  compareParticipationTokens,
  createOwnershipCoordinator,
  isLeaseExpired,
  parseOwnerLease,
  parseOwnershipMessage,
  resolveLeaseConflict,
  resolvePredecessor,
  type MediaIntent,
  type OwnerLease,
} from "./callOwnership";

const callId = "00000000-0000-4000-8000-000000000546";

class SharedStorage implements Storage {
  readonly values = new Map<string, string>();
  get length() {
    return this.values.size;
  }
  clear() {
    this.values.clear();
  }
  getItem(key: string) {
    return this.values.get(key) ?? null;
  }
  key(index: number) {
    return [...this.values.keys()][index] ?? null;
  }
  removeItem(key: string) {
    this.values.delete(key);
  }
  setItem(key: string, value: string) {
    this.values.set(key, value);
  }
}

class TestChannel extends EventTarget {
  readonly postMessage = vi.fn<(value: unknown) => void>();
  readonly close = vi.fn();
}

describe("call ownership", () => {
  it("parses only versioned, bounded messages", () => {
    expect(
      parseOwnershipMessage({ v: 1, type: "ready", callId, tabId: "tab-a", epoch: 2 }),
    ).toEqual({
      v: 1,
      type: "ready",
      callId,
      tabId: "tab-a",
      epoch: 2,
    });
    expect(
      parseOwnershipMessage({ v: 2, type: "ready", callId, tabId: "tab-a", epoch: 2 }),
    ).toBeNull();
    expect(
      parseOwnershipMessage({ v: 1, type: "unknown", callId, tabId: "tab-a", epoch: 2 }),
    ).toBeNull();
    expect(
      parseOwnershipMessage({ v: 1, type: "ready", callId, tabId: "x".repeat(129), epoch: 2 }),
    ).toBeNull();
    expect(parseOwnershipMessage(null)).toBeNull();
    expect(parseOwnershipMessage([])).toBeNull();
    expect(
      parseOwnershipMessage({ v: 1, type: "ready", callId: "", tabId: "tab", epoch: -1 }),
    ).toBeNull();
    expect(
      parseOwnershipMessage({ v: 1, type: "ready", callId, tabId: "tab", epoch: 1, extra: true }),
    ).toBeNull();
    expect(
      parseOwnershipMessage({ v: 1, type: "handoff", callId, tabId: "tab", epoch: 1 }),
    ).toBeNull();
    expect(
      parseOwnershipMessage({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab",
        targetTabId: "target",
        epoch: 1,
      }),
    ).toMatchObject({ type: "handoff", targetTabId: "target" });
  });

  it("parses leases without accepting extra persisted data", () => {
    const lease = { v: 1, callId, tabId: "tab-a", epoch: 3, role: "main", expiresAt: 2000 };

    expect(parseOwnerLease(JSON.stringify(lease))).toEqual(lease);
    expect(parseOwnerLease(JSON.stringify({ ...lease, token: "secret" }))).toBeNull();
    expect(parseOwnerLease("not-json")).toBeNull();
    expect(parseOwnerLease(null)).toBeNull();
    expect(parseOwnerLease("[]")).toBeNull();
    expect(parseOwnerLease(JSON.stringify({ ...lease, role: "unknown" }))).toBeNull();
    expect(parseOwnerLease(JSON.stringify({ ...lease, expiresAt: "soon" }))).toBeNull();
  });

  it("expires leases and resolves conflicts deterministically", () => {
    const a: OwnerLease = { v: 1, callId, tabId: "tab-a", epoch: 2, role: "main", expiresAt: 2000 };
    const b: OwnerLease = {
      v: 1,
      callId,
      tabId: "tab-b",
      epoch: 3,
      role: "dedicated",
      expiresAt: 2000,
    };

    expect(isLeaseExpired({ expiresAt: 999 }, 1000)).toBe(true);
    expect(isLeaseExpired({ expiresAt: 1001 }, 1000)).toBe(false);
    expect(resolveLeaseConflict(a, b)).toBe(b);
    expect(resolveLeaseConflict(b, a)).toBe(b);
    expect(resolveLeaseConflict(a, { ...b, epoch: 2 })).toBe(a);
    expect(resolveLeaseConflict({ ...a, tabId: "tab-z" }, { ...b, epoch: 2 })).toEqual({
      ...b,
      epoch: 2,
    });
  });

  it("rejects every malformed message and lease boundary", () => {
    expect(
      parseOwnershipMessage({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-a",
        targetTabId: "",
        epoch: 1,
      }),
    ).toBeNull();
    expect(parseOwnershipMessage({ v: 1, type: "ready", callId, tabId: "", epoch: 1 })).toBeNull();
    expect(
      parseOwnershipMessage({ v: 1, type: "ready", callId, tabId: "tab-a", epoch: 1.5 }),
    ).toBeNull();

    const valid = { v: 1, callId, tabId: "tab-a", epoch: 1, role: "main", expiresAt: 2 };
    expect(parseOwnerLease(JSON.stringify({ ...valid, v: 2 }))).toBeNull();
    expect(parseOwnerLease(JSON.stringify({ ...valid, callId: "" }))).toBeNull();
    expect(parseOwnerLease(JSON.stringify({ ...valid, tabId: "" }))).toBeNull();
    expect(parseOwnerLease(JSON.stringify({ ...valid, epoch: -1 }))).toBeNull();
    expect(
      parseOwnerLease(JSON.stringify({ ...valid, expiresAt: Number.POSITIVE_INFINITY })),
    ).toBeNull();
    expect(parseOwnerLease("false")).toBeNull();
  });

  it("allows only one live owner and permits takeover after expiry", async () => {
    let now = 1000;
    const storage = new SharedStorage();
    const channelA = new TestChannel();
    const channelB = new TestChannel();
    const lostA = vi.fn();
    const options = {
      storage,
      now: () => now,
      settle: () => Promise.resolve(),
      setInterval: vi.fn(() => 1),
      clearInterval: vi.fn(),
      leaseMs: 100,
      heartbeatMs: 25,
    };
    const a = createOwnershipCoordinator({
      ...options,
      tabId: "tab-a",
      channel: channelA,
      onOwnershipLost: lostA,
    });
    const b = createOwnershipCoordinator({ ...options, tabId: "tab-b", channel: channelB });

    const first = await a.claim(callId, "main");
    expect(first?.tabId).toBe("tab-a");
    expect(a.getOwner(callId)).toEqual(first);
    expect(await b.claim(callId, "dedicated")).toBeNull();

    now = 1100;
    const takeover = await b.claim(callId, "dedicated");
    expect(takeover).toMatchObject({ tabId: "tab-b", epoch: 2, role: "dedicated" });

    channelA.dispatchEvent(
      new MessageEvent("message", {
        data: { v: 1, type: "heartbeat", callId, tabId: "tab-b", epoch: 2 },
      }),
    );
    channelA.dispatchEvent(
      new MessageEvent("message", {
        data: { v: 1, type: "heartbeat", callId, tabId: "tab-b", epoch: 2 },
      }),
    );
    expect(lostA).toHaveBeenCalledTimes(1);
  });

  it("ignores stale messages and cleans up idempotently", async () => {
    const storage = new SharedStorage();
    const channel = new TestChannel();
    const clearInterval = vi.fn();
    const listener = vi.fn();
    const coordinator = createOwnershipCoordinator({
      storage,
      channel,
      tabId: "tab-a",
      now: () => 1000,
      settle: () => Promise.resolve(),
      setInterval: vi.fn(() => 7),
      clearInterval,
    });
    coordinator.subscribe(listener);
    await coordinator.claim(callId, "main");

    channel.dispatchEvent(
      new MessageEvent("message", {
        data: { v: 1, type: "heartbeat", callId, tabId: "tab-b", epoch: 2 },
      }),
    );
    channel.dispatchEvent(
      new MessageEvent("message", {
        data: { v: 1, type: "heartbeat", callId, tabId: "tab-c", epoch: 1 },
      }),
    );
    expect(listener).toHaveBeenCalledTimes(1);

    expect(coordinator.isClosed()).toBe(false);
    coordinator.close();
    coordinator.close();
    expect(clearInterval).toHaveBeenCalledTimes(1);
    expect(channel.close).toHaveBeenCalledTimes(1);
    expect(storage.length).toBe(0);
    expect(coordinator.isClosed()).toBe(true);
  });

  it.each(["participating", "leaving", "left", "leave-cancelled"] as const)(
    "delivers '%s' to listeners unconditionally, even after a higher epoch was already seen for that call (issue #570)",
    async (type) => {
      const storage = new SharedStorage();
      const channel = new TestChannel();
      const coordinator = createOwnershipCoordinator({
        storage,
        channel,
        tabId: "tab-a",
        now: () => 1000,
        settle: () => Promise.resolve(),
        setInterval: vi.fn(() => 1),
        clearInterval: vi.fn(),
      });
      const listener = vi.fn();
      coordinator.subscribe(listener);

      // This tab has already seen a higher epoch for this call (e.g. a
      // takeover) — the epoch-staleness gate would normally drop anything
      // older than that for the same callId.
      channel.dispatchEvent(
        new MessageEvent("message", {
          data: { v: 1, type: "heartbeat", callId, tabId: "tab-b", epoch: 5 },
        }),
      );
      listener.mockClear();

      // A same-call participation message at a lower epoch must still reach
      // listeners: it states a fact about this participant's own join/leave,
      // never a competing ownership claim, and must never be silently
      // dropped by that gate. Ordering for these is judged on the
      // (generation, writerId) token plus sequence by the listener, never
      // on epoch.
      channel.dispatchEvent(
        new MessageEvent("message", {
          data: {
            v: 1,
            type,
            callId,
            tabId: "tab-c",
            epoch: 1,
            generation: 2,
            writerId: "tab-c",
            sequence: 3,
          },
        }),
      );
      expect(listener).toHaveBeenCalledExactlyOnceWith(
        expect.objectContaining({
          type,
          callId,
          epoch: 1,
          generation: 2,
          writerId: "tab-c",
          sequence: 3,
        }),
      );
    },
  );

  it("rejects participation messages missing 'generation'/'writerId'/'sequence' and accepts them once all three are present", () => {
    expect(
      parseOwnershipMessage({ v: 1, type: "leaving", callId, tabId: "tab-a", epoch: 1 }),
    ).toBeNull();
    expect(
      parseOwnershipMessage({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-a",
        epoch: 1,
        generation: "soon",
        writerId: "tab-a",
        sequence: 0,
      }),
    ).toBeNull();
    expect(
      parseOwnershipMessage({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-a",
        epoch: 1,
        generation: 2,
        writerId: "",
        sequence: 0,
      }),
    ).toBeNull();
    expect(
      parseOwnershipMessage({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-a",
        epoch: 1,
        generation: 2,
        writerId: "tab-a",
        sequence: "soon",
      }),
    ).toBeNull();
    expect(
      parseOwnershipMessage({
        v: 1,
        type: "leave-cancelled",
        callId,
        tabId: "tab-a",
        epoch: 1,
        generation: 2,
        writerId: "tab-a",
        sequence: 5,
      }),
    ).toEqual({
      v: 1,
      type: "leave-cancelled",
      callId,
      tabId: "tab-a",
      epoch: 1,
      generation: 2,
      writerId: "tab-a",
      sequence: 5,
    });
  });

  it("heartbeats, releases, posts, and unsubscribes without leaving ownership behind", async () => {
    const storage = new SharedStorage();
    const channel = new TestChannel();
    let heartbeat: () => void = () => undefined;
    const coordinator = createOwnershipCoordinator({
      storage,
      channel,
      tabId: "tab-a",
      now: () => 1000,
      settle: () => Promise.resolve(),
      setInterval: (callback) => {
        heartbeat = callback;
        return 9;
      },
      clearInterval: vi.fn(),
      leaseMs: 100,
    });
    const listener = vi.fn();
    const unsubscribe = coordinator.subscribe(listener);
    await coordinator.claim(callId, "main");
    heartbeat();
    expect(channel.postMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "heartbeat" }),
    );
    expect(coordinator.getOwner(callId)?.tabId).toBe("tab-a");
    expect(coordinator.getOwner("another-call")).toBeNull();

    coordinator.post({ v: 1, type: "ready", callId, tabId: "tab-a", epoch: 1 });
    expect(channel.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "ready" }));
    unsubscribe();
    channel.dispatchEvent(
      new MessageEvent("message", {
        data: { v: 1, type: "ready", callId, tabId: "tab-b", epoch: 2 },
      }),
    );
    expect(listener).not.toHaveBeenCalled();

    coordinator.release("another-call");
    expect(coordinator.getLease()).not.toBeNull();
    coordinator.release(callId);
    expect(coordinator.getLease()).toBeNull();
    expect(channel.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "released" }));
    coordinator.close();
    expect(coordinator.subscribe(listener)()).toBeUndefined();
  });

  it("loses a lease on heartbeat conflict and tolerates unavailable browser storage/channel", async () => {
    const storage = new SharedStorage();
    const channel = new TestChannel();
    let heartbeat: () => void = () => undefined;
    const lost = vi.fn();
    const coordinator = createOwnershipCoordinator({
      storage,
      channel,
      tabId: "tab-z",
      now: () => 1000,
      settle: () => Promise.resolve(),
      setInterval: (callback) => {
        heartbeat = callback;
        return 1;
      },
      clearInterval: vi.fn(),
      onOwnershipLost: lost,
    });
    await coordinator.claim(callId, "main");
    storage.setItem(
      "nchat.call.owner.v1",
      JSON.stringify({ ...coordinator.getLease(), tabId: "tab-a" }),
    );
    heartbeat();
    expect(lost).toHaveBeenCalledOnce();

    const brokenStorage = {
      ...storage,
      getItem: () => {
        throw new Error("storage");
      },
    } as unknown as Storage;
    const brokenChannel = new TestChannel();
    brokenChannel.postMessage.mockImplementation(() => {
      throw new Error("channel");
    });
    const broken = createOwnershipCoordinator({
      storage: brokenStorage,
      channel: brokenChannel,
      tabId: "tab-b",
      locks: null,
      settle: () => Promise.resolve(),
    });
    expect(await broken.claim(callId, "main")).toBeNull();
    expect(broken.getOwner(callId)).toBeNull();
    broken.post({ v: 1, type: "ready", callId, tabId: "tab-b", epoch: 1 });
    broken.close();
  });

  it("uses a native lock, rejects invalid claims, and reports settlement conflicts", async () => {
    const storage = new SharedStorage();
    const channel = new TestChannel();
    const request = vi.fn(async (_name: string, callback: () => Promise<unknown>) => callback());
    const coordinator = createOwnershipCoordinator({
      storage,
      channel,
      locks: { request } as never,
      tabId: "tab-a",
      now: () => 1000,
      settle: () => Promise.resolve(),
      setInterval: vi.fn(() => 1),
      clearInterval: vi.fn(),
    });

    expect(await coordinator.claim("", "main")).toBeNull();
    expect(await coordinator.claim(callId, "main")).toMatchObject({ tabId: "tab-a" });
    expect(request).toHaveBeenCalledWith(expect.stringContaining(callId), expect.any(Function));
    const remove = coordinator.onOwnershipLost(vi.fn());
    remove();
    coordinator.close();
    expect(await coordinator.claim(callId, "main")).toBeNull();

    let settlements = 0;
    const contestedStorage = new SharedStorage();
    const contested = createOwnershipCoordinator({
      storage: contestedStorage,
      channel: new TestChannel(),
      locks: null,
      tabId: "tab-z",
      now: () => 1000,
      settle: () => {
        settlements += 1;
        contestedStorage.setItem(
          "nchat.call.owner.v1",
          JSON.stringify({
            v: 1,
            callId,
            tabId: "tab-a",
            epoch: 1,
            role: "main",
            expiresAt: 2000,
          }),
        );
        return Promise.resolve();
      },
    });
    expect(await contested.claim(callId, "main")).toBeNull();
    expect(settlements).toBe(1);
    contested.close();
  });

  // Issue #570 follow-up: participation generation must be allocated from
  // persisted, cross-tab-shared, PER-CALLID storage — never from a tab's own
  // local, possibly-empty knowledge, and never from a single global slot
  // that a different call in between could silently overwrite.
  describe("participation generation allocation (issue #570 follow-up)", () => {
    it("a fresh tab allocates strictly past a generation another tab already recorded for the same callId, via shared storage alone", () => {
      const storage = new SharedStorage();
      const tabA = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      expect(tabA.allocateParticipationGeneration(callId)).toEqual({
        generation: 1,
        writerId: "tab-a",
      });
      tabA.close();

      // A brand-new coordinator (fresh tab / reload) with EMPTY local
      // knowledge — it never saw tab-a's local state, only shared storage.
      const tabB = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-b",
      });
      expect(tabB.allocateParticipationGeneration(callId)).toEqual({
        generation: 2,
        writerId: "tab-b",
      });
      tabB.close();
    });

    it("getParticipationToken reads the current token without allocating a new one", () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      expect(coordinator.getParticipationToken(callId)).toBeNull();
      coordinator.allocateParticipationGeneration(callId);
      expect(coordinator.getParticipationToken(callId)).toEqual({
        generation: 1,
        writerId: "tab-a",
      });
      expect(coordinator.getParticipationToken(callId)).toEqual({
        generation: 1,
        writerId: "tab-a",
      });
      expect(coordinator.allocateParticipationGeneration(callId)).toEqual({
        generation: 2,
        writerId: "tab-a",
      });
      coordinator.close();
    });

    // HIGH finding: PARTICIPATION_KEY used to be a single global slot, so a
    // different call used in between silently forgot the first call's own
    // generation — X1 -> Y1 -> X2 must prove X2 still lands strictly past X1.
    it("X1 -> Y1 -> X2: a different call allocated in between never resets X's own generation history", () => {
      const otherCallId = "00000000-0000-4000-8000-000000000998";
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });

      const x1 = coordinator.allocateParticipationGeneration(callId);
      expect(x1).toEqual({ generation: 1, writerId: "tab-a" });

      const y1 = coordinator.allocateParticipationGeneration(otherCallId);
      expect(y1).toEqual({ generation: 1, writerId: "tab-a" });

      // X is still active for other participants; this tab rejoins it —
      // X's OWN history (untouched by Y) is what decides X2's generation.
      const x2 = coordinator.allocateParticipationGeneration(callId);
      expect(x2).toEqual({ generation: 2, writerId: "tab-a" });
      expect(compareParticipationTokens(x2, x1)).toBeGreaterThan(0);

      // Y's own history is likewise untouched by X's activity.
      expect(coordinator.getParticipationToken(otherCallId)).toEqual(y1);
      coordinator.close();
    });

    // HIGH finding: a bounded/LRU history used to silently forget a
    // callId's own generation once enough OTHER calls were touched in
    // between — a tab still holding that generation in memory would then
    // reject a real, later rejoin of that exact callId as stale. There is
    // no eviction of any kind now: a callId's entry survives for as long as
    // the storage key exists, however many other distinct calls are
    // allocated in between.
    it("storage never forgets X's own generation, no matter how many other calls are allocated in between", () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });

      coordinator.allocateParticipationGeneration(callId);
      coordinator.allocateParticipationGeneration(callId);
      expect(coordinator.allocateParticipationGeneration(callId)).toEqual({
        generation: 3,
        writerId: "tab-a",
      });

      for (let index = 1; index <= 50; index += 1) {
        coordinator.allocateParticipationGeneration(
          `00000000-0000-4000-8000-${String(index).padStart(12, "0")}`,
        );
      }

      expect(coordinator.getParticipationToken(callId)).toEqual({
        generation: 3,
        writerId: "tab-a",
      });
      expect(coordinator.allocateParticipationGeneration(callId)).toEqual({
        generation: 4,
        writerId: "tab-a",
      });
      coordinator.close();
    });

    it("the allocator itself leaves its own token registered, readable via getParticipationToken", () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const token = coordinator.allocateParticipationGeneration(callId);
      expect(coordinator.getParticipationToken(callId)).toEqual(token);
      coordinator.close();
    });
  });

  // HIGH finding: a single shared key (nchat.call.participation.v1, storing
  // every writer's token in one blob) let a stale writer's own read-modify-
  // write regress storage back to an older token even after every provider
  // had already converged on a newer one — localStorage.setItem is a blind
  // overwrite of the WHOLE key, so no read-then-write pattern on a shared
  // key is safe without a real lock, no matter how many times it re-reads.
  // The fix removes the shared key entirely: nchat.call.participation.v2.
  // <callId>.<writerId> gives every writer its own EXCLUSIVE key that no
  // other writer ever touches, so a stale write from one writer can
  // structurally never destroy another writer's own write — "current" is
  // derived at read time as the max across every writer's key, not "the
  // most recently written key".
  describe("per-writer participation keys are race-free without Web Locks (issue #570 follow-up, HIGH)", () => {
    // Required test 1: stale writer cannot regress storage.
    it("a stale writer's late write never regresses storage back below a token every provider already converged on", () => {
      const storage = new SharedStorage();
      const seed = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "seed",
      });
      seed.allocateParticipationGeneration(callId);
      seed.allocateParticipationGeneration(callId);
      seed.allocateParticipationGeneration(callId); // base max = generation 3
      seed.close();

      // A and B each independently observe base=3 (neither has seen the
      // other's write yet — the worst case, no synchronization at all) and
      // each compute generation 4 under their own writerId.
      const a = createOwnershipCoordinator({
        storage: new SharedStorage(),
        channel: new TestChannel(),
        tabId: "writer-a",
      });
      const b = createOwnershipCoordinator({
        storage: new SharedStorage(),
        channel: new TestChannel(),
        tabId: "writer-b",
      });
      // Both start from the same seeded base of 3 — simulated by seeding
      // each independent storage identically before either allocates.
      for (const coordinator of [a, b]) {
        coordinator.allocateParticipationGeneration(callId);
        coordinator.allocateParticipationGeneration(callId);
        coordinator.allocateParticipationGeneration(callId);
      }
      const tokenA = a.allocateParticipationGeneration(callId);
      const tokenB = b.allocateParticipationGeneration(callId);
      expect(tokenA).toEqual({ generation: 4, writerId: "writer-a" });
      expect(tokenB).toEqual({ generation: 4, writerId: "writer-b" });

      // B's write becomes visible to providers first (this is what "B
      // persiste, providers consideram B vencedor" means at the storage
      // layer: B's own key already durably holds {4,B}). A's write to ITS
      // OWN key lands strictly after that, but never touches B's key.
      const shared = new SharedStorage();
      shared.setItem(
        `nchat.call.participation.v2.${callId}:writer-b`,
        JSON.stringify({ v: 2, generation: tokenB.generation }),
      );
      const reader = createOwnershipCoordinator({
        storage: shared,
        channel: new TestChannel(),
        tabId: "reader",
      });
      expect(reader.getParticipationToken(callId)).toEqual(tokenB);

      // A's stale write lands AFTER — on its own exclusive key.
      shared.setItem(
        `nchat.call.participation.v2.${callId}:writer-a`,
        JSON.stringify({ v: 2, generation: tokenA.generation }),
      );

      // The max is still whichever of A/B compareParticipationTokens picks
      // — B's key was never overwritten, so storage never regressed.
      const winner = compareParticipationTokens(tokenA, tokenB) > 0 ? tokenA : tokenB;
      expect(reader.getParticipationToken(callId)).toEqual(winner);

      a.close();
      b.close();
      reader.close();
    });

    // Required test 2 (storage layer half — the leaving/left convergence
    // half is covered end to end in CallSessionProvider.test.tsx).
    it("a fresh/reloaded coordinator with no local state reads exactly the causal winner among all persisted writer keys", () => {
      const storage = new SharedStorage();
      const a = createOwnershipCoordinator({ storage, channel: new TestChannel(), tabId: "a" });
      const b = createOwnershipCoordinator({
        storage: new SharedStorage(),
        channel: new TestChannel(),
        tabId: "b",
      });
      const tokenA = a.allocateParticipationGeneration(callId);
      const tokenB = b.allocateParticipationGeneration(callId);
      // Merge b's independently-computed key into the shared storage, same
      // as the previous test — representing two tabs that raced with zero
      // coordination and both landed in the one real localStorage.
      storage.setItem(
        `nchat.call.participation.v2.${callId}:b`,
        JSON.stringify({ v: 2, generation: tokenB.generation }),
      );
      const winner = compareParticipationTokens(tokenA, tokenB) > 0 ? tokenA : tokenB;

      const fresh = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "fresh-reload",
      });
      expect(fresh.getParticipationToken(callId)).toEqual(winner);

      a.close();
      b.close();
      fresh.close();
    });

    // Required test 4: writer keys are independent — a write from one
    // writer, or a later update to that SAME writer's own key, never
    // touches another writer's key.
    it("writing/updating one writer's key never removes or overwrites another writer's key", () => {
      const storage = new SharedStorage();
      const a = createOwnershipCoordinator({ storage, channel: new TestChannel(), tabId: "a" });
      const b = createOwnershipCoordinator({ storage, channel: new TestChannel(), tabId: "b" });

      const tokenA1 = a.allocateParticipationGeneration(callId);
      const tokenB = b.allocateParticipationGeneration(callId);
      expect(tokenB.generation).toBe(2); // b observed a's key and moved past it

      // a allocates again — updating ONLY its own key.
      const tokenA2 = a.allocateParticipationGeneration(callId);
      expect(tokenA2.generation).toBe(3);

      // b's own key is untouched by either of a's writes.
      expect(storage.getItem(`nchat.call.participation.v2.${callId}:b`)).toEqual(
        JSON.stringify({ v: 2, generation: tokenB.generation }),
      );
      // a's first write is gone (a's OWN key was legitimately updated by
      // a itself, generation 1 -> 3) — this is a's own key, not a foreign
      // overwrite.
      expect(storage.getItem(`nchat.call.participation.v2.${callId}:a`)).toEqual(
        JSON.stringify({ v: 2, generation: tokenA2.generation }),
      );
      expect(tokenA1.generation).not.toBe(tokenA2.generation);

      a.close();
      b.close();
    });

    // Required test 5: concurrency without Web Locks.
    it("two allocators observing the same base generation without Web Locks produce distinct, both-persisted tokens with a single deterministic winner", () => {
      const a = createOwnershipCoordinator({
        storage: new SharedStorage(),
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const b = createOwnershipCoordinator({
        storage: new SharedStorage(),
        channel: new TestChannel(),
        tabId: "tab-b",
      });
      const tokenA = a.allocateParticipationGeneration(callId);
      const tokenB = b.allocateParticipationGeneration(callId);
      expect(tokenA.generation).toBe(1);
      expect(tokenB.generation).toBe(1);
      expect(tokenA).not.toEqual(tokenB);

      const verdict = compareParticipationTokens(tokenA, tokenB);
      expect(verdict).not.toBe(0);
      expect(compareParticipationTokens(tokenB, tokenA)).toBe(-verdict);

      // Both tokens are independently persisted — merging both writer keys
      // into one real storage (what a single shared localStorage would
      // actually hold once both writes land, in either order) never loses
      // either one.
      const merged = new SharedStorage();
      merged.setItem(
        `nchat.call.participation.v2.${callId}:tab-a`,
        JSON.stringify({ v: 2, generation: tokenA.generation }),
      );
      merged.setItem(
        `nchat.call.participation.v2.${callId}:tab-b`,
        JSON.stringify({ v: 2, generation: tokenB.generation }),
      );
      const winner = verdict > 0 ? tokenA : tokenB;
      const reader1 = createOwnershipCoordinator({
        storage: merged,
        channel: new TestChannel(),
        tabId: "reader-1",
      });
      const reader2 = createOwnershipCoordinator({
        storage: merged,
        channel: new TestChannel(),
        tabId: "reader-2",
      });
      // Any reader, queried in any order, gets the identical winner.
      expect(reader2.getParticipationToken(callId)).toEqual(winner);
      expect(reader1.getParticipationToken(callId)).toEqual(winner);

      a.close();
      b.close();
      reader1.close();
      reader2.close();
    });

    // Required test 6: a later participation orders after BOTH raced
    // tokens, regardless of writerId.
    it("a later join after an A/B tie allocates strictly past both, beating whichever one would have won the tie", () => {
      const merged = new SharedStorage();
      merged.setItem(
        `nchat.call.participation.v2.${callId}:tab-a`,
        JSON.stringify({ v: 2, generation: 4 }),
      );
      merged.setItem(
        `nchat.call.participation.v2.${callId}:tab-b`,
        JSON.stringify({ v: 2, generation: 4 }),
      );
      const tokenA = { generation: 4, writerId: "tab-a" };
      const tokenB = { generation: 4, writerId: "tab-b" };

      const c = createOwnershipCoordinator({
        storage: merged,
        channel: new TestChannel(),
        tabId: "tab-c",
      });
      const tokenC = c.allocateParticipationGeneration(callId);
      expect(tokenC.generation).toBe(5);
      expect(compareParticipationTokens(tokenC, tokenA)).toBeGreaterThan(0);
      expect(compareParticipationTokens(tokenC, tokenB)).toBeGreaterThan(0);
      c.close();
    });
  });

  // MEDIUM finding: callOwnership.ts's own signature (`callId: string`)
  // never actually guarantees UUID shape/no-colons — that was only ever
  // true because every CURRENT caller happens to validate UUIDs upstream.
  // A callId containing the ":" delimiter used between the callId and
  // writerId components of a storage key could make one callId's writer
  // keys a literal string-prefix of a DIFFERENT callId's own keys, letting
  // readParticipationTokens cross-read them. The callId component is now
  // percent-encoded before that delimiter, so this can never happen by
  // construction, not by an implicit UUID assumption.
  describe("participation key callId isolation (issue #570 follow-up, MEDIUM)", () => {
    it("a callId that is the literal prefix of another callId (once a ':' delimiter is naively appended) never cross-reads its tokens", () => {
      const storage = new SharedStorage();
      const short = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const long = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-b",
      });

      const tokenShort = short.allocateParticipationGeneration("AB");
      const tokenLong = long.allocateParticipationGeneration("AB:CD");

      expect(short.getParticipationToken("AB")).toEqual(tokenShort);
      expect(long.getParticipationToken("AB:CD")).toEqual(tokenLong);
      // Neither callId's reader ever sees the other's token — a naive
      // `key.startsWith("...AB:")` match would have picked up "AB:CD"'s
      // key as if it belonged to "AB".
      expect(short.getParticipationToken("AB")).not.toEqual(tokenLong);
      expect(long.getParticipationToken("AB:CD")).not.toEqual(tokenShort);

      // A fresh rejoin of the SHORT callId only ever sees its own history,
      // never inflated by the unrelated LONG callId's generation.
      expect(short.allocateParticipationGeneration("AB")).toEqual({
        generation: tokenShort.generation + 1,
        writerId: "tab-a",
      });

      short.close();
      long.close();
    });

    it("a callId containing the encoding scheme's own escape character never collides with the literal string it would decode to", () => {
      const storage = new SharedStorage();
      const literalColon = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const literalPercentEncoding = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-b",
      });

      // "AB:CD" (a real colon) and "AB%3ACD" (the literal three characters
      // '%', '3', 'A' followed by "CD" — what "AB:CD" would look like
      // AFTER percent-encoding, but here typed as the actual callId).
      const tokenColon = literalColon.allocateParticipationGeneration("AB:CD");
      const tokenPercent = literalPercentEncoding.allocateParticipationGeneration("AB%3ACD");

      expect(literalColon.getParticipationToken("AB:CD")).toEqual(tokenColon);
      expect(literalPercentEncoding.getParticipationToken("AB%3ACD")).toEqual(tokenPercent);
      expect(tokenColon).not.toEqual(tokenPercent);

      literalColon.close();
      literalPercentEncoding.close();
    });

    it("a malformed (lone surrogate) callId never throws — allocate and read both degrade gracefully", () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const malformed = "\uD800";

      expect(() => coordinator.allocateParticipationGeneration(malformed)).not.toThrow();
      expect(() => coordinator.getParticipationToken(malformed)).not.toThrow();
      expect(coordinator.allocateParticipationGeneration(malformed)).toEqual({
        generation: 1,
        writerId: "tab-a",
      });

      coordinator.close();
    });

    it("ignores corrupted JSON at a writer key instead of treating it as a valid token", () => {
      const storage = new SharedStorage();
      storage.setItem(`nchat.call.participation.v2.${callId}:tab-a`, "{not valid json");
      const coordinator = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-b",
      });
      expect(coordinator.getParticipationToken(callId)).toBeNull();
      expect(coordinator.allocateParticipationGeneration(callId)).toEqual({
        generation: 1,
        writerId: "tab-b",
      });
      coordinator.close();
    });

    it("ignores a writer key holding a negative or non-integer generation", () => {
      const storage = new SharedStorage();
      storage.setItem(
        `nchat.call.participation.v2.${callId}:tab-a`,
        JSON.stringify({ v: 2, generation: -1 }),
      );
      storage.setItem(
        `nchat.call.participation.v2.${callId}:tab-b`,
        JSON.stringify({ v: 2, generation: 1.5 }),
      );
      const coordinator = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-c",
      });
      expect(coordinator.getParticipationToken(callId)).toBeNull();
      coordinator.close();
    });

    it("ignores localStorage keys unrelated to the participation protocol", () => {
      const storage = new SharedStorage();
      storage.setItem("nchat.call.owner.v1", "not a participation key at all");
      storage.setItem("some-other-app-key", JSON.stringify({ v: 2, generation: 99 }));
      const coordinator = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      expect(coordinator.getParticipationToken(callId)).toBeNull();
      expect(coordinator.allocateParticipationGeneration(callId)).toEqual({
        generation: 1,
        writerId: "tab-a",
      });
      coordinator.close();
    });

    it("degrades to a safe fallback, never an unhandled exception, when storage itself throws", () => {
      const throwing: Storage = {
        get length(): number {
          throw new Error("storage unavailable");
        },
        key: () => {
          throw new Error("storage unavailable");
        },
        getItem: () => {
          throw new Error("storage unavailable");
        },
        setItem: () => {
          throw new Error("storage unavailable");
        },
        removeItem: () => undefined,
        clear: () => undefined,
      };
      const coordinator = createOwnershipCoordinator({
        storage: throwing,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      expect(() => coordinator.allocateParticipationGeneration(callId)).not.toThrow();
      expect(() => coordinator.getParticipationToken(callId)).not.toThrow();
      expect(coordinator.allocateParticipationGeneration(callId)).toEqual({
        generation: 1,
        writerId: "tab-a",
      });
      expect(coordinator.getParticipationToken(callId)).toBeNull();
      coordinator.close();
    });
  });

  describe("compareParticipationTokens", () => {
    it("orders strictly by generation regardless of writerId", () => {
      expect(
        compareParticipationTokens(
          { generation: 2, writerId: "z" },
          { generation: 1, writerId: "a" },
        ),
      ).toBeGreaterThan(0);
      expect(
        compareParticipationTokens(
          { generation: 1, writerId: "z" },
          { generation: 2, writerId: "a" },
        ),
      ).toBeLessThan(0);
    });

    it("breaks a generation tie by writerId, symmetrically and deterministically", () => {
      const a = { generation: 5, writerId: "tab-a" };
      const b = { generation: 5, writerId: "tab-b" };
      const forward = compareParticipationTokens(a, b);
      expect(forward).not.toBe(0);
      expect(compareParticipationTokens(b, a)).toBe(-forward);
      expect(compareParticipationTokens(a, a)).toBe(0);
    });
  });

  it("scopes lease conflicts to the same callId (achado #2)", async () => {
    const otherCallId = "00000000-0000-4000-8000-000000000999";
    let now = 1000;
    const storage = new SharedStorage();
    const options = {
      storage,
      now: () => now,
      settle: () => Promise.resolve(),
      setInterval: vi.fn(() => 1),
      clearInterval: vi.fn(),
      leaseMs: 100,
    };

    // A live, unexpired lease held by another tab for a DIFFERENT call must
    // never block a claim for the current call.
    const owner = createOwnershipCoordinator({ ...options, tabId: "tab-owner" });
    const first = await owner.claim(otherCallId, "main");
    expect(first?.tabId).toBe("tab-owner");

    const claimant = createOwnershipCoordinator({ ...options, tabId: "tab-claimant" });
    const claimed = await claimant.claim(callId, "main");
    expect(claimed).toMatchObject({ tabId: "tab-claimant", callId, epoch: 1 });
    claimant.close();

    // Same-call active lease still blocks a competing tab.
    const storageSameCall = new SharedStorage();
    const sameCallOwner = createOwnershipCoordinator({
      ...options,
      storage: storageSameCall,
      tabId: "tab-a",
    });
    await sameCallOwner.claim(callId, "main");
    const blocked = createOwnershipCoordinator({
      ...options,
      storage: storageSameCall,
      tabId: "tab-b",
    });
    expect(await blocked.claim(callId, "dedicated")).toBeNull();
    blocked.close();
    sameCallOwner.close();

    // An expired lease — even for the same call — never blocks a claim.
    const storageExpired = new SharedStorage();
    const expiredOwner = createOwnershipCoordinator({
      ...options,
      storage: storageExpired,
      tabId: "tab-expired",
    });
    await expiredOwner.claim(callId, "main");
    now = 5000;
    const afterExpiry = createOwnershipCoordinator({
      ...options,
      storage: storageExpired,
      tabId: "tab-fresh",
    });
    expect(await afterExpiry.claim(callId, "dedicated")).toMatchObject({ tabId: "tab-fresh" });
    afterExpiry.close();
    now = 1000;

    // A higher-epoch lease belonging to a different call must not affect
    // (or be conflated with) the current call's own epoch numbering.
    const storageEpoch = new SharedStorage();
    const highEpochOtherCall = createOwnershipCoordinator({
      ...options,
      storage: storageEpoch,
      tabId: "tab-high",
    });
    await highEpochOtherCall.claim(otherCallId, "main");
    await highEpochOtherCall.claim(otherCallId, "main", 50);
    const freshClaimForThisCall = createOwnershipCoordinator({
      ...options,
      storage: storageEpoch,
      tabId: "tab-new",
    });
    const result = await freshClaimForThisCall.claim(callId, "main");
    expect(result).toMatchObject({ tabId: "tab-new", callId, epoch: 1 });
    freshClaimForThisCall.close();

    // Takeover of the same call by a higher epoch still works as before.
    const storageTakeover = new SharedStorage();
    const holder = createOwnershipCoordinator({
      ...options,
      storage: storageTakeover,
      tabId: "tab-holder",
    });
    const held = await holder.claim(callId, "main");
    const taker = createOwnershipCoordinator({
      ...options,
      storage: storageTakeover,
      tabId: "tab-taker",
    });
    now = 1100;
    const takenOver = await taker.claim(callId, "dedicated", held?.epoch);
    expect(takenOver).toMatchObject({ tabId: "tab-taker", epoch: (held?.epoch ?? 0) + 1 });
    now = 1000;
  });

  it("loses ownership when heartbeat storage fails and tolerates release cleanup failure", async () => {
    const storage = new SharedStorage();
    const channel = new TestChannel();
    let heartbeat: () => void = () => undefined;
    const lost = vi.fn();
    const coordinator = createOwnershipCoordinator({
      storage,
      channel,
      tabId: "tab-a",
      now: () => 1000,
      settle: () => Promise.resolve(),
      setInterval: (callback) => {
        heartbeat = callback;
        return 1;
      },
      clearInterval: vi.fn(),
      onOwnershipLost: lost,
    });
    await coordinator.claim(callId, "main");
    storage.getItem = () => {
      throw new Error("storage");
    };
    heartbeat();
    expect(lost).toHaveBeenCalledOnce();

    const releaseStorage = new SharedStorage();
    const release = createOwnershipCoordinator({
      storage: releaseStorage,
      channel: new TestChannel(),
      tabId: "tab-b",
      now: () => 1000,
      settle: () => Promise.resolve(),
      setInterval: vi.fn(() => 1),
      clearInterval: vi.fn(),
    });
    await release.claim(callId, "main");
    releaseStorage.getItem = () => {
      throw new Error("storage");
    };
    expect(() => release.release(callId)).not.toThrow();
    release.close();
  });

  describe("compareOwnershipClaims", () => {
    it("orders strictly by epoch regardless of tabId", () => {
      expect(
        compareOwnershipClaims({ epoch: 2, tabId: "z" }, { epoch: 1, tabId: "a" }),
      ).toBeGreaterThan(0);
      expect(
        compareOwnershipClaims({ epoch: 1, tabId: "z" }, { epoch: 2, tabId: "a" }),
      ).toBeLessThan(0);
    });

    it("breaks an epoch tie by tabId, symmetrically and deterministically, matching resolveLeaseConflict", () => {
      const a = { epoch: 5, tabId: "tab-a" };
      const b = { epoch: 5, tabId: "tab-b" };
      const forward = compareOwnershipClaims(a, b);
      expect(forward).toBeGreaterThan(0); // "tab-a" <= "tab-b" lexicographically -> a wins
      expect(compareOwnershipClaims(b, a)).toBe(-forward);
      expect(compareOwnershipClaims(a, a)).toBe(0);
    });

    it("is the exact rule resolveLeaseConflict now delegates to", () => {
      const a: OwnerLease = {
        v: 1,
        callId,
        tabId: "tab-a",
        epoch: 2,
        role: "main",
        expiresAt: 2000,
      };
      const b: OwnerLease = {
        v: 1,
        callId,
        tabId: "tab-b",
        epoch: 3,
        role: "dedicated",
        expiresAt: 2000,
      };
      expect(resolveLeaseConflict(a, b)).toBe(compareOwnershipClaims(a, b) >= 0 ? a : b);
      expect(resolveLeaseConflict(a, { ...b, epoch: 2 })).toBe(
        compareOwnershipClaims(a, { ...b, epoch: 2 }) >= 0 ? a : { ...b, epoch: 2 },
      );
    });
  });

  describe("claim epoch floor over media-intent (issue #610)", () => {
    const floorOptions = () => ({
      now: () => 1000,
      settle: () => Promise.resolve(),
      setInterval: vi.fn(() => 1),
      clearInterval: vi.fn(),
      leaseMs: 100,
    });

    it("floors the next epoch above the highest media-intent epoch even after release() removed the lease", async () => {
      const storage = new SharedStorage();
      const options = { ...floorOptions(), storage };
      const main = createOwnershipCoordinator({ ...options, tabId: "tab-main" });
      const lease = await main.claim(callId, "main");
      expect(lease?.epoch).toBe(1);
      expect(
        main.writeMediaIntent(callId, lease!, { microphone: true, camera: false }, "confirmed"),
      ).toEqual({ ok: true, revision: 1 });
      main.release(callId);
      main.close();

      // No OwnerLease remains, but the media-intent entry (epoch 1) does.
      const dedicated = createOwnershipCoordinator({ ...options, tabId: "tab-dedicated" });
      const reclaimed = await dedicated.claim(callId, "dedicated");
      expect(reclaimed?.epoch).toBeGreaterThan(1);
      dedicated.close();
    });

    it("floors above a lower afterEpoch too when media-intent recorded a higher epoch", async () => {
      const storage = new SharedStorage();
      const options = { ...floorOptions(), storage };
      const a = createOwnershipCoordinator({ ...options, tabId: "tab-a" });
      let lease = await a.claim(callId, "main");
      for (let i = 0; i < 4; i += 1) {
        lease = await a.claim(callId, "main", lease!.epoch);
      }
      expect(lease!.epoch).toBeGreaterThanOrEqual(5);
      expect(
        a.writeMediaIntent(callId, lease!, { microphone: false, camera: false }, "confirmed"),
      ).toEqual({ ok: true, revision: 1 });
      a.release(callId);
      a.close();

      const b = createOwnershipCoordinator({ ...options, tabId: "tab-b" });
      const claimed = await b.claim(callId, "dedicated", 1);
      expect(claimed?.epoch).toBeGreaterThan(lease!.epoch);
      b.close();
    });

    it("fails closed (returns null) when the epoch floor has reached Number.MAX_SAFE_INTEGER", async () => {
      const storage = new SharedStorage();
      storage.setItem(
        `nchat.call.media-intent.v1.${encodeURIComponent(callId)}:ancient-writer`,
        JSON.stringify({
          v: 1,
          ownershipEpoch: Number.MAX_SAFE_INTEGER,
          revision: 1,
          phase: "confirmed",
          microphone: false,
          camera: false,
        }),
      );
      const coordinator = createOwnershipCoordinator({
        ...floorOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      expect(await coordinator.claim(callId, "main")).toBeNull();
      coordinator.close();
    });
  });

  describe("media intent (issue #610)", () => {
    const intentOptions = () => ({
      now: () => 1000,
      settle: () => Promise.resolve(),
      setInterval: vi.fn(() => 1),
      clearInterval: vi.fn(),
      leaseMs: 5000,
    });
    const mediaIntentKey = (id: string, writerId: string) =>
      `nchat.call.media-intent.v1.${encodeURIComponent(id)}:${writerId}`;
    const rawEntry = (
      ownershipEpoch: number,
      revision: number,
      phase: "confirmed" | "pending",
      microphone: boolean,
      camera: boolean,
    ) => JSON.stringify({ v: 1, ownershipEpoch, revision, phase, microphone, camera });

    it("round-trips all four microphone/camera combinations as confirmed", async () => {
      const combos: MediaIntent[] = [
        { microphone: true, camera: true },
        { microphone: true, camera: false },
        { microphone: false, camera: true },
        { microphone: false, camera: false },
      ];
      for (const intent of combos) {
        const storage = new SharedStorage();
        const coordinator = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "tab-a",
        });
        const lease = await coordinator.claim(callId, "main");
        const outcome = coordinator.writeMediaIntent(callId, lease!, intent, "confirmed");
        expect(outcome).toEqual({ ok: true, revision: 1 });
        // Own entry at the own current epoch — always valid, no predecessor
        // lookup needed (§7 same-owner reconnect).
        expect(coordinator.readMediaIntentForLease(callId)).toEqual(intent);
        coordinator.close();
      }
    });

    it("a winning PENDING entry is never exposed as a specific preset — always OFF/OFF regardless of its stored booleans", async () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const lease = await coordinator.claim(callId, "main");
      const outcome = coordinator.writeMediaIntent(
        callId,
        lease!,
        { microphone: true, camera: true },
        "pending",
      );
      expect(outcome).toEqual({ ok: true, revision: 1 });
      expect(coordinator.readMediaIntentForLease(callId)).toEqual({
        microphone: false,
        camera: false,
      });
      coordinator.close();
    });

    it("readMediaIntentForLease returns null when this coordinator holds no lease for callId", () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      expect(coordinator.readMediaIntentForLease(callId)).toBeNull();
      coordinator.close();
    });

    it("rejects a write whose captured lease tabId is not this coordinator's own (stale)", async () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const lease = await coordinator.claim(callId, "main");
      const foreign: OwnerLease = { ...lease!, tabId: "tab-other" };
      expect(
        coordinator.writeMediaIntent(
          callId,
          foreign,
          { microphone: true, camera: true },
          "confirmed",
        ),
      ).toEqual({ ok: false, reason: "stale" });
      expect(coordinator.readMediaIntentForLease(callId)).toBeNull();
      coordinator.close();
    });

    it("rejects a write when the coordinator's current lease no longer matches the captured one (stale)", async () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const captured = await coordinator.claim(callId, "main");
      coordinator.release(callId);
      expect(
        coordinator.writeMediaIntent(
          callId,
          captured!,
          { microphone: true, camera: true },
          "confirmed",
        ),
      ).toEqual({ ok: false, reason: "stale" });
      coordinator.close();
    });

    it("rejects a stale same-writer write from a superseded epoch (E1 -> E3 -> delayed E1)", async () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const e1 = await coordinator.claim(callId, "main");
      expect(
        coordinator.writeMediaIntent(callId, e1!, { microphone: true, camera: false }, "confirmed"),
      ).toEqual({ ok: true, revision: 1 });

      coordinator.release(callId);
      const e3 = await coordinator.claim(callId, "main");
      expect(e3!.epoch).toBeGreaterThan(e1!.epoch);
      expect(
        coordinator.writeMediaIntent(callId, e3!, { microphone: false, camera: true }, "confirmed"),
      ).toEqual({ ok: true, revision: 1 });

      expect(
        coordinator.writeMediaIntent(callId, e1!, { microphone: true, camera: true }, "confirmed"),
      ).toEqual({ ok: false, reason: "stale" });
      expect(coordinator.readMediaIntentForLease(callId)).toEqual({
        microphone: false,
        camera: true,
      });
      coordinator.close();
    });

    it("increments revision monotonically for sequential same-epoch writes from the same writer, across phases", async () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const lease = await coordinator.claim(callId, "main");
      coordinator.writeMediaIntent(callId, lease!, { microphone: true, camera: false }, "pending");
      coordinator.writeMediaIntent(
        callId,
        lease!,
        { microphone: true, camera: false },
        "confirmed",
      );
      const third = coordinator.writeMediaIntent(
        callId,
        lease!,
        { microphone: false, camera: true },
        "pending",
      );
      expect(third).toEqual({ ok: true, revision: 3 });
      const raw = storage.getItem(mediaIntentKey(callId, "tab-a"));
      expect(JSON.parse(raw!)).toMatchObject({
        ownershipEpoch: lease!.epoch,
        revision: 3,
        phase: "pending",
      });
      // The latest write (pending) governs the read, regardless of what an
      // earlier confirmed write at a lower revision said — there is only
      // ever one stored value per writer key (issue #610 audit: "pending
      // newer que confirmed antigo não ressuscita confirmed").
      expect(coordinator.readMediaIntentForLease(callId)).toEqual({
        microphone: false,
        camera: false,
      });
      coordinator.close();
    });

    describe("expectedRevision fencing (issue #610 privacy blocker: old operation cannot confirm a newer pending)", () => {
      it("a confirmed write with the correct expectedRevision succeeds and advances revision", async () => {
        const storage = new SharedStorage();
        const coordinator = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "tab-a",
        });
        const lease = await coordinator.claim(callId, "main");
        const pending = coordinator.writeMediaIntent(
          callId,
          lease!,
          { microphone: false, camera: false },
          "pending",
        );
        expect(pending).toEqual({ ok: true, revision: 1 });
        const confirmed = coordinator.writeMediaIntent(
          callId,
          lease!,
          { microphone: false, camera: false },
          "confirmed",
          { expectedRevision: (pending as { ok: true; revision: number }).revision },
        );
        expect(confirmed).toEqual({ ok: true, revision: 2 });
        expect(coordinator.readMediaIntentForLease(callId)).toEqual({
          microphone: false,
          camera: false,
        });
        coordinator.close();
      });

      it("an old operation's confirmed write is rejected once a newer pending already advanced the revision", async () => {
        const storage = new SharedStorage();
        const coordinator = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "tab-a",
        });
        const lease = await coordinator.claim(callId, "main");
        // Operation 1 (mic) writes its own pending at revision 1.
        const op1Pending = coordinator.writeMediaIntent(
          callId,
          lease!,
          { microphone: false, camera: false },
          "pending",
        );
        expect(op1Pending).toEqual({ ok: true, revision: 1 });
        // Operation 2 (camera, newer) writes its own pending, advancing to
        // revision 2 — overwriting the same per-writer key.
        const op2Pending = coordinator.writeMediaIntent(
          callId,
          lease!,
          { microphone: false, camera: true },
          "pending",
        );
        expect(op2Pending).toEqual({ ok: true, revision: 2 });
        // Operation 1's SDK call finally resolves and tries to confirm —
        // still holding its OWN captured revision (1) — must be rejected.
        const op1Confirm = coordinator.writeMediaIntent(
          callId,
          lease!,
          { microphone: true, camera: false },
          "confirmed",
          { expectedRevision: 1 },
        );
        expect(op1Confirm).toEqual({ ok: false, reason: "stale" });
        // Storage must still reflect operation 2's (pending) entry — never
        // operation 1's stale confirm.
        const raw = storage.getItem(mediaIntentKey(callId, "tab-a"));
        expect(JSON.parse(raw!)).toMatchObject({ revision: 2, phase: "pending" });
        coordinator.close();
      });
    });

    it("distinguishes a genuine storage failure from a stale write", async () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const lease = await coordinator.claim(callId, "main");
      storage.setItem = () => {
        throw new Error("quota exceeded");
      };
      const outcome = coordinator.writeMediaIntent(
        callId,
        lease!,
        { microphone: false, camera: false },
        "pending",
      );
      expect(outcome).toEqual({ ok: false, reason: "storage-error" });
      coordinator.close();
    });

    it("full pending -> confirmed path: recovery only sees the confirmed value once settled", async () => {
      const storage = new SharedStorage();
      const coordinator = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const lease = await coordinator.claim(callId, "main");
      const pending = coordinator.writeMediaIntent(
        callId,
        lease!,
        { microphone: true, camera: false },
        "pending",
      );
      expect(coordinator.readMediaIntentForLease(callId)).toEqual({
        microphone: false,
        camera: false,
      });
      const confirmed = coordinator.writeMediaIntent(
        callId,
        lease!,
        { microphone: true, camera: false },
        "confirmed",
        { expectedRevision: (pending as { ok: true; revision: number }).revision },
      );
      expect(confirmed.ok).toBe(true);
      expect(coordinator.readMediaIntentForLease(callId)).toEqual({
        microphone: true,
        camera: false,
      });
      coordinator.close();
    });

    it("a corrupt own-entry falls through to the predecessor instead of crashing", async () => {
      const storage = new SharedStorage();
      // Predecessor L, epoch 1, valid confirmed entry.
      const l = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "L",
      });
      const lLease = await l.claim(callId, "main");
      l.writeMediaIntent(callId, lLease!, { microphone: true, camera: false }, "confirmed");

      // New owner claims past L, then its OWN key is corrupted directly.
      const owner = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "owner",
      });
      await owner.claim(callId, "main", lLease!.epoch);
      storage.setItem(mediaIntentKey(callId, "owner"), "not-json");
      expect(owner.readMediaIntentForLease(callId)).toEqual({ microphone: true, camera: false });
      l.close();
      owner.close();
    });

    it("never reads across callIds even when percent-encoding could textually resemble another boundary", async () => {
      const trickyCallId = "call%3Aimpersonator";
      const storage = new SharedStorage();
      const other = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
      });
      const otherLease = await other.claim(trickyCallId, "main");
      other.writeMediaIntent(
        trickyCallId,
        otherLease!,
        { microphone: true, camera: true },
        "confirmed",
      );
      const reader = createOwnershipCoordinator({
        ...intentOptions(),
        storage,
        channel: new TestChannel(),
        tabId: "reader",
      });
      await reader.claim(callId, "main");
      expect(reader.readMediaIntentForLease(callId)).toBeNull();
      other.close();
      reader.close();
    });

    describe("predecessor provenance (issue #610 audit — historical loser blocker)", () => {
      it("1. mic: a loser's stray confirmed ON at E is never used — the legitimate E-1 predecessor's OFF wins, even though the true E-winner never wrote anything", async () => {
        const storage = new SharedStorage();
        const l = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "L",
        });
        const lLease = await l.claim(callId, "main"); // E-1
        l.writeMediaIntent(callId, lLease!, { microphone: false, camera: false }, "confirmed");

        // A wins epoch E (supersedes L, which is still "live" from A's
        // point of view — exactly how a real handoff claims past a known
        // epoch) but never writes media-intent (its connect/toggle never
        // got that far before closing).
        const a = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "A",
        });
        const aLease = await a.claim(callId, "main", lLease!.epoch);
        expect(aLease!.epoch).toBeGreaterThan(lLease!.epoch);

        // B, a concurrent loser without Web Locks, still manages to write a
        // stray confirmed entry at the SAME epoch E before observing its
        // own defeat — simulated directly, since reproducing the exact
        // runClaim race deterministically isn't the point of this test;
        // what matters is that this entry, once present, is provably never
        // read as a predecessor by anyone.
        storage.setItem(
          mediaIntentKey(callId, "B"),
          rawEntry(aLease!.epoch, 1, "confirmed", true, true),
        );

        // A never releases or writes media-intent — it just goes silent
        // (crash), leaving its lease sitting in storage exactly as any
        // crashed tab would. C reclaims past it using the known epoch
        // (mirroring a real handoff/dead-owner-poll claim).
        const c = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "C",
        });
        const cLease = await c.claim(callId, "main", aLease!.epoch);
        expect(cLease!.epoch).toBeGreaterThan(aLease!.epoch);

        // C's predecessor is A:E (existingForCall, still readable from A's
        // never-released lease) — A never wrote, so the read finds nothing
        // there and must NOT fall back to B's entry.
        expect(c.readMediaIntentForLease(callId)).toBeNull();

        l.close();
        a.close();
        c.close();
      });

      it("2. camera: same shape, predecessor lookup finds nothing for A:E, loser B:E is never consulted", async () => {
        const storage = new SharedStorage();
        const l = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "L",
        });
        const lLease = await l.claim(callId, "main");
        l.writeMediaIntent(callId, lLease!, { microphone: false, camera: false }, "confirmed");

        const a = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "A",
        });
        const aLease = await a.claim(callId, "main", lLease!.epoch);
        storage.setItem(
          mediaIntentKey(callId, "B"),
          rawEntry(aLease!.epoch, 1, "confirmed", true, true),
        );

        const c = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "C",
        });
        await c.claim(callId, "main", aLease!.epoch);
        expect(c.readMediaIntentForLease(callId)).toBeNull();

        l.close();
        a.close();
        c.close();
      });

      it("3. winner E OFF + loser E ON (both wrote): a future owner uses the winner's OFF, never the loser's ON", async () => {
        const storage = new SharedStorage();
        const a = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "A",
        });
        const aLease = await a.claim(callId, "main");
        a.writeMediaIntent(callId, aLease!, { microphone: false, camera: false }, "confirmed");
        // Loser B also manages to write, at the SAME epoch, the opposite
        // value.
        storage.setItem(
          mediaIntentKey(callId, "B"),
          rawEntry(aLease!.epoch, 1, "confirmed", true, true),
        );

        const c = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "C",
        });
        await c.claim(callId, "main", aLease!.epoch);
        expect(c.readMediaIntentForLease(callId)).toEqual({ microphone: false, camera: false });
        a.close();
        c.close();
      });

      it("4. predecessor known via an explicit handoff hint: the exact snapshot is preserved, independent of storage's own existingForCall", async () => {
        const storage = new SharedStorage();
        const main = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "main-tab",
        });
        const mainLease = await main.claim(callId, "main");
        main.writeMediaIntent(callId, mainLease!, { microphone: true, camera: true }, "confirmed");
        // Main releases (as the real handoff-start flow does) BEFORE the
        // dedicated tab ever claims — so existingForCall alone would see
        // nothing. Only the explicit predecessorHint (mirroring the
        // "handoff" message's own {tabId, epoch}) can recover it.
        main.release(callId);

        const dedicated = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "dedicated-tab",
        });
        await dedicated.claim(callId, "dedicated", mainLease!.epoch, {
          tabId: "main-tab",
          epoch: mainLease!.epoch,
        });
        expect(dedicated.readMediaIntentForLease(callId)).toEqual({
          microphone: true,
          camera: true,
        });
        main.close();
        dedicated.close();
      });

      it("5. predecessor known via a crashed/expired OwnerLease still in storage: snapshot preserved", async () => {
        let now = 1000;
        const storage = new SharedStorage();
        const crashed = createOwnershipCoordinator({
          storage,
          channel: new TestChannel(),
          tabId: "crashed-tab",
          now: () => now,
          settle: () => Promise.resolve(),
          setInterval: vi.fn(() => 1),
          clearInterval: vi.fn(),
          leaseMs: 100,
        });
        const crashedLease = await crashed.claim(callId, "main");
        crashed.writeMediaIntent(
          callId,
          crashedLease!,
          { microphone: false, camera: true },
          "confirmed",
        );
        // Never released — simulates the tab/process dying outright. Its
        // lease just sits in storage, now expired.
        now = 5000;

        const recoverer = createOwnershipCoordinator({
          storage,
          channel: new TestChannel(),
          tabId: "recoverer",
          now: () => now,
          settle: () => Promise.resolve(),
          setInterval: vi.fn(() => 1),
          clearInterval: vi.fn(),
          leaseMs: 100,
        });
        const recovererLease = await recoverer.claim(callId, "main");
        expect(recovererLease!.epoch).toBeGreaterThan(crashedLease!.epoch);
        expect(recoverer.readMediaIntentForLease(callId)).toEqual({
          microphone: false,
          camera: true,
        });
        crashed.close();
        recoverer.close();
      });

      it("6. predecessor unknown/unprovable (a genuinely first-ever claim): null, never a global-max approximation", async () => {
        const storage = new SharedStorage();
        // Some UNRELATED writer's entry at a lower epoch for the SAME
        // callId, with no OwnerLease evidence connecting it to this claim
        // at all (no existingForCall, no hint, no same-instance memory) —
        // must never be picked up as a fallback predecessor.
        storage.setItem(
          mediaIntentKey(callId, "unrelated"),
          rawEntry(1, 1, "confirmed", true, true),
        );
        const coordinator = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "first-ever",
        });
        await coordinator.claim(callId, "main");
        expect(coordinator.readMediaIntentForLease(callId)).toBeNull();
        coordinator.close();
      });

      it("7. current owner + own entry at the current epoch is always valid (same-lease reconnect, no new claim)", async () => {
        const storage = new SharedStorage();
        const coordinator = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "owner",
        });
        const lease = await coordinator.claim(callId, "main");
        coordinator.writeMediaIntent(
          callId,
          lease!,
          { microphone: true, camera: false },
          "confirmed",
        );
        // Re-reading without any new claim() call — same lease, same epoch.
        expect(coordinator.readMediaIntentForLease(callId)).toEqual({
          microphone: true,
          camera: false,
        });
        coordinator.close();
      });

      it("8. a same-epoch foreign loser is never consulted even when it is the ONLY other entry present", async () => {
        const storage = new SharedStorage();
        const owner = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "owner",
        });
        const lease = await owner.claim(callId, "main"); // fresh claim, no predecessor at all
        storage.setItem(
          mediaIntentKey(callId, "loser"),
          rawEntry(lease!.epoch, 1, "confirmed", true, true),
        );
        expect(owner.readMediaIntentForLease(callId)).toBeNull();
        owner.close();
      });

      it("9. a PENDING entry at the legitimate predecessor's epoch degrades to OFF/OFF, never the booleans it carries", async () => {
        const storage = new SharedStorage();
        const l = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "L",
        });
        const lLease = await l.claim(callId, "main");
        l.writeMediaIntent(callId, lLease!, { microphone: true, camera: true }, "pending");

        const a = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "A",
        });
        await a.claim(callId, "main", lLease!.epoch);
        expect(a.readMediaIntentForLease(callId)).toEqual({ microphone: false, camera: false });
        l.close();
        a.close();
      });

      it("10. a PENDING entry from a non-predecessor loser is completely ignored, not merely degraded", async () => {
        const storage = new SharedStorage();
        const l = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "L",
        });
        const lLease = await l.claim(callId, "main");
        l.writeMediaIntent(callId, lLease!, { microphone: false, camera: false }, "confirmed");

        const a = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "A",
        });
        const aLease = await a.claim(callId, "main", lLease!.epoch);
        // Loser's stray write is PENDING this time, not confirmed — must
        // still never be looked up at all, exactly like test 1/3.
        storage.setItem(
          mediaIntentKey(callId, "B"),
          rawEntry(aLease!.epoch, 1, "pending", true, true),
        );

        const c = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "C",
        });
        await c.claim(callId, "main", aLease!.epoch);
        // C's predecessor is A:E, which never wrote anything — the read
        // stops there (deliberately single-hop, never walking further back
        // to L) and returns null, regardless of B's stray pending entry.
        expect(c.readMediaIntentForLease(callId)).toBeNull();
        l.close();
        a.close();
        c.close();
      });
    });

    describe("resolvePredecessor (issue #610 audit — final blocker: lastReleasedByCallId removed)", () => {
      it("3. same-tab immediate reclaim with an explicit hint for the exact epoch just released is trusted", () => {
        expect(resolvePredecessor(null, { tabId: "A", epoch: 1 }, 1)).toEqual({
          tabId: "A",
          epoch: 1,
        });
      });

      it("4. a stale hint never overrides a newer existingForCall", () => {
        const existing = { tabId: "B", epoch: 2 };
        const staleHint = { tabId: "A", epoch: 1 };
        expect(resolvePredecessor(existing, staleHint, 1)).toEqual(existing);
      });

      it("5. same epoch, different tabId: the winner is exactly resolveLeaseConflict's own tie-break", () => {
        const existing = { tabId: "B", epoch: 2 };
        const hint = { tabId: "A", epoch: 2 };
        const expected = compareOwnershipClaims(existing, hint) > 0 ? existing : hint;
        expect(resolvePredecessor(existing, hint, 2)).toEqual(expected);
        // Sanity: this is genuinely exercising the tie-break, not a no-op —
        // prove it against resolveLeaseConflict directly on equivalent
        // OwnerLease objects.
        const asLease = (c: { tabId: string; epoch: number }): OwnerLease => ({
          v: 1,
          callId,
          tabId: c.tabId,
          epoch: c.epoch,
          role: "main",
          expiresAt: 0,
        });
        expect(resolveLeaseConflict(asLease(existing), asLease(hint)).tabId).toBe(expected.tabId);
      });

      it("6. a hint whose epoch does not match afterEpoch is rejected, falling back to existingForCall", () => {
        const existing = { tabId: "B", epoch: 2 };
        const mismatchedHint = { tabId: "A", epoch: 1 };
        expect(resolvePredecessor(existing, mismatchedHint, 5)).toEqual(existing);
        expect(resolvePredecessor(null, mismatchedHint, 5)).toBeNull();
      });

      it("7. no hint and no existingForCall: predecessor is unknown", () => {
        expect(resolvePredecessor(null, undefined, undefined)).toBeNull();
        expect(resolvePredecessor(null, undefined, 5)).toBeNull();
      });

      it("no hint at all: existingForCall alone governs, whatever it is", () => {
        const existing = { tabId: "B", epoch: 2 };
        expect(resolvePredecessor(existing, undefined, 2)).toEqual(existing);
        expect(resolvePredecessor(null, undefined, 2)).toBeNull();
      });
    });

    describe("missed cross-tab message: lastReleasedByCallId must never substitute for real provenance (issue #610 audit — final blocker)", () => {
      it("1. mic: A:E1 ON releases, B:E2 OFF claims+writes+releases, A never sees B's message and reclaims E3 with empty storage — predecessor is UNKNOWN, never A's own stale A:E1", async () => {
        const storage = new SharedStorage();
        const a1 = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "A",
        });
        const aLease1 = await a1.claim(callId, "main");
        a1.writeMediaIntent(callId, aLease1!, { microphone: true, camera: false }, "confirmed");
        a1.release(callId); // storage now empty; A privately "remembers" A:E1, but must never use it

        // B claims past A, changes the intent, and cleanly releases too —
        // exactly the intermediate owner A never learns about.
        const b = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "B",
        });
        const bLease = await b.claim(callId, "main", aLease1!.epoch);
        b.writeMediaIntent(callId, bLease!, { microphone: false, camera: false }, "confirmed");
        b.release(callId); // storage empty again

        // A reclaims — new coordinator instance stands in for "A resumed
        // after being suspended", but even the SAME instance (a1) must
        // never fall back to its own release-time memory here.
        const a2 = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "A",
        });
        const aLease3 = await a2.claim(callId, "main", bLease!.epoch);
        expect(aLease3!.epoch).toBeGreaterThan(bLease!.epoch);
        expect(a2.readMediaIntentForLease(callId)).toBeNull();

        a1.close();
        b.close();
        a2.close();
      });

      it("2. camera: same shape as test 1", async () => {
        const storage = new SharedStorage();
        const a1 = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "A",
        });
        const aLease1 = await a1.claim(callId, "main");
        a1.writeMediaIntent(callId, aLease1!, { microphone: false, camera: true }, "confirmed");
        a1.release(callId);

        const b = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "B",
        });
        const bLease = await b.claim(callId, "main", aLease1!.epoch);
        b.writeMediaIntent(callId, bLease!, { microphone: false, camera: false }, "confirmed");
        b.release(callId);

        const a2 = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "A",
        });
        await a2.claim(callId, "main", bLease!.epoch);
        expect(a2.readMediaIntentForLease(callId)).toBeNull();

        a1.close();
        b.close();
        a2.close();
      });

      it("10. dedicated->main with the 'released' message lost: OFF/OFF, never an old ancestor's confirmed value", async () => {
        // Mirrors CallSessionProvider's own shape: main (M) hands off to
        // dedicated (D) via an explicit hint (handoff message always
        // arrives — it targets a specific tab and the flow depends on it).
        // D changes intent and later releases cleanly (minimize), but the
        // "released" broadcast is the one message that can legitimately be
        // missed (no targeted delivery guarantee, no ack). M's own
        // fallback poll then reclaims with NO hint at all.
        const storage = new SharedStorage();
        const m1 = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "M",
        });
        const mLease = await m1.claim(callId, "main");
        m1.writeMediaIntent(callId, mLease!, { microphone: true, camera: true }, "confirmed");
        m1.release(callId);

        const d = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "D",
        });
        const dLease = await d.claim(callId, "dedicated", mLease!.epoch, {
          tabId: "M",
          epoch: mLease!.epoch,
        });
        d.writeMediaIntent(callId, dLease!, { microphone: false, camera: false }, "confirmed");
        d.release(callId); // the "released" message this produces is the one M misses

        // M's poll-based reclaim: no hint (the message never arrived).
        const m2 = createOwnershipCoordinator({
          ...intentOptions(),
          storage,
          channel: new TestChannel(),
          tabId: "M",
        });
        await m2.claim(callId, "main", dLease!.epoch);
        expect(m2.readMediaIntentForLease(callId)).toBeNull();

        m1.close();
        d.close();
        m2.close();
      });
    });

    it("heartbeat write failure and media-intent write failure can coexist — a durable pending marker written before both broke still governs recovery", async () => {
      const now = 1000;
      const storage = new SharedStorage();
      const lost = vi.fn();
      let heartbeat!: () => void;
      const coordinator = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-a",
        now: () => now,
        settle: () => Promise.resolve(),
        setInterval: (callback) => {
          heartbeat = callback;
          return 1;
        },
        clearInterval: vi.fn(),
        leaseMs: 100,
        onOwnershipLost: lost,
      });
      const lease = await coordinator.claim(callId, "main");
      const pending = coordinator.writeMediaIntent(
        callId,
        lease!,
        { microphone: false, camera: false },
        "pending",
      );
      expect(pending.ok).toBe(true);

      // Storage breaks AFTER the pending write already landed — the SAME
      // failure mode affects the heartbeat's own writeLease call.
      storage.setItem = () => {
        throw new Error("storage unavailable");
      };
      const confirmed = coordinator.writeMediaIntent(
        callId,
        lease!,
        { microphone: true, camera: false },
        "confirmed",
        { expectedRevision: (pending as { ok: true; revision: number }).revision },
      );
      expect(confirmed).toEqual({ ok: false, reason: "storage-error" });
      heartbeat();
      expect(lost).toHaveBeenCalledOnce();

      // A brand-new coordinator (another tab, or this one after storage
      // recovers) reclaims the callId — storage.setItem works again for
      // this fresh instance's own writes. loseOwnership()'s own cleanup
      // (storage.removeItem, which never throws even when setItem does)
      // already erased the crashed lease, so unlike a true hard crash
      // (browser closes, nothing runs, the lease just sits there until it
      // expires — see the "expired OwnerLease" predecessor test) there is
      // no OwnerLease left in storage for this claim to read as
      // existingForCall. The predecessor is therefore UNKNOWN here, not
      // "known but pending" — readMediaIntentForLease returns null, which
      // is exactly as safe: CallSessionProvider's own
      // `readMediaIntentForLease(...) ?? {microphone:false,camera:false}`
      // still degrades to OFF/OFF either way. The already-durable pending
      // entry from before the outage is never read as a stale ON/confirmed
      // value regardless of which of these two null-shaped outcomes it is.
      storage.setItem = SharedStorage.prototype.setItem.bind(storage);
      const recovering = createOwnershipCoordinator({
        storage,
        channel: new TestChannel(),
        tabId: "tab-b",
        now: () => now,
        settle: () => Promise.resolve(),
        setInterval: vi.fn(() => 1),
        clearInterval: vi.fn(),
        leaseMs: 100,
      });
      const nextLease = await recovering.claim(callId, "main");
      expect(nextLease?.epoch).toBeGreaterThan(lease!.epoch);
      expect(recovering.readMediaIntentForLease(callId)).toBeNull();
      coordinator.close();
      recovering.close();
    });
  });
});
