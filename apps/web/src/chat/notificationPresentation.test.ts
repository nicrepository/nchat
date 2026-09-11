import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SOUND_COOLDOWN_MS } from "./notificationBurst";
import type {
  MessageNotificationEvent,
  MessagePresentationContext,
} from "./notificationPresentation";
import type { WSNotificationPolicy } from "./useChatWebSocket";

const { mockPlayMessageSound, mockShowBrowserMessageNotification, mockGetSoundNotificationMode } =
  vi.hoisted(() => ({
    mockPlayMessageSound: vi.fn(),
    mockShowBrowserMessageNotification: vi.fn(() => ({ shown: true })),
    mockGetSoundNotificationMode: vi.fn(
      () => "all" as "off" | "all" | "mentions" | "mentions_and_dms",
    ),
  }));

vi.mock("./messageSound", () => ({ playMessageSound: mockPlayMessageSound }));
vi.mock("./browserNotification", () => ({
  showBrowserMessageNotification: mockShowBrowserMessageNotification,
}));
vi.mock("./soundPreference", () => ({ getSoundNotificationMode: mockGetSoundNotificationMode }));

const currentUserId = "00000000-0000-4000-8000-0000000000f1";
const senderId = "00000000-0000-4000-8000-0000000000f2";
const channelId = "11111111-1111-4111-8111-111111111111";

function policy(overrides: Partial<WSNotificationPolicy> = {}): WSNotificationPolicy {
  return {
    policy_version: 1,
    in_app: "allow",
    sound: "allow",
    web_push: "deny",
    sound_class: "general",
    ...overrides,
  };
}

function event(overrides: Partial<MessageNotificationEvent> = {}): MessageNotificationEvent {
  return {
    eventId: "message-1",
    targetKind: "channel",
    targetId: channelId,
    senderId,
    senderDisplayName: "Ana",
    bodyText: "bom dia",
    conversationName: "geral",
    policy: policy(),
    ...overrides,
  };
}

function context(overrides: Partial<MessagePresentationContext> = {}): MessagePresentationContext {
  return {
    currentUserId,
    isMutedConversation: false,
    isActiveConversation: false,
    ...overrides,
  };
}

function sinks() {
  return { showInApp: vi.fn(), navigate: vi.fn() };
}

/** Reader in front of this window, looking at some other conversation. */
function setFocused(focused: boolean) {
  vi.spyOn(document, "visibilityState", "get").mockReturnValue(focused ? "visible" : "hidden");
  vi.spyOn(document, "hasFocus").mockReturnValue(focused);
}

/**
 * A LockManager with the one property this module depends on: a name is held by
 * exactly one caller at a time, and `ifAvailable` refuses the second comer
 * outright rather than queueing it behind the first.
 *
 * Deliberately *not* a serialising fake. One that queued every request and let
 * each acquire in turn would report exclusion while handing every tab the lock
 * — and every tab would then present, which is the defect under test.
 */
function installLockManager() {
  const held = new Set<string>();
  const request = vi.fn(
    async <T>(
      name: string,
      _options: LockOptions,
      callback: (lock: Lock | null) => Promise<T> | T,
    ): Promise<T> => {
      if (held.has(name)) return await callback(null);
      held.add(name);
      try {
        return await callback({ name, mode: "exclusive" });
      } finally {
        held.delete(name);
      }
    },
  );
  Object.defineProperty(navigator, "locks", { value: { request }, configurable: true });
  return { held, request };
}

/** A browser whose lock manager exists but refuses the request. */
function installRefusingLockManager() {
  const request = vi.fn(() => Promise.reject(new Error("lock request refused")));
  Object.defineProperty(navigator, "locks", { value: { request }, configurable: true });
  return request;
}

/**
 * One more browser tab: a fresh module instance sharing the one lock manager.
 * Two calls into a single instance would only ever prove local deduplication —
 * the exclusion under test is between tabs.
 */
async function loadTab() {
  vi.resetModules();
  return await import("./notificationPresentation");
}

/** Lets the claim's promises settle without letting its hold elapse. */
function flush() {
  return vi.advanceTimersByTimeAsync(0);
}

