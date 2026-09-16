/**
 * Turns a persisted conversation event into the sentence a reader sees
 * (issue #527, extended by issue #685).
 *
 * The database stores facts — an event type, an actor, target users, an old
 * name, a new name, a call id — and never a pre-formatted sentence: a
 * persisted phrase would freeze one language into the rows, so translating
 * the product later would need a data migration. The wording lives here
 * instead.
 *
 * Pure and data-only. It resolves nothing, fetches nothing and renders
 * nothing: the actor's name is already on the message (its sender, resolved
 * by the same authorized projection every other message's sender goes
 * through), a target's name is already in the payload (resolved once,
 * server-side, at write time — see ConversationEventTargetUser), and the
 * caller turns the returned parts into React text nodes.
 */

import type {
  ConversationEventPayload,
  ConversationEventTargetUser,
  ConversationEventType,
  Message,
} from "./chatTypes";

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
  /**
   * A Material Symbols Outlined ligature name (issue #685 visual pass), e.g.
   * "call". Purely decorative — the event this build cannot describe already
   * renders nothing above, so there is no icon-only fallback to invent here.
   */
  icon: string;
  /**
   * "call" gets the design's tinted, higher-emphasis pill (matching the
   * prototype's .syscall treatment); every other event keeps the existing
   * neutral one, unchanged since issue #527.
   */
  tone: "neutral" | "call";
}

/** One Material Symbols Outlined ligature per event type, chosen for what
 * the event is about rather than decoration: adding is the mirror of
 * removing, a rename is an edit, an archive is a container being closed. */
const eventIcon: Record<ConversationEventType, string> = {
  conversation_renamed: "edit",
  conversation_member_left: "logout",
  conversation_created: "forum",
  conversation_archived: "archive",
  conversation_member_added: "person_add",
  conversation_member_removed: "person_remove",
  call_started: "call",
  call_ended: "call_end",
};

/**
 * The fallback name for an actor or a target the server could not resolve.
 *
 * A deleted account, or one this reader may not see. Never the raw UUID: an
 * identifier is not a name, and showing one is worse than saying plainly that
 * the person is unknown.
 */
const unknownActor = "Alguém";

function targetName(user: ConversationEventTargetUser): string {
  return user.displayName?.trim() || unknownActor;
}

/**
 * Renders a batch of target names as one clause: every name when there are
 * two or fewer, otherwise the first two plus a count — "Ana, Bruno e mais 3
 * pessoas" — so a large bulk-add never turns into an unreadable name dump.
 */
