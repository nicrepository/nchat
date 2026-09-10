/**
 * The one client-side authority for *presenting* a notification (issue #749).
 *
 * Everything that interrupts the reader for an incoming message goes through
 * `presentMessageNotification`: the in-app toast, the chime, the OS-level
 * notification. No component plays audio, opens a channel, or decides on its
 * own whether an event deserves a surface — this module is the only importer of
 * messageSound.ts, and the only place the three surfaces are executed.
 *
 * What it deliberately does **not** own, because each already has an owner:
 *
 *   - the delivery decision: `notification_policy`, resolved per recipient by
 *     chat-service (issue #744) and narrowed by the local gates in
 *     soundRules.ts. Nothing here re-decides it, and nothing here can turn a
 *     `deny` into a surface;
 *   - unread, message state and WebSocket subscriptions: useChatSidebar's. A
 *     failed chime never touches a badge, which is why presentation runs after
 *     the classification and returns nothing to it;
 *   - the chime preference: soundPreference.ts, the existing single source of
 *     truth (#132/#729). No parallel key, no second storage;
 *   - Web Push and the Service Worker: unchanged. Whether the OS surface is
 *     this page's to raise is `policy.web_push`, the decision the system
 *     already made — never inferred from `Notification.permission`.
 *
 * ## At most one presentation per event, across tabs
 *
 * Several tabs of the same account receive the same `message.created` and would
 * each announce it. Exactly one may, and the exclusion is the Web Locks API —
 * the browser's own mutual exclusion, one lock per event id. It is the only
 * primitive used for this: a BroadcastChannel round has no membership, no
 * ordering and no compare-and-swap, so two tabs can finish a timed round
 * disagreeing about who won, and no amount of protocol on top makes it atomic.
 *
 * The same choice issue #663 made for the incoming-call ringtone, and for the
 * same reason: a hand-rolled localStorage lease cannot do this either, because
 * Chromium applies a tab's own write to that tab's cache optimistically and two
 * racing tabs each read their own write back as confirmed. The lock namespace
 * is separate from the call ringtone's — notification presentation and call
 * ownership are different domains with different lifetimes, and sharing a
 * leader between them would tie a chime to who happens to own a call.
 *
 * Without that primitive the claim **fails closed**: no lock manager, or a
 * request the browser refuses, means nothing is presented here, reported as
 * `unavailable`. Not duplicating outranks announcing locally without
 * coordination, and the cost is contained — the message itself, the unread
 * badge, the socket and Web Push are all decided elsewhere.
 *
 * The property this provides is **at-most-once per event id while coordination
 * is available**, and liveness is best effort. It is not exactly-once: a tab
 * that dies between winning the lock and painting has announced nothing, and no
 * browser primitive spans that gap.
 *
 * What follows from the lock, rather than from any protocol here:
 *
 *   - there is no leader and no election — a claim is per event, so a tab that
 *     closes blocks nothing and there is nothing to fail over;
 *   - a claim expires: the holder releases after PRESENTATION_CLAIM_HOLD_MS,
 *     and the browser releases it outright if the tab dies. No claim goes stale
 *     and no lock is ever permanent;
 *   - nothing is subscribed and no channel is opened, so a remount — React
 *     StrictMode included — has no handler to duplicate and none to leak;
 *   - no state is kept between events, and nothing crosses a tab boundary but
 *     the lock name, which is the message id and nothing else.
 *
 * A claim is not a record of what has been shown, and does not need to be:
 * chat-service states its realtime contract as "in-process best-effort, no
 * durability, no replay" (hub.go, PublishMessageCreated), and a (re)subscribe
 * returns a presence snapshot only. A tab that was disconnected when a message
 * was published never receives that event; it catches up through the sidebar's
 * REST refetch, which announces nothing. So the only concurrency is the live
 * fan-out to tabs connected at publish time, which is exactly what the lock and
 * its hold cover.
 */

import { showBrowserMessageNotification } from "./browserNotification";
import type { InAppAlert } from "./InAppMessageAlert";
import { buildMessagePreview } from "./messagePreview";
import { playMessageSound } from "./messageSound";
import { getSoundNotificationMode } from "./soundPreference";
import {
  shouldExecuteInAppNotification,
  shouldExecuteNativeNotification,
  shouldExecuteSound,
} from "./soundRules";
import type { WSNotificationPolicy } from "./useChatWebSocket";

