/**
 * The shape of a conversation's message state, and every action that changes it.
 *
 * Shared by the pure reducers under this directory and by the hooks that
 * compose them, so both halves agree on one vocabulary. Nothing here imports
 * React or performs I/O.
 */

import type {
  ChannelAttachment,
  Message,
  MessagePage,
  MessageSecuritySnapshot,
} from "../chatTypes";
import type { WSMessageUpdatedEvent, WSReactionUpdatedEvent } from "../useChatWebSocket";

export type MessagesStatus = "idle" | "loading" | "ready" | "error";

/**
 * Explicit record of the most recent messages mutation.
 * Used by MessageList's useLayoutEffect to apply the correct scroll strategy
 * without relying on fragile first/last ID comparisons.
 *
 * "ws_append" — message appended from a WebSocket event; MessageList scrolls
 *               to bottom only when the user is already near the bottom.
 */
export type LastMutation = "initial" | "append" | "prepend" | "ws_append" | "none";

/**
 * One reaction this reader has asked for and is still waiting on: the state
 * they want that `(message, emoji)` to end up in.
 *
 * The pair is the identity of the intent, so it is the key rather than a field —
 * nothing here has to build, parse or escape a composite string.
 */
export type ReactionIntent = "added" | "removed";

export interface PendingReactions {
  /** The last list the server stated for this message. */
  confirmed: Message["reactions"];
  /** Still awaiting confirmation, by emoji. */
  intents: Map<string, ReactionIntent>;
}

/** A versioned link-safety verdict this client holds for one message. */
export type LinkSafetyChange = { state: Message["linkSafetyState"]; updatedAt: string };

export type LinkSafetyCorrections = Map<string, LinkSafetyChange>;

export interface MessagesState {
  status: MessagesStatus;
  messages: Message[];
  /** Opaque cursor for loading older messages; empty string when no older page. */
  nextCursor: string;
  sendError: string | null;
  sending: boolean;
  /** True while an older-page fetch is in progress. */
  loadingMore: boolean;
  /** Describes the most recent change to the messages array for scroll management. */
  lastMutation: LastMutation;
  /** Recoverable realtime fallback error; initial loads and manual retries remain authoritative. */
  realtimeError: string | null;
  /** Feedback for rejected/unsent message actions (reactions, favorites). */
  actionError: string | null;
  /**
   * Reactions this reader has toggled but the server has not confirmed yet,
   * per message (issue #496).
   *
   * `confirmed` is the last list the server stated; `intents` is what this
   * reader has asked for since, one entry per emoji. What is rendered is always
   * `confirmed` with `intents` applied on top — see applyPendingReactions.
   */
  pendingReactions: Map<string, PendingReactions>;
  /** RF-07: message currently selected as the parent quote for the composer. */
  replyTo: Message | null;
  /** Versioned corrections that arrived before their message.created payload. */
  linkSafetyCorrections: LinkSafetyCorrections;
}

/**
 * Explicit result returned by sendMessage.
 *
 * "sent"  — POST succeeded and state was updated for the current target.
 * "stale" — target changed before POST resolved/rejected; caller must not
 *            treat this as success or failure for the current target.
 *
 * Current-target failures throw instead of returning a result, preserving
 * the existing draft-retention contract in callers.
 */
export type SendResult = { status: "sent" } | { status: "stale" };

export type Action =
  | { type: "loading" }
  | { type: "loaded"; page: MessagePage }
  | { type: "error" }
  | { type: "sending" }
  | { type: "sent"; message: Message }
  | { type: "send_error"; error: string }
  /**
   * RF-21: a message this client is showing as pending has reached a terminal
   * state. `malicious_link` and `link_check_inconclusive` are the two refusal
   * reasons the server distinguishes; `unavailable` is a message that is simply
   * gone, which must not be reported as either.
   */
  | {
      type: "message_blocked";
      messageId: string;
      reason?: "malicious_link" | "link_check_inconclusive" | "unavailable";
    }
  /**
   * RF-21: what is known about a *published* message's links changed (issue
   * #135). Applied to a message this view already holds, never inserting one, so
   * a repeated delivery is idempotent.
   */
  | {
      type: "link_safety_changed";
      messageId: string;
      state: Message["linkSafetyState"];
      updatedAt: string;
    }
  | { type: "security_snapshots_refreshed"; snapshots: MessageSecuritySnapshot[] }
  | { type: "prepending" }
  | { type: "prepended"; page: MessagePage }
  | { type: "prepend_error" }
  | { type: "ws_received"; message: Message }
  | {
      type: "edit_optimistic";
      messageId: string;
      body: string;
      bodyFormat: Message["bodyFormat"];
      editedAt: string;
    }
  | { type: "edit_confirmed"; message: Message }
  | { type: "edit_revert"; message: Message; optimisticEditedAt: string }
  | { type: "message_updated"; event: WSMessageUpdatedEvent }
  | { type: "message_snapshot"; message: Message; insertIfMissing: boolean }
  | {
      type: "references_refreshed";
      references: Record<string, NonNullable<Message["reference"]>>;
    }
  | { type: "delete_error"; error: string }
  | { type: "reaction_updated"; event: WSReactionUpdatedEvent; actorIsMe: boolean }
  | { type: "reaction_snapshot"; messageId: string; reactions: Message["reactions"] }
  | { type: "reaction_error"; error: string }
  | { type: "reaction_error_clear" }
  | { type: "reply_set"; message: Message }
  | { type: "reply_clear" }
  | { type: "favorite_set"; messageId: string; isFavorited: boolean }
  | { type: "favorite_error"; error: string }
  | { type: "reaction_optimistic"; messageId: string; emoji: string }
  | { type: "reaction_revert"; messageId: string; emoji: string; error: string }
  | { type: "ws_fetch_error"; error: string }
  | { type: "attachment_status"; attachmentId: string; status: ChannelAttachment["status"] }
  | { type: "attachments_reconciled"; attachments: ChannelAttachment[] }
  | { type: "ws_subscription_ready" };

/** The action of one specific type, narrowed out of the union. */
export type ActionOf<T extends Action["type"]> = Extract<Action, { type: T }>;

/**
 * A sub-reducer: answers for the actions it owns and returns undefined for
 * every other, which is what lets the dispatcher keep the groups disjoint.
 */
export type SubReducer = (state: MessagesState, action: Action) => MessagesState | undefined;

export const initialState: MessagesState = {
  status: "idle",
  messages: [],
  nextCursor: "",
  sendError: null,
  sending: false,
  loadingMore: false,
  lastMutation: "none",
  realtimeError: null,
  actionError: null,
  pendingReactions: new Map(),
  replyTo: null,
  linkSafetyCorrections: new Map(),
};

/** Which conversation a piece of message work belongs to. */
export interface ConversationTarget {
  kind: "channel" | "dm";
  targetId: string;
}

/** The key that names a conversation for the whole lifetime of one target. */
export function conversationKey(target: ConversationTarget): string {
  return `${target.kind}:${target.targetId}`;
}
