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
    ["a newer version", 2],
    ["a string", "1"],
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
