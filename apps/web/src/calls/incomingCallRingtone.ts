const RINGTONE_URL = "/sounds/incoming-call.wav";
const RINGTONE_PREFERENCE_KEY = "nchat.notifications.calls.ringtone.enabled";
const RINGTONE_LOCK_PREFIX = "nchat.calls.ringtone.presentation.";

// The selected ~1.35s motif plus a ~2.15s pause before repeating — a ~3.5s
// total cycle, in the middle of the target 3-4s ringing cadence.
export const RINGTONE_REPEAT_MS = 3_500;

let audio: HTMLAudioElement | null = null;
let activeCallId: string | null = null;
let repeatTimer: number | null = null;
let releasePresentationLock: (() => void) | null = null;

function getAudio(): HTMLAudioElement | null {
  if (audio) return audio;
  try {
    audio = new Audio(RINGTONE_URL);
    audio.preload = "auto";
    return audio;
  } catch {
    return null;
  }
}

function playOnce(player: HTMLAudioElement): void {
  try {
    player.currentTime = 0;
  } catch {
    // Best-effort reset.
  }
  try {
    player.play()?.catch(() => undefined);
  } catch {
    // Browser/media failures must never affect call lifecycle.
  }
}

function playAndSchedule(callId: string): void {
  const player = getAudio();
  if (!player || activeCallId !== callId) return;
  playOnce(player);
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
  if (!audio) return;
  try {
    audio.pause();
  } catch {
    // Best-effort pause.
  }
  try {
    audio.currentTime = 0;
  } catch {
    // Best-effort reset.
  }
}

export function playIncomingCallRingtonePreview(): void {
  const player = getAudio();
  if (player) playOnce(player);
}
