import { afterEach, describe, expect, it, vi } from "vitest";

/**
 * Behaviour tests for apps/web/public/sw.js.
 *
 * The worker is exercised through its only public surface — the two events the
 * browser delivers to it — with the Service Worker globals it reads (`self`'s
 * registration, clients and location) replaced by stubs. Nothing here renders
 * React or touches the DOM, which is the point: a push arrives when there is no
 * page at all.
 */

type EventHandler = (event: unknown) => void;

interface WindowClientStub {
  url: string | undefined;
  visibilityState?: DocumentVisibilityState;
  focused?: boolean;
  focus: ReturnType<typeof vi.fn>;
  navigate: ReturnType<typeof vi.fn>;
}

const ORIGIN = window.location.origin;

const validPayload = {
  v: 1,
  id: "8bd0a1de-0000-4000-8000-000000000001",
  type: "mention",
  source_type: "message",
  source_id: "1f3c9a77-0000-4000-8000-000000000002",
  occurred_at: "2026-09-08T14:30:00Z",
};

function windowClient(path: string, overrides: Partial<WindowClientStub> = {}): WindowClientStub {
  return {
    url: `${ORIGIN}${path}`,
    focus: vi.fn().mockResolvedValue(undefined),
    navigate: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

interface Harness {
  handlers: Map<string, EventHandler>;
  showNotification: ReturnType<typeof vi.fn>;
  matchAll: ReturnType<typeof vi.fn>;
  openWindow: ReturnType<typeof vi.fn>;
}

async function loadServiceWorker(options: Partial<Harness> = {}): Promise<Harness> {
  const handlers = new Map<string, EventHandler>();
  vi.stubGlobal("addEventListener", (type: string, handler: EventHandler) => {
    handlers.set(type, handler);
  });

  const showNotification = options.showNotification ?? vi.fn().mockResolvedValue(undefined);
  const matchAll = options.matchAll ?? vi.fn().mockResolvedValue([]);
  const openWindow = options.openWindow ?? vi.fn().mockResolvedValue(null);
  vi.stubGlobal("registration", { showNotification });
  vi.stubGlobal("clients", { matchAll, openWindow });

  vi.resetModules();
  await import("../../public/sw.js");

  return { handlers, showNotification, matchAll, openWindow };
}

/** Dispatches a push and settles whatever the handler passed to waitUntil(). */
async function dispatchPush(harness: Harness, data: { json: () => unknown } | null) {
  const waitUntil = vi.fn();
  harness.handlers.get("push")?.({ data, waitUntil });
  for (const [pending] of waitUntil.mock.calls) await pending;
  return waitUntil;
}

async function dispatchClick(harness: Harness, data: unknown) {
  const close = vi.fn();
  const waitUntil = vi.fn();
  harness.handlers.get("notificationclick")?.({ notification: { close, data }, waitUntil });
  for (const [pending] of waitUntil.mock.calls) await pending;
  return { close, waitUntil };
}

const jsonData = (value: unknown) => ({ json: () => value });

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("service worker: registration of handlers", () => {
  it("registers exactly one push and one notificationclick handler", async () => {
    const harness = await loadServiceWorker();

    expect([...harness.handlers.keys()].sort()).toEqual(["notificationclick", "push"]);
  });

  it("declares no install or activate handler, so activation stays the browser default", async () => {
    const harness = await loadServiceWorker();

    expect(harness.handlers.has("install")).toBe(false);
    expect(harness.handlers.has("activate")).toBe(false);
  });
});

describe("service worker: push", () => {
  it("shows one notification for a valid payload, with local assets and a dedupe tag", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData(validPayload));

    expect(harness.showNotification).toHaveBeenCalledTimes(1);
    expect(harness.showNotification).toHaveBeenCalledWith("Você foi mencionado no NChat", {
      tag: `nchat-notification-${validPayload.id}`,
      icon: "/assets/nic-labs-icon.png",
      badge: "/assets/favicon.png",
      data: { url: "/chat" },
      timestamp: Date.parse(validPayload.occurred_at),
    });
  });

  it("shows a notification with no page mounted at all", async () => {
    const harness = await loadServiceWorker();
    expect(document.getElementById("root")).toBeNull();

    await dispatchPush(harness, jsonData(validPayload));

    expect(harness.showNotification).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["direct_message", "Nova mensagem direta no NChat"],
    ["reply", "Responderam sua mensagem no NChat"],
    ["channel_message", "Nova mensagem em um canal do NChat"],
    ["reaction", "Nova reação na sua mensagem"],
    ["call", "Chamada no NChat"],
    // Issue #825: a reminder says the urgent message is still waiting rather
    // than announcing a new one, because the recipient has already been told.
    ["urgent_reminder", "Mensagem urgente ainda aguarda você no NChat"],
  ])("titles a '%s' event as %s", async (type, title) => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData({ ...validPayload, type }));

    expect(harness.showNotification).toHaveBeenCalledWith(title, expect.anything());
  });

  it.each(["invitation", "constructor", "toString", "__proto__"])(
    "falls back to a generic title for the event type '%s'",
    async (type) => {
      const harness = await loadServiceWorker();

      await dispatchPush(harness, jsonData({ ...validPayload, type }));

      expect(harness.showNotification).toHaveBeenCalledWith(
        "Nova notificação do NChat",
        expect.anything(),
      );
    },
  );

  it("omits the timestamp when occurred_at is not a parseable instant", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData({ ...validPayload, occurred_at: "ontem" }));

    expect(harness.showNotification.mock.calls[0][1]).not.toHaveProperty("timestamp");
  });

  it("never copies payload fields into the notification", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(
      harness,
      jsonData({ ...validPayload, token: "secret-access-token", url: "https://evil.example/x" }),
    );

    const [title, options] = harness.showNotification.mock.calls[0];
    expect(JSON.stringify({ title, options })).not.toContain("secret-access-token");
    expect(options.data).toEqual({ url: "/chat" });
  });

  it("shows nothing when the push carries no data", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, null);

    expect(harness.showNotification).not.toHaveBeenCalled();
  });

  it("shows nothing when the body is not JSON", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, {
      json: () => {
        throw new SyntaxError("Unexpected token");
      },
    });

    expect(harness.showNotification).not.toHaveBeenCalled();
  });

  it.each([
    ["a JSON string", "mention"],
    ["a JSON array", [validPayload]],
    ["null", null],
    ["a number", 7],
  ])("shows nothing when the payload is %s", async (_label, body) => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData(body));

    expect(harness.showNotification).not.toHaveBeenCalled();
  });

  it.each([
    ["absent", undefined],
    // Issue #870 made 2 a version this build understands, so the "newer
    // version" case moved up. It has to stay expressible: failing closed on a
    // version nobody has written yet is what lets version 3 add a field without
    // this build guessing at what it means.
    ["a newer version", 3],
    ["a string", "1"],
    ["a float", 1.5],
  ])("shows nothing when the version is %s", async (_label, version) => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData({ ...validPayload, v: version }));

    expect(harness.showNotification).not.toHaveBeenCalled();
  });

  it.each(["id", "type", "source_type", "source_id", "occurred_at"])(
    "shows nothing when the required field '%s' is missing",
    async (field) => {
      const harness = await loadServiceWorker();
      const payload: Record<string, unknown> = { ...validPayload };
      delete payload[field];

      await dispatchPush(harness, jsonData(payload));

      expect(harness.showNotification).not.toHaveBeenCalled();
    },
  );

  it.each([
    ["empty", ""],
    ["not a string", 42],
  ])("shows nothing when a required field is %s", async (_label, value) => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData({ ...validPayload, source_id: value }));

    expect(harness.showNotification).not.toHaveBeenCalled();
  });

  // Issue #862 / #678: only a window that is visible and focused presents the
  // event itself (a toast needs focus), so only then is the OS notification
  // redundant. Visible without focus or hidden, the push is the visual surface.
  async function pushWith(...windows: WindowClientStub[]) {
    const harness = await loadServiceWorker({ matchAll: vi.fn().mockResolvedValue(windows) });
    await dispatchPush(harness, jsonData(validPayload));
    return harness;
  }

  it("shows nothing while an NChat window is visible and focused", async () => {
    const harness = await pushWith(
      windowClient("/chat", { visibilityState: "visible", focused: true }),
    );

    expect(harness.matchAll).toHaveBeenCalledWith({ type: "window", includeUncontrolled: true });
    expect(harness.showNotification).not.toHaveBeenCalled();
  });

  it("shows the notification when the NChat window is visible but not focused", async () => {
    const harness = await pushWith(
      windowClient("/chat", { visibilityState: "visible", focused: false }),
    );

    expect(harness.showNotification).toHaveBeenCalledTimes(1);
  });

  it("shows the notification when every NChat window is hidden", async () => {
    const harness = await pushWith(
      windowClient("/chat", { visibilityState: "hidden", focused: false }),
      windowClient("/profile", { visibilityState: "hidden", focused: false }),
    );

    expect(harness.showNotification).toHaveBeenCalledTimes(1);
  });

  it("does not trust a focus flag on a window that is not visible", async () => {
    const harness = await pushWith(
      windowClient("/chat", { visibilityState: "hidden", focused: true }),
    );

    expect(harness.showNotification).toHaveBeenCalledTimes(1);
  });

  it("shows nothing when one of several NChat windows is visible and focused", async () => {
    const harness = await pushWith(
      windowClient("/chat", { visibilityState: "hidden", focused: false }),
      windowClient("/profile", { visibilityState: "visible", focused: false }),
      windowClient("/chat/dm/x", { visibilityState: "visible", focused: true }),
    );

    expect(harness.showNotification).not.toHaveBeenCalled();
  });

  it("shows the notification when several NChat windows are visible but none is focused", async () => {
    const harness = await pushWith(
      windowClient("/chat", { visibilityState: "visible", focused: false }),
      windowClient("/profile", { visibilityState: "visible", focused: false }),
    );

    expect(harness.showNotification).toHaveBeenCalledTimes(1);
  });

  it("is not silenced by a visible window of another origin", async () => {
    const foreign = windowClient("/chat", { visibilityState: "visible", focused: true });
    foreign.url = "https://nchat.example.com.evil.test/chat";
    const harness = await loadServiceWorker({ matchAll: vi.fn().mockResolvedValue([foreign]) });

    await dispatchPush(harness, jsonData(validPayload));

    expect(harness.showNotification).toHaveBeenCalledTimes(1);
  });

  it("still shows the notification when the Clients API cannot be asked", async () => {
    const harness = await loadServiceWorker({
      matchAll: vi.fn().mockRejectedValue(new Error("clients unavailable")),
    });

    await dispatchPush(harness, jsonData(validPayload));

    expect(harness.showNotification).toHaveBeenCalledTimes(1);
  });

  it("does not reject waitUntil when showNotification fails", async () => {
    const showNotification = vi.fn().mockRejectedValue(new Error("permission revoked"));
    const harness = await loadServiceWorker({ showNotification });

    await expect(dispatchPush(harness, jsonData(validPayload))).resolves.toBeDefined();
  });
});

