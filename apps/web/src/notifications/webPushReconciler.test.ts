import { act, renderHook } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { requestBrowserNotificationPermission } from "../chat/browserNotification";
import { ApiRequestError } from "../lib/api";
import { _resetListeners, clearTokens, setTokens } from "../lib/authSession";
import {
  deletePushSubscription,
  listPushSubscriptions,
  registerPushSubscription,
  type PushSubscriptionRecord,
} from "./pushSubscriptionApi";
import {
  createLocalSubscription,
  getLocalSubscription,
  getPushRegistration,
  getWebPushDeviceId,
  readWebPushCredentials,
  readWebPushPermission,
  webPushCapability,
} from "./webPushBrowser";
import {
  _resetWebPushState,
  disableWebPush,
  enableWebPush,
  getWebPushSnapshot,
  needsRepair,
  reconcileWebPush,
  startWebPushLifecycle,
  useWebPushHealth,
  type WebPushSnapshot,
} from "./webPushReconciler";

vi.mock("../chat/browserNotification");
vi.mock("./webPushBrowser");
vi.mock("./pushSubscriptionApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./pushSubscriptionApi")>()),
  listPushSubscriptions: vi.fn(),
  registerPushSubscription: vi.fn(),
  deletePushSubscription: vi.fn(),
}));

const DEVICE_ID = "device-a";
const ENDPOINT_A = "https://push.example.com/s/aaa";
const ENDPOINT_B = "https://push.example.com/s/bbb";

let clock = 1_700_000_000_000;

function advanceClock(ms: number): void {
  clock += ms;
}

function fakeSubscription(endpoint: string): PushSubscription {
  return { endpoint, unsubscribe: vi.fn().mockResolvedValue(true) } as unknown as PushSubscription;
}

function activeRegistration(): ServiceWorkerRegistration {
  return { active: {} } as unknown as ServiceWorkerRegistration;
}

function record(status: PushSubscriptionRecord["status"] = "active"): PushSubscriptionRecord {
  return { id: "sub-1", deviceId: DEVICE_ID, status };
}

/** The world as it is when everything works: granted, active worker, signed in. */
function healthyWorld(): void {
  vi.mocked(webPushCapability).mockReturnValue("supported");
  vi.mocked(readWebPushPermission).mockReturnValue("granted");
  vi.mocked(getWebPushDeviceId).mockReturnValue(DEVICE_ID);
  vi.mocked(getPushRegistration).mockResolvedValue(activeRegistration());
  vi.mocked(getLocalSubscription).mockResolvedValue(fakeSubscription(ENDPOINT_A));
  vi.mocked(createLocalSubscription).mockResolvedValue(fakeSubscription(ENDPOINT_B));
  vi.mocked(readWebPushCredentials).mockImplementation((subscription) => ({
    endpoint: subscription.endpoint,
    p256dh: "BP256",
    auth: "AUTH",
  }));
  vi.mocked(listPushSubscriptions).mockResolvedValue([record()]);
  vi.mocked(registerPushSubscription).mockResolvedValue(record());
  setTokens("access-token");
}

function expectAvailable(snapshot: WebPushSnapshot) {
  if (snapshot.status !== "available") {
    throw new Error(`expected an available snapshot, got ${snapshot.status}`);
  }
  return snapshot;
}

beforeEach(() => {
  vi.spyOn(Date, "now").mockImplementation(() => clock);
  healthyWorld();
});

afterEach(() => {
  _resetWebPushState();
  _resetListeners();
  sessionStorage.clear();
  vi.restoreAllMocks();
  vi.clearAllMocks();
  advanceClock(60_000);
});

