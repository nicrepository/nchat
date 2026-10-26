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

import { useState } from "react";

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

/** The content endpoint returns the native original, including audio. */
export function attachmentDownloadFilename(attachment: ChannelAttachment): string {
  return attachment.filename || "arquivo";
}

export type AttachmentDownloadState = "idle" | "loading" | "failed";

/**
 * One attachment's Baixar, with the state every surface that offers it shows:
 * busy while the bytes travel, a failure afterwards. Shared by the message card
 * and the details panel's recent files (issue #897), so both save the same
 * name through the same route and neither can start a second download while
 * one is running.
 *
 * Offer it only for a clean attachment; file-service refuses anything else
 * regardless, and a refusal lands here as "failed".
 */
export function useAttachmentDownload(attachment: ChannelAttachment): {
  state: AttachmentDownloadState;
  download: () => Promise<void>;
} {
  const [state, setState] = useState<AttachmentDownloadState>("idle");

  const download = async () => {
    if (state === "loading") return;
    setState("loading");
    try {
      await saveAttachmentToDisk(attachment.id, attachmentDownloadFilename(attachment));
      setState("idle");
    } catch {
      // No server text is surfaced: it may carry detail that does not belong
      // in the UI, and every failure means the same thing here.
      setState("failed");
    }
  };

  return { state, download };
}
