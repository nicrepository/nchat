/**
 * Notification chime for new incoming messages.
 *
 * Playback is best-effort: browsers reject play() before any user gesture on
 * the page (autoplay policy), which is a routine outcome the first time a tab
 * receives a message with no prior click/keypress — this must never throw or
 * block the caller, which is why every path here is swallowed.
 */

const MESSAGE_SOUND_URL = "/sounds/message-received.wav";

let sharedAudio: HTMLAudioElement | null = null;

function getSharedAudio(): HTMLAudioElement | null {
  if (sharedAudio) return sharedAudio;
  try {
    const audio = new Audio(MESSAGE_SOUND_URL);
    audio.preload = "auto";
    sharedAudio = audio;
    return audio;
  } catch {
    return null;
  }
}

export function playMessageSound(): void {
  const audio = getSharedAudio();
  if (!audio) return;
  try {
    audio.currentTime = 0;
    audio.play()?.catch(() => {
      // Autoplay restriction or similar — expected, not an error to surface.
    });
  } catch {
    // Defensive: a synchronous throw here must never break message handling.
  }
}