describe("permission", () => {
  it("never asks for permission while it is default — not on reconcile", async () => {
    vi.mocked(readWebPushPermission).mockReturnValue("default");

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(requestBrowserNotificationPermission).not.toHaveBeenCalled();
    expect(snapshot.permission).toBe("default");
    expect(snapshot.health).toBe("reconnect_required");
    expect(createLocalSubscription).not.toHaveBeenCalled();
    expect(registerPushSubscription).not.toHaveBeenCalled();
  });

  it("never asks on mount, focus or visibilitychange", async () => {
    vi.mocked(readWebPushPermission).mockReturnValue("default");

    startWebPushLifecycle();
    await reconcileWebPush();
    advanceClock(5_000);
    window.dispatchEvent(new Event("focus"));
    await reconcileWebPush();
    advanceClock(5_000);
    document.dispatchEvent(new Event("visibilitychange"));
    await reconcileWebPush();

    expect(requestBrowserNotificationPermission).not.toHaveBeenCalled();
  });

  it("repairs once granted", async () => {
    vi.mocked(getLocalSubscription).mockResolvedValue(null);

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(createLocalSubscription).toHaveBeenCalledTimes(1);
    expect(snapshot.health).toBe("healthy");
    expect(requestBrowserNotificationPermission).not.toHaveBeenCalled();
  });

  it("enable does not re-prompt when permission is already granted", async () => {
    await enableWebPush();
    expect(requestBrowserNotificationPermission).not.toHaveBeenCalled();
  });

  it("enable prompts exactly once, and only from default", async () => {
    vi.mocked(readWebPushPermission).mockReturnValue("default");
    vi.mocked(requestBrowserNotificationPermission).mockImplementation(async () => {
      vi.mocked(readWebPushPermission).mockReturnValue("granted");
      return "granted";
    });
    vi.mocked(getLocalSubscription).mockResolvedValue(null);

    const snapshot = expectAvailable(await enableWebPush());

    expect(requestBrowserNotificationPermission).toHaveBeenCalledTimes(1);
    expect(snapshot.permission).toBe("granted");
    expect(snapshot.health).toBe("healthy");
  });

  it("enable never prompts when the browser cannot do push at all", async () => {
    vi.mocked(webPushCapability).mockReturnValue("insecure_context");
    await expect(enableWebPush()).resolves.toEqual({
      status: "unavailable",
      reason: "insecure_context",
    });
    expect(requestBrowserNotificationPermission).not.toHaveBeenCalled();
  });

  it("denied is informative: no prompt, no retry, no mutation", async () => {
    vi.mocked(readWebPushPermission).mockReturnValue("denied");

    const first = expectAvailable(await enableWebPush());
    advanceClock(5_000);
    const second = expectAvailable(await reconcileWebPush());

    expect(first.permission).toBe("denied");
    expect(first.health).toBe("reconnect_required");
    expect(second).toEqual(first);
    expect(requestBrowserNotificationPermission).not.toHaveBeenCalled();
    expect(createLocalSubscription).not.toHaveBeenCalled();
    expect(registerPushSubscription).not.toHaveBeenCalled();
    expect(listPushSubscriptions).not.toHaveBeenCalled();
  });

  it("picks up a permission granted outside the app on the next focus", async () => {
    vi.mocked(readWebPushPermission).mockReturnValue("default");
    vi.mocked(getLocalSubscription).mockResolvedValue(null);
    startWebPushLifecycle();
    await reconcileWebPush();
    expect(expectAvailable(getWebPushSnapshot()).permission).toBe("default");

    vi.mocked(readWebPushPermission).mockReturnValue("granted");
    advanceClock(5_000);
    window.dispatchEvent(new Event("focus"));
    await reconcileWebPush();

    const snapshot = expectAvailable(getWebPushSnapshot());
    expect(snapshot.permission).toBe("granted");
    expect(snapshot.health).toBe("healthy");
    expect(requestBrowserNotificationPermission).not.toHaveBeenCalled();
  });

  it("picks up a permission revoked outside the app on the next visibilitychange", async () => {
    startWebPushLifecycle();
    await reconcileWebPush();

    vi.mocked(readWebPushPermission).mockReturnValue("denied");
    advanceClock(5_000);
    document.dispatchEvent(new Event("visibilitychange"));
    await reconcileWebPush();

    expect(expectAvailable(getWebPushSnapshot()).permission).toBe("denied");
  });
});

