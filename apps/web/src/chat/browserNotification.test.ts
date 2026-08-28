import { afterEach, describe, expect, it, vi } from "vitest";

import {
  getBrowserNotificationPermission,
  isBrowserNotificationSecureContext,
  requestBrowserNotificationPermission,
  showBrowserMessageNotification,
} from "./browserNotification";

/** jsdom does not implement Notification at all — stub it like IntersectionObserver. */
class MockNotification {
  static permission: NotificationPermission = "default";
  static requestPermission = vi.fn<() => Promise<NotificationPermission>>();
  static instances: MockNotification[] = [];

  onclick: (() => void) | null = null;
  close = vi.fn();
  title: string;
  options?: NotificationOptions;

  constructor(title: string, options?: NotificationOptions) {
    this.title = title;
    this.options = options;
    MockNotification.instances.push(this);
  }
}

function stubNotification(permission: NotificationPermission = "default", secureContext = true) {
  MockNotification.permission = permission;
  MockNotification.requestPermission = vi.fn<() => Promise<NotificationPermission>>();
  MockNotification.instances = [];
  vi.stubGlobal("isSecureContext", secureContext);
  vi.stubGlobal("Notification", MockNotification);
  return MockNotification;
}

describe("browserNotification", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  describe("getBrowserNotificationPermission", () => {
    it("reports 'unsupported' when window.Notification does not exist (secure context)", () => {
      vi.unstubAllGlobals();
      vi.stubGlobal("isSecureContext", true);
      expect(getBrowserNotificationPermission()).toBe("unsupported");
    });

    it("reports 'unsupported' when reading .permission throws (secure context)", () => {
      vi.stubGlobal("isSecureContext", true);
      class ThrowingNotification {
        static get permission(): NotificationPermission {
          throw new Error("blocked by policy");
        }
      }
      vi.stubGlobal("Notification", ThrowingNotification);
      expect(getBrowserNotificationPermission()).toBe("unsupported");
    });

    it.each(["default", "granted", "denied"] as const)(
      "reflects a '%s' permission from the API in a secure context",
      (permission) => {
        stubNotification(permission, true);
        expect(getBrowserNotificationPermission()).toBe(permission);
      },
    );

    it("reports 'unsupported' — not 'denied' — when the origin is insecure, even if the browser reports 'denied'", () => {
      // Reproduces the confirmed Firefox report: http://nchat.local:8080
      // (isSecureContext === false) has Notification.permission === "denied"
      // even though the site was never actually denied by the user — it's the
      // origin the browser is refusing, not a permission choice to reverse.
      stubNotification("denied", false);
      expect(getBrowserNotificationPermission()).toBe("unsupported");
    });

    it("reports 'unsupported' for an insecure origin even when the browser reports 'granted'", () => {
      stubNotification("granted", false);
      expect(getBrowserNotificationPermission()).toBe("unsupported");
    });

    it("checks the secure-context origin before even asking whether the API exists", () => {
      vi.unstubAllGlobals();
      vi.stubGlobal("isSecureContext", false);
      // No Notification global stubbed at all — still "unsupported", for the
      // origin reason, not a missing-API reason (isBrowserNotificationSecureContext
      // below is what the UI uses to tell the two apart).
      expect(getBrowserNotificationPermission()).toBe("unsupported");
    });
  });

  describe("isBrowserNotificationSecureContext", () => {
    it("reflects window.isSecureContext", () => {
      vi.stubGlobal("isSecureContext", true);
      expect(isBrowserNotificationSecureContext()).toBe(true);

      vi.stubGlobal("isSecureContext", false);
      expect(isBrowserNotificationSecureContext()).toBe(false);
    });
  });

  describe("requestBrowserNotificationPermission", () => {
    it("calls Notification.requestPermission() and returns its result", async () => {
      const mock = stubNotification("default");
      mock.requestPermission.mockResolvedValue("granted");

      await expect(requestBrowserNotificationPermission()).resolves.toBe("granted");
      expect(mock.requestPermission).toHaveBeenCalledTimes(1);
    });

    it("falls back to the real current permission when requestPermission rejects", async () => {
      const mock = stubNotification("denied");
      mock.requestPermission.mockRejectedValue(new Error("dismissed"));

      await expect(requestBrowserNotificationPermission()).resolves.toBe("denied");
    });

    it("falls back to the real current permission when requestPermission throws synchronously", async () => {
      const mock = stubNotification("default");
      mock.requestPermission.mockImplementation(() => {
        throw new Error("synchronous failure");
      });

      await expect(requestBrowserNotificationPermission()).resolves.toBe("default");
    });

    it("reports 'unsupported' without throwing when the API is absent (secure context)", async () => {
      vi.unstubAllGlobals();
      vi.stubGlobal("isSecureContext", true);
      await expect(requestBrowserNotificationPermission()).resolves.toBe("unsupported");
    });

    it("never calls Notification.requestPermission() on an insecure origin, and resolves 'unsupported'", async () => {
      const mock = stubNotification("denied", false);

      await expect(requestBrowserNotificationPermission()).resolves.toBe("unsupported");
      expect(mock.requestPermission).not.toHaveBeenCalled();
    });
  });

  describe("showBrowserMessageNotification", () => {
    const baseInput = {
      targetKind: "channel" as const,
      targetId: "11111111-1111-4111-8111-111111111111",
      senderDisplayName: "Ana",
      bodyText: "Olá time",
      onNavigate: vi.fn(),
    };

    it.each(["default", "denied"] as const)(
      "does not attempt to construct a notification when permission is '%s'",
      (permission) => {
        stubNotification(permission);
        const result = showBrowserMessageNotification({ ...baseInput, onNavigate: vi.fn() });
        expect(result).toEqual({ shown: false });
        expect(MockNotification.instances).toHaveLength(0);
      },
    );

    it("reports {shown:false} without throwing when the API is unsupported", () => {
      vi.unstubAllGlobals();
      expect(() =>
        showBrowserMessageNotification({ ...baseInput, onNavigate: vi.fn() }),
      ).not.toThrow();
      expect(showBrowserMessageNotification({ ...baseInput, onNavigate: vi.fn() })).toEqual({
        shown: false,
      });
    });

    it("constructs a notification with title, body, stable tag and the official icon when granted", () => {
      stubNotification("granted");
      const result = showBrowserMessageNotification({ ...baseInput, onNavigate: vi.fn() });

      expect(result).toEqual({ shown: true });
      expect(MockNotification.instances).toHaveLength(1);
      const [created] = MockNotification.instances;
      expect(created.title).toBe("Ana");
      expect(created.options).toMatchObject({
        body: "Olá time",
        tag: `nchat-message-channel-${baseInput.targetId}`,
        icon: "/assets/nic-labs-icon.png",
      });
    });

    it("falls back to a generic title when the sender has no display name", () => {
      stubNotification("granted");
      showBrowserMessageNotification({ ...baseInput, senderDisplayName: "", onNavigate: vi.fn() });

      expect(MockNotification.instances[0]?.title).toBe("Nova mensagem");
    });

    it("replaces a mention token with its readable label in the preview, not raw markup", () => {
      stubNotification("granted");
      showBrowserMessageNotification({
        ...baseInput,
        bodyText: "oi @[Você](mention:user:00000000-0000-4000-8000-0000000000f1) bora?",
        onNavigate: vi.fn(),
      });

      expect(MockNotification.instances[0]?.options?.body).toBe("oi @Você bora?");
    });

    it("leaves '#channel' text untouched — never treated as a person mention", () => {
      stubNotification("granted");
      showBrowserMessageNotification({
        ...baseInput,
        bodyText: "confere o #geral por favor",
        onNavigate: vi.fn(),
      });

      expect(MockNotification.instances[0]?.options?.body).toBe("confere o #geral por favor");
    });

    it("truncates a long body to a bounded plain-text preview", () => {
      stubNotification("granted");
      const longBody = "a".repeat(300);
      showBrowserMessageNotification({ ...baseInput, bodyText: longBody, onNavigate: vi.fn() });

      const body = MockNotification.instances[0]?.options?.body as string;
      expect(body.length).toBeLessThan(150);
      expect(body).toMatch(/…$/);
    });

    it("reports {shown:false} without throwing when the Notification constructor throws", () => {
      class ThrowingCtor {
        static permission: NotificationPermission = "granted";
        static requestPermission = vi.fn();
        constructor() {
          throw new Error("blocked");
        }
      }
      vi.stubGlobal("isSecureContext", true);
      vi.stubGlobal("Notification", ThrowingCtor);

      expect(() =>
        showBrowserMessageNotification({ ...baseInput, onNavigate: vi.fn() }),
      ).not.toThrow();
      expect(showBrowserMessageNotification({ ...baseInput, onNavigate: vi.fn() })).toEqual({
        shown: false,
      });
    });

    it("on click: focuses the window, closes the notification and navigates to the DM/channel route", () => {
      stubNotification("granted");
      const focusSpy = vi.spyOn(window, "focus").mockImplementation(() => {});
      const onNavigate = vi.fn();
      showBrowserMessageNotification({ ...baseInput, targetKind: "dm", onNavigate });

      const [created] = MockNotification.instances;
      expect(focusSpy).not.toHaveBeenCalled(); // not yet — only on click.

      created.onclick?.();

      expect(focusSpy).toHaveBeenCalledTimes(1);
      expect(created.close).toHaveBeenCalledTimes(1);
      expect(onNavigate).toHaveBeenCalledWith(`/chat/dm/${baseInput.targetId}`);
    });

    it("click navigation falls back to /chat when onNavigate throws", () => {
      stubNotification("granted");
      vi.spyOn(window, "focus").mockImplementation(() => {});
      const onNavigate = vi
        .fn()
        .mockImplementationOnce(() => {
          throw new Error("route rejected");
        })
        .mockImplementationOnce(() => {});

      showBrowserMessageNotification({ ...baseInput, onNavigate });
      MockNotification.instances[0]?.onclick?.();

      expect(onNavigate).toHaveBeenNthCalledWith(1, `/chat/channel/${baseInput.targetId}`);
      expect(onNavigate).toHaveBeenNthCalledWith(2, "/chat");
    });

    it("window.focus() throwing does not prevent close/navigate on click", () => {
      stubNotification("granted");
      vi.spyOn(window, "focus").mockImplementation(() => {
        throw new Error("focus refused");
      });
      const onNavigate = vi.fn();

      showBrowserMessageNotification({ ...baseInput, onNavigate });
      const [created] = MockNotification.instances;

      expect(() => created.onclick?.()).not.toThrow();
      expect(created.close).toHaveBeenCalledTimes(1);
      expect(onNavigate).toHaveBeenCalledWith(`/chat/channel/${baseInput.targetId}`);
    });
  });
});