/** How many tabs actually presented. */
function presentationCount(tabs: { showInApp: ReturnType<typeof vi.fn> }[]): number {
  return tabs.reduce((total, tab) => total + tab.showInApp.mock.calls.length, 0);
}

function resetEnvironment() {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  mockShowBrowserMessageNotification.mockReturnValue({ shown: true });
  mockGetSoundNotificationMode.mockReturnValue("all");
  Reflect.deleteProperty(navigator, "locks");
}

describe("notificationPresentation — the presentation matrix", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    setFocused(true);
    installLockManager();
  });

  afterEach(resetEnvironment);

  // Case B: foreground, reader is in another conversation.
  it("shows the toast and chimes once for an event the policy allowed", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    void presentLiveMessageNotification(event(), context(), surfaces);
    await flush();

    expect(surfaces.showInApp).toHaveBeenCalledTimes(1);
    expect(surfaces.showInApp).toHaveBeenCalledWith(
      expect.objectContaining({ messageId: "message-1", conversationName: "geral" }),
    );
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  // Case A: the conversation is open, right here, and the reader is looking.
  it("presents nothing for the conversation this tab is already showing", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    const disposition = await presentLiveMessageNotification(
      event(),
      context({ isActiveConversation: true }),
      surfaces,
    );

    expect(disposition).toBe("suppressed");
    expect(surfaces.showInApp).not.toHaveBeenCalled();
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  // Case D/E: outside working hours, a reaction, an imported event — all of
  // them reach the browser as a decision that denied every channel.
  it("presents nothing when the central decision denied every channel", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    const disposition = await presentLiveMessageNotification(
      event({ policy: policy({ in_app: "deny", sound: "deny", web_push: "deny" }) }),
      context(),
      surfaces,
    );

    expect(disposition).toBe("suppressed");
    expect(surfaces.showInApp).not.toHaveBeenCalled();
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
  });

  // A suppressed event never reaches the lock at all.
  it("does not claim an event it would present nothing for", async () => {
    const locks = installLockManager();
    const { presentLiveMessageNotification } = await import("./notificationPresentation");

    await presentLiveMessageNotification(event(), context({ isActiveConversation: true }), sinks());

    expect(locks.request).not.toHaveBeenCalled();
  });

  it("keeps the toast when the chime preference is off — they are separate channels", async () => {
    mockGetSoundNotificationMode.mockReturnValue("off");
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    void presentLiveMessageNotification(event(), context(), surfaces);
    await flush();

    expect(surfaces.showInApp).toHaveBeenCalledTimes(1);
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("never toasts a window nobody is looking at", async () => {
    setFocused(false);
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    void presentLiveMessageNotification(event(), context(), surfaces);
    await flush();

    expect(surfaces.showInApp).not.toHaveBeenCalled();
  });

  // Case C: the OS surface is the one that exists for a background window, and
  // once it has interrupted the reader the chime would say the same thing twice.
  it("does not chime when the OS surface already announced the message", async () => {
    setFocused(false);
    const { presentLiveMessageNotification } = await import("./notificationPresentation");

    void presentLiveMessageNotification(
      event({ policy: policy({ web_push: "allow" }) }),
      context(),
      sinks(),
    );
    await flush();

    expect(mockShowBrowserMessageNotification).toHaveBeenCalledTimes(1);
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("falls back to the chime when the OS surface did not appear", async () => {
    setFocused(false);
    mockShowBrowserMessageNotification.mockReturnValue({ shown: false });
    const { presentLiveMessageNotification } = await import("./notificationPresentation");

    void presentLiveMessageNotification(
      event({ policy: policy({ web_push: "allow" }) }),
      context(),
      sinks(),
    );
    await flush();

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("never raises the OS surface over a window that is already in front", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");

    void presentLiveMessageNotification(
      event({ policy: policy({ web_push: "allow" }) }),
      context(),
      sinks(),
    );
    await flush();

    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
  });

  it("shows a mention as its label in the toast, never the wire token", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    void presentLiveMessageNotification(
      event({ bodyText: `@[Ana](mention:user:${currentUserId}) olha isso` }),
      context(),
      surfaces,
    );
    await flush();

    expect(surfaces.showInApp).toHaveBeenCalledWith(
      expect.objectContaining({ bodyText: "@Ana olha isso" }),
    );
  });

  it("presents nothing for the reader's own message", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    const disposition = await presentLiveMessageNotification(
      event({ senderId: currentUserId }),
      context(),
      surfaces,
    );

    expect(disposition).toBe("suppressed");
    expect(surfaces.showInApp).not.toHaveBeenCalled();
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });
});

describe("notificationPresentation — audio failure is contained", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    setFocused(true);
    installLockManager();
  });

  afterEach(resetEnvironment);

  it("absorbs a chime that fails and still presents the toast", async () => {
    mockPlayMessageSound.mockImplementationOnce(() => {
      throw new Error("autoplay blocked");
    });
    const { presentLiveMessageNotification, PRESENTATION_CLAIM_HOLD_MS } =
      await import("./notificationPresentation");
    const surfaces = sinks();

    const claim = presentLiveMessageNotification(event(), context(), surfaces);
    await vi.advanceTimersByTimeAsync(PRESENTATION_CLAIM_HOLD_MS);

    await expect(claim).resolves.toBe("acquired");
    expect(surfaces.showInApp).toHaveBeenCalledTimes(1);
  });

  it("absorbs an OS surface that throws and still chimes", async () => {
    setFocused(false);
    mockShowBrowserMessageNotification.mockImplementationOnce(() => {
      throw new Error("notification constructor failed");
    });
    const { presentLiveMessageNotification, PRESENTATION_CLAIM_HOLD_MS } =
      await import("./notificationPresentation");

    const claim = presentLiveMessageNotification(
      event({ policy: policy({ web_push: "allow" }) }),
      context(),
      sinks(),
    );
    await vi.advanceTimersByTimeAsync(PRESENTATION_CLAIM_HOLD_MS);

    await expect(claim).resolves.toBe("acquired");
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });
});