describe("reconcile", () => {
  it("registers once per page load, then is a no-op while nothing changed", async () => {
    const first = expectAvailable(await reconcileWebPush());
    advanceClock(5_000);
    const second = expectAvailable(await reconcileWebPush());

    expect(registerPushSubscription).toHaveBeenCalledTimes(1);
    expect(createLocalSubscription).not.toHaveBeenCalled();
    expect(first).toEqual(second);
    expect(second.health).toBe("healthy");
    expect(second.backend).toBe("connected");
  });

  it("creates and registers a subscription that disappeared under a granted permission", async () => {
    vi.mocked(getLocalSubscription).mockResolvedValue(null);
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(createLocalSubscription).toHaveBeenCalledTimes(1);
    expect(registerPushSubscription).toHaveBeenCalledTimes(1);
    expect(registerPushSubscription).toHaveBeenCalledWith({
      deviceId: DEVICE_ID,
      endpoint: ENDPOINT_B,
      p256dh: "BP256",
      auth: "AUTH",
    });
    expect(snapshot.subscription).toBe("present");
    expect(snapshot.backend).toBe("connected");
  });

  it("converges a backend that retired this device while the browser is healthy", async () => {
    vi.mocked(listPushSubscriptions).mockResolvedValue([record("invalid")]);

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(createLocalSubscription).not.toHaveBeenCalled();
    expect(registerPushSubscription).toHaveBeenCalledWith({
      deviceId: DEVICE_ID,
      endpoint: ENDPOINT_A,
      p256dh: "BP256",
      auth: "AUTH",
    });
    expect(snapshot.backend).toBe("connected");
  });

  it("converges a backend that never heard of this device", async () => {
    vi.mocked(listPushSubscriptions).mockResolvedValue([
      { id: "sub-other", deviceId: "another-browser", status: "active" },
    ]);

    expect(expectAvailable(await reconcileWebPush()).backend).toBe("connected");
    expect(registerPushSubscription).toHaveBeenCalledTimes(1);
  });

  it("updates the backend when the browser rotates the subscription", async () => {
    await reconcileWebPush();
    vi.mocked(getLocalSubscription).mockResolvedValue(fakeSubscription(ENDPOINT_B));
    advanceClock(5_000);

    await reconcileWebPush();

    expect(registerPushSubscription).toHaveBeenCalledTimes(2);
    expect(vi.mocked(registerPushSubscription).mock.calls[1][0].endpoint).toBe(ENDPOINT_B);
    expect(createLocalSubscription).not.toHaveBeenCalled();
  });

  it("diagnoses the subscription of the registration it was actually handed", async () => {
    const updated = activeRegistration();
    vi.mocked(getPushRegistration).mockResolvedValue(updated);
    await reconcileWebPush();
    expect(getLocalSubscription).toHaveBeenCalledWith(updated);
  });

  it("two concurrent reconciles are one pass, one subscribe and one registration", async () => {
    vi.mocked(getLocalSubscription).mockResolvedValue(null);
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);

    const [first, second] = await Promise.all([reconcileWebPush(), reconcileWebPush()]);

    expect(createLocalSubscription).toHaveBeenCalledTimes(1);
    expect(registerPushSubscription).toHaveBeenCalledTimes(1);
    expect(listPushSubscriptions).toHaveBeenCalledTimes(1);
    expect(first).toEqual(second);
  });

  it("recovers from an endpoint another instance already owns, exactly once", async () => {
    const stale = fakeSubscription(ENDPOINT_A);
    vi.mocked(getLocalSubscription).mockResolvedValue(stale);
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);
    vi.mocked(registerPushSubscription)
      .mockRejectedValueOnce(new ApiRequestError(409, "push_endpoint_conflict", "taken"))
      .mockResolvedValueOnce(record());

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(stale.unsubscribe).toHaveBeenCalledTimes(1);
    expect(createLocalSubscription).toHaveBeenCalledTimes(1);
    expect(registerPushSubscription).toHaveBeenCalledTimes(2);
    expect(vi.mocked(registerPushSubscription).mock.calls[1][0].endpoint).toBe(ENDPOINT_B);
    expect(snapshot.health).toBe("healthy");
  });

  it("does not retry a second conflict", async () => {
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);
    vi.mocked(registerPushSubscription).mockRejectedValue(
      new ApiRequestError(409, "push_endpoint_conflict", "taken"),
    );

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(registerPushSubscription).toHaveBeenCalledTimes(2);
    expect(snapshot.health).toBe("error");
    expect(snapshot.error).toBe("backend");
  });

  it("reports a backend outage as degraded, and resolves rather than rejecting", async () => {
    vi.mocked(listPushSubscriptions).mockRejectedValue(
      new ApiRequestError(0, "network_error", "Network error"),
    );

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(snapshot.health).toBe("error");
    expect(snapshot.error).toBe("backend");
    expect(snapshot.backend).toBe("unavailable");
    expect(createLocalSubscription).not.toHaveBeenCalled();
  });

  it("does not poll to recover from a backend outage", async () => {
    const setInterval = vi.spyOn(window, "setInterval");
    const setTimeout = vi.spyOn(window, "setTimeout");
    vi.mocked(listPushSubscriptions).mockRejectedValue(
      new ApiRequestError(503, "unavailable", "down"),
    );

    startWebPushLifecycle();
    await reconcileWebPush();

    expect(setInterval).not.toHaveBeenCalled();
    expect(setTimeout).not.toHaveBeenCalled();
    expect(listPushSubscriptions).toHaveBeenCalledTimes(1);
  });

  it("does nothing at all while nobody is signed in", async () => {
    clearTokens();

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(listPushSubscriptions).not.toHaveBeenCalled();
    expect(registerPushSubscription).not.toHaveBeenCalled();
    expect(snapshot.health).toBe("reconnect_required");
    expect(snapshot.backend).toBe("unknown");
  });

  it("does not let a pass answer for the session that replaced it", async () => {
    vi.mocked(listPushSubscriptions).mockImplementation(async () => {
      clearTokens();
      return [record()];
    });

    const computed = await reconcileWebPush();

    // The pass produces no snapshot of its own: it hands back whatever the
    // current session holds, and publishes nothing.
    expect(computed).toBe(getWebPushSnapshot());
    expect(expectAvailable(getWebPushSnapshot()).health).toBe("reconciling");
  });
});

