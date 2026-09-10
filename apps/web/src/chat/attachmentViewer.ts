/**
 * The request a message makes when it wants a viewer opened (issue #675).
 *
 * Separate from AttachmentViewerHost so the host file exports a component and
 * nothing else, and so a card can ask for a viewer without importing the thing
 * that renders one. What the API means, and why the viewer is hosted above the
 * timeline at all, is documented on the host.
 */

import { createContext, useContext } from "react";

import type { AttachmentImageOpenPayload } from "./AttachmentImagePreview";
import type { ChannelAttachment } from "./chatTypes";

export interface AttachmentViewerApi {
  openImage: (attachment: ChannelAttachment, payload: AttachmentImageOpenPayload) => void;
  openDocument: (attachment: ChannelAttachment, trigger: HTMLButtonElement) => void;
}

export const AttachmentViewerContext = createContext<AttachmentViewerApi | null>(null);

/**
 * The viewer API for the nearest host.
 *
 * Throws rather than degrading: a card whose Ampliar silently did nothing would
 * be a far worse bug to find than a missing provider is at first render, and
 * every path that reaches this goes through MessageAttachments, which hosts one
 * itself.
 */
export function useAttachmentViewer(): AttachmentViewerApi {
  const api = useContext(AttachmentViewerContext);
  if (!api) throw new Error("useAttachmentViewer requires an AttachmentViewerHost");
  return api;
}
