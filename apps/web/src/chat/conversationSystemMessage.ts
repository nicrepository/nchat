/**
 * Turns a persisted conversation event into the sentence a reader sees
 * (issue #527).
 *
 * The database stores facts — an event type, an old name, a new name — and never
 * a pre-formatted sentence: a persisted phrase would freeze one language into
 * the rows, so translating the product later would need a data migration. The
 * wording lives here instead.
 *
 * Pure and data-only. It resolves nothing, fetches nothing and renders nothing:
 * the actor's name is already on the message (its sender, resolved by the same
 * authorized projection every other message's sender goes through), and the
 * caller turns the returned parts into React text nodes.
 */

import type { ConversationEventPayload, ConversationEventType, Message } from "./chatTypes";

/** How the sentence refers to the place the event happened in. */
export type SystemMessageScope = "channel" | "group" | "dm";

const scopeNoun: Record<SystemMessageScope, string> = {
  channel: "canal",
  group: "grupo",
  dm: "conversa",
};

/**
 * Which noun a conversation's system messages use.
 *
 * Derived from the details discriminant, which is the server's own value — a
 * group is a `chat.dm_conversations` row of type 'group' — so a group never
 * reads as a channel and a 1:1 never reads as either.
 */
export function systemScopeFor(
  detailsKind: "channel" | "group" | "direct" | null,
  targetKind: "channel" | "dm",
): SystemMessageScope {
  if (detailsKind === "group") return "group";
  return targetKind === "channel" ? "channel" : "dm";
}

/**
 * The sentence, split so the caller can render it as plain text.
 *
 * A single string rather than a template with slots, because every part of it —
 * including the names — is text and must be escaped identically. There is
 * deliberately nothing here a renderer could be tempted to treat as markup.
 */
export interface SystemMessagePresentation {
  text: string;
}

function renamedText(
  actor: string,
  scope: SystemMessageScope,
  payload: ConversationEventPayload | undefined,
): string | null {
  const oldName = payload?.oldName;
  const newName = payload?.newName;
  if (!newName) return null;
  const noun = scopeNoun[scope];
  // Both names when the old one is known; only the new one when it is not, which
  // is what a rename from an untitled group looks like. Never "de  para X".
  return oldName
    ? `${actor} renomeou o ${noun} de ${oldName} para ${newName}`
    : `${actor} renomeou o ${noun} para ${newName}`;
}

function memberLeftText(actor: string, scope: SystemMessageScope): string {
  return `${actor} saiu do ${scopeNoun[scope]}`;
}

/**
 * The fallback name for an actor the server could not resolve.
 *
 * A deleted account, or one this reader may not see. Never the raw UUID: an
 * identifier is not a name, and showing one is worse than saying plainly that
 * the person is unknown.
 */
const unknownActor = "Alguém";

/**
 * The presentation for one system message, or null when there is nothing to
 * show.
 *
 * Null covers every shape this build cannot describe honestly: a message that
 * is not a system one, an event type from a newer server, and a rename with no
 * new name. The timeline renders nothing for those rather than an empty line or
 * a guess — an unknown event must never break the messages around it.
 */
export function systemMessagePresentation(
  message: Pick<Message, "kind" | "eventType" | "eventPayload" | "senderDisplayName">,
  scope: SystemMessageScope,
): SystemMessagePresentation | null {
  if (message.kind !== "system" || !message.eventType) return null;
  const actor = message.senderDisplayName.trim() || unknownActor;
  const builders: Record<ConversationEventType, () => string | null> = {
    conversation_renamed: () => renamedText(actor, scope, message.eventPayload),
    conversation_member_left: () => memberLeftText(actor, scope),
  };
  const text = builders[message.eventType]?.() ?? null;
  return text ? { text } : null;
}