/**
 * The session a pass started under can end in any gap between two of its
 * awaits. Everything it does after that gap would be done on behalf of an
 * identity that no longer exists, so these pin the session change to one exact
 * await each, proving the guard sits at every effect and not only at the
 * snapshot the pass publishes.
 */
describe("a pass that outlives its session", () => {
  it("stops at the backend read: no subscribe, no registration, no snapshot", async () => {
    // Session A has no subscription and an empty backend, so an uninterrupted
    // pass would certainly subscribe and register.
    vi.mocked(getLocalSubscription).mockResolvedValue(null);
    vi.mocked(listPushSubscriptions).mockImplementation(async () => {
      setTokens("session-b");
      return [];
    });

    const stale = await reconcileWebPush();

    expect(createLocalSubscription).not.toHaveBeenCalled();
    expect(registerPushSubscription).not.toHaveBeenCalled();
    expect(stale).toBe(getWebPushSnapshot());
    expect(expectAvailable(getWebPushSnapshot()).health).toBe("reconciling");
  });

  it("stops between subscribing and registering", async () => {
    vi.mocked(getLocalSubscription).mockResolvedValue(null);
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);
    vi.mocked(createLocalSubscription).mockImplementation(async () => {
      setTokens("session-b");
      return fakeSubscription(ENDPOINT_B);
    });

    await reconcileWebPush();

    expect(createLocalSubscription).toHaveBeenCalledTimes(1);
    expect(registerPushSubscription).not.toHaveBeenCalled();
  });

  it("does not restore the endpoint the session change invalidated", async () => {
    // A's registration lands, and the session changes while it is in flight.
    // The write that would tell a later pass "the backend already has these
    // bytes" belongs to A, and B must not inherit it.
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);
    vi.mocked(registerPushSubscription).mockImplementationOnce(async () => {
      setTokens("session-b");
      return record();
    });

    await reconcileWebPush();
    expect(registerPushSubscription).toHaveBeenCalledTimes(1);

    // B now reconciles for itself. Its browser holds the same endpoint and its
    // backend row reads active, so the only thing that could make B skip the
    // re-registration is A's write — and B must not skip it.
    vi.mocked(registerPushSubscription).mockResolvedValue(record());
    vi.mocked(listPushSubscriptions).mockResolvedValue([record()]);

    const fresh = expectAvailable(await reconcileWebPush());

    expect(registerPushSubscription).toHaveBeenCalledTimes(2);
    expect(vi.mocked(registerPushSubscription).mock.calls[1][0].endpoint).toBe(ENDPOINT_A);
    expect(createLocalSubscription).not.toHaveBeenCalled();
    expect(fresh.health).toBe("healthy");
  });

  it("lets the new session run its own pass instead of coalescing onto the stale one", async () => {
    vi.mocked(getLocalSubscription).mockResolvedValue(null);
    let releaseListing!: (records: PushSubscriptionRecord[]) => void;
    const listing = new Promise<PushSubscriptionRecord[]>((resolve) => {
      releaseListing = resolve;
    });
    vi.mocked(listPushSubscriptions).mockReturnValueOnce(listing).mockResolvedValue([]);

    const stale = reconcileWebPush();
    setTokens("session-b");
    const fresh = reconcileWebPush();

    expect(fresh).not.toBe(stale);

    releaseListing([]);
    const [, freshSnapshot] = await Promise.all([stale, fresh]);

    // B did the repair A was forbidden to do, and exactly once.
    expect(createLocalSubscription).toHaveBeenCalledTimes(1);
    expect(registerPushSubscription).toHaveBeenCalledTimes(1);
    expect(expectAvailable(freshSnapshot).health).toBe("healthy");
  });

  it("still refuses to publish a pass that mutated nothing", async () => {
    // Nothing on this path repairs anything, so no effect guard is reached —
    // the commit guard is what keeps the previous session's reading out of the
    // store, and it is still the last line of defence.
    vi.mocked(getPushRegistration).mockImplementation(async () => {
      setTokens("session-b");
      return null;
    });

    const computed = expectAvailable(await reconcileWebPush());

    expect(computed.worker).toBe("failed");
    expect(expectAvailable(getWebPushSnapshot()).health).toBe("reconciling");
    expect(getWebPushSnapshot()).not.toBe(computed);
  });

  it("does not disable a subscription on behalf of the session that left", async () => {
    const subscription = fakeSubscription(ENDPOINT_A);
    vi.mocked(getLocalSubscription).mockImplementationOnce(async () => {
      setTokens("session-b");
      return subscription;
    });

    await disableWebPush();

    expect(subscription.unsubscribe).not.toHaveBeenCalled();
    expect(deletePushSubscription).not.toHaveBeenCalled();
  });
});