describe("notificationPresentation — Web Locks is the exclusion", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    setFocused(true);
  });

  afterEach(resetEnvironment);

  it("gives the claim to one of two tabs racing for the same event", async () => {
    installLockManager();
    const tabA = await loadTab();
    const tabB = await loadTab();
    const sinksA = sinks();
    const sinksB = sinks();

    const claimA = tabA.presentLiveMessageNotification(event(), context(), sinksA);
    const claimB = tabB.presentLiveMessageNotification(event(), context(), sinksB);
    await flush();

    expect(await claimB).toBe("contended");
    expect(presentationCount([sinksA, sinksB])).toBe(1);
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(tabA.PRESENTATION_CLAIM_HOLD_MS);
    expect(await claimA).toBe("acquired");
  });

  it("gives the claim to one of three tabs racing for the same event", async () => {
    installLockManager();
    const tabs = [await loadTab(), await loadTab(), await loadTab()];
    const tabSinks = [sinks(), sinks(), sinks()];

    const claims = tabs.map((tab, index) =>
      tab.presentLiveMessageNotification(event(), context(), tabSinks[index]!),
    );
    await flush();
    await vi.advanceTimersByTimeAsync(tabs[0]!.PRESENTATION_CLAIM_HOLD_MS);
    const dispositions = await Promise.all(claims);

    expect(dispositions.filter((value) => value === "acquired")).toHaveLength(1);
    expect(dispositions.filter((value) => value === "contended")).toHaveLength(2);
    expect(presentationCount(tabSinks)).toBe(1);
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  /**
   * The scenario a timed round could not survive: a tab that starts after
   * another has already claimed, having observed nothing at all from it.
   *
   * Nothing here depends on shared history or on two deadlines agreeing — the
   * late tab is refused by the browser because the lock is held.
   */
  it("refuses a tab that joins while another already holds the claim", async () => {
    installLockManager();
    const early = await loadTab();
    const earlySinks = sinks();

    const claimEarly = early.presentLiveMessageNotification(event(), context(), earlySinks);
    await flush();
    expect(earlySinks.showInApp).toHaveBeenCalledTimes(1);

    // Only now does this tab exist, and it has heard nothing from the other.
    const late = await loadTab();
    const lateSinks = sinks();
    const claimLate = late.presentLiveMessageNotification(event(), context(), lateSinks);
    await flush();

    expect(await claimLate).toBe("contended");
    expect(lateSinks.showInApp).not.toHaveBeenCalled();
    expect(presentationCount([earlySinks, lateSinks])).toBe(1);
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(early.PRESENTATION_CLAIM_HOLD_MS);
    expect(await claimEarly).toBe("acquired");
  });

  // One lock per event, never one lock for notifications: two messages do not
  // queue behind each other.
  it("claims each event on its own lock", async () => {
    const locks = installLockManager();
    const tab = await loadTab();
    const surfaces = sinks();

    void tab.presentLiveMessageNotification(event({ eventId: "message-1" }), context(), surfaces);
    void tab.presentLiveMessageNotification(event({ eventId: "message-2" }), context(), surfaces);
    await flush();

    expect(surfaces.showInApp).toHaveBeenCalledTimes(2);
    expect(locks.request.mock.calls.map((call) => call[0])).toEqual([
      "nchat.notifications.presentation.message-1",
      "nchat.notifications.presentation.message-2",
    ]);
  });

  // No permanent leader: the lock is released, and the next event is claimed
  // fresh by whoever gets there.
  it("releases the claim so a later event is claimed freshly", async () => {
    const locks = installLockManager();
    const tab = await loadTab();
    const surfaces = sinks();

    const claim = tab.presentLiveMessageNotification(
      event({ eventId: "message-1" }),
      context(),
      surfaces,
    );
    await flush();
    expect(locks.held.size).toBe(1);

    await vi.advanceTimersByTimeAsync(tab.PRESENTATION_CLAIM_HOLD_MS);
    expect(await claim).toBe("acquired");
    expect(locks.held.size).toBe(0);

    void tab.presentLiveMessageNotification(event({ eventId: "message-2" }), context(), surfaces);
    await flush();
    expect(surfaces.showInApp).toHaveBeenCalledTimes(2);
  });

  // The lock name is the message id and nothing else: no body, no preview, no
  // sender, no conversation, no token.
  it("names the lock with the event id alone", async () => {
    const locks = installLockManager();
    const tab = await loadTab();

    void tab.presentLiveMessageNotification(
      event({
        eventId: "message-1",
        bodyText: "segredo confidencial",
        senderDisplayName: "Ana",
        conversationName: "geral",
      }),
      context(),
      sinks(),
    );
    await flush();

    const name = locks.request.mock.calls[0]?.[0] ?? "";
    expect(name).toBe("nchat.notifications.presentation.message-1");
    expect(name).not.toContain("segredo");
    expect(name).not.toContain("Ana");
    expect(name).not.toContain("geral");
    // Its own namespace; a call's ringtone lock must never be mistaken for it.
    expect(name.startsWith("nchat.calls.")).toBe(false);
  });

  it("asks for an exclusive lock it will not queue for", async () => {
    const locks = installLockManager();
    const tab = await loadTab();

    void tab.presentLiveMessageNotification(event(), context(), sinks());
    await flush();

    expect(locks.request.mock.calls[0]?.[1]).toEqual({ mode: "exclusive", ifAvailable: true });
  });
});

