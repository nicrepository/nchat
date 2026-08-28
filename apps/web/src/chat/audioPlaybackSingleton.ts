/**
 * Enforces "only one audio player active per tab" (issue #670).
 *
 * A module-level singleton rather than React context or a store: every
 * AudioPlayer instance is already independent state (see AudioPlayer.tsx), so
 * the only thing that needs to be shared across a whole timeline of them is
 * "who is currently playing" — one mutable reference is the whole mechanism,
 * and it needs no provider wrapping the message list.
 */

let current: HTMLMediaElement | null = null;

/**
 * Call from a player's own `play` handler. Pauses whatever else was playing,
 * then claims the slot. A player pausing another does not fire that other
 * player's `pause` handler into a loop: `.pause()` does not call back into
 * this module, so there is nothing to re-entrantly claim.
 */
export function claimAudioPlayback(element: HTMLMediaElement): void {
  if (current !== null && current !== element) {
    current.pause();
  }
  current = element;
}

/** Call from cleanup (unmount, attachment change) once this element stops being eligible to hold the slot. */
export function releaseAudioPlayback(element: HTMLMediaElement): void {
  if (current === element) {
    current = null;
  }
}