/**
 * There is one PushManager per browser, not one per session. Two passes that
 * legitimately belong to different sessions are still two callers of the same
 * object, and a session guard cannot arbitrate that: it can only tell a pass it
 * went stale once the `subscribe()` it already started comes back.
 */
describe("one PushManager, many sessions", () => {
  /** Lets every already-resolved await chain advance; no timers are involved. */
  async function settleMicrotasks(): Promise<void> {
    for (let index = 0; index < 20; index += 1) await Promise.resolve();
  }

  it("serializes creation across sessions, and the second pass reuses what the first made", async () => {
    // One browser, one subscription slot: the mocks model the PushManager as
    // the single piece of state it really is.
    let browserSubscription: PushSubscription | null = null;
    vi.mocked(getLocalSubscription).mockImplementation(async () => browserSubscription);
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);

    let completeSubscribe!: () => void;
    vi.mocked(createLocalSubscription).mockImplementation(
      () =>
        new Promise<PushSubscription>((resolve) => {
          completeSubscribe = () => {
            browserSubscription = fakeSubscription(ENDPOINT_B);
            resolve(browserSubscription);
          };
        }),
    );

    // Session A diagnoses "absent" and reaches subscribe(), which hangs.
    const staleA = reconcileWebPush();
    await settleMicrotasks();
    expect(createLocalSubscription).toHaveBeenCalledTimes(1);

    // The session changes and B starts a pass of its own. It diagnoses "absent"
    // too — the subscription A is minting does not exist yet.
    setTokens("session-b");
    const freshB = reconcileWebPush();
    await settleMicrotasks();

    expect(freshB).not.toBe(staleA);
    // B has finished diagnosing and is waiting on the browser, not calling into
    // it: this is the assertion the bug used to fail.
    expect(createLocalSubscription).toHaveBeenCalledTimes(1);
    expect(registerPushSubscription).not.toHaveBeenCalled();

    completeSubscribe();
    const [snapshotA, snapshotB] = await Promise.all([staleA, freshB]);

    // One logical subscribe for the whole browser, not one per session.
    expect(createLocalSubscription).toHaveBeenCalledTimes(1);

    // A lost its session mid-flight: it registered nothing and published
    // nothing, so the single POST is B's, for the subscription A had minted.
    expect(registerPushSubscription).toHaveBeenCalledTimes(1);
    expect(vi.mocked(registerPushSubscription).mock.calls[0][0].endpoint).toBe(ENDPOINT_B);
    expect(expectAvailable(snapshotB).health).toBe("healthy");
    expect(expectAvailable(snapshotB).subscription).toBe("present");
    expect(getWebPushSnapshot()).toBe(snapshotB);
    expect(snapshotA).not.toBe(snapshotB);

    // And the endpoint B recorded is B's own: a further pass of B is the no-op
    // it should be, which it could not be if A had written the marker.
    vi.mocked(listPushSubscriptions).mockResolvedValue([record()]);
    const settledB = expectAvailable(await reconcileWebPush());

    expect(registerPushSubscription).toHaveBeenCalledTimes(1);
    expect(settledB.health).toBe("healthy");
  });

  it("a rejected browser mutation orders the next one instead of poisoning it", async () => {
    vi.mocked(getLocalSubscription).mockResolvedValue(null);
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);
    vi.mocked(createLocalSubscription).mockRejectedValueOnce(new Error("permission gone"));

    const failed = expectAvailable(await reconcileWebPush());

    expect(failed.health).toBe("error");
    expect(failed.error).toBe("browser");

    // The queue waits for the previous operation to *settle*, never for it to
    // succeed, so the pass behind a rejection still runs.
    vi.mocked(createLocalSubscription).mockResolvedValue(fakeSubscription(ENDPOINT_B));
    const recovered = expectAvailable(await reconcileWebPush());

    expect(createLocalSubscription).toHaveBeenCalledTimes(2);
    expect(recovered.health).toBe("healthy");
  });

  it("keeps the 409 recovery's cancel-and-mint indivisible", async () => {
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);
    vi.mocked(registerPushSubscription)
      .mockRejectedValueOnce(new ApiRequestError(409, "push_endpoint_conflict", "taken"))
      .mockResolvedValueOnce(record());

    const snapshot = expectAvailable(await reconcileWebPush());

    // Still exactly one replacement minted, and it went through the same queue
    // as every other mutation of this PushManager.
    expect(createLocalSubscription).toHaveBeenCalledTimes(1);
    expect(registerPushSubscription).toHaveBeenCalledTimes(2);
    expect(snapshot.health).toBe("healthy");
  });
});