/**
 * How long the winning tab holds an event's claim.
 *
 * Long enough to cover the spread between tabs receiving the same fan-out over
 * their own sockets, short enough that nothing accumulates. It is not a
 * deduplication window for the reader: repeats within one tab are already
 * dropped upstream by message id.
 */
export const PRESENTATION_CLAIM_HOLD_MS = 5_000;

/** Its own namespace, never the call ringtone's (see the module comment). */
const CLAIM_LOCK_PREFIX = "nchat.notifications.presentation.";

/**
 * One event, as the presentation layer needs it.
 *
 * `eventId` is the message id: stable, assigned by the server, and the same
 * value in every tab — which is what makes it usable as the claim's identity.
 * Only it is ever shared between tabs, and only as a lock name.
 */
export interface MessageNotificationEvent {
  eventId: string;
  targetKind: "channel" | "dm";
  targetId: string;
  senderId: string;
  senderDisplayName: string;
  senderAvatarUrl?: string;
  bodyText: string;
  conversationName: string;
  /** The central decision (#744). Absent means a server that predates it. */
  policy: WSNotificationPolicy | undefined;
}

/** The strictly local facts no server observes. */
export interface MessagePresentationContext {
  currentUserId: string;
  isMutedConversation: boolean;
  /** This tab is showing the conversation the event belongs to. */
  isActiveConversation: boolean;
}

/**
 * Where a presentation lands. Both are the app's own: the toast is rendered by
 * the shell, and navigation goes through the router. Neither takes a URL from
 * the event — the path is built here from a typed target — and neither is an
 * authorisation: the route and the backend still decide what may be opened.
 */
export interface MessagePresentationSinks {
  showInApp: (alert: InAppAlert) => void;
  navigate: (path: string) => void;
}

interface AuthorisedSurfaces {
  inApp: boolean;
  native: boolean;
  sound: boolean;
}

/** Whether this window is in front of the reader right now. */
function isWindowFocused(): boolean {
  return (
    document.visibilityState === "visible" &&
    typeof document.hasFocus === "function" &&
    document.hasFocus()
  );
}

/**
 * Which surfaces this browser is allowed to execute, asked one channel at a
 * time. No answer is ever reused for another surface: a toast, a chime and an
 * OS notification are different interruptions, decided separately by the engine.
 */
function authorisedSurfaces(
  event: MessageNotificationEvent,
  context: MessagePresentationContext,
): AuthorisedSurfaces {
  const focused = isWindowFocused();
  const execution = {
    policy: event.policy,
    currentUserId: context.currentUserId,
    localMode: getSoundNotificationMode(),
    // Repeats are dropped by message id before an event ever reaches here.
    isDuplicate: false,
    isOwnMessage: event.senderId === context.currentUserId,
    isMutedConversation: context.isMutedConversation,
    isActiveConversation: context.isActiveConversation,
    isWindowFocused: focused,
  };
  return {
    inApp: shouldExecuteInAppNotification(execution),
    // An OS notification while this window is already in front of the reader
    // interrupts them with what they are looking at. Where they are looking
    // decides which mechanism makes sense, never whether it is allowed.
    native: shouldExecuteNativeNotification(execution) && !focused,
    sound: shouldExecuteSound(execution),
  };
}

/** The event projected onto what the toast renders — a preview, not a message. */
function toAlert(event: MessageNotificationEvent): InAppAlert {
  return {
    messageId: event.eventId,
    targetKind: event.targetKind,
    targetId: event.targetId,
    senderDisplayName: event.senderDisplayName,
    senderAvatarUrl: event.senderAvatarUrl,
    // Plain text: mention tokens become their label, and the toast renders the
    // result as data. No body ever reaches a surface as markup.
    bodyText: buildMessagePreview(event.bodyText),
    conversationName: event.conversationName,
  };
}

/** Reports whether the OS surface appeared — which is what tells the chime it would be redundant. */
function raiseNative(event: MessageNotificationEvent, sinks: MessagePresentationSinks): boolean {
  try {
    return showBrowserMessageNotification({
      targetKind: event.targetKind,
      targetId: event.targetId,
      senderDisplayName: event.senderDisplayName,
      bodyText: event.bodyText,
      onNavigate: sinks.navigate,
    }).shown;
  } catch {
    // The module already guards itself; this is defence in depth — a surface
    // must never break the caller.
    return false;
  }
}

