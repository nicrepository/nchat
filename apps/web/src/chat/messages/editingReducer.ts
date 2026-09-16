/**
 * Editing and deleting a message: this reader's own optimistic edit, and the
 * message.updated event that announces somebody's edit or deletion.
 *
 * Both paths defer to a link-safety correction that is newer than what they
 * carry, so a verdict that already arrived is never undone by an older edit.
 */

import { normalizeBodyFormat, normalizeLinkSafety, type Message } from "../chatTypes";
import type { WSMessageUpdatedEvent } from "../useChatWebSocket";
import {
  applyLinkSafetyCorrection,
  applyLinkSafetyCorrections,
  isNotNewerSecurityVersion,
} from "./linkSafetyCorrections";
import type {
  Action,
  ActionOf,
  LinkSafetyChange,
  LinkSafetyCorrections,
  MessagesState,
} from "./types";

function applyEditOptimistic(
  state: MessagesState,
  action: ActionOf<"edit_optimistic">,
): MessagesState {
  const messages = state.messages.map((message) =>
    message.id === action.messageId
      ? {
          ...message,
          bodyText: action.body,
          bodyFormat: action.bodyFormat,
          isEdited: true,
          editCount: message.editCount + 1,
          editedAt: action.editedAt,
          // The new body has not yet received the server's verdict. Keeping the
          // old body's clearance here would briefly authorize different links.
          linkSafetyState: "unknown" as const,
        }
      : message,
  );
  return { ...state, messages, lastMutation: "none" };
}

/** The confirmed edit written onto the message, unless a newer verdict outranks it. */
function confirmEditOnMessage(
  message: Message,
  confirmed: Message,
  correction: LinkSafetyChange | undefined,
): Message {
  if (correction) return applyLinkSafetyCorrection(message, correction);
  return {
    ...message,
    bodyText: confirmed.bodyText,
    bodyFormat: confirmed.bodyFormat,
    editedAt: confirmed.editedAt,
    updatedAt: confirmed.updatedAt,
    editCount: confirmed.editCount,
    isEdited: confirmed.isEdited,
    linkSafetyState: confirmed.linkSafetyState,
  };
}

function applyEditConfirmed(
  state: MessagesState,
  action: ActionOf<"edit_confirmed">,
): MessagesState {
  const correction = state.linkSafetyCorrections.get(action.message.id);
  const correctionWins =
    correction && isNotNewerSecurityVersion(action.message.updatedAt, correction.updatedAt);
  const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
  if (!correctionWins) linkSafetyCorrections.delete(action.message.id);
  return {
    ...state,
    messages: state.messages.map((message) =>
      message.id === action.message.id &&
      !message.isRemoved &&
      action.message.editCount >= message.editCount
        ? confirmEditOnMessage(message, action.message, correctionWins ? correction : undefined)
        : message,
    ),
    linkSafetyCorrections,
    lastMutation: "none",
  };
}

function applyEditRevert(state: MessagesState, action: ActionOf<"edit_revert">): MessagesState {
  return {
    ...state,
    messages: state.messages.map((message) =>
      message.id === action.message.id &&
      !message.isRemoved &&
      message.editedAt === action.optimisticEditedAt
        ? applyLinkSafetyCorrections(action.message, state.linkSafetyCorrections)
        : message,
    ),
    lastMutation: "none",
  };
}

type MessageUpdate = NonNullable<WSMessageUpdatedEvent["message_update"]>;

/** The removal an update announces, written onto the message it names. */
function withdrawUpdatedMessage(
  message: Message,
  update: MessageUpdate,
  deletedAt: string | null,
): Message {
  return {
    ...message,
    bodyText: "",
    quoted: undefined,
    reactions: [],
    status: "deleted" as const,
    isRemoved: true,
    deletedAt,
    updatedAt: update.updated_at ?? deletedAt ?? message.updatedAt,
  };
}

/** The edit an update announces, unless this client already holds a later one. */
function applyUpdatedBody(message: Message, update: MessageUpdate): Message {
  if (update.edit_count < message.editCount) return message;
  const linkSafetyState =
    update.link_safety_state === undefined
      ? message.linkSafetyState
      : normalizeLinkSafety(update.link_safety_state);
  return {
    ...message,
    bodyText: linkSafetyState === "malicious" ? "" : update.body,
    bodyFormat: normalizeBodyFormat(update.body_format),
    editedAt: update.edited_at,
    updatedAt: update.updated_at ?? update.edited_at,
    editCount: update.edit_count,
    isEdited: update.is_edited,
    linkSafetyState,
  };
}

interface MessageUpdateContext {
  update: MessageUpdate;
  removed: boolean;
  deletedAt: string | null;
  /** True while a local correction is still newer than this update. */
  correctionWins: boolean;
}

function applyUpdateToMessage(message: Message, context: MessageUpdateContext): Message {
  const { update, removed, deletedAt } = context;
  if (message.id !== update.message_id) {
    if (!removed || message.quoted?.id !== update.message_id) return message;
    return {
      ...message,
      quoted: { ...message.quoted, bodyText: "", isRemoved: true, deletedAt },
    };
  }
  if (context.correctionWins) return message;
  if (removed) return withdrawUpdatedMessage(message, update, deletedAt);
  if (message.isRemoved) return message;
  return applyUpdatedBody(message, update);
}

/**
 * Everything one update decides before it is applied, including whether a local
 * correction outranks it. A correction that lost is dropped here.
 */
function messageUpdateContext(
  update: MessageUpdate,
  corrections: LinkSafetyCorrections,
): MessageUpdateContext {
  const correction = corrections.get(update.message_id);
  const updateVersion = update.updated_at ?? update.edited_at;
  const correctionWins = Boolean(
    correction && isNotNewerSecurityVersion(updateVersion, correction.updatedAt),
  );
  if (!correctionWins) corrections.delete(update.message_id);
  return {
    update,
    removed: update.is_removed === true || update.status === "deleted",
    deletedAt: update.deleted_at ?? update.updated_at ?? null,
    correctionWins,
  };
}

/**
 * A message.updated event: an edit or a deletion someone else performed.
 *
 * A local link-safety correction newer than the update wins over it, so a
 * verdict that arrived first is not undone by an edit event that predates it.
 */
function applyMessageUpdated(
  state: MessagesState,
  action: ActionOf<"message_updated">,
): MessagesState {
  const update = action.event.message_update;
  if (!update) return state;
  const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
  const context = messageUpdateContext(update, linkSafetyCorrections);
  const stillTargeted = state.replyTo?.id === update.message_id;
  return {
    ...state,
    messages: state.messages.map((message) => applyUpdateToMessage(message, context)),
    replyTo: context.removed && stillTargeted ? null : state.replyTo,
    lastMutation: "none",
    realtimeError: null,
    linkSafetyCorrections,
  };
}

/** Editing and deleting a message, locally and as announced by the server. */
export function reduceEditing(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "edit_optimistic":
      return applyEditOptimistic(state, action);
    case "edit_confirmed":
      return applyEditConfirmed(state, action);
    case "edit_revert":
      return applyEditRevert(state, action);
    case "message_updated":
      return applyMessageUpdated(state, action);
    case "delete_error":
      return { ...state, actionError: action.error };
    default:
      return undefined;
  }
}
