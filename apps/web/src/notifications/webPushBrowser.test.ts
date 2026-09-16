import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  getBrowserNotificationPermission,
  isBrowserNotificationSecureContext,
} from "../chat/browserNotification";
import { registerNotificationServiceWorker } from "./serviceWorkerRegistration";
import {
  _resetWebPushDeviceId,
  createLocalSubscription,
  getLocalSubscription,
  getPushRegistration,
  getWebPushDeviceId,
  readWebPushCredentials,
  readWebPushPermission,
  subscribedWithKey,
  webPushCapability,
} from "./webPushBrowser";

vi.mock("../chat/browserNotification");
vi.mock("./serviceWorkerRegistration");

const VAPID_KEY = "BExampleVapidPublicKeyBase64Url";

/** Captured before any test can replace the global with a throwing accessor. */
const realNavigator = navigator;

/** The shape a real PushSubscription hands back from toJSON(). */
function subscriptionJson(overrides: Partial<PushSubscriptionJSON> = {}): PushSubscription {
  return {
    endpoint: "https://push.example.com/s/abc",
    toJSON: () => ({
      endpoint: "https://push.example.com/s/abc",
      keys: { p256dh: "BP256", auth: "AUTH" },
      ...overrides,
    }),
  } as unknown as PushSubscription;
}

function supportPushApis(): void {
  vi.stubGlobal("Notification", class {});
  vi.stubGlobal("PushManager", class {});
  Object.defineProperty(navigator, "serviceWorker", { configurable: true, value: {} });
}

beforeEach(() => {
  vi.mocked(isBrowserNotificationSecureContext).mockReturnValue(true);
  vi.mocked(getBrowserNotificationPermission).mockReturnValue("granted");
  supportPushApis();
  localStorage.clear();
  _resetWebPushDeviceId();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  Reflect.deleteProperty(navigator, "serviceWorker");
});

describe("webPushCapability", () => {
  it("reports supported when the origin and the APIs are there", () => {
    expect(webPushCapability()).toBe("supported");
  });

  it("separates an insecure origin from an absent API", () => {
    vi.mocked(isBrowserNotificationSecureContext).mockReturnValue(false);
    expect(webPushCapability()).toBe("insecure_context");
  });

  it("reports unsupported without a Notification API", () => {
    vi.stubGlobal("Notification", undefined);
    Reflect.deleteProperty(globalThis, "Notification");
    expect(webPushCapability()).toBe("unsupported");
  });

  it("reports unsupported without a PushManager", () => {
    Reflect.deleteProperty(globalThis, "PushManager");
    expect(webPushCapability()).toBe("unsupported");
  });

  it("reports unsupported without a Service Worker container", () => {
    Reflect.deleteProperty(navigator, "serviceWorker");
    expect(webPushCapability()).toBe("unsupported");
  });

  it("reports unsupported when reading the APIs throws", () => {
    Object.defineProperty(globalThis, "navigator", {
      configurable: true,
      get() {
        throw new Error("blocked by policy");
      },
    });
    try {
      expect(webPushCapability()).toBe("unsupported");
    } finally {
      Object.defineProperty(globalThis, "navigator", {
        configurable: true,
        value: realNavigator,
      });
    }
  });
});

describe("readWebPushPermission", () => {
  it("reads the browser fresh through the one Notification adapter", () => {
    vi.mocked(getBrowserNotificationPermission).mockReturnValue("denied");
    expect(readWebPushPermission()).toBe("denied");
  });
});