describe("notificationPresentation — fails closed without coordination", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    setFocused(true);
    Reflect.deleteProperty(navigator, "locks");
  });

  afterEach(resetEnvironment);

  /**
   * A browser with no Web Locks cannot be told what another tab is doing, so it
   * presents nothing rather than chiming in every tab at once. Not duplicating
   * outranks announcing locally without coordination — and the message, the
   * unread badge and Web Push are decided elsewhere and are unaffected.
   */
  it("presents nothing at all when there is no lock manager", async () => {
    const tabA = await loadTab();
    const tabB = await loadTab();
    const sinksA = sinks();
    const sinksB = sinks();

    const disposition = await tabA.presentLiveMessageNotification(event(), context(), sinksA);
    await tabB.presentLiveMessageNotification(event(), context(), sinksB);
    await vi.advanceTimersByTimeAsync(tabA.PRESENTATION_CLAIM_HOLD_MS);

    expect(disposition).toBe("unavailable");
    expect(presentationCount([sinksA, sinksB])).toBe(0);
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
  });

  /**
   * The refused request is the case that used to be swallowed and reported as
   * coordinated. It is an absent guarantee, and now says so.
   */
  it("reports a refused lock request as unavailable and presents nothing", async () => {
    const request = installRefusingLockManager();
    const unhandled = vi.fn();
    process.on("unhandledRejection", unhandled);
    const tab = await loadTab();
    const surfaces = sinks();

    const disposition = await tab.presentLiveMessageNotification(event(), context(), surfaces);
    await vi.advanceTimersByTimeAsync(tab.PRESENTATION_CLAIM_HOLD_MS);
    process.off("unhandledRejection", unhandled);

    expect(request).toHaveBeenCalledTimes(1);
    expect(disposition).toBe("unavailable");
    expect(surfaces.showInApp).not.toHaveBeenCalled();
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
    expect(unhandled).not.toHaveBeenCalled();
  });

  it("treats a browser that hides its lock manager the same way", async () => {
    Object.defineProperty(navigator, "locks", {
      get() {
        throw new Error("blocked by policy");
      },
      configurable: true,
    });
    const tab = await loadTab();
    const surfaces = sinks();

    const disposition = await tab.presentLiveMessageNotification(event(), context(), surfaces);

    expect(disposition).toBe("unavailable");
    expect(surfaces.showInApp).not.toHaveBeenCalled();
  });

  // Failing closed is about presentation only: an event that authorises no
  // surface is still reported as suppressed, not as a coordination failure.
  it("still reports a denied event as suppressed", async () => {
    const tab = await loadTab();

    const disposition = await tab.presentLiveMessageNotification(
      event({ policy: policy({ in_app: "deny", sound: "deny", web_push: "deny" }) }),
      context(),
      sinks(),
    );

    expect(disposition).toBe("suppressed");
  });
});

