/**
 * Saving an attachment's bytes to the viewer's disk (RF-32; voice messages,
 * issue #740).
 *
 * There is exactly one way to obtain attachment bytes in this app, and it is
 * the one used here: the authenticated content route, whose Authorization
 * header no anchor can send. file-service re-checks the caller's access to the
 * conversation and the malware-scan verdict on every request, so this module
 * decides nothing about permission — it only turns an already-authorised
 * response into a file the browser saves.
 *
 * The object URL exists for the length of the click that created it and is
 * revoked in the same task: nothing produced here can be shared, bookmarked or
 * replayed, and no storage key, bucket path or signed URL is ever seen by this
 * layer at all.
 */

import type { ChannelAttachment } from "./chatTypes";
import { fetchAttachmentContent } from "./filesApi";

/**
 * Fetches one approved attachment and hands it to the browser as a download.
 *
 * Rejects on any failure — the caller decides what to say about it, since a
 * server message may carry detail that does not belong in the UI.
 */
export async function saveAttachmentToDisk(attachmentId: string, filename: string): Promise<void> {
  const blob = await fetchAttachmentContent(attachmentId);
  const url = URL.createObjectURL(blob);
  try {
    const anchor = document.createElement("a");
    anchor.href = url;
    // An attribute value React never renders as markup and the browser never
    // resolves as a path.
    anchor.download = filename;
    anchor.click();
  } finally {
    URL.revokeObjectURL(url);
  }
}

/** `YYYY-MM-DD-HHMM` in the viewer's own timezone, or null for an unusable date. */
function recordedAtStamp(createdAt: string): string | null {
  const at = new Date(createdAt);
  if (Number.isNaN(at.getTime())) return null;
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}-${pad(at.getHours())}${pad(at.getMinutes())}`;
}

/** The real container extension, or null when the stored name carries none. */
function extensionOf(filename: string): string | null {
  return /\.([a-z0-9]{1,8})$/i.exec(filename)?.[1].toLowerCase() ?? null;
}

/**
 * A readable name for a saved voice message.
 *
 * Every recording is stored as `voice-message.<ext>` (see useVoiceRecorder), so
 * saving several of them would otherwise produce one name the browser has to
 * disambiguate for the viewer. `sentAt` — the *message's* timestamp, since an
 * attachment of a message carries none of its own (see chatTypes'
 * parseMessageAttachment) — distinguishes them.
 *
 * The extension is the stored one and nothing else: it names the container the
 * bytes actually are — WebM, Ogg or MP4 — and no byte is converted to justify a
 * friendlier one. Every part of the result is constructed here (a fixed prefix,
 * digits and hyphens, plus an extension matched as `[a-z0-9]{1,8}`), so no
 * character of a server- or client-supplied name survives into it.
 */
export function voiceMessageFilename(attachment: ChannelAttachment, sentAt: string): string {
  const stamp = recordedAtStamp(sentAt);
  const base = stamp ? `mensagem-de-voz-${stamp}` : "mensagem-de-voz";
  const extension = extensionOf(attachment.filename);
  return extension ? `${base}.${extension}` : base;
}