describe("browser states", () => {
  it.each([["unsupported" as const], ["insecure_context" as const], ["not_configured" as const]])(
    "reports %s without touching the network",
    async (reason) => {
      vi.mocked(webPushCapability).mockReturnValue(reason);

      await expect(reconcileWebPush()).resolves.toEqual({ status: "unavailable", reason });
      expect(getPushRegistration).not.toHaveBeenCalled();
      expect(listPushSubscriptions).not.toHaveBeenCalled();
    },
  );

  it("reports unsupported when the Notification API itself cannot be read", async () => {
    vi.mocked(readWebPushPermission).mockReturnValue("unsupported");
    await expect(reconcileWebPush()).resolves.toEqual({
      status: "unavailable",
      reason: "unsupported",
    });
  });

  it("reports a failed worker when registration did not happen", async () => {
    vi.mocked(getPushRegistration).mockResolvedValue(null);

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(snapshot.worker).toBe("failed");
    expect(snapshot.health).toBe("reconnect_required");
    expect(listPushSubscriptions).not.toHaveBeenCalled();
  });

  it("waits for a worker that has registered but not activated, without a timer", async () => {
    vi.mocked(getPushRegistration).mockResolvedValue({
      active: null,
    } as unknown as ServiceWorkerRegistration);
    const setTimeout = vi.spyOn(window, "setTimeout");

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(snapshot.worker).toBe("registering");
    expect(setTimeout).not.toHaveBeenCalled();
    expect(createLocalSubscription).not.toHaveBeenCalled();
  });

  it("reports a rejected subscribe as a browser failure", async () => {
    vi.mocked(getLocalSubscription).mockResolvedValue(null);
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);
    vi.mocked(createLocalSubscription).mockRejectedValue(new Error("permission gone"));

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(snapshot.health).toBe("error");
    expect(snapshot.error).toBe("browser");
    expect(snapshot.backend).toBe("disconnected");
    expect(registerPushSubscription).not.toHaveBeenCalled();
  });

  it("reports a rejected getSubscription as a browser failure", async () => {
    vi.mocked(getLocalSubscription).mockRejectedValue(new Error("storage gone"));

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(snapshot.health).toBe("error");
    expect(snapshot.error).toBe("browser");
    expect(snapshot.subscription).toBe("absent");
  });

  it("refuses to register a subscription with no keys", async () => {
    vi.mocked(readWebPushCredentials).mockReturnValue(null);
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);

    const snapshot = expectAvailable(await reconcileWebPush());

    expect(registerPushSubscription).not.toHaveBeenCalled();
    expect(snapshot.error).toBe("browser");
  });
});

