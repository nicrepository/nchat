/**
 * The full message DTO a message.created event carries, turned into the Message
 * the timeline draws.
 *
 * This is the primary realtime path: when the event carries a payload no GET is
 * made, so this mapping is the only thing standing between the wire format and
 * what is rendered. It is pure, and it uses exactly the same normalisers the
 * HTTP path uses, so an event and a refetch of the same message describe it
 * identically.
 */

import {
  normalizeBodyFormat,
  normalizeLinkSafety,
  normalizeMessagePriority,
  parseMessageAttachments,
  type Message,
} from "../chatTypes";
import { safeAvatarUrl } from "../chatApi";
import { parseMessageLinks } from "../messageLinks";
import type { WSMessagePayload, WSQuotePayload } from "../useChatWebSocket";

/** A message the server has already withdrawn carries no content to render. */
function payloadIsRemoved(payload: WSMessagePayload): boolean {
  return payload.is_removed || payload.status === "deleted" || Boolean(payload.deleted_at);
}

function quotedFromPayload(quoted: WSQuotePayload): NonNullable<Message["quoted"]> {
  return {
    id: quoted.id,
    authorId: quoted.author_id,
    bodyText: quoted.body ?? "",
    bodyFormat: normalizeBodyFormat(quoted.body_format),
    isRemoved: quoted.is_removed ?? false,
    deletedAt: quoted.deleted_at ?? null,
    createdAt: quoted.created_at ?? "",
    updatedAt: quoted.updated_at ?? quoted.created_at ?? "",
    linkSafetyState: normalizeLinkSafety(quoted.link_safety_state),
  };
}

export function messageFromCreatedPayload(payload: WSMessagePayload): Message {
  const removed = payloadIsRemoved(payload);
  return {
    id: payload.id,
    senderId: payload.sender_id,
    senderDisplayName: payload.sender_display_name,
    senderEmail: payload.sender_email ?? "",
    senderAvatarUrl: safeAvatarUrl(payload.sender_avatar_url),
    kind: payload.kind as Message["kind"],
    bodyText: removed ? "" : payload.body_text,
    bodyFormat: normalizeBodyFormat(payload.body_format),
    isRemoved: removed,
    status: removed ? "deleted" : "active",
    // RF-21 (issue #135). A published message may carry links the provider
    // could not produce a verdict for; this is what draws the notice. It is
    // rendered, never acted on — nothing in this client fetches a URL.
    linkSafetyState: removed ? "" : normalizeLinkSafety(payload.link_safety_state),
    deletedAt: payload.deleted_at ?? null,
    createdAt: payload.created_at,
    updatedAt: payload.updated_at,
    isEdited: Boolean(payload.edited_at),
    editCount: 0,
    editedAt: payload.edited_at ?? undefined,
    reactions: [],
    // WS create events never carry the caller's favorite state; a message
    // just created cannot be favorited yet.
    isFavorited: false,
    isForwarded: payload.is_forwarded === true,
    // Issues #821/#824, rendered by #823. Both travel on the event for the same
    // reason: a message inserted from realtime and the same message after a
    // reload must render identically, and a field carried by only one of the
    // two paths is a badge — or a confirmation request — that appears after a
    // refresh and not before. Both use the HTTP path's own normalisers, and
    // both are claims about the message that authorise nothing.
    priority: normalizeMessagePriority(payload.priority),
    acknowledgementRequired: payload.acknowledgement_required === true,
    persistentNotifications: payload.persistent_notifications === true,
    ...(removed ? {} : contentFromPayload(payload)),
  };
}

/**
 * What a removed message no longer carries: the quote it answered, its
 * attachments and its links (issue #807). Same parsers as the HTTP path, so an
 * event and a refetch describe the same message.
 */
function contentFromPayload(
  payload: WSMessagePayload,
): Pick<Message, "quoted" | "attachments" | "links"> {
  return {
    quoted: payload.quoted ? quotedFromPayload(payload.quoted) : undefined,
    attachments: parseMessageAttachments(payload.attachments),
    links: parseMessageLinks(payload.links),
  };
}
