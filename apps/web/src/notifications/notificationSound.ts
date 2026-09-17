/**
 * The one place a notification sound becomes an `HTMLAudioElement` (issue #827).
 *
 * Before this module every surface that wanted to be heard built its own audio:
 * a URL string in `chat/messageSound.ts`, another in `calls/incomingCallRingtone.ts`,
 * each with its own `new Audio(...)`, its own cache, and its own idea of what a
 * rejected `play()` means. Three copies of the same fifteen lines is three places
 * for the autoplay policy to be handled differently, and three places to forget
 * an element on the way out.
 *
 * So there is one player, and consumers name a *sound*, never a file.
 *
 * ## What this is, and what it is not
 *
 * It is **playback and lifecycle only** — layer C and D of the sound stack. It
 * decides nothing:
 *
 *   - *whether* a sound may be heard is the central policy engine's answer
 *     (`libs/go/platform/notificationpolicy`, #744), narrowed locally by
 *     `chat/soundRules`' per-surface gates and by `chat/soundPreference`;
 *   - *how hard* an event asks for attention is `chat/notificationClass` (#826);
 *   - *whether a burst collapses into one chime* is `chat/notificationBurst`'s
 *     cooldown (#750).
 *
 * None of those are re-derived here and none may be. A second throttle in this
 * module would be a competing algorithm silently disagreeing with the one that
 * already exists, so there is deliberately no timing logic below: every call
 * that reaches this module has already been authorised to be heard.
 *
 * `NotificationClass` is assignable to `NotificationSound` by construction — the
 * four message-side keys are spelled the same — so the class a consumer was
 * handed *is* the sound it plays, checked by the compiler at the call site
 * rather than restated as a mapping that could drift.
 *
 * ## Failure is silence, never an exception
 *
 * A sound is the least important thing happening when a message arrives. Every
 * path here is swallowed so that a browser which refuses to play cannot reach
 * the caller: `play()` returns a promise that browsers reject with
 * `NotAllowedError` before any user gesture on the page (the autoplay policy)
 * and with `AbortError` when a newer load interrupts it. Both are routine
 * outcomes, not errors — the rejection is consumed, nothing is retried, and
 * nothing is logged, because a console line per blocked chime is noise that
 * arrives once per tab for a condition the reader resolves by clicking.
 */

/**
 * The closed vocabulary of sounds this product can make.
 *
 * The four message-side values are `chat/notificationClass`' `NotificationClass`
 * spelled identically and on purpose; the three call-side values complete the
 * Lumen family. A key is the only thing a caller may name, which is what keeps
 * an audio source from ever being built out of anything a caller supplies.
 */
export type NotificationSound =
  | "message"
  | "in-conversation"
  | "mention"
  | "urgent"
  | "incoming-call"
  | "call-start"
  | "call-end";

/**
 * The canonical key-to-asset map — the single place a sound has a file.
 *
 * Every source is a same-origin absolute path under `/sounds/`, served from
 * `apps/web/public/sounds/` (see SOUNDS.md for provenance and hashes). There is
 * no remote origin, no hotlink and no interpolation: a source is *looked up* by
 * a key from the allowlist, never assembled from one, so no caller-supplied
 * value can become a URL this app fetches.
 */
const SOUND_SOURCES: Record<NotificationSound, string> = {
  message: "/sounds/nchat_lumen_message.wav",
  "in-conversation": "/sounds/nchat_lumen_in_conversation.wav",
  mention: "/sounds/nchat_lumen_mention.wav",
  urgent: "/sounds/nchat_lumen_urgent.wav",
  "incoming-call": "/sounds/nchat_lumen_incoming_call.wav",
  "call-start": "/sounds/nchat_lumen_call_start.wav",
  "call-end": "/sounds/nchat_lumen_call_end.wav",
};

/** Every sound, derived from the map so the two can never disagree. */
export const NOTIFICATION_SOUNDS = Object.keys(SOUND_SOURCES) as readonly NotificationSound[];

/** The asset a sound resolves to, or undefined for a key outside the allowlist. */
export function notificationSoundSource(sound: NotificationSound): string | undefined {
  return Object.hasOwn(SOUND_SOURCES, sound) ? SOUND_SOURCES[sound] : undefined;
}

/**
 * One element per sound, built on first use and reused for every later play.
 *
 * Bounded by the type: there are seven keys and a key that is not one of them
 * resolves to no source and is never cached, so the map cannot grow past seven
 * entries however many times it is called. Reuse is also what keeps a burst
 * from stacking — replaying a sound rewinds the element it already has instead
 * of adding a second one the page would then have to garbage-collect.
 */
const players = new Map<NotificationSound, HTMLAudioElement>();

function playerFor(sound: NotificationSound): HTMLAudioElement | null {
  const cached = players.get(sound);
  if (cached) return cached;
  const source = notificationSoundSource(sound);
  if (!source) return null;
  try {
    const audio = new Audio(source);
    audio.preload = "auto";
    players.set(sound, audio);
    return audio;
  } catch {
    // No Audio constructor, or one that refuses to build. Nothing is cached, so
    // a later call retries once and is just as harmless if it fails again.
    return null;
  }
}

/** Stops an element and rewinds it, each step best-effort and independent. */
function silence(audio: HTMLAudioElement): void {
  try {
    audio.pause();
  } catch {
    // A pause that throws must not cost the rewind below.
  }
  try {
    audio.currentTime = 0;
  } catch {
    // Some elements reject a seek before metadata has loaded.
  }
}

export interface PlayNotificationSoundOptions {
  /**
   * Silence every other sound before starting this one.
   *
   * For the surfaces where two sounds at once is the bug rather than the point
   * — a settings preview, which must replace whatever it is previewing over.
   * It is not a policy: a caller asks for exclusivity, this module never
   * decides that two permitted sounds may not coexist.
   */
  exclusive?: boolean;
}

/**
 * Plays one sound, from the start, and cannot throw.
 *
 * Replaying a sound that is still playing rewinds it rather than layering a
 * second copy over itself.
 */
export function playNotificationSound(
  sound: NotificationSound,
  options: PlayNotificationSoundOptions = {},
): void {
  // Before the element is resolved, so that a repeat of the *same* sound is
  // stopped and rewound by the same rule as every other one, and so that
  // exclusivity is honoured even if this sound turns out to be unplayable.
  if (options.exclusive) stopNotificationSounds();
  const audio = playerFor(sound);
  if (!audio) return;
  try {
    audio.currentTime = 0;
  } catch {
    // Best-effort rewind; a sound that cannot seek still plays.
  }
  try {
    // The rejection is consumed here and nowhere else: an unhandled one
    // surfaces as `unhandledrejection`, which is a page-level event for what is
    // routinely just a tab that has not been clicked yet.
    audio.play()?.catch(() => undefined);
  } catch {
    // A synchronous throw from play() must never reach message handling.
  }
}

/** Silences one sound, if it has ever been played. */
export function stopNotificationSound(sound: NotificationSound): void {
  const audio = players.get(sound);
  if (audio) silence(audio);
}

/** Silences every sound this module owns, keeping the elements for reuse. */
export function stopNotificationSounds(): void {
  for (const audio of players.values()) silence(audio);
}

/**
 * End of life: silence everything and drop the elements.
 *
 * For a real lifecycle boundary only — a logout, or an owner unmounting for
 * good — because it discards the decoded audio a warm cache is holding. The
 * next play rebuilds whatever it needs.
 */
export function disposeNotificationSoundPlayer(): void {
  stopNotificationSounds();
  players.clear();
}
