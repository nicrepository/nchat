/**
 * What the sidebar's row menu may offer, derived from one target (issue #527).
 *
 * One list, built by one function, for channels, the general channel, group DMs
 * and 1:1 DMs alike. Four independent menus would be four places for "Sair" to
 * appear on a direct conversation, or for a rename to be offered where the
 * backend refuses one.
 *
 * The rules are semantic, not cosmetic — an action appears when the product has
 * a real, reachable implementation of it for this kind of target. Archiving and
 * hiding are deliberately absent: they have no backend at all, and a menu item
 * for one would be a promise nothing can keep.
 *
 * Nothing here is an authorization decision. Every flag comes from the server's
 * own sidebar payload, and every endpoint re-derives its decision from the
 * session: a client that ignores this list gets a refusal, not an effect. The
 * general-channel restrictions in particular are enforced in SQL — this module
 * omits the items so the UI does not offer what the server would refuse.
 */

export type ConversationTargetKind = "channel" | "dm" | "group";

/**
 * The one shape a row hands to the menu.
 *
 * `kind` carries the server's own classification: a channel is a channel, and
 * `partitionDMs` decides group vs 1:1 from `chat.dm_conversations.type`, never
 * from a name or a participant count. It never travels to the server — the API
 * pair an action uses is chosen from `conversationTargetKind` below.
 */
export interface ConversationTarget {
  kind: ConversationTargetKind;
  id: string;
  name: string;
  pinned: boolean;
  /** Whether this conversation currently shows an unread badge. */
  hasUnread: boolean;
  /** This viewer's own notification preference, from the server. */
  muted: boolean;
  /** Server-derived. Absent or false hides the rename item; it never grants it. */
  canRename?: boolean;
  /**
   * The workspace's structural general channel. It cannot be renamed, muted or
   * left — by anybody — and the backend refuses all three regardless of what
   * this flag says.
   */
  isGeneral?: boolean;
}

export type ConversationActionId =
  | "pin"
  | "unpin"
  | "mark-read"
  | "mute"
  | "unmute"
  | "rename"
  | "details"
  | "leave";

/**
 * The icon each action draws, named from the product's existing inline SVG set
 * rather than a new icon dependency. The menu maps these to components; keeping
 * the name here is what lets the list stay a plain data structure.
 */
export type ConversationActionIcon =
  | "pin"
  | "check"
  | "bell"
  | "bell-off"
  | "pencil"
  | "info"
  | "logout";

export interface ConversationAction {
  id: ConversationActionId;
  label: string;
  icon: ConversationActionIcon;
  /**
   * Actions render in three groups, separated in this order: the frequent ones,
   * then management and navigation, then the destructive one. The group is data
   * rather than a position, so the menu never has to know which ids belong
   * together.
   */
  group: "frequent" | "manage" | "destructive";
  /**
   * True only for an action that removes the viewer from the conversation. It is
   * what earns the separator, the confirmation and the one place destructive
   * colour is allowed.
   */
  destructive?: boolean;
}

/**
 * The API target kind for an action.
 *
 * Addressed by the persisted aggregate, not by the sidebar section: a group is a
 * `chat.dm_conversations` row, so it pins, mutes, reads and leaves through the
 * DM endpoints exactly as a 1:1 does. Stated once here so no call site has to
 * remember it.
 */
export function conversationTargetKind(kind: ConversationTargetKind): "channel" | "dm" {
  return kind === "channel" ? "channel" : "dm";
}

/** Details is labelled for what the panel actually shows. */
function detailsLabel(kind: ConversationTargetKind): string {
  if (kind === "channel") return "Detalhes do canal";
  if (kind === "group") return "Detalhes do grupo";
  return "Detalhes da conversa";
}

/**
 * Whether this target may be silenced.
 *
 * Everything except the general channel: muting is an individual preference and
 * takes no role, but the general channel is where everyone is reachable by
 * construction, so it is not silenceable by anybody.
 */
function canMute(target: ConversationTarget): boolean {
  return !(target.kind === "channel" && target.isGeneral);
}

/**
 * Whether this target may be left.
 *
 * Never a 1:1 conversation — a direct conversation has no membership to leave,
 * which is why "Sair" is absent rather than disabled — and never the general
 * channel, whose membership belongs to the workspace sync.
 */
function canLeave(target: ConversationTarget): boolean {
  if (target.kind === "dm") return false;
  return !(target.kind === "channel" && target.isGeneral);
}

/**
 * Whether this target may be renamed.
 *
 * A channel needs the server's capability and must not be the general one; a
 * group needs only participation, which the row implies — every group in this
 * sidebar is one the viewer is in. A 1:1 is never renameable: its name is the
 * counterpart's, resolved per viewer, so there is nothing to rename.
 */
function canRenameTarget(target: ConversationTarget): boolean {
  if (target.kind === "dm") return false;
  if (target.kind === "group") return true;
  return Boolean(target.canRename) && !target.isGeneral;
}

const renameLabel: Record<ConversationTargetKind, string> = {
  channel: "Renomear canal",
  group: "Renomear grupo",
  dm: "",
};

const leaveLabel: Record<ConversationTargetKind, string> = {
  channel: "Sair do canal",
  group: "Sair do grupo",
  dm: "",
};

/**
 * The frequent group: what a person does to a conversation most often, and what
 * changes nothing for anyone else.
 */
function frequentActions(target: ConversationTarget): ConversationAction[] {
  const actions: ConversationAction[] = [
    target.pinned
      ? { id: "unpin", label: "Desafixar", icon: "pin", group: "frequent" }
      : { id: "pin", label: "Fixar no topo", icon: "pin", group: "frequent" },
  ];
  // Only when there is something to mark. There is no "mark as unread" in this
  // domain, so the action simply disappears once the badge is gone.
  if (target.hasUnread) {
    actions.push({ id: "mark-read", label: "Marcar como lido", icon: "check", group: "frequent" });
  }
  if (canMute(target)) {
    actions.push(
      target.muted
        ? { id: "unmute", label: "Ativar notificações", icon: "bell", group: "frequent" }
        : { id: "mute", label: "Silenciar notificações", icon: "bell-off", group: "frequent" },
    );
  }
  return actions;
}

/** The management and navigation group. Details is offered for every target. */
function manageActions(target: ConversationTarget): ConversationAction[] {
  const actions: ConversationAction[] = [];
  if (canRenameTarget(target)) {
    actions.push({
      id: "rename",
      label: renameLabel[target.kind],
      icon: "pencil",
      group: "manage",
    });
  }
  actions.push({ id: "details", label: detailsLabel(target.kind), icon: "info", group: "manage" });
  return actions;
}

/**
 * The menu for one target, in render order.
 *
 * Three groups, concatenated. Where the separators go is the menu's business —
 * it draws one wherever the group changes — so this function never has to think
 * about position.
 */
export function conversationActions(target: ConversationTarget): ConversationAction[] {
  const actions = [...frequentActions(target), ...manageActions(target)];
  if (canLeave(target)) {
    actions.push({
      id: "leave",
      label: leaveLabel[target.kind],
      icon: "logout",
      group: "destructive",
      destructive: true,
    });
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
