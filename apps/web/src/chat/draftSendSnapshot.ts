/**
 * The send snapshot — what one send takes out of a conversation's draft, and
 * the draft that is left once the server acknowledges it (issue #929).
 *
 * Pure domain of useConversationDrafts, kept apart from the store so the
 * reconciliation is a function anyone can read and test in isolation:
 * `consumeSnapshot(current, snapshot) -> next`. The store applies it as one
 * mutation; the composer only ever captures a snapshot at submit and hands
 * it back on `sent`.
 *
 * Every field is matched by *identity*, never by value: a text revision, a
 * message id, an attachment's localId, a recording's id. The draft's global
 * `revision` is deliberately not used — it is bumped by the send's own
 * cleanup too (#875), so it cannot say whether the reader has moved on.
 */

import type { ConversationDraft } from "./conversationDraft";

/**
 * What a send carried out of its draft, by identity — never by value
 * (issue #929). Captured at submit and handed back on the confirmed `sent`,
 * so the acknowledgement consumes exactly what went out and nothing the
 * reader composed while the request was in flight.
 */
export interface SendSnapshot {
  /** The draft the send was issued from; the acknowledgement reconciles only this one. */
  draftKey: string;
  /** `textRevision` at submit; null when the send carried no text (a voice note). */
  textRevision: number | null;
  replyToMessageId: string | null;
  /** localIds of the attachments the send published. */
  attachmentLocalIds: readonly string[];
  /** `DraftVoiceMessage.id` at submit; null when the send carried no voice. */
  voiceId: string | null;
}

/** Which parts of the draft a send is about to carry (the reply always goes). */
export interface SendParts {
  text: boolean;
  attachmentLocalIds: readonly string[];
  voice: boolean;
}

/** The identity of what `parts` of `draft` a send is carrying (issue #929). */
export function sendSnapshotOf(
  draftKey: string,
  draft: ConversationDraft | undefined,
  parts: SendParts,
): SendSnapshot {
  return {
    draftKey,
    textRevision: parts.text ? (draft?.textRevision ?? 0) : null,
    replyToMessageId: draft?.replyToMessageId ?? null,
    attachmentLocalIds: [...parts.attachmentLocalIds],
    voiceId: parts.voice ? (draft?.voiceMessage?.id ?? null) : null,
  };
}

/**
 * The draft after the send `snapshot` describes was confirmed (issue #929).
 *
 * Pure and deterministic: each field is consumed only while it still holds
 * exactly what the snapshot captured — the same text revision, the same
 * reply, the same attachment identities, the same recording — and preserved
 * otherwise, because a mismatch means the reader has moved on to the next
 * message and this acknowledgement has no claim on it.
 */
export function consumeSnapshot(
  draft: ConversationDraft,
  snapshot: SendSnapshot,
): ConversationDraft {
  const sent = new Set(snapshot.attachmentLocalIds);
  return {
    ...draft,
    text: draft.textRevision === snapshot.textRevision ? null : draft.text,
    replyToMessageId:
      draft.replyToMessageId === snapshot.replyToMessageId ? null : draft.replyToMessageId,
    attachments: draft.attachments.filter((item) => !sent.has(item.localId)),
    voiceMessage: draft.voiceMessage?.id === snapshot.voiceId ? null : draft.voiceMessage,
  };
}