describe("notificationPresentation — lifecycle", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    setFocused(true);
  });

  afterEach(resetEnvironment);

  /**
   * The strongest form of "a remount cannot duplicate a handler": no handler,
   * no channel, no listener. Coordination is a lock the browser owns, so
   * StrictMode's second mount has nothing to register twice and nothing to
   * leak. The module does hold the bounded memory issue #750 added, which is
   * state and not a subscription — it is read, never fired.
   */
  it("opens no channel and subscribes to nothing", async () => {
    installLockManager();
    const channel = vi.fn();
    vi.stubGlobal("BroadcastChannel", channel);
    const windowListener = vi.spyOn(window, "addEventListener");
    const documentListener = vi.spyOn(document, "addEventListener");
    const tab = await loadTab();

    void tab.presentLiveMessageNotification(event({ eventId: "message-1" }), context(), sinks());
    void tab.presentLiveMessageNotification(event({ eventId: "message-2" }), context(), sinks());
    await flush();

    expect(channel).not.toHaveBeenCalled();
    expect(windowListener).not.toHaveBeenCalled();
    expect(documentListener).not.toHaveBeenCalled();
  });
});

// ── Dedupe and bursts (issue #750) ───────────────────────────────────────────
//
// What separates a live event from recovered state is not tested here, because
// it is not decided here: only the WebSocket handler reaches this module, and
// that boundary is enforced by eslint.config.js and proven at its real seams in
// useChatSidebar.test.tsx and useMessages.test.ts.

describe("notificationPresentation — redelivery of a known event", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    setFocused(true);
    installLockManager();
  });

  afterEach(resetEnvironment);

  // Reconnecting delivers ids this tab has already announced. The claim's own
  // hold cannot cover this: it lasts seconds, and a reconnect does not.
  it("does not announce an event id it already announced", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    void presentLiveMessageNotification(event({ eventId: "message-1" }), context(), surfaces);
    await flush();
    await vi.advanceTimersByTimeAsync(60_000);
    const disposition = await presentLiveMessageNotification(
      event({ eventId: "message-1" }),
      context(),
      surfaces,
    );

    expect(disposition).toBe("repeat");
    expect(surfaces.showInApp).toHaveBeenCalledTimes(1);
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  // Identity is the id and nothing derived from the delivery: a redelivery that
  // differs in body, sender name or conversation name is the same event.
  it("recognises a redelivery that differs in everything but its id", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    void presentLiveMessageNotification(event({ eventId: "message-1" }), context(), surfaces);
    await flush();
    const disposition = await presentLiveMessageNotification(
      event({ eventId: "message-1", bodyText: "editado", senderDisplayName: "Outro" }),
      context(),
      surfaces,
    );

    expect(disposition).toBe("repeat");
    expect(surfaces.showInApp).toHaveBeenCalledTimes(1);
  });

  // A tab that presented nothing must remember nothing: the reader never heard
  // this event here, so a later delivery of it is still news for this tab.
  it("remembers only what it actually announced", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    await presentLiveMessageNotification(
      event({ eventId: "message-1" }),
      context({ isActiveConversation: true }),
      surfaces,
    );
    void presentLiveMessageNotification(event({ eventId: "message-1" }), context(), surfaces);
    await flush();

    expect(surfaces.showInApp).toHaveBeenCalledTimes(1);
  });
});