describe("service worker: notificationclick", () => {
  it("closes the notification before doing anything else", async () => {
    const harness = await loadServiceWorker();

    const { close } = await dispatchClick(harness, { url: "/chat" });

    expect(close).toHaveBeenCalledTimes(1);
  });

  it("focuses an existing NChat window that is already inside the destination", async () => {
    const client = windowClient("/chat/dm/1f3c9a77-0000-4000-8000-000000000002");
    const harness = await loadServiceWorker({ matchAll: vi.fn().mockResolvedValue([client]) });

    await dispatchClick(harness, { url: "/chat" });

    expect(client.focus).toHaveBeenCalledTimes(1);
    expect(client.navigate).not.toHaveBeenCalled();
    expect(harness.openWindow).not.toHaveBeenCalled();
  });

  it("navigates an existing NChat window that is elsewhere, then focuses it", async () => {
    const navigated = windowClient("/chat");
    const client = windowClient("/login", {
      navigate: vi.fn().mockResolvedValue(navigated),
    });
    const harness = await loadServiceWorker({ matchAll: vi.fn().mockResolvedValue([client]) });

    await dispatchClick(harness, { url: "/chat" });

    expect(client.navigate).toHaveBeenCalledWith("/chat");
    expect(navigated.focus).toHaveBeenCalledTimes(1);
    expect(harness.openWindow).not.toHaveBeenCalled();
  });

  it("focuses the original window when navigate() resolves without a client", async () => {
    const client = windowClient("/login", { navigate: vi.fn().mockResolvedValue(null) });
    const harness = await loadServiceWorker({ matchAll: vi.fn().mockResolvedValue([client]) });

    await dispatchClick(harness, { url: "/chat" });

    expect(client.focus).toHaveBeenCalledTimes(1);
  });

  it("still focuses an uncontrolled window whose navigate() is refused", async () => {
    const client = windowClient("/login", {
      navigate: vi.fn().mockRejectedValue(new DOMException("not controlled", "InvalidAccessError")),
    });
    const harness = await loadServiceWorker({ matchAll: vi.fn().mockResolvedValue([client]) });

    await dispatchClick(harness, { url: "/chat" });

    expect(client.focus).toHaveBeenCalledTimes(1);
    expect(harness.openWindow).not.toHaveBeenCalled();
  });

  it("opens exactly one window when there is no NChat window", async () => {
    const harness = await loadServiceWorker();

    await dispatchClick(harness, { url: "/chat" });

    expect(harness.openWindow).toHaveBeenCalledTimes(1);
    expect(harness.openWindow).toHaveBeenCalledWith("/chat");
  });

  it("reuses one window and opens none when several are eligible", async () => {
    const first = windowClient("/login");
    const second = windowClient("/profile");
    const harness = await loadServiceWorker({
      matchAll: vi.fn().mockResolvedValue([first, second]),
    });

    await dispatchClick(harness, { url: "/chat" });

    expect(first.navigate).toHaveBeenCalledTimes(1);
    expect(second.navigate).not.toHaveBeenCalled();
    expect(second.focus).not.toHaveBeenCalled();
    expect(harness.openWindow).not.toHaveBeenCalled();
  });

  it.each([
    ["another origin", { url: "https://evil.example/chat" }],
    ["an origin this one is a prefix of", { url: `${ORIGIN}.evil.example/chat` }],
    ["no url at all", { url: undefined }],
  ])("never focuses a window from %s", async (_label, overrides) => {
    const foreign = { ...windowClient("/chat"), ...overrides };
    const harness = await loadServiceWorker({ matchAll: vi.fn().mockResolvedValue([foreign]) });

    await dispatchClick(harness, { url: "/chat" });

    expect(foreign.focus).not.toHaveBeenCalled();
    expect(harness.openWindow).toHaveBeenCalledWith("/chat");
  });

  it.each([
    ["an absolute external URL", "https://evil.example/chat"],
    ["a protocol-relative URL", "//evil.example/chat"],
    ["a backslash protocol-relative URL", "/\\evil.example/chat"],
    ["a javascript: URL", "javascript:alert(1)"],
    ["a data: URL", "data:text/html,<script>alert(1)</script>"],
    ["a relative path", "chat/dm/1"],
    ["an empty string", ""],
    ["a number", 7],
    ["absent", undefined],
  ])("refuses %s as a destination and opens the app root instead", async (_label, url) => {
    const harness = await loadServiceWorker();

    await dispatchClick(harness, { url });

    expect(harness.openWindow).toHaveBeenCalledWith("/chat");
  });

  it("opens the app root when the notification carries no data", async () => {
    const harness = await loadServiceWorker();

    await dispatchClick(harness, undefined);

    expect(harness.openWindow).toHaveBeenCalledWith("/chat");
  });

  it("honours an internal path a future worker version may have stored", async () => {
    const harness = await loadServiceWorker();

    await dispatchClick(harness, { url: "/chat/dm/1f3c9a77-0000-4000-8000-000000000002" });

    expect(harness.openWindow).toHaveBeenCalledWith(
      "/chat/dm/1f3c9a77-0000-4000-8000-000000000002",
    );
  });

  it("does not reject waitUntil when the Clients API fails", async () => {
    const matchAll = vi.fn().mockRejectedValue(new Error("clients unavailable"));
    const harness = await loadServiceWorker({ matchAll });

    const { close } = await dispatchClick(harness, { url: "/chat" });

    expect(close).toHaveBeenCalledTimes(1);
    expect(harness.openWindow).not.toHaveBeenCalled();
  });
});