describe("getWebPushDeviceId", () => {
  it("creates one, persists it and reuses it", () => {
    const first = getWebPushDeviceId();
    expect(first).toMatch(/^[A-Za-z0-9_.:-]{1,128}$/);
    expect(localStorage.getItem("nchat.notifications.push.deviceId")).toBe(first);

    _resetWebPushDeviceId();
    expect(getWebPushDeviceId()).toBe(first);
  });

  it("memoises so repeated calls do not re-read storage", () => {
    const first = getWebPushDeviceId();
    const getItem = vi.spyOn(Storage.prototype, "getItem");
    expect(getWebPushDeviceId()).toBe(first);
    expect(getItem).not.toHaveBeenCalled();
  });

  it("replaces a stored value that could not have come from here", () => {
    localStorage.setItem("nchat.notifications.push.deviceId", "not a device id!");
    const created = getWebPushDeviceId();
    expect(created).not.toBe("not a device id!");
    expect(created).toMatch(/^[A-Za-z0-9_.:-]{1,128}$/);
  });

  it("falls back to memory when storage refuses to be read", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const created = getWebPushDeviceId();
    expect(created).toMatch(/^[A-Za-z0-9_.:-]{1,128}$/);
    expect(getWebPushDeviceId()).toBe(created);
  });

  it("falls back to memory when storage refuses to be written", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    const created = getWebPushDeviceId();
    _resetWebPushDeviceId();
    expect(getWebPushDeviceId()).not.toBe(created);
  });
});

describe("registration and subscription", () => {
  it("delegates registration to the one registrar", async () => {
    const registration = {} as ServiceWorkerRegistration;
    vi.mocked(registerNotificationServiceWorker).mockResolvedValue(registration);
    await expect(getPushRegistration()).resolves.toBe(registration);
  });

  it("reads the subscription from the registration it was given", async () => {
    const subscription = subscriptionJson();
    const registration = {
      pushManager: { getSubscription: vi.fn().mockResolvedValue(subscription) },
    } as unknown as ServiceWorkerRegistration;
    await expect(getLocalSubscription(registration)).resolves.toBe(subscription);
  });

  it("subscribes user-visible only, with the configured application server key", async () => {
    const subscribe = vi.fn().mockResolvedValue(subscriptionJson());
    const registration = { pushManager: { subscribe } } as unknown as ServiceWorkerRegistration;
    await createLocalSubscription(registration, VAPID_KEY);
    expect(subscribe).toHaveBeenCalledWith({
      userVisibleOnly: true,
      applicationServerKey: VAPID_KEY,
    });
  });
});

describe("subscribedWithKey", () => {
  const key = Uint8Array.from([4, 250, 251, 252, 253, 254, 255]);
  const keyBase64Url = "BPr7_P3-_w";

  function subscribedWith(bytes: Uint8Array | null): PushSubscription {
    return { options: { applicationServerKey: bytes?.buffer ?? null } } as PushSubscription;
  }

  it("matches the key the subscription was minted with, in the url-safe alphabet", () => {
    expect(subscribedWithKey(subscribedWith(key), keyBase64Url)).toBe(true);
  });

  it("refuses a subscription minted for a different key", () => {
    const other = Uint8Array.from([4, 0, 0, 0, 0, 0, 0]);
    expect(subscribedWithKey(subscribedWith(key.slice(0, 6)), keyBase64Url)).toBe(false);
    expect(subscribedWithKey(subscribedWith(other), keyBase64Url)).toBe(false);
  });

  it("refuses when the configured key is not base64url at all", () => {
    expect(subscribedWithKey(subscribedWith(key), "***")).toBe(false);
  });

  it("gives a browser that does not expose the key the benefit of the doubt", () => {
    expect(subscribedWithKey(subscribedWith(null), keyBase64Url)).toBe(true);
    expect(subscribedWithKey({} as PushSubscription, keyBase64Url)).toBe(true);
  });
});

describe("readWebPushCredentials", () => {
  it("reads the endpoint and both keys", () => {
    expect(readWebPushCredentials(subscriptionJson())).toEqual({
      endpoint: "https://push.example.com/s/abc",
      p256dh: "BP256",
      auth: "AUTH",
    });
  });

  it("reports nothing rather than a half subscription", () => {
    expect(readWebPushCredentials(subscriptionJson({ keys: { p256dh: "BP256" } }))).toBeNull();
    expect(readWebPushCredentials(subscriptionJson({ keys: undefined }))).toBeNull();
    expect(readWebPushCredentials(subscriptionJson({ endpoint: undefined }))).toBeNull();
  });
});
