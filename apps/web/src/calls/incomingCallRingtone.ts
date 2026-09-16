import { playNotificationSound, stopNotificationSound } from "../notifications/notificationSound";

const RINGTONE_PREFERENCE_KEY = "nchat.notifications.calls.ringtone.enabled";
const RINGTONE_LOCK_PREFIX = "nchat.calls.ringtone.presentation.";

/**
 * How long one pass of the ringtone asset lasts, which is what decides when to
 * start the next one (issue #827).
 *
 * The Lumen incoming-call asset is a composed *phrase*, not a single ring: four
 * rings of ~1.4s followed by ~0.56s of silence, 6.00s end to end. Repeating it
 * on any shorter interval restarts the file mid-phrase and turns a ring into a
 * stutter, so the interval is the asset's own length — the pause the cadence
 * needs is already inside the file, where the sound designer put it.
 *
 * It replaces the 3.5s interval the previous asset needed (a ~1.35s motif plus
 * an externally-timed pause); the cadence a caller hears is unchanged in kind.
 */
export const RINGTONE_REPEAT_MS = 6_000;

let activeCallId: string | null = null;
let repeatTimer: number | null = null;
let releasePresentationLock: (() => void) | null = null;

/**
 * One pass, then the next.
 *
 * The callId is re-checked before every pass: a stop that lands between two
 * passes must not be followed by a sixth ring, and `activeCallId` is what makes
 * that true without the timer needing to be cancelled in the same tick.
 *
 * Playback itself is the central player's problem — see notifications/
 * notificationSound: it owns the element, the autoplay rejection and the cache,
 * and it cannot throw, which is why nothing here is wrapped.
 */
function playAndSchedule(callId: string): void {
  if (activeCallId !== callId) return;
  playNotificationSound("incoming-call");
  repeatTimer = window.setTimeout(() => {
    repeatTimer = null;
    playAndSchedule(callId);
  }, RINGTONE_REPEAT_MS);
}

// Cross-tab presentation coordination (issue #663 multi-tab check: two
// "main" tabs open for the same account both receive the same incoming-call
// event and would otherwise both play). Web Locks gives genuine,
// browser-arbitrated mutual exclusion across tabs — a hand-rolled
// localStorage "write, then read back" lease cannot: Chromium applies a
// tab's own write to that tab's local storage cache optimistically, so two
// tabs racing can each observe their own write as "confirmed" independently
// of the other. A lock is held only while this tab is actually presenting
// this callId, and is released automatically by the browser if the tab
// crashes or closes — no manual expiry/renewal/heartbeat needed. Browsers
// without Web Locks just always present, same as every tab did before #663.
function claimPresentation(callId: string): void {
  const locks = typeof navigator !== "undefined" ? navigator.locks : undefined;
  if (!locks) {
    playAndSchedule(callId);
    return;
  }
  locks
    .request(RINGTONE_LOCK_PREFIX + callId, { ifAvailable: true }, (lock) => {
      if (!lock || activeCallId !== callId) return undefined;
      return new Promise<void>((resolve) => {
        releasePresentationLock = resolve;
        playAndSchedule(callId);
      });
    })
    .catch(() => undefined);
}

export function getIncomingCallRingtoneEnabled(): boolean {
  try {
    return localStorage.getItem(RINGTONE_PREFERENCE_KEY) !== "false";
  } catch {
    return true;
  }
}

export function setIncomingCallRingtoneEnabled(enabled: boolean): void {
  try {
    localStorage.setItem(RINGTONE_PREFERENCE_KEY, String(enabled));
  } catch {
    // Best-effort local preference.
  }
  if (!enabled) stopIncomingCallRingtone();
}

export function startIncomingCallRingtone(callId: string): void {
  if (!getIncomingCallRingtoneEnabled()) {
    stopIncomingCallRingtone();
    return;
  }
  if (activeCallId === callId) return;
  stopIncomingCallRingtone();
  activeCallId = callId;
  claimPresentation(callId);
}

/**
 * Silences the ringtone and releases everything this module holds.
 *
 * Only the incoming-call sound is stopped, never every sound: a chime for a
 * message that arrived while the phone was ringing is a different surface with
 * its own permission, and a call ending is not a reason to cut it off.
 */
export function stopIncomingCallRingtone(): void {
  activeCallId = null;
  if (repeatTimer !== null) {
    window.clearTimeout(repeatTimer);
    repeatTimer = null;
  }
  if (releasePresentationLock) {
    releasePresentationLock();
    releasePresentationLock = null;
  }
  stopNotificationSound("incoming-call");
}

/**
 * The settings preview, which is the same sound through the same player.
 *
 * Exclusive because a preview is a deliberate act of listening: whatever else
 * was audible is what the reader is trying to hear past, and a second press
 * replaces the first rather than layering on it.
 */
export function playIncomingCallRingtonePreview(): void {
  playNotificationSound("incoming-call", { exclusive: true });
}
