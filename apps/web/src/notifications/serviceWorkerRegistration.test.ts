import { afterEach, describe, expect, it, vi } from "vitest";

/**
 * The module memoises its registration for the life of the page, so every test
 * imports a fresh copy — that is the state under test, not an artefact.
 */
async function loadModule() {
  vi.resetModules();
  return import("./serviceWorkerRegistration");
}

function stubServiceWorkerContainer(register: ReturnType<typeof vi.fn>) {
  vi.stubGlobal("isSecureContext", true);
  vi.stubGlobal("PushManager", class {});
  Object.defineProperty(navigator, "serviceWorker", {
    configurable: true,
    value: { register },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  Reflect.deleteProperty(navigator, "serviceWorker");
});

describe("registerNotificationServiceWorker", () => {
  it("registers /sw.js at the root scope", async () => {
    const registration = { scope: "/" } as ServiceWorkerRegistration;
    const register = vi.fn().mockResolvedValue(registration);
    stubServiceWorkerContainer(register);

    const { registerNotificationServiceWorker } = await loadModule();

    await expect(registerNotificationServiceWorker()).resolves.toBe(registration);
    expect(register).toHaveBeenCalledWith("/sw.js", { scope: "/" });
  });

  it("is idempotent: repeated calls share one register() call", async () => {
    const register = vi.fn().mockResolvedValue({} as ServiceWorkerRegistration);
    stubServiceWorkerContainer(register);

    const { registerNotificationServiceWorker } = await loadModule();
    const first = registerNotificationServiceWorker();
    const second = registerNotificationServiceWorker();
    await Promise.all([first, second, registerNotificationServiceWorker()]);

    expect(first).toBe(second);
    expect(register).toHaveBeenCalledTimes(1);
  });

  it("does not re-register after the first attempt failed", async () => {
    const register = vi.fn().mockRejectedValue(new Error("refused"));
    stubServiceWorkerContainer(register);
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    const { registerNotificationServiceWorker } = await loadModule();

    await expect(registerNotificationServiceWorker()).resolves.toBeNull();
    await expect(registerNotificationServiceWorker()).resolves.toBeNull();
    expect(register).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("resolves null on an insecure origin without touching the API", async () => {
    const register = vi.fn();
    stubServiceWorkerContainer(register);
    vi.stubGlobal("isSecureContext", false);

    const { registerNotificationServiceWorker } = await loadModule();

    await expect(registerNotificationServiceWorker()).resolves.toBeNull();
    expect(register).not.toHaveBeenCalled();
  });

  it("resolves null when the browser has no Service Worker support", async () => {
    vi.stubGlobal("isSecureContext", true);
    vi.stubGlobal("PushManager", class {});

    const { registerNotificationServiceWorker } = await loadModule();

    await expect(registerNotificationServiceWorker()).resolves.toBeNull();
  });

  it("resolves null when the browser has no Push support", async () => {
    // jsdom implements no PushManager, so this is the real absent-API shape.
    const register = vi.fn();
    vi.stubGlobal("isSecureContext", true);
    Object.defineProperty(navigator, "serviceWorker", {
      configurable: true,
      value: { register },
    });

    const { registerNotificationServiceWorker } = await loadModule();

    await expect(registerNotificationServiceWorker()).resolves.toBeNull();
    expect(register).not.toHaveBeenCalled();
  });

  it("resolves null when reading the support flags throws", async () => {
    const register = vi.fn();
    vi.stubGlobal("PushManager", class {});
    Object.defineProperty(navigator, "serviceWorker", {
      configurable: true,
      value: { register },
    });
    vi.stubGlobal("isSecureContext", true);
    Object.defineProperty(globalThis, "isSecureContext", {
      configurable: true,
      get() {
        throw new Error("blocked by policy");
      },
    });

    const { registerNotificationServiceWorker } = await loadModule();

    await expect(registerNotificationServiceWorker()).resolves.toBeNull();
    expect(register).not.toHaveBeenCalled();
  });
});