function namesClause(names: string[]): string {
  if (names.length === 0) return "";
  if (names.length === 1) return names[0];
  if (names.length === 2) return `${names[0]} e ${names[1]}`;
  const rest = names.length - 2;
  return `${names[0]}, ${names[1]} e mais ${rest} ${rest === 1 ? "pessoa" : "pessoas"}`;
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

function createdText(actorIsViewer: boolean, actor: string, scope: SystemMessageScope): string {
  const noun = scopeNoun[scope];
  return actorIsViewer ? `Você criou o ${noun}` : `${actor} criou o ${noun}`;
}

function archivedText(actorIsViewer: boolean, actor: string, scope: SystemMessageScope): string {
  const noun = scopeNoun[scope];
  return actorIsViewer ? `Você arquivou o ${noun}` : `${actor} arquivou o ${noun}`;
}

/**
 * member.added / member.removed, viewer-aware.
 *
 * Three shapes, decided in this order because an actor acting on their own
 * membership is impossible for these two events (adding yourself, and
 * self-removal, are both refused server-side — self-removal is LeaveGroup's
 * job) but the order still matters for legibility: "you did this to others"
 * beats "this was done to you and others" beats "this was done to others".
 *
 * - The viewer is the actor: "Você {verbo} {nomes} {preposição} {noun}".
 * - The viewer is among the targets: "{ator} {verbo} você {e mais N pessoas}
 *   {preposição} {noun}" — the viewer's own name never appears in the list,
 *   since "removeu você e Ana" reads worse than naming the others alongside
 *   "você" once the reader already knows they are one of the people meant.
 * - Neither: "{ator} {verbo} {nomes} {preposição} {noun}".
 */
function memberChangeText(
  verb: "added" | "removed",
  actorIsViewer: boolean,
  actor: string,
  viewerId: string,
  scope: SystemMessageScope,
  payload: ConversationEventPayload | undefined,
): string | null {
  const targets = payload?.targetUsers ?? [];
  if (targets.length === 0) return null;
  const noun = scopeNoun[scope];
  const verbWord = verb === "added" ? "adicionou" : "removeu";
  const preposition = verb === "added" ? "ao" : "do";

  if (actorIsViewer) {
    const names = namesClause(targets.map(targetName));
    return `Você ${verbWord} ${names} ${preposition} ${noun}`;
  }

  const others = targets.filter((target) => target.userId !== viewerId);
  const viewerIsTarget = others.length < targets.length;
  if (viewerIsTarget) {
    if (others.length === 0) {
      return `${actor} ${verbWord} você ${preposition} ${noun}`;
    }
    const count = others.length;
    return `${actor} ${verbWord} você e mais ${count} ${count === 1 ? "pessoa" : "pessoas"} ${preposition} ${noun}`;
  }

  const names = namesClause(targets.map(targetName));
  return `${actor} ${verbWord} ${names} ${preposition} ${noun}`;
}

const callTypeNoun: Record<NonNullable<ConversationEventPayload["callType"]>, string> = {
  audio: "chamada de voz",
  video: "chamada de vídeo",
};

function callNoun(callType: ConversationEventPayload["callType"] | undefined): string {
  return callType ? callTypeNoun[callType] : "chamada";
}

function callStartedText(
  actorIsViewer: boolean,
  actor: string,
  payload: ConversationEventPayload | undefined,
): string {
  const noun = callNoun(payload?.callType);
  return actorIsViewer ? `Você iniciou uma ${noun}` : `${actor} iniciou uma ${noun}`;
}

/** "12 min 5 s", "5 s" (no minutes), or "12 min" (no leftover seconds). */
function formatCallDuration(totalSeconds: number): string {
  const seconds = Math.max(0, Math.floor(totalSeconds));
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  if (minutes === 0) return `${remainder} s`;
  if (remainder === 0) return `${minutes} min`;
  return `${minutes} min ${remainder} s`;
}

function callEndedText(
  actorIsViewer: boolean,
  actor: string,
  payload: ConversationEventPayload | undefined,
): string {
  const noun = callNoun(payload?.callType);
  const base = actorIsViewer ? `Você encerrou a ${noun}` : `${actor} encerrou a ${noun}`;
  const duration = payload?.callDurationSeconds;
  return duration != null ? `${base} (${formatCallDuration(duration)})` : base;
}

/**
 * The presentation for one system message, or null when there is nothing to
 * show.
 *
 * Null covers every shape this build cannot describe honestly: a message that
 * is not a system one, an event type from a newer server, a rename with no
 * new name, and a member change with no targets. The timeline renders
 * nothing for those rather than an empty line or a guess — an unknown event
 * must never break the messages around it.
 *
 * `viewerId` is the reader's own user id, used only to pick which of the
 * "você" variants applies — it never changes which event is described, only
 * how it reads. Omitting it (or passing one that matches nobody) degrades
 * gracefully to the third-party phrasing everywhere, which is what makes it
 * optional for callers, like tests, that do not have a viewer in scope.
 */
export function systemMessagePresentation(
  message: Pick<Message, "kind" | "eventType" | "eventPayload" | "senderDisplayName" | "senderId">,
  scope: SystemMessageScope,
  viewerId = "",
): SystemMessagePresentation | null {
  if (message.kind !== "system" || !message.eventType) return null;
  const actor = message.senderDisplayName.trim() || unknownActor;
  const actorIsViewer = !!viewerId && message.senderId === viewerId;
  const builders: Record<ConversationEventType, () => string | null> = {
    conversation_renamed: () => renamedText(actor, scope, message.eventPayload),
    conversation_member_left: () => memberLeftText(actor, scope),
    conversation_created: () => createdText(actorIsViewer, actor, scope),
    conversation_archived: () => archivedText(actorIsViewer, actor, scope),
    conversation_member_added: () =>
      memberChangeText("added", actorIsViewer, actor, viewerId, scope, message.eventPayload),
    conversation_member_removed: () =>
      memberChangeText("removed", actorIsViewer, actor, viewerId, scope, message.eventPayload),
    call_started: () => callStartedText(actorIsViewer, actor, message.eventPayload),
    call_ended: () => callEndedText(actorIsViewer, actor, message.eventPayload),
  };
  const text = builders[message.eventType]?.() ?? null;
  if (!text) return null;
  const eventType = message.eventType;
  const tone = eventType === "call_started" || eventType === "call_ended" ? "call" : "neutral";
  return { text, icon: eventIcon[eventType], tone };
}
