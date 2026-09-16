import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SOUND_COOLDOWN_MS } from "./notificationBurst";
import type {
  MessageNotificationEvent,
  MessagePresentationContext,
} from "./notificationPresentation";
import type { WSNotificationPolicy } from "./useChatWebSocket";

const {
  mockPlayNotificationSound,
  mockShowBrowserMessageNotification,
  mockGetSoundNotificationMode,
} = vi.hoisted(() => ({
  mockPlayNotificationSound: vi.fn(),
  mockShowBrowserMessageNotification: vi.fn(() => ({ shown: true })),
  mockGetSoundNotificationMode: vi.fn(
    () => "all" as "off" | "all" | "mentions" | "mentions_and_dms",
  ),
}));

vi.mock("../notifications/notificationSound", () => ({
  playNotificationSound: mockPlayNotificationSound,
}));
vi.mock("./browserNotification", () => ({
  showBrowserMessageNotification: mockShowBrowserMessageNotification,
}));
vi.mock("./soundPreference", () => ({ getSoundNotificationMode: mockGetSoundNotificationMode }));

const currentUserId = "00000000-0000-4000-8000-0000000000f1";
const senderId = "00000000-0000-4000-8000-0000000000f2";
const channelId = "11111111-1111-4111-8111-111111111111";

function policy(overrides: Partial<WSNotificationPolicy> = {}): WSNotificationPolicy {
  return {
    policy_version: 2,
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
    priority: "standard",
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
  setAttention({ documentVisible: focused, windowFocused: focused });
}

/**
 * The two browser facts, moved independently (issue #829).
 *
 * `setFocused` moves them together, which is every pre-#829 case and reads
 * better for them. It cannot express the two states #829 turns on, though — a
 * visible tab whose window lost focus, and a focused window whose tab is
 * hidden — and those are precisely where in-conversation must *not* apply.
 * Both are real browser states, and each is set here the way the browser
 * reports it rather than simulated through a timer.
 */
function setAttention(attention: { documentVisible: boolean; windowFocused: boolean }) {
  vi.spyOn(document, "visibilityState", "get").mockReturnValue(
    attention.documentVisible ? "visible" : "hidden",
  );
  vi.spyOn(document, "hasFocus").mockReturnValue(attention.windowFocused);
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

/**
 * Which Lumen sound a permitted event plays (#827).
 *
 * The key is the resolved notification class and nothing else, so these cases
 * are the class ladder read back through the surface that consumes it. They
 * assert the *argument*, not just that something was heard: a player that was
 * called with the wrong key is exactly as audible as one called with the right
 * one, and no other assertion in this file would notice.
 */
describe("notificationPresentation — which sound plays", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    setFocused(true);
    installLockManager();
  });

  afterEach(resetEnvironment);

  async function soundKeyFor(
    eventOverrides: Partial<MessageNotificationEvent>,
    contextOverrides: Partial<MessagePresentationContext> = {},
  ): Promise<string | undefined> {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    void presentLiveMessageNotification(event(eventOverrides), context(contextOverrides), sinks());
    await flush();
    return mockPlayNotificationSound.mock.calls[0]?.[0] as string | undefined;
  }

  it("plays the ordinary message sound for an ordinary message", async () => {
    expect(await soundKeyFor({})).toBe("message");
  });

  it("plays the mention sound when the message names this reader", async () => {
    expect(await soundKeyFor({ policy: policy({ named_user_ids: [currentUserId] }) })).toBe(
      "mention",
    );
  });

  it("plays the mention sound for a message that names everyone", async () => {
    expect(await soundKeyFor({ policy: policy({ names_everyone: true }) })).toBe("mention");
  });

  it("plays the urgent sound, outranking a mention", async () => {
    expect(
      await soundKeyFor({
        priority: "urgent",
        policy: policy({ named_user_ids: [currentUserId] }),
      }),
    ).toBe("urgent");
  });

  it("gives an important message the same sound as a standard one", async () => {
    // #826 is explicit that "important" introduces no class of its own, so it
    // must not have acquired a sound of its own here either.
    expect(await soundKeyFor({ priority: "important" })).toBe("message");
  });

  it("plays the in-conversation sound while the reader is attending the conversation", async () => {
    expect(await soundKeyFor({}, { isActiveConversation: true })).toBe("in-conversation");
  });

  it("keeps urgent ahead of in-conversation in the attended conversation", async () => {
    expect(await soundKeyFor({ priority: "urgent" }, { isActiveConversation: true })).toBe(
      "urgent",
    );
  });

  it("keeps a mention ahead of in-conversation in the attended conversation", async () => {
    expect(
      await soundKeyFor(
        { policy: policy({ named_user_ids: [currentUserId] }) },
        { isActiveConversation: true },
      ),
    ).toBe("mention");
  });

  it("gives an important message in the attended conversation no sound of its own", async () => {
    expect(await soundKeyFor({ priority: "important" }, { isActiveConversation: true })).toBe(
      "in-conversation",
    );
  });
});

