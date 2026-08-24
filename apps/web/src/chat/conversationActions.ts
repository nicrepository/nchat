/**
 * What the sidebar's row menu may offer, derived from one target (issue #527).
 *
 * The point of this module is that there is exactly one list, built by one
 * function, for channels, group DMs and 1:1 DMs alike. Four independent menus
 * would be four places for "Sair" to appear on a direct conversation, or for a
 * rename to be offered where no backend accepts one.
 *
 * The rules are semantic, not cosmetic — an action appears when the product has
 * a real, reachable implementation of it for this kind of target, and is absent
 * otherwise. Deliberately absent today, because chat-service has no such
 * operation at all: archive, mute, and leaving a channel or a group. A menu that
 * is honestly short beats one padded with items that no-op.
 *
 * "Detalhes" is absent for a different reason: the panel is owned by the open
 * conversation (ChatMessageArea), so reaching it from the sidebar would mean
 * navigating — which is precisely what a row menu must never do.
 *
 * Nothing here is an authorization decision. `canRename` is the server's own
 * answer carried in the sidebar payload, and PATCH /api/chat/channels/{id}
 * re-derives it on every call; a client that ignores this list gets a 403.
 */

export type ConversationTargetKind = "channel" | "dm" | "group";

/**
 * The one shape a row hands to the menu.
 *
 * `kind` is the presentation discriminant and carries the server's own
 * classification: a channel is a channel, and `partitionDMs` decides group vs
 * 1:1 from `chat.dm_conversations.type`, never from a name or a participant
 * count. It never travels to the server — the API pair a pin uses is chosen from
 * `pinTargetKind` below, and a rename is only reachable for a channel.
 */
export interface ConversationTarget {
  kind: ConversationTargetKind;
  id: string;
  name: string;
  pinned: boolean;
  /** Server-derived. Absent or false hides the rename item; it never grants it. */
  canRename?: boolean;
  /** Whether this conversation currently shows an unread badge. */
  hasUnread: boolean;
}

export type ConversationActionId = "pin" | "unpin" | "mark-read" | "rename";

// Pin, mark-as-read and rename are all ordinary, reversible operations, so an
// action carries nothing beyond its identity and its label. When a destructive
// action is actually implemented — leaving a channel or a group — that is the
// issue that adds the grouping, the confirmation and the styling it needs; a
// flag nothing sets is not a head start, it is a claim the menu cannot honour.
export interface ConversationAction {
  id: ConversationActionId;
  label: string;
}

/**
 * The pin API is addressed by the persisted aggregate, not by the sidebar
 * section: a group is a `chat.dm_conversations` row, so it pins through the DM
 * endpoint exactly as a 1:1 does. Stated once here so no call site has to
 * remember it.
 */
export function pinTargetKind(kind: ConversationTargetKind): "channel" | "dm" {
  return kind === "channel" ? "channel" : "dm";
}

/**
 * The menu for one target, in the order it is rendered.
 *
 * Pinning is offered for every kind (#474 implements it for channels and DMs
 * alike), mark-as-read only when there is something to mark, and rename only
 * for a channel the server said this caller may rename.
 */
export function conversationActions(target: ConversationTarget): ConversationAction[] {
  const actions: ConversationAction[] = [
    target.pinned ? { id: "unpin", label: "Desafixar" } : { id: "pin", label: "Fixar no topo" },
  ];
  if (target.hasUnread) {
    actions.push({ id: "mark-read", label: "Marcar como lido" });
  }
  if (target.kind === "channel" && target.canRename) {
    actions.push({ id: "rename", label: "Renomear canal" });
  }
  return actions;
}

/**
 * The trigger's accessible name.
 *
 * Never just "…": it names the conversation, so a screen-reader user moving
 * through forty rows can tell which menu they are about to open. The kind is
 * spelled out for the same reason the row's own label spells it out — "#geral"
 * and a person called Geral are different things.
 */
export function actionsTriggerLabel(target: ConversationTarget): string {
  const prefix =
    target.kind === "channel" ? "canal" : target.kind === "group" ? "grupo" : "conversa com";
  return `Mais opções para ${prefix} ${target.name}`;
}
