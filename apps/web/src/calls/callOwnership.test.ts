import { describe, expect, it, vi } from "vitest";

import {
  createOwnershipCoordinator,
  isLeaseExpired,
  parseOwnerLease,
  parseOwnershipMessage,
  resolveLeaseConflict,
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
    const request = vi.fn(async (_name: string, callback: () => Promise<OwnerLease | null>) =>
      callback(),
    );
    const coordinator = createOwnershipCoordinator({
      storage,
      channel,
      locks: { request },
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
});
