/**
 * A conversation's draft — the complete state of what has not been sent yet
 * (issues #769, #929) — and the pure rules the store applies to it.
 *
 * Kept apart from useConversationDrafts so the shape and its invariants
 * (what counts as empty, what a hydrated draft looks like, who releases a
 * recording's preview URL) can be read and tested without the React hook
 * around them.
 */

import type { AttachmentUploadItem } from "./useAttachmentUpload";
import type { TTNode } from "./tiptapSerializer";
import type { DraftPersistencePayload } from "./chatDraftPersistence";

export interface DraftVoiceMessage {
  /**
   * Local identity of this recording (issue #929). What a SendSnapshot
   * carries, so the acknowledgement of one recording can never consume a
   * later one that happens to sit in the same slot.
   */
  id: string;
  blob: Blob;
  previewUrl: string;
  durationMs: number;
  mimeType: string;
}

export interface ConversationDraft {
  text: TTNode | null;
  attachments: AttachmentUploadItem[];
  voiceMessage: DraftVoiceMessage | null;
  replyToMessageId: string | null;
  /**
   * Monotonic; bumped by every mutation of any field. Kept for persistence
   * and GC bookkeeping (#769) — deliberately *not* the identity a send
   * consumes by (issues #875, #929): consuming a reply or an attachment
   * bumps it too, so it cannot tell "the reader typed" from "the send
   * cleaned up after itself".
   */
  revision: number;
  /**
   * Bumped by setText only — once per TipTap onUpdate, the same signal
   * useChatEditor's textRevisionRef counts, so the two agree by
   * construction. The text identity a SendSnapshot captures (issue #929).
   */
  textRevision: number;
  updatedAt: number;
}

export const emptyDraft = (): ConversationDraft => ({
  text: null,
  attachments: [],
  voiceMessage: null,
  replyToMessageId: null,
  revision: 0,
  textRevision: 0,
  updatedAt: Date.now(),
});

/** A draft restored from sessionStorage: text and reply only, nothing sent yet. */
export const hydratedDraft = (payload: DraftPersistencePayload): ConversationDraft => ({
  ...emptyDraft(),
  text: payload.text,
  replyToMessageId: payload.replyToMessageId,
  updatedAt: payload.updatedAt,
});

/** No mention, no non-blank text anywhere in the document. */
export function isTextMeaningful(node: TTNode | null): boolean {
  if (!node) return false;
  if (node.type === "mention") return true;
  if (node.text && node.text.trim().length > 0) return true;
  return (node.content ?? []).some(isTextMeaningful);
}

/** Draft #769 "REGRA DE DRAFT VAZIO": empty only when none of these hold. */
export function isDraftEmpty(draft: ConversationDraft): boolean {
  return (
    !isTextMeaningful(draft.text) &&
    draft.attachments.length === 0 &&
    draft.voiceMessage === null &&
    !draft.replyToMessageId
  );
}

/**
 * The store owns a recording's preview URL from the moment the recording is
 * handed to it: revoked exactly when the recording leaves the draft —
 * replaced, discarded, consumed by a send, or cleared with the draft.
 */
export function releaseVoice(
  previous: DraftVoiceMessage | null,
  next: DraftVoiceMessage | null,
): void {
  if (previous && previous.id !== next?.id) URL.revokeObjectURL(previous.previewUrl);
}