describe("notificationPresentation — sound burst suppression", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    setFocused(true);
    installLockManager();
  });

  afterEach(resetEnvironment);

  /** One conversation, one message id per event, as fast as the socket delivers. */
  async function burst(
    present: typeof import("./notificationPresentation").presentLiveMessageNotification,
    surfaces: ReturnType<typeof sinks>,
    count: number,
    overrides: Partial<MessageNotificationEvent> = {},
  ) {
    for (let index = 0; index < count; index += 1) {
      void present(event({ eventId: `burst-${index}`, ...overrides }), context(), surfaces);
    }
    await flush();
  }

  it("chimes once for a rajada in one conversation", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    await burst(presentLiveMessageNotification, surfaces, 50);

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  // The toast is not silenced with the chime: it is replaced, which is the
  // surface's existing design (one alert, the newest). Every event still
  // reaches it, so the reader always sees the latest activity and can still
  // navigate to it.
  it("keeps offering the newest activity while the chime is on cooldown", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    await burst(presentLiveMessageNotification, surfaces, 50);

    expect(surfaces.showInApp).toHaveBeenCalledTimes(50);
    expect(surfaces.showInApp).toHaveBeenLastCalledWith(
      expect.objectContaining({ messageId: "burst-49" }),
    );
  });

  it("chimes again for the first event after the window closes", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    await burst(presentLiveMessageNotification, surfaces, 10);
    await vi.advanceTimersByTimeAsync(SOUND_COOLDOWN_MS);
    void presentLiveMessageNotification(event({ eventId: "after-window" }), context(), surfaces);
    await flush();

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(2);
  });

  it("stays silent at the last instant of the window", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    void presentLiveMessageNotification(event({ eventId: "first" }), context(), surfaces);
    await flush();
    await vi.advanceTimersByTimeAsync(SOUND_COOLDOWN_MS - 1);
    void presentLiveMessageNotification(event({ eventId: "inside-window" }), context(), surfaces);
    await flush();

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("does not silence a different conversation", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    await burst(presentLiveMessageNotification, surfaces, 20);
    void presentLiveMessageNotification(
      event({ eventId: "elsewhere", targetId: "22222222-2222-4222-8222-222222222222" }),
      context(),
      surfaces,
    );
    await flush();

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(2);
  });

  // A room going fast is exactly when being named personally has to stay
  // audible. The split is the server's own mention decision, not a new priority.
  it("still chimes for a message that names the reader in the same busy room", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    await burst(presentLiveMessageNotification, surfaces, 20);
    void presentLiveMessageNotification(
      event({ eventId: "named", policy: policy({ named_user_ids: [currentUserId] }) }),
      context(),
      surfaces,
    );
    await flush();

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(2);
  });

  // The cooldown is a sound decision and only a sound decision. It never
  // narrows what the policy authorised on another channel.
  it("does not withdraw the OS surface from an event whose chime was suppressed", async () => {
    setFocused(false);
    mockShowBrowserMessageNotification.mockReturnValue({ shown: false });
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    await burst(presentLiveMessageNotification, surfaces, 5, {
      policy: policy({ web_push: "allow" }),
    });

    expect(mockShowBrowserMessageNotification).toHaveBeenCalledTimes(5);
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });
});