/**
 * Executes whichever surfaces were authorised.
 *
 * The chime is last and is swallowed whole: playMessageSound() already never
 * throws and already absorbs the autoplay rejection, and this is the guarantee
 * that a failed sound stays a failed sound — it cannot alter unread, cannot
 * stop the event being processed, and is never retried.
 */
function executeSurfaces(
  event: MessageNotificationEvent,
  surfaces: AuthorisedSurfaces,
  sinks: MessagePresentationSinks,
): void {
  if (surfaces.inApp) sinks.showInApp(toAlert(event));
  const nativeShown = surfaces.native && raiseNative(event, sinks);
  if (nativeShown || !surfaces.sound) return;
  try {
    playMessageSound();
  } catch {
    // Swallowed on purpose: see above.
  }
}

/**
 * The browser's lock manager, or nothing.
 *
 * "Nothing" covers both a browser without Web Locks and one that refuses to
 * hand it over (an insecure origin, a permissions policy) — the caller treats
 * the two identically, because both mean this tab coordinates with no one.
 */
function lockManager(): Pick<LockManager, "request"> | undefined {
  try {
    return navigator.locks;
  } catch {
    return undefined;
  }
}

/** What a claim attempt established. Every value is an outcome, never a guess. */
export type PresentationDisposition =
  /** No surface was authorised for this event; no claim was attempted. */
  | "suppressed"
  /** This tab holds the claim and presented. */
  | "acquired"
  /** Another tab holds the claim for this event; this tab presented nothing. */
  | "contended"
  /**
   * No exclusion could be obtained — Web Locks absent, or the request refused.
   * Nothing was presented: see claimAndPresent.
   */
  | "unavailable";

/** The claim's lifetime, and the whole of what it protects. See claimAndPresent. */
function holdClaim(): Promise<void> {
  return new Promise<void>((resolve) => {
    globalThis.setTimeout(resolve, PRESENTATION_CLAIM_HOLD_MS);
  });
}

/**
 * Presents on the one tab that wins the event's lock, and on no other.
 *
 * `ifAvailable` is what makes this a claim rather than a queue: a tab that does
 * not get the lock is handed `null` at once and presents nothing, instead of
 * waiting its turn and announcing the same message a second time later.
 *
 * The winner keeps the lock for PRESENTATION_CLAIM_HOLD_MS. That window is not
 * a memory of what has been shown — it is how long the claim lasts, and it
 * exists because tabs receive one fan-out over separate sockets and a few
 * milliseconds apart. Releasing the instant the toast appeared would let the
 * next tab's copy acquire and announce the same message again. The browser
 * releases it outright if the tab dies, so no claim can go stale.
 *
 * Both failure modes fail closed, and neither is reported as success: with no
 * lock manager, and with a request the browser refuses, nothing is presented
 * and the caller is told `unavailable`. See the module comment for why nothing
 * local stands in for the lock.
 */
async function claimAndPresent(
  eventId: string,
  present: () => void,
): Promise<PresentationDisposition> {
  const locks = lockManager();
  if (!locks) return "unavailable";
  try {
    return await locks.request(
      CLAIM_LOCK_PREFIX + eventId,
      { mode: "exclusive", ifAvailable: true },
      async (lock): Promise<PresentationDisposition> => {
        if (!lock) return "contended";
        present();
        await holdClaim();
        return "acquired";
      },
    );
  } catch {
    // A refused lock is an absent guarantee, not a reason to present anyway.
    return "unavailable";
  }
}

/**
 * Presents one incoming message, if any surface authorised it and this tab wins
 * the event's claim.
 *
 * Resolves with what the claim established, and never rejects — so a caller
 * that has no use for the outcome may ignore the promise. Unread, message state
 * and the socket are decided upstream from the same event and do not depend on
 * whether this tab was the one that announced it.
 */
export async function presentMessageNotification(
  event: MessageNotificationEvent,
  context: MessagePresentationContext,
  sinks: MessagePresentationSinks,
): Promise<PresentationDisposition> {
  const surfaces = authorisedSurfaces(event, context);
  // Nothing to present is not a claim: a tab with the conversation open must
  // not take the event away from a tab that would actually announce it.
  if (!surfaces.inApp && !surfaces.native && !surfaces.sound) return "suppressed";
  return await claimAndPresent(event.eventId, () => executeSurfaces(event, surfaces, sinks));
}