describe("service worker: payload version 2", () => {
  /**
   * Issue #870. Version 2 is version 1 plus a title and a body preview, both
   * produced and approved by the server.
   *
   * Two properties are under test and they pull in opposite directions. An
   * approved preview must actually reach the screen, or the feature does not
   * exist. Anything that is not an approved preview must not — and the fallback
   * it lands on has to be indistinguishable from the one a version 1 payload
   * gets, because the reasons a preview is missing (deleted, withheld, access
   * revoked, previews turned off) are exactly what a banner must not disclose.
   */
  const v2Payload = {
    ...validPayload,
    v: 2,
    title: "Ana Ribeiro · #incidentes",
    body_preview: "o deploy de ontem derrubou o gateway",
  };

  it("shows the server's title and preview", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData(v2Payload));

    expect(harness.showNotification).toHaveBeenCalledWith(
      "Ana Ribeiro · #incidentes",
      expect.objectContaining({ body: "o deploy de ontem derrubou o gateway" }),
    );
  });

  it("keeps the dedupe tag, the local assets, the timestamp and the click target", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData(v2Payload));

    expect(harness.showNotification.mock.calls[0][1]).toEqual({
      tag: `nchat-notification-${validPayload.id}`,
      icon: "/assets/nic-labs-icon.png",
      badge: "/assets/favicon.png",
      data: { url: "/chat" },
      timestamp: Date.parse(validPayload.occurred_at),
      body: "o deploy de ontem derrubou o gateway",
    });
  });

  it("still shows a version 1 payload exactly as before", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData(validPayload));

    expect(harness.showNotification).toHaveBeenCalledWith("Você foi mencionado no NChat", {
      tag: `nchat-notification-${validPayload.id}`,
      icon: "/assets/nic-labs-icon.png",
      badge: "/assets/favicon.png",
      data: { url: "/chat" },
      timestamp: Date.parse(validPayload.occurred_at),
    });
  });

  it("ignores v2 fields carried by a version 1 payload", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(
      harness,
      jsonData({ ...validPayload, title: "forjado", body_preview: "segredo" }),
    );

    expect(harness.showNotification).toHaveBeenCalledWith(
      "Você foi mencionado no NChat",
      expect.not.objectContaining({ body: expect.anything() }),
    );
  });

  it("falls back to the generic title when the server approved none", async () => {
    const harness = await loadServiceWorker();
    const withoutTitle = { ...v2Payload, title: undefined };

    await dispatchPush(harness, jsonData(withoutTitle));

    expect(harness.showNotification).toHaveBeenCalledWith(
      "Você foi mencionado no NChat",
      expect.anything(),
    );
    expect(harness.showNotification.mock.calls[0][1]).not.toHaveProperty("body");
  });

  it("shows no body when the server approved no preview", async () => {
    const harness = await loadServiceWorker();
    const withoutPreview = { ...v2Payload, body_preview: undefined };

    await dispatchPush(harness, jsonData(withoutPreview));

    expect(harness.showNotification.mock.calls[0][0]).toBe(v2Payload.title);
    expect(harness.showNotification.mock.calls[0][1]).not.toHaveProperty("body");
  });

  it("refuses an unknown version even with valid presentation fields", async () => {
    const harness = await loadServiceWorker();
    await dispatchPush(harness, jsonData({ ...v2Payload, v: 3 }));
    expect(harness.showNotification).not.toHaveBeenCalled();
  });

  // A field that is not a non-empty string of a plausible length is not a field
  // this worker repairs. It is dropped whole, and the generic copy takes over:
  // trimming or slicing it here would be the Service Worker deciding
  // presentation, which is what #870 moved to the server.
  it.each([
    ["not a string", 42],
    ["null", null],
    ["an object", { toString: () => "gotcha" }],
    ["an array", ["gotcha"]],
    ["empty", ""],
    ["whitespace", " \t\n "],
    // Zero-width space: not empty, not whitespace, and invisible on a banner.
    // The server cannot produce one — sanitizeLine drops the whole Cf category
    // — so this is the case that only arrives from a payload we did not write.
    ["a zero-width space", "​"],
    ["a mix of invisible characters", "​ ﻿‎"],
    ["longer than the contract allows", "x".repeat(201)],
  ])("ignores a title that is %s", async (_label, title) => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData({ ...v2Payload, title }));

    expect(harness.showNotification).toHaveBeenCalledWith(
      "Você foi mencionado no NChat",
      expect.anything(),
    );
    expect(harness.showNotification.mock.calls[0][1]).not.toHaveProperty("body");
  });

  it.each([
    ["not a string", 42],
    ["null", null],
    ["an object", { toString: () => "gotcha" }],
    ["empty", ""],
    ["an array", ["gotcha"]],
    ["whitespace", " \t\n "],
    ["a zero-width space", "​"],
    ["longer than the contract allows", "x".repeat(401)],
  ])("ignores a body preview that is %s", async (_label, body_preview) => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData({ ...v2Payload, body_preview }));

    expect(harness.showNotification.mock.calls[0][1]).not.toHaveProperty("body");
  });

  // The invisible-character check says "only invisible characters", not
  // "contains one". A ZWJ emoji sequence carries U+200D and a family emoji is
  // built entirely out of pictographs joined by it; a combining accent is Mn,
  // not Cf. All of them are text somebody meant to send, and all of them are
  // kept exactly as they arrived — nothing here rewrites a string.
  it.each([
    ["an emoji", "Ana 🎉"],
    ["a ZWJ emoji sequence", "👨‍👩‍👦 chegou"],
    ["combining marks", "Ana Ribeiro · #operações"],
    ["non-Latin script", "田中さん"],
    ["a zero-width joiner inside real text", "Ana​Ribeiro"],
  ])("keeps a title that is %s", async (_label, title) => {
    const harness = await loadServiceWorker();

    await dispatchPush(harness, jsonData({ ...v2Payload, title }));

    expect(harness.showNotification.mock.calls[0][0]).toBe(title);
    expect(harness.showNotification.mock.calls[0][1].body).toBe(v2Payload.body_preview);
  });

  // The title is passed to showNotification as a string argument and the body
  // as a string option. There is no element, no innerHTML and no parser here,
  // so markup is the characters it is made of — asserted rather than assumed,
  // because the day someone builds a DOM in this file is the day it matters.
  it("passes markup through as literal text and never interprets it", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(
      harness,
      jsonData({
        ...v2Payload,
        title: "<img src=x onerror=alert(1)>",
        body_preview: "<script>alert(1)</script>",
      }),
    );

    expect(harness.showNotification).toHaveBeenCalledWith(
      "<img src=x onerror=alert(1)>",
      expect.objectContaining({ body: "<script>alert(1)</script>" }),
    );
  });

  // The preview never becomes a destination. A click goes where every other
  // click goes, and nothing in the payload can redirect it.
  it("does not let the preview change where a click lands", async () => {
    const harness = await loadServiceWorker();

    await dispatchPush(
      harness,
      jsonData({ ...v2Payload, url: "https://evil.example/x", data: { url: "//evil.example" } }),
    );

    expect(harness.showNotification.mock.calls[0][1].data).toEqual({ url: "/chat" });
  });

  // The suppression rule #862 settled is about which surface presents the
  // event, not about what the payload carries, so a preview does not change it.
  it("is still suppressed by a visible and focused window", async () => {
    const harness = await loadServiceWorker({
      matchAll: vi
        .fn()
        .mockResolvedValue([windowClient("/chat", { visibilityState: "visible", focused: true })]),
    });

    await dispatchPush(harness, jsonData(v2Payload));

    expect(harness.showNotification).not.toHaveBeenCalled();
  });

  it.each([
    ["visible without focus", { visibilityState: "visible" as const, focused: false }],
    ["hidden", { visibilityState: "hidden" as const, focused: false }],
  ])("is still shown for a window that is %s", async (_label, state) => {
    const harness = await loadServiceWorker({
      matchAll: vi.fn().mockResolvedValue([windowClient("/chat", state)]),
    });

    await dispatchPush(harness, jsonData(v2Payload));

    expect(harness.showNotification).toHaveBeenCalledTimes(1);
  });

  it("is still shown when there is no window at all", async () => {
    const harness = await loadServiceWorker({ matchAll: vi.fn().mockResolvedValue([]) });

    await dispatchPush(harness, jsonData(v2Payload));

    expect(harness.showNotification).toHaveBeenCalledTimes(1);
  });
});
