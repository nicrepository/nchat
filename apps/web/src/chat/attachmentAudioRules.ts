/**
 * Eligibility and semantics for inline audio playback (issue #670).
 *
 * Mirrors attachmentVideoRules.ts: the content route needs an Authorization
 * header no `<audio src>` can send, so eligible bytes are fetched like any
 * other authenticated request and played from a blob URL — the whole file in
 * memory, bounded by MAX_INLINE_AUDIO_BYTES for the same reason the video cap
 * exists. See attachmentVideoRules.ts for the full trade-off.
 *
 * # Why "is this audio" is not just `contentType.startsWith("audio/")`
 *
 * A voice message recorded by this app's own composer is usually *not*
 * detected as `audio/*` at all: MediaRecorder's audio-only output is still
 * wrapped in a WebM or MP4 container, and file-service's sniffer (Go's
 * net/http.DetectContentType, which does not parse tracks) reports the
 * container's ordinary type — `video/webm` or `video/mp4` — exactly as it
 * would for a real video. A real .ogg file lands on `application/ogg` for the
 * same reason: the sniffer's table has no separate "audio/ogg" entry.
 *
 * So `isAudioAttachment` also trusts the server's explicit `audioKind` tag
 * (never inferred — see chatTypes.ts) for exactly the recordings that MIME
 * sniffing cannot tell from a video. It never uses `audioKind` to *grant*
 * anything: the malware-scan gate and the size cap below are unchanged.
 */

import type { ChannelAttachment } from "./chatTypes";

/** The largest attachment loaded for inline audio playback. See module doc. */
export const MAX_INLINE_AUDIO_BYTES = 50 * 1024 * 1024;

const AUDIO_CONTENT_TYPES = new Set([
  "audio/mpeg",
  "audio/ogg",
  "audio/wav",
  "audio/wave",
  "audio/x-wav",
  "application/ogg",
]);

/**
 * Reports whether an attachment's content is playable audio: either a
 * recognised audio container, or a composer recording explicitly tagged
 * `audioKind: "voice"` by the server (see module doc for why the two checks
 * are both needed). Never derived from `filename`.
 */
export function isAudioAttachment(attachment: ChannelAttachment): boolean {
  return (
    attachment.audioKind === "voice" ||
    AUDIO_CONTENT_TYPES.has(attachment.contentType.toLowerCase())
  );
}

/**
 * Reports whether this attachment is a voice message — the one fact this
 * feature never infers from filename, extension or content type. It is
 * purely `audioKind`, the server's own explicit tag.
 */
export function isVoiceMessage(attachment: ChannelAttachment): boolean {
  return attachment.audioKind === "voice";
}

/**
 * Reports whether a player may be drawn. `status === "clean"` mirrors the
 * malware-scan gate the same way canPlayInline does for video: the server
 * refuses the content route for anything else, so this only avoids spending
 * a request to be told what the metadata already said.
 */
export function canPlayAudioInline(attachment: ChannelAttachment): boolean {
  return (
    attachment.status === "clean" &&
    isAudioAttachment(attachment) &&
    attachment.size > 0 &&
    attachment.size <= MAX_INLINE_AUDIO_BYTES
  );
}