describe("notificationPresentation — bursts across tabs", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    setFocused(true);
    installLockManager();
  });

  afterEach(resetEnvironment);

  /**
   * The composition issue #750 has to leave intact: a burst, delivered to every
   * tab, with a redelivery of each event on top. Whatever the burst gate does
   * locally, the claim from issue #749 still decides that each event is
   * announced by exactly one tab, exactly once.
   */
  it("announces each event of a burst exactly once across two tabs", async () => {
    const first = await loadTab();
    const second = await loadTab();
    const firstSinks = sinks();
    const secondSinks = sinks();

    for (let index = 0; index < 20; index += 1) {
      const burstEvent = event({ eventId: `shared-${index}` });
      void first.presentLiveMessageNotification(burstEvent, context(), firstSinks);
      void second.presentLiveMessageNotification(burstEvent, context(), secondSinks);
      // The reconnect's redelivery of the same event, to both tabs.
      void first.presentLiveMessageNotification(burstEvent, context(), firstSinks);
      void second.presentLiveMessageNotification(burstEvent, context(), secondSinks);
    }
    await flush();

    expect(presentationCount([firstSinks, secondSinks])).toBe(20);
  });

  // Each tab holds its own window, so a burst costs at most one chime per tab
  // that won something — never one per message. See notificationPresentation.
  it("keeps a burst from becoming one chime per message across tabs", async () => {
    const first = await loadTab();
    const second = await loadTab();

    for (let index = 0; index < 40; index += 1) {
      const burstEvent = event({ eventId: `shared-${index}` });
      void first.presentLiveMessageNotification(burstEvent, context(), sinks());
      void second.presentLiveMessageNotification(burstEvent, context(), sinks());
    }
    await flush();

    expect(mockPlayMessageSound.mock.calls.length).toBeLessThanOrEqual(2);
  });
});

/**
 * The memory outlives every reset in the app except one: the reader changing
 * (issue #750). It is scoped to the session generation the rest of the app
 * already keys on, so a logout or a different account starts it empty — driven
 * here through setTokens/clearTokens, which is the mechanism production uses,
 * not a reset hook that exists for tests.
 */
describe("notificationPresentation — memory belongs to a session", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    setFocused(true);
    installLockManager();
  });

  afterEach(resetEnvironment);

  /**
   * One tab: the presentation module and the session it reads must come from
   * the same module graph, which `vi.resetModules` has just replaced.
   */
  async function loadSession() {
    const auth = await import("../lib/authSession");
    const presentation = await import("./notificationPresentation");
    auth.setTokens("session-a");
    return {
      auth,
      present: presentation.presentLiveMessageNotification,
      claimHoldMs: presentation.PRESENTATION_CLAIM_HOLD_MS,
    };
  }

  it("announces an event the previous session had already announced", async () => {
    const { auth, present, claimHoldMs } = await loadSession();
    const surfaces = sinks();
    void present(event({ eventId: "message-1" }), context(), surfaces);
    await flush();
    // Past the cross-tab claim, so what is under test is the memory and not the
    // lock this tab is still holding for the event.
    await vi.advanceTimersByTimeAsync(claimHoldMs);

    auth.setTokens("session-b");
    void present(event({ eventId: "message-1" }), context(), surfaces);
    await flush();

    expect(surfaces.showInApp).toHaveBeenCalledTimes(2);
  });

  // A new reader does not inherit the previous one's cooldown either: their
  // first message chimes, whatever the session before them just heard.
  it("does not carry a sound cooldown into the next session", async () => {
    const { auth, present } = await loadSession();
    void present(event({ eventId: "message-1" }), context(), sinks());
    await flush();
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);

    auth.clearTokens();
    auth.setTokens("session-b");
    void present(event({ eventId: "message-2" }), context(), sinks());
    await flush();

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(2);
  });

  // The other half, and the one that matters more: the boundary is identity,
  // not time or activity. Nothing about staying in one session forgets.
  it("keeps remembering while the session is unchanged", async () => {
    const { present } = await loadSession();
    const surfaces = sinks();
    void present(event({ eventId: "message-1" }), context(), surfaces);
    await flush();

    const disposition = await present(event({ eventId: "message-1" }), context(), surfaces);

    expect(disposition).toBe("repeat");
    expect(surfaces.showInApp).toHaveBeenCalledTimes(1);
  });
});