/**
 * Attention is three facts, and in-conversation needs all three (issue #829).
 *
 * The conversation being open is the one the app knows; the other two belong to
 * the browser, and a reader with the conversation open in a hidden tab, or in a
 * window they have clicked away from, is not attending it however selected it
 * is. Each case below moves exactly one fact and asserts the sound, because the
 * failure this guards against is a key that is right for the wrong reason.
 */
describe("notificationPresentation — in-conversation needs the reader's attention", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    installLockManager();
  });

  afterEach(resetEnvironment);

  async function soundKeyWhenAttending(
    attention: { documentVisible: boolean; windowFocused: boolean },
    eventOverrides: Partial<MessageNotificationEvent> = {},
  ): Promise<string | undefined> {
    setAttention(attention);
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    void presentLiveMessageNotification(
      event(eventOverrides),
      context({ isActiveConversation: true }),
      sinks(),
    );
    await flush();
    return mockPlayNotificationSound.mock.calls[0]?.[0] as string | undefined;
  }

  // Case A.
  it("is in-conversation with the conversation open, the tab visible and the window focused", async () => {
    expect(await soundKeyWhenAttending({ documentVisible: true, windowFocused: true })).toBe(
      "in-conversation",
    );
  });

  // Case B: the tab is hidden. The reader is somewhere else entirely, so this
  // is room activity they are away from and soundRules' ambient gate keeps it
  // silent — what matters here is that it is not in-conversation.
  it("is not in-conversation while the tab is hidden", async () => {
    expect(await soundKeyWhenAttending({ documentVisible: false, windowFocused: true })).not.toBe(
      "in-conversation",
    );
  });

  // Case C: the tab is visible but the window lost focus — another window is in
  // front. Same conclusion, reached through the other fact.
  it("is not in-conversation while the window is not focused", async () => {
    expect(await soundKeyWhenAttending({ documentVisible: true, windowFocused: false })).not.toBe(
      "in-conversation",
    );
  });

  // And the fallback is a real class, not a dropped event: something addressed
  // to the reader personally still reaches them while they are away, as its own
  // sound rather than the discreet one.
  it("falls back to the mention sound while the reader is away from the open conversation", async () => {
    const named = { policy: policy({ named_user_ids: [currentUserId] }) };
    expect(
      await soundKeyWhenAttending({ documentVisible: false, windowFocused: true }, named),
    ).toBe("mention");
  });

  // Case D: attention alone is not the conversation. A reader watching this tab
  // intently is still not attending a conversation they do not have open.
  it("is the ordinary message sound for a conversation this tab is not showing", async () => {
    setAttention({ documentVisible: true, windowFocused: true });
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    void presentLiveMessageNotification(event(), context(), sinks());
    await flush();

    expect(mockPlayNotificationSound).toHaveBeenCalledWith("message");
  });

  // The exclusivity #829 states outright: one logical event, one sound. The
  // class is resolved once and handed down, so there is no path on which both
  // keys are reached — and a single call is what proves it.
  it("never sounds both message and in-conversation for one event", async () => {
    setAttention({ documentVisible: true, windowFocused: true });
    const { presentLiveMessageNotification } = await import("./notificationPresentation");

    void presentLiveMessageNotification(event(), context({ isActiveConversation: true }), sinks());
    await flush();

    expect(mockPlayNotificationSound).toHaveBeenCalledExactlyOnceWith("in-conversation");
  });

  // The mirror of the visibility transition covered in useChatSidebar: the
  // window keeps the conversation on screen and simply loses focus between two
  // events. Attention is read per event, from the browser, so there is no state
  // to go stale and no listener to have missed the change.
  it("stops being in-conversation once the window loses focus between events", async () => {
    setAttention({ documentVisible: true, windowFocused: true });
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();
    const attended = context({ isActiveConversation: true });

    void presentLiveMessageNotification(event({ eventId: "focused" }), attended, surfaces);
    await flush();
    expect(mockPlayNotificationSound).toHaveBeenCalledExactlyOnceWith("in-conversation");

    setAttention({ documentVisible: true, windowFocused: false });
    void presentLiveMessageNotification(
      event({ eventId: "blurred", policy: policy({ named_user_ids: [currentUserId] }) }),
      attended,
      surfaces,
    );
    await flush();

    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(2);
    expect(mockPlayNotificationSound).toHaveBeenLastCalledWith("mention");
  });

  it("stays silent in the attended conversation for the reader's own message", async () => {
    expect(
      await soundKeyWhenAttending(
        { documentVisible: true, windowFocused: true },
        { senderId: currentUserId },
      ),
    ).toBeUndefined();
  });

  // Mute and do-not-disturb reach the browser as a central deny on this
  // channel. Attending the conversation is not a way around one.
  it("stays silent in the attended conversation when the policy denied sound", async () => {
    expect(
      await soundKeyWhenAttending(
        { documentVisible: true, windowFocused: true },
        { policy: policy({ in_app: "deny", sound: "deny" }) },
      ),
    ).toBeUndefined();
  });

  it("stays silent in the attended conversation when the chime preference is off", async () => {
    mockGetSoundNotificationMode.mockReturnValue("off");
    expect(
      await soundKeyWhenAttending({ documentVisible: true, windowFocused: true }),
    ).toBeUndefined();
  });
});

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
    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);
  });

  // Case A: the conversation is open, right here, and the reader is looking.
  // Since #829 that is heard — quietly — and still never drawn: a toast over
  // the message the reader is watching announces what is already on screen.
  it("only chimes for the conversation this tab is already showing", async () => {
    const { presentLiveMessageNotification, PRESENTATION_CLAIM_HOLD_MS } =
      await import("./notificationPresentation");
    const surfaces = sinks();

    const claim = presentLiveMessageNotification(
      event(),
      context({ isActiveConversation: true }),
      surfaces,
    );
    await vi.advanceTimersByTimeAsync(PRESENTATION_CLAIM_HOLD_MS);

    await expect(claim).resolves.toBe("acquired");
    expect(surfaces.showInApp).not.toHaveBeenCalled();
    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
    expect(mockPlayNotificationSound).toHaveBeenCalledExactlyOnceWith("in-conversation");
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
    expect(mockPlayNotificationSound).not.toHaveBeenCalled();
    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
  });

  // A suppressed event never reaches the lock at all.
  it("does not claim an event it would present nothing for", async () => {
    const locks = installLockManager();
    const { presentLiveMessageNotification } = await import("./notificationPresentation");

    await presentLiveMessageNotification(
      event({ policy: policy({ in_app: "deny", sound: "deny", web_push: "deny" }) }),
      context(),
      sinks(),
    );

    expect(locks.request).not.toHaveBeenCalled();
  });

  it("keeps the toast when the chime preference is off — they are separate channels", async () => {
    mockGetSoundNotificationMode.mockReturnValue("off");
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    void presentLiveMessageNotification(event(), context(), surfaces);
    await flush();

    expect(surfaces.showInApp).toHaveBeenCalledTimes(1);
    expect(mockPlayNotificationSound).not.toHaveBeenCalled();
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
    expect(mockPlayNotificationSound).not.toHaveBeenCalled();
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

    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);
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
    expect(mockPlayNotificationSound).not.toHaveBeenCalled();
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
    mockPlayNotificationSound.mockImplementationOnce(() => {
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
    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);
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
    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);

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
    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);
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
    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);

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
    expect(mockPlayNotificationSound).not.toHaveBeenCalled();
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
    expect(mockPlayNotificationSound).not.toHaveBeenCalled();
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
    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);
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
      event({
        eventId: "message-1",
        policy: policy({ in_app: "deny", sound: "deny", web_push: "deny" }),
      }),
      context(),
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

    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);
  });

  // The conversation being attended is the case a burst is most likely in — a
  // fast exchange the reader is part of — and it goes through the same central
  // cooldown as every other class, not one of its own. #829 asks for exactly
  // that: a short sound repeating freely is a wall of audio.
  it("collapses a rajada in the attended conversation through the same cooldown", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    for (let index = 0; index < 50; index += 1) {
      void presentLiveMessageNotification(
        event({ eventId: `attended-${index}` }),
        context({ isActiveConversation: true }),
        surfaces,
      );
    }
    await flush();

    expect(mockPlayNotificationSound).toHaveBeenCalledExactlyOnceWith("in-conversation");
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

    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(2);
  });

  it("stays silent at the last instant of the window", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    void presentLiveMessageNotification(event({ eventId: "first" }), context(), surfaces);
    await flush();
    await vi.advanceTimersByTimeAsync(SOUND_COOLDOWN_MS - 1);
    void presentLiveMessageNotification(event({ eventId: "inside-window" }), context(), surfaces);
    await flush();

    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);
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

    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(2);
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

    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(2);
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
    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);
  });

  // The resolved notification class is what splits the budget (#826), so an
  // event the author marked urgent is not spent by the room's ordinary traffic.
  it("still chimes for an urgent message in the same busy room", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    await burst(presentLiveMessageNotification, surfaces, 20);
    void presentLiveMessageNotification(
      event({ eventId: "urgent", priority: "urgent" }),
      context(),
      surfaces,
    );
    await flush();

    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(2);
  });

  // #826 is explicit that `important` introduces no class of its own, and the
  // budget is the observable consequence: it shares the ordinary message's.
  it("does not give an important message a sound budget of its own", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    await burst(presentLiveMessageNotification, surfaces, 20);
    void presentLiveMessageNotification(
      event({ eventId: "important", priority: "important" }),
      context(),
      surfaces,
    );
    await flush();

    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);
  });

  // The class says how loud an event would be, never whether it may be heard.
  // A central deny ends the matter, and the strongest class cannot reopen it.
  it("does not let an urgent class execute a surface the policy denied", async () => {
    const { presentLiveMessageNotification } = await import("./notificationPresentation");
    const surfaces = sinks();

    const disposition = await presentLiveMessageNotification(
      event({
        eventId: "denied",
        priority: "urgent",
        policy: policy({ in_app: "deny", sound: "deny" }),
      }),
      context(),
      surfaces,
    );

    expect(disposition).toBe("suppressed");
    expect(surfaces.showInApp).not.toHaveBeenCalled();
    expect(mockPlayNotificationSound).not.toHaveBeenCalled();
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

    expect(mockPlayNotificationSound.mock.calls.length).toBeLessThanOrEqual(2);
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
    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(1);

    auth.clearTokens();
    auth.setTokens("session-b");
    void present(event({ eventId: "message-2" }), context(), sinks());
    await flush();

    expect(mockPlayNotificationSound).toHaveBeenCalledTimes(2);
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