describe("lifecycle", () => {
  it("adds exactly one set of listeners, and a second start does not add more", () => {
    const onWindow = vi.spyOn(window, "addEventListener");
    const onDocument = vi.spyOn(document, "addEventListener");

    const stop = startWebPushLifecycle();
    expect(startWebPushLifecycle()).toBe(stop);

    expect(onWindow.mock.calls.filter(([type]) => type === "focus")).toHaveLength(1);
    expect(onDocument.mock.calls.filter(([type]) => type === "visibilitychange")).toHaveLength(1);
  });

  it("removes what it added, and a remount does not accumulate", () => {
    const offWindow = vi.spyOn(window, "removeEventListener");
    const offDocument = vi.spyOn(document, "removeEventListener");

    startWebPushLifecycle()();
    const secondStop = startWebPushLifecycle();
    secondStop();

    expect(offWindow.mock.calls.filter(([type]) => type === "focus")).toHaveLength(2);
    expect(offDocument.mock.calls.filter(([type]) => type === "visibilitychange")).toHaveLength(2);
  });

  it("stops reconciling once its listeners are gone", async () => {
    startWebPushLifecycle()();
    await reconcileWebPush();
    vi.mocked(listPushSubscriptions).mockClear();

    advanceClock(5_000);
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    await Promise.resolve();

    expect(listPushSubscriptions).not.toHaveBeenCalled();
  });

  it("coalesces a focus and a visibilitychange that arrive together", async () => {
    startWebPushLifecycle();
    await reconcileWebPush();
    vi.mocked(listPushSubscriptions).mockClear();

    advanceClock(5_000);
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    await reconcileWebPush();

    expect(listPushSubscriptions).toHaveBeenCalledTimes(1);
  });

  it("ignores a tab going away", async () => {
    startWebPushLifecycle();
    await reconcileWebPush();
    vi.mocked(listPushSubscriptions).mockClear();
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");

    advanceClock(5_000);
    document.dispatchEvent(new Event("visibilitychange"));
    await Promise.resolve();

    expect(listPushSubscriptions).not.toHaveBeenCalled();
  });

  it("never schedules anything: no interval, no timeout, no second pass", async () => {
    const setInterval = vi.spyOn(window, "setInterval");
    const setTimeout = vi.spyOn(window, "setTimeout");

    startWebPushLifecycle();
    await reconcileWebPush();

    expect(setInterval).not.toHaveBeenCalled();
    expect(setTimeout).not.toHaveBeenCalled();
    expect(listPushSubscriptions).toHaveBeenCalledTimes(1);
  });

  it("reconciles a session change immediately rather than coalescing it away", async () => {
    clearTokens();
    startWebPushLifecycle();
    await reconcileWebPush();
    expect(listPushSubscriptions).not.toHaveBeenCalled();

    setTokens("a-new-session");
    await reconcileWebPush();

    expect(listPushSubscriptions).toHaveBeenCalledTimes(1);
  });
});

describe("public API", () => {
  it("never exposes an endpoint or a key to its consumers", async () => {
    const snapshot = await reconcileWebPush();
    const serialised = JSON.stringify(snapshot);

    expect(serialised).not.toContain(ENDPOINT_A);
    expect(serialised).not.toContain("push.example.com");
    expect(serialised).not.toContain("BP256");
    expect(serialised).not.toContain("AUTH");
    expect(Object.keys(snapshot)).toEqual([
      "status",
      "permission",
      "worker",
      "subscription",
      "backend",
      "health",
      "error",
    ]);
  });

  it("starts from a snapshot that admits it has not settled yet", () => {
    const snapshot = expectAvailable(getWebPushSnapshot());
    expect(snapshot.health).toBe("reconciling");
    expect(getWebPushSnapshot()).toBe(snapshot);
  });

  it("starts unavailable when the browser was never going to work", () => {
    vi.mocked(webPushCapability).mockReturnValue("unsupported");
    expect(getWebPushSnapshot()).toEqual({ status: "unavailable", reason: "unsupported" });
  });

  it("starts unavailable when the Notification API cannot be read", () => {
    vi.mocked(readWebPushPermission).mockReturnValue("unsupported");
    expect(getWebPushSnapshot()).toEqual({ status: "unavailable", reason: "unsupported" });
  });

  it("disable cancels the browser subscription and the backend row", async () => {
    const subscription = fakeSubscription(ENDPOINT_A);
    vi.mocked(getLocalSubscription).mockResolvedValueOnce(subscription).mockResolvedValue(null);
    // #745 keeps the row and marks it disabled rather than deleting it.
    vi.mocked(listPushSubscriptions)
      .mockResolvedValueOnce([record()])
      .mockResolvedValue([record("disabled")]);

    const snapshot = expectAvailable(await disableWebPush());

    expect(subscription.unsubscribe).toHaveBeenCalledTimes(1);
    expect(deletePushSubscription).toHaveBeenCalledWith("sub-1");
    expect(snapshot.subscription).toBe("absent");
    expect(snapshot.backend).toBe("disconnected");
    expect(snapshot.health).toBe("reconnect_required");
    expect(createLocalSubscription).not.toHaveBeenCalled();
  });

  it("a device its owner switched off is not repaired by the next focus", async () => {
    vi.mocked(getLocalSubscription).mockResolvedValue(null);
    vi.mocked(listPushSubscriptions).mockResolvedValue([record("disabled")]);

    startWebPushLifecycle();
    const snapshot = expectAvailable(await reconcileWebPush());
    advanceClock(5_000);
    window.dispatchEvent(new Event("focus"));
    await reconcileWebPush();

    expect(createLocalSubscription).not.toHaveBeenCalled();
    expect(registerPushSubscription).not.toHaveBeenCalled();
    expect(snapshot.health).toBe("reconnect_required");
  });

  it("enable reconnects a device its owner had switched off", async () => {
    vi.mocked(getLocalSubscription).mockResolvedValue(null);
    vi.mocked(listPushSubscriptions).mockResolvedValue([record("disabled")]);

    const snapshot = expectAvailable(await enableWebPush());

    expect(requestBrowserNotificationPermission).not.toHaveBeenCalled();
    expect(createLocalSubscription).toHaveBeenCalledTimes(1);
    expect(registerPushSubscription).toHaveBeenCalledTimes(1);
    expect(snapshot.health).toBe("healthy");
  });

  it("disable surfaces a backend failure without leaving the app broken", async () => {
    vi.mocked(deletePushSubscription).mockRejectedValue(
      new ApiRequestError(500, "internal", "boom"),
    );

    const snapshot = expectAvailable(await disableWebPush());

    expect(snapshot.health).toBe("error");
    expect(snapshot.error).toBe("backend");
  });

  it("disable does not call the backend for an anonymous page", async () => {
    clearTokens();
    await disableWebPush();
    expect(deletePushSubscription).not.toHaveBeenCalled();
  });

  it("disable tolerates a browser that has no registration", async () => {
    vi.mocked(getPushRegistration).mockResolvedValue(null);
    vi.mocked(listPushSubscriptions).mockResolvedValue([]);
    await expect(disableWebPush()).resolves.toBeDefined();
    expect(deletePushSubscription).not.toHaveBeenCalled();
  });

  it("needsRepair is the whole decision, and it is pure", () => {
    expect(needsRepair(null, record(), ENDPOINT_A)).toBe(true);
    expect(needsRepair(ENDPOINT_A, null, ENDPOINT_A)).toBe(true);
    expect(needsRepair(ENDPOINT_A, record("invalid"), ENDPOINT_A)).toBe(true);
    // The owner switched it off; nothing automatic turns it back on.
    expect(needsRepair(ENDPOINT_A, record("disabled"), ENDPOINT_A)).toBe(false);
    expect(needsRepair(null, record("disabled"), null)).toBe(false);
    expect(needsRepair(ENDPOINT_A, record(), null)).toBe(true);
    expect(needsRepair(ENDPOINT_B, record(), ENDPOINT_A)).toBe(true);
    expect(needsRepair(ENDPOINT_A, record(), ENDPOINT_A)).toBe(false);
  });
});

describe("useWebPushHealth", () => {
  it("publishes the settled snapshot to its subscribers", async () => {
    const { result } = renderHook(() => useWebPushHealth());
    expect(expectAvailable(result.current).health).toBe("reconciling");

    await act(async () => {
      await reconcileWebPush();
    });

    expect(expectAvailable(result.current).health).toBe("healthy");
  });

  it("a StrictMode double mount is still one pass", async () => {
    renderHook(() => useWebPushHealth(), { wrapper: StrictMode });
    await act(async () => {
      await reconcileWebPush();
    });

    expect(listPushSubscriptions).toHaveBeenCalledTimes(1);
  });

  it("unsubscribes on unmount", async () => {
    const { unmount } = renderHook(() => useWebPushHealth());
    await act(async () => {
      await reconcileWebPush();
    });
    unmount();

    advanceClock(5_000);
    await act(async () => {
      await reconcileWebPush();
    });
    expect(listPushSubscriptions).toHaveBeenCalledTimes(2);
  });
});
