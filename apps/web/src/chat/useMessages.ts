/**
 * useMessages — hook for loading and sending messages in a channel or DM.
 *
 * Security notes:
 * - No tokens are stored or exposed; authentication is handled by authenticatedFetch.
 * - No author_id is sent from the client; the server derives sender identity from the JWT.
 * - AbortController cancels in-flight list and fallback single-message requests
 *   on target change or unmount.
 * - latestTargetRef is updated via useLayoutEffect (no deps) after every render,
 *   synchronously in the same JS task, before any microtask can run. This ensures
 *   stale POST completions are detected reliably regardless of effect scheduling.
 *
 * WebSocket realtime delivery:
 * Connected to /api/chat/ws via useChatWebSocket. Auth uses the Bearer access
 * token passed as the Sec-WebSocket-Protocol subprotocol (browser WebSocket
 * upgrade does not support custom headers; token-in-URL is explicitly rejected
 * by the server). Incoming message.created events carry the full message DTO
 * in evt.payload and are inserted directly into the timeline without an
 * additional GET (dedup by id in reducer). If payload is absent (old server
 * during rolling deploy) a targeted GET is used as fallback.
 * Cleanup happens on unmount and target change via useChatWebSocket's effect.
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useReducer, useRef } from "react";

import {
  deleteMessage as deleteMessageRequest,
  editMessage as editMessageRequest,
  favoriteMessage,
  fetchChannelMessage,
  fetchChannelMessageSecuritySnapshots,
  fetchChannelMessages,
  fetchDMMessage,
  fetchDMMessageSecuritySnapshots,
  fetchDMMessages,
  fetchLinkSafetyStatuses,
  postChannelMessage,
  reconcileMessageLinkSafety,
  postDMMessage,
  resolveChannelMessageReferences,
  resolveDMMessageReferences,
  safeAvatarUrl,
  unfavoriteMessage,
} from "./chatApi";
import { markPresenceActivity } from "./chatSocket";
import { fetchConversationAttachments } from "./filesApi";
import { isPreviewWorkPending } from "./useAttachmentPreview";
import { previewReconcileDelayMs, previewReconcileMaxAttempts } from "./useConversationDetails";
import { ApiRequestError } from "../lib/api";
import { randomId } from "../lib/randomId";
import {
  normalizeBodyFormat,
  normalizeLinkSafety,
  parseMessageAttachments,
  parseReactionUsers,
  type LinkSafetyRecheck,
  type ChannelAttachment,
  type Message,
  type MessagePage,
  type MessageSecuritySnapshot,
} from "./chatTypes";
import {
  useChatWebSocket,
  type WSMessageBlockedEvent,
  type WSMessageCreatedEvent,
  type WSMessageLinkSafetyChangedEvent,
  type WSMessageUpdatedEvent,
  type WSClientErrorEvent,
  type WSMembersAddedEvent,
  type WSAttachmentStatusEvent,
  type WSPinUpdatedEvent,
  type WSReactionUpdatedEvent,
  type WSTypingUpdatedEvent,
} from "./useChatWebSocket";

// ── State shape ───────────────────────────────────────────────────────────────

type MessagesStatus = "idle" | "loading" | "ready" | "error";

/**
 * Explicit record of the most recent messages mutation.
 * Used by MessageList's useLayoutEffect to apply the correct scroll strategy
 * without relying on fragile first/last ID comparisons.
 *
 * "ws_append" — message appended from a WebSocket event; MessageList scrolls
 *               to bottom only when the user is already near the bottom.
 */
export type LastMutation = "initial" | "append" | "prepend" | "ws_append" | "none";

const referenceRevalidationMs = 15_000;
const referenceRevalidationBatchSize = 100;

/**
 * How many withheld messages one reconnect may ask about.
 *
 * Matches the server's cap. A client holds a message in this state only between
 * sending it and its scan resolving, so the realistic count is one or two; the
 * bound is here so a long-lived tab that accumulated them cannot turn a
 * reconnect into an unbounded query.
 */
const linkSafetyReconcileBatchSize = 100;

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
  /** Reactions snapshot to restore per messageId if the in-flight optimistic toggle is rejected or times out. */
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
  linkSafetyCorrections: Map<string, { state: Message["linkSafetyState"]; updatedAt: string }>;
}

// ── Send result ───────────────────────────────────────────────────────────────

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

// ── Reducer ───────────────────────────────────────────────────────────────────

type Action =
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

// ── Preview reconciliation window ────────────────────────────────────────────
//
// Mirrors useConversationDetails.ts's ReconcileWindow/reconcileReducer exactly
// (same shape, same "polled"/"resumed" vocabulary), kept local rather than
// shared because the two hooks watch different data — this one a loaded page
// of messages, that one a destination's file listing — and importing a
// reducer across hook modules for one struct's worth of bookkeeping would be
// tighter coupling than the bookkeeping is worth.
//
// Only "generation" (waiting for a preview to appear) is needed here, not
// useConversationDetails.ts's open-ended "revocation": a preview that stops
// being servable after publication is a scan re-verdict, and that already
// arrives as attachment_status over the socket.
interface PreviewReconcileWindow {
  round: number;
  attempt: number;
  target: string;
  progressKey: string;
}

type PreviewReconcileAction =
  | { type: "polled"; target: string; progressKey: string }
  | { type: "restart" }
  | { type: "resumed" };

const initialPreviewReconcile: PreviewReconcileWindow = {
  round: 0,
  attempt: 0,
  target: "",
  progressKey: "",
};

function previewReconcileReducer(
  state: PreviewReconcileWindow,
  action: PreviewReconcileAction,
): PreviewReconcileWindow {
  switch (action.type) {
    case "polled": {
      const sameWindow = state.target === action.target && state.progressKey === action.progressKey;
      return {
        round: state.round + 1,
        attempt: sameWindow ? state.attempt + 1 : 1,
        target: action.target,
        progressKey: action.progressKey,
      };
    }
    case "restart":
      return { ...initialPreviewReconcile, round: state.round + 1 };
    case "resumed":
      return { ...state, round: state.round + 1 };
  }
}

/** How many of a destination's most recent attachments one reconciliation poll reads back. */
const messagePreviewReconcileLimit = 20;

function isAbort(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}

/** Applies a local toggle to a message's reaction list, mirroring server semantics for count/reactedByMe. */
function toggleOptimisticReaction(
  reactions: Message["reactions"],
  emoji: string,
): Message["reactions"] {
  const index = reactions.findIndex((item) => item.emoji === emoji);
  if (index < 0) {
    // No named author yet: the tooltip says "Você" from reactedByMe, and the
    // server's own list replaces this the moment the toggle is confirmed.
    return [...reactions, { emoji, count: 1, reactedByMe: true, users: [] }];
  }
  const current = reactions[index];
  if (!current.reactedByMe) {
    return reactions.map((item, i) =>
      i === index ? { ...item, count: item.count + 1, reactedByMe: true } : item,
    );
  }
  if (current.count <= 1) {
    return reactions.filter((_, i) => i !== index);
  }
  return reactions.map((item, i) =>
    i === index ? { ...item, count: item.count - 1, reactedByMe: false } : item,
  );
}

/**
 * One reaction this reader has asked for and is still waiting on: the state
 * they want that `(message, emoji)` to end up in.
 *
 * The pair is the identity of the intent, so it is the key rather than a field —
 * nothing here has to build, parse or escape a composite string.
 */
type ReactionIntent = "added" | "removed";

interface PendingReactions {
  /** The last list the server stated for this message. */
  confirmed: Message["reactions"];
  /** Still awaiting confirmation, by emoji. */
  intents: Map<string, ReactionIntent>;
}

/**
 * Forces one emoji to the state an intent asks for.
 *
 * Idempotent on purpose: replaying an intent over a list that already agrees
 * with it changes nothing, which is what lets the same intents be applied again
 * over every new confirmed list without the count drifting.
 */
function applyReactionIntent(
  reactions: Message["reactions"],
  emoji: string,
  desired: ReactionIntent,
): Message["reactions"] {
  const reacted = reactions.find((item) => item.emoji === emoji)?.reactedByMe ?? false;
  if (reacted === (desired === "added")) return reactions;
  return toggleOptimisticReaction(reactions, emoji);
}

/** Confirmed server state + this reader's outstanding intents = what is drawn. */
function applyPendingReactions(pending: PendingReactions): Message["reactions"] {
  let reactions = pending.confirmed;
  for (const [emoji, desired] of pending.intents) {
    reactions = applyReactionIntent(reactions, emoji, desired);
  }
  return reactions;
}

/** The pending entry for a message, or an empty one anchored on what is drawn. */
function pendingFor(
  state: MessagesState,
  messageId: string,
  rendered: Message["reactions"],
): PendingReactions {
  return state.pendingReactions.get(messageId) ?? { confirmed: rendered, intents: new Map() };
}

/**
 * Writes a message's pending entry back, dropping it once nothing is in flight
 * so the map holds only messages that actually have something outstanding.
 */
function withPending(
  state: MessagesState,
  messageId: string,
  pending: PendingReactions | null,
): Map<string, PendingReactions> {
  const next = new Map(state.pendingReactions);
  if (pending === null || pending.intents.size === 0) next.delete(messageId);
  else next.set(messageId, pending);
  return next;
}

/**
 * What an incoming event settled, if anything.
 *
 * Carries the intent it settled so the caller does not have to work out again
 * whether it was an addition — that question decides the emoji history, and it
 * must have exactly one answer.
 */
interface ReactionConfirmation {
  confirmed: boolean;
  intent?: ReactionIntent;
}

const unconfirmed: ReactionConfirmation = { confirmed: false };

/**
 * Whether an event is the confirmation of what *this reader* asked for.
 *
 * Observing an event is not the same as having your own toggle confirmed. The
 * server fans one event out to every subscriber, so a reaction the reader is
 * still waiting on and an identical reaction from somebody else look alike on
 * the wire; and the reader's own events can arrive after they have already
 * changed their mind. Only an event that says back the state the outstanding
 * intent asked for settles it — which is what makes it safe to stop that
 * intent's rollback timer and to count the emoji as used.
 *
 * The three cases this exists to refuse:
 *
 *  - another reader toggling the same emoji. It moves the confirmed count, and
 *    this reader's own toggle stays pending on top of it;
 *  - a stale `added` reaching a reader who has since asked to remove the same
 *    emoji, and the mirror case. The newer intent survives and is re-applied,
 *    so the event cannot resurrect what the reader has already taken back;
 *  - a redelivery. The first copy settled the intent, so by the second there is
 *    nothing outstanding and nothing to settle.
 *
 * Two tabs of the same reader are indistinguishable here: the protocol carries
 * an actor, not an origin. When the other tab's action happens to be the state
 * this tab is waiting for, the server already satisfies the intent and treating
 * that as convergence is correct.
 */
function confirmReactionIntent(
  pending: Map<string, PendingReactions>,
  reaction: NonNullable<WSReactionUpdatedEvent["reaction"]>,
  actorIsMe: boolean,
): ReactionConfirmation {
  if (!actorIsMe) return unconfirmed;
  const intent = pending.get(reaction.message_id)?.intents.get(reaction.emoji);
  if (intent === undefined || intent !== (reaction.added ? "added" : "removed")) {
    return unconfirmed;
  }
  return { confirmed: true, intent };
}

/** The intents left once an event has settled the one it confirms, if any. */
function remainingIntents(
  pending: PendingReactions,
  emoji: string,
  confirmation: ReactionConfirmation,
): Map<string, ReactionIntent> {
  if (!confirmation.confirmed) return pending.intents;
  const next = new Map(pending.intents);
  next.delete(emoji);
  return next;
}

/**
 * Applies a reaction.updated event to the message it names (issue #496).
 *
 * The event carries absolute values — a count and a bounded list of names, never
 * an increment — so replaying one is a no-op: nothing accumulates, and no author
 * is listed twice. Each event's list replaces the previous one wholesale, and
 * events for a target arrive in publication order over a single connection, so
 * the last one delivered is the current state; a reconnect is reconciled by
 * refetching the message, not by ordering rules here.
 *
 * What the event does *not* carry is `reacted_by_me`: one event is fanned out to
 * every subscriber, so the reader's own state is derived here instead.
 *
 * That derivation reconciles against the pre-optimistic baseline, not the
 * current — possibly still unconfirmed — optimistic guess, so an update from
 * another actor cannot inherit this reader's own pending toggle as ground truth.
 */
function applyReactionEvent(
  state: MessagesState,
  event: WSReactionUpdatedEvent,
  actorIsMe: boolean,
): MessagesState {
  const { reaction } = event;
  if (!reaction) return state;
  const index = state.messages.findIndex((message) => message.id === reaction.message_id);
  if (index < 0) return state;
  const message = state.messages[index];
  const previous = pendingFor(state, reaction.message_id, message.reactions);
  const wasReacted = new Map(previous.confirmed.map((item) => [item.emoji, item.reactedByMe]));
  const confirmed = reaction.reactions.map((item) => ({
    emoji: item.emoji,
    count: item.count,
    // The names travel with the event, so a tooltip is correct the instant the
    // count changes — no refetch, and no request when it is hovered.
    users: parseReactionUsers(item.users),
    reactedByMe:
      actorIsMe && item.emoji === reaction.emoji
        ? reaction.added
        : (wasReacted.get(item.emoji) ?? false),
  }));
  const confirmation = confirmReactionIntent(state.pendingReactions, reaction, actorIsMe);
  const pending = {
    confirmed,
    intents: remainingIntents(previous, reaction.emoji, confirmation),
  };
  const messages = [...state.messages];
  messages[index] = { ...message, reactions: applyPendingReactions(pending) };
  return {
    ...state,
    messages,
    pendingReactions: withPending(state, reaction.message_id, pending),
    lastMutation: "none",
    realtimeError: null,
    actionError: null,
  };
}

function insertMessageChronologically(
  messages: Message[],
  message: Message,
): { messages: Message[]; isNewer: boolean } {
  const isNewer =
    messages.length === 0 ||
    message.createdAt > messages[messages.length - 1].createdAt ||
    (message.createdAt === messages[messages.length - 1].createdAt &&
      message.id > messages[messages.length - 1].id);
  return {
    messages: isNewer
      ? [...messages, message]
      : [...messages, message].sort((a, b) => {
          if (a.createdAt < b.createdAt) return -1;
          if (a.createdAt > b.createdAt) return 1;
          return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
        }),
    isNewer,
  };
}

const initialState: MessagesState = {
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

const realtimeFallbackErrorMessage = "Não foi possível atualizar mensagens em tempo real.";
const reactionConfirmTimeoutMs = 8_000;

/**
 * Copy for the send failures this client recognises by code (RF-21).
 *
 * Keyed on `code` and never on the message text: the code is the stable part of
 * the contract, and matching on English prose from a server would break the
 * moment that prose changed. The two entries are deliberately different
 * sentences — one says the link is dangerous and is final, the other says the
 * check could not run and is worth retrying — because telling someone to try
 * again on a blocked link is as wrong as telling them a transient outage means
 * their link is malicious.
 *
 * The security decision itself is entirely server-side. Nothing here inspects a
 * URL, and there is no provider credential in this bundle: this only renders a
 * verdict the backend already made and enforced.
 */
const sendErrorMessages: Record<string, string> = {
  malicious_url: "Este link foi bloqueado por segurança.",
  link_check_unavailable:
    "Não foi possível verificar a segurança do link. Tente novamente em instantes.",
  // A terminal outcome, not a transient one — the scan finished and produced no
  // usable verdict, so unlike link_check_unavailable this deliberately carries
  // no "try again" implication: retrying does not resubmit anything.
  link_check_inconclusive: "Não foi possível verificar a segurança deste link.",
  // The backend declined to start a new scan right now — a spent window or a
  // full queue. Deliberately worded like the unavailable case and deliberately
  // not like the blocked one: nothing was decided about this link, and telling
  // someone their link looks dangerous because a queue was full is a claim with
  // nothing behind it.
  link_check_capacity: "Não foi possível verificar os links agora. Tente novamente em instantes.",
};

function blockedMessageReason(reason?: string): "malicious_link" | "link_check_inconclusive" {
  return reason === "link_check_inconclusive" ? reason : "malicious_link";
}

/**
 * The copy for a pending message that is gone for a reason nobody attributed to
 * the link check.
 *
 * Separate from the blocked copy on purpose. Telling an author their link was
 * malicious when the evidence only says the message no longer exists is a claim
 * we cannot make, and it is the inference this whole path is built to avoid.
 */
const pendingMessageUnavailable = "Esta mensagem não está mais disponível.";

function sendErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    const known = sendErrorMessages[error.code];
    if (known) return known;
  }
  // Unchanged for everything else: the previous behaviour is the fallback, so
  // no existing error path is affected by RF-21.
  return error instanceof Error ? error.message : "Não foi possível enviar a mensagem.";
}

function isOlderSecurityVersion(candidate: string, current: string): boolean {
  const candidateMs = Date.parse(candidate);
  const currentMs = Date.parse(current);
  return Number.isFinite(candidateMs) && Number.isFinite(currentMs) && candidateMs < currentMs;
}

function isNotNewerSecurityVersion(candidate: string, current: string): boolean {
  const candidateMs = Date.parse(candidate);
  const currentMs = Date.parse(current);
  return Number.isFinite(candidateMs) && Number.isFinite(currentMs) && candidateMs <= currentMs;
}

function applyLinkSafetyCorrection(
  message: Message,
  correction: { state: Message["linkSafetyState"]; updatedAt: string } | undefined,
): Message {
  if (!correction || isOlderSecurityVersion(correction.updatedAt, message.updatedAt))
    return message;
  return {
    ...message,
    linkSafetyState: correction.state,
    bodyText: correction.state === "malicious" ? "" : message.bodyText,
    updatedAt: correction.updatedAt,
  };
}

function applyLinkSafetyCorrections(
  message: Message,
  corrections: MessagesState["linkSafetyCorrections"],
): Message {
  let next = applyLinkSafetyCorrection(message, corrections.get(message.id));
  const quoted = next.quoted;
  const quoteCorrection = quoted && corrections.get(quoted.id);
  if (
    quoted &&
    quoteCorrection &&
    !isOlderSecurityVersion(quoteCorrection.updatedAt, quoted.updatedAt ?? quoted.createdAt)
  ) {
    next = {
      ...next,
      quoted: {
        ...quoted,
        linkSafetyState: quoteCorrection.state ?? "unknown",
        bodyText: quoteCorrection.state === "malicious" ? "" : quoted.bodyText,
        updatedAt: quoteCorrection.updatedAt,
      },
    };
  }
  const reference = next.reference;
  const referenceCorrection = reference?.available && corrections.get(reference.messageId);
  if (
    reference?.available &&
    referenceCorrection &&
    !isOlderSecurityVersion(
      referenceCorrection.updatedAt,
      reference.updatedAt ?? reference.createdAt,
    )
  ) {
    next = {
      ...next,
      reference: {
        ...reference,
        linkSafetyState: referenceCorrection.state ?? "unknown",
        bodyText: referenceCorrection.state === "malicious" ? "" : reference.bodyText,
        updatedAt: referenceCorrection.updatedAt,
      },
    };
  }
  return next;
}

/** The action of one specific type, narrowed out of the union. */
type ActionOf<T extends Action["type"]> = Extract<Action, { type: T }>;

function applyLoaded(state: MessagesState, action: ActionOf<"loaded">): MessagesState {
  return {
    status: "ready",
    messages: action.page.messages.map((message) =>
      applyLinkSafetyCorrections(message, state.linkSafetyCorrections),
    ),
    nextCursor: action.page.nextCursor,
    sendError: null,
    sending: false,
    loadingMore: false,
    lastMutation: "initial",
    realtimeError: null,
    actionError: null,
    pendingReactions: new Map(),
    replyTo: null,
    linkSafetyCorrections: state.linkSafetyCorrections,
  };
}

function applySent(state: MessagesState, action: ActionOf<"sent">): MessagesState {
  // Deduplicate: a realtime event or a prior send might have already added this message.
  const alreadyPresent = state.messages.some((m) => m.id === action.message.id);
  const message = applyLinkSafetyCorrections(action.message, state.linkSafetyCorrections);
  const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
  linkSafetyCorrections.delete(action.message.id);
  return {
    ...state,
    messages: alreadyPresent ? state.messages : [...state.messages, message],
    sending: false,
    sendError: null,
    lastMutation: alreadyPresent ? "none" : "append",
    realtimeError: null,
    replyTo: null,
    linkSafetyCorrections,
  };
}

function applyMessageBlocked(
  state: MessagesState,
  action: ActionOf<"message_blocked">,
): MessagesState {
  // Only a message this client is still showing as pending is affected. A
  // late event for something already resolved, or for a message this view
  // never held, is a no-op.
  const blocked = state.messages.find(
    (m) => m.id === action.messageId && m.status === "pending_link_scan",
  );
  if (!blocked) return state;
  return {
    ...state,
    messages: state.messages.filter((m) => m.id !== action.messageId),
    sending: false,
    // "unavailable" is the one explicit sentinel for "the message itself is
    // gone" (reconciliation found no blocked verdict behind it). Every other
    // reason — a recognised one, or one this client does not know yet — is a
    // link refusal, and defaults to the malicious wording rather than the
    // "gone" one: downgrading an unrecognised-but-real refusal to "message
    // unavailable" would discard a verdict already established.
    sendError:
      action.reason === "unavailable"
        ? pendingMessageUnavailable
        : action.reason === "link_check_inconclusive"
          ? sendErrorMessages.link_check_inconclusive
          : sendErrorMessages.malicious_url,
    lastMutation: "none",
  };
}

type LinkSafetyChange = { state: Message["linkSafetyState"]; updatedAt: string };

/**
 * Whether a link-safety correction is already reflected, or is older than what
 * is drawn.
 *
 * A quote or a cross-target reference can be the only visible copy of the
 * source, and its version is authoritative too: accepting an older correction
 * here would let a later stale snapshot re-apply it. At-least-once delivery of
 * this event is free precisely because this says "nothing to do" for a repeat.
 */
function linkSafetyChangeIsStale(
  state: MessagesState,
  action: ActionOf<"link_safety_changed">,
): boolean {
  const previous = state.linkSafetyCorrections.get(action.messageId);
  if (previous?.state === action.state && previous?.updatedAt === action.updatedAt) return true;
  if (previous && isOlderSecurityVersion(action.updatedAt, previous.updatedAt)) return true;
  if (
    state.replyTo?.id === action.messageId &&
    isOlderSecurityVersion(action.updatedAt, state.replyTo.updatedAt)
  ) {
    return true;
  }
  return state.messages.some((message) => holdsNewerVersionOf(message, action));
}

/** True when this message, its quote or its reference is newer than the change. */
function holdsNewerVersionOf(message: Message, action: ActionOf<"link_safety_changed">): boolean {
  const quoted = message.quoted;
  const reference = message.reference;
  if (message.id === action.messageId) {
    return isOlderSecurityVersion(action.updatedAt, message.updatedAt);
  }
  if (quoted?.id === action.messageId) {
    return isOlderSecurityVersion(action.updatedAt, quoted.updatedAt ?? quoted.createdAt);
  }
  if (reference?.available && reference.messageId === action.messageId) {
    return isOlderSecurityVersion(action.updatedAt, reference.updatedAt ?? reference.createdAt);
  }
  return false;
}

/** The message body's own correction: nothing when it is already applied. */
function correctMessageBody(
  message: Message,
  action: ActionOf<"link_safety_changed">,
  malicious: boolean,
): Message {
  const unchanged =
    (message.linkSafetyState ?? "") === (action.state ?? "") &&
    !(malicious && message.bodyText !== "");
  if (message.id !== action.messageId || unchanged) return message;
  if (isOlderSecurityVersion(action.updatedAt, message.updatedAt)) return message;
  return {
    ...message,
    linkSafetyState: action.state,
    bodyText: malicious ? "" : message.bodyText,
    updatedAt: action.updatedAt,
  };
}

/** The same correction applied to the quote preview this message carries. */
function correctQuotedPreview(
  message: Message,
  action: ActionOf<"link_safety_changed">,
  malicious: boolean,
): Message {
  const quoted = message.quoted;
  if (!quoted || quoted.id !== action.messageId) return message;
  const unchanged =
    quoted.linkSafetyState === action.state && !(malicious && quoted.bodyText !== "");
  if (unchanged) return message;
  if (isOlderSecurityVersion(action.updatedAt, quoted.updatedAt ?? quoted.createdAt))
    return message;
  return {
    ...message,
    quoted: {
      ...quoted,
      linkSafetyState: action.state ?? "",
      bodyText: malicious ? "" : quoted.bodyText,
      updatedAt: action.updatedAt,
    },
  };
}

/** Narrows a reference to the available branch and to one source message. */
function referenceIsAbout(
  reference: Message["reference"],
  messageId: string,
): reference is Extract<NonNullable<Message["reference"]>, { available: true }> {
  return reference?.available === true && reference.messageId === messageId;
}

/** The same correction applied to the cross-target reference preview. */
function correctReferencePreview(
  message: Message,
  action: ActionOf<"link_safety_changed">,
  malicious: boolean,
): Message {
  const reference = message.reference;
  if (!referenceIsAbout(reference, action.messageId)) return message;
  const unchanged =
    reference.linkSafetyState === action.state && !(malicious && reference.bodyText !== "");
  if (unchanged) return message;
  if (isOlderSecurityVersion(action.updatedAt, reference.updatedAt ?? reference.createdAt)) {
    return message;
  }
  return {
    ...message,
    reference: {
      ...reference,
      linkSafetyState: action.state ?? "",
      bodyText: malicious ? "" : reference.bodyText,
      updatedAt: action.updatedAt,
    },
  };
}

/**
 * RF-21 (issue #135): what is known about a published message's links changed.
 *
 * Nothing here inserts a message and nothing changes `status`: if the message
 * has not arrived yet, only a versioned correction is retained for its eventual
 * create event. Nothing here fetches a URL either — see MessageContent, where
 * this state is rendered and never acted on.
 *
 * lastMutation stays "none": nothing was added or removed, so the list must not
 * scroll. A notice appearing above a message the reader is looking at should not
 * move the conversation under them.
 */
function applyLinkSafetyChanged(
  state: MessagesState,
  action: ActionOf<"link_safety_changed">,
): MessagesState {
  if (linkSafetyChangeIsStale(state, action)) return state;
  const change: LinkSafetyChange = { state: action.state, updatedAt: action.updatedAt };
  const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
  linkSafetyCorrections.set(action.messageId, change);
  const malicious = action.state === "malicious";
  const messages = state.messages.map((message) =>
    correctReferencePreview(
      correctQuotedPreview(correctMessageBody(message, action, malicious), action, malicious),
      action,
      malicious,
    ),
  );
  const replyTo =
    state.replyTo?.id === action.messageId
      ? applyLinkSafetyCorrection(state.replyTo, change)
      : state.replyTo;
  return { ...state, messages, replyTo, linkSafetyCorrections, lastMutation: "none" };
}

type AvailableSnapshot = Extract<MessageSecuritySnapshot, { available: true }>;
type LinkSafetyCorrections = Map<string, LinkSafetyChange>;

/**
 * Which version of a message's security state wins: a local correction the
 * snapshot has not caught up with, the snapshot itself, or what is already
 * drawn. A correction that lost is dropped, because the snapshot now carries it.
 */
function resolveSnapshotVersion(
  message: Message,
  snapshot: AvailableSnapshot,
  corrections: LinkSafetyCorrections,
): { state: Message["linkSafetyState"]; updatedAt: string; status: Message["status"] } {
  const correction = corrections.get(message.id);
  const correctionWins = Boolean(
    correction && isNotNewerSecurityVersion(snapshot.updatedAt, correction.updatedAt),
  );
  if (!correctionWins) corrections.delete(message.id);
  const snapshotWins = !isOlderSecurityVersion(snapshot.updatedAt, message.updatedAt);
  const status = snapshotWins ? snapshot.status : message.status;
  if (correction && correctionWins) {
    return { state: correction.state, updatedAt: correction.updatedAt, status };
  }
  if (snapshotWins) {
    return { state: snapshot.linkSafetyState, updatedAt: snapshot.updatedAt, status };
  }
  return { state: message.linkSafetyState, updatedAt: message.updatedAt, status };
}

type QuoteLinkSafety = NonNullable<Message["quoted"]>["linkSafetyState"];

/**
 * Which version of a quote preview's security state wins.
 *
 * A quote can be the only visible copy of its source, so its own version is
 * compared too — a correction only wins while it is newer than both the snapshot
 * and what is drawn.
 */
function resolveQuoteVersion(
  quoted: NonNullable<Message["quoted"]>,
  snapshotQuote: NonNullable<AvailableSnapshot["quoted"]>,
  corrections: LinkSafetyCorrections,
): { state: QuoteLinkSafety; updatedAt: string; removed: boolean } {
  const current = quoted.updatedAt ?? quoted.createdAt;
  const correction = corrections.get(quoted.id);
  const correctionWins = Boolean(
    correction &&
    isNotNewerSecurityVersion(snapshotQuote.updatedAt, correction.updatedAt) &&
    !isOlderSecurityVersion(correction.updatedAt, current),
  );
  if (!correctionWins) corrections.delete(quoted.id);
  const snapshotWins = !isOlderSecurityVersion(snapshotQuote.updatedAt, current);
  const removed = snapshotWins ? snapshotQuote.status === "deleted" : quoted.isRemoved;
  if (correction && correctionWins) {
    return { state: correction.state ?? "unknown", updatedAt: correction.updatedAt, removed };
  }
  if (snapshotWins) {
    return {
      state: snapshotQuote.linkSafetyState,
      updatedAt: snapshotQuote.updatedAt,
      removed,
    };
  }
  return { state: quoted.linkSafetyState, updatedAt: current, removed };
}

/** The resolved quote version, written back onto the message that carries it. */
function applySnapshotToQuote(
  next: Message,
  quoted: NonNullable<Message["quoted"]>,
  snapshotQuote: NonNullable<AvailableSnapshot["quoted"]>,
  corrections: LinkSafetyCorrections,
): Message {
  const { state, updatedAt, removed } = resolveQuoteVersion(quoted, snapshotQuote, corrections);
  return {
    ...next,
    quoted: {
      ...quoted,
      linkSafetyState: state,
      updatedAt,
      isRemoved: removed,
      bodyText: removed || state === "malicious" ? "" : quoted.bodyText,
    },
  };
}

/** A message the server says is gone: it keeps its place and nothing else. */
function withdrawMessage(message: Message): Message {
  return {
    ...message,
    bodyText: "",
    quoted: undefined,
    reference: undefined,
    reactions: [],
    status: "deleted" as const,
    isRemoved: true,
  };
}

function applySnapshotToMessage(
  message: Message,
  snapshot: MessageSecuritySnapshot,
  corrections: LinkSafetyCorrections,
): Message {
  if (!snapshot.available) {
    corrections.delete(message.id);
    return withdrawMessage(message);
  }
  const resolved = resolveSnapshotVersion(message, snapshot, corrections);
  const removed = resolved.status === "deleted";
  const malicious = resolved.state === "malicious";
  const next: Message = {
    ...message,
    status: resolved.status,
    linkSafetyState: resolved.state,
    bodyText: removed || malicious ? "" : message.bodyText,
    isRemoved: removed,
    updatedAt: resolved.updatedAt,
    ...(removed ? { quoted: undefined, reactions: [] } : {}),
  };
  const quoted = message.quoted;
  if (removed || !quoted || snapshot.quoted?.messageId !== quoted.id) return next;
  return applySnapshotToQuote(next, quoted, snapshot.quoted, corrections);
}

/**
 * RF-21 reconciliation: the authoritative security state of a page of messages,
 * read back from the server and merged with any correction still in flight.
 */
function applySecuritySnapshotsRefreshed(
  state: MessagesState,
  action: ActionOf<"security_snapshots_refreshed">,
): MessagesState {
  const snapshots = new Map(action.snapshots.map((snapshot) => [snapshot.messageId, snapshot]));
  const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
  let changed = false;
  const messages = state.messages.map((message) => {
    const snapshot = snapshots.get(message.id);
    if (!snapshot) return message;
    changed = true;
    return applySnapshotToMessage(message, snapshot, linkSafetyCorrections);
  });
  if (!changed) return state;
  const replySnapshot = state.replyTo ? snapshots.get(state.replyTo.id) : undefined;
  const replyTo =
    replySnapshot && (!replySnapshot.available || replySnapshot.status === "deleted")
      ? null
      : state.replyTo;
  return { ...state, messages, replyTo, linkSafetyCorrections, lastMutation: "none" };
}

function applyPrepended(state: MessagesState, action: ActionOf<"prepended">): MessagesState {
  // Prepend older messages; deduplicate by ID to guard against cursor overlaps.
  const existingIds = new Set(state.messages.map((m) => m.id));
  const fresh = action.page.messages
    .filter((m) => !existingIds.has(m.id))
    .map((message) => applyLinkSafetyCorrections(message, state.linkSafetyCorrections));
  // If every message in this page was already present, no DOM change occurs:
  // skip the scroll delta calculation by keeping lastMutation as "none".
  return {
    ...state,
    messages: fresh.length > 0 ? [...fresh, ...state.messages] : state.messages,
    nextCursor: action.page.nextCursor,
    loadingMore: false,
    lastMutation: fresh.length > 0 ? "prepend" : "none",
  };
}

function applyWsReceived(state: MessagesState, action: ActionOf<"ws_received">): MessagesState {
  const received = applyLinkSafetyCorrections(action.message, state.linkSafetyCorrections);
  const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
  linkSafetyCorrections.delete(action.message.id);
  // Dedup: if the message is already present (e.g. our own POST response
  // arrived before the WS event), this is normally a pure no-op.
  const existingIndex = state.messages.findIndex((m) => m.id === received.id);
  if (existingIndex >= 0) {
    const existing = state.messages[existingIndex];
    // RF-21: the one case where "already present" is not a no-op.
    //
    // A message whose links were still being scanned was returned to its
    // own sender as pending_link_scan and shown to nobody else. When the
    // scan clears, the backend promotes it and broadcasts message.created
    // with the same id — so discarding the event by id, as this branch used
    // to do unconditionally, left the sender looking at "checking links…"
    // forever while everyone else saw the message.
    //
    // The event carries the authoritative published row, so it replaces the
    // local one in place: same position, no duplicate, no re-sort. Only this
    // transition is special-cased; every other repeat delivery stays the
    // no-op it was, which is what keeps at-least-once outbox delivery safe.
    if (existing.status === "pending_link_scan" && received.status !== "pending_link_scan") {
      const messages = [...state.messages];
      messages[existingIndex] = received;
      return { ...state, messages, linkSafetyCorrections, realtimeError: null };
    }
    return { ...state, linkSafetyCorrections, realtimeError: null };
  }

  // Insert in stable (createdAt, id) order to handle out-of-order delivery.
  // Most WS messages are newer than all existing ones, so a quick tail-check
  // avoids a full sort in the common case.
  const insertion = insertMessageChronologically(state.messages, received);

  return {
    ...state,
    messages: insertion.messages,
    // ws_append: MessageList scrolls to bottom only if the user is already
    // near the bottom, preserving position when reading history.
    // If the message was inserted mid-list (out-of-order), no auto-scroll.
    lastMutation: insertion.isNewer ? "ws_append" : "none",
    realtimeError: null,
    linkSafetyCorrections,
  };
}

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
        ? correctionWins
          ? applyLinkSafetyCorrection(message, correction)
          : {
              ...message,
              bodyText: action.message.bodyText,
              bodyFormat: action.message.bodyFormat,
              editedAt: action.message.editedAt,
              updatedAt: action.message.updatedAt,
              editCount: action.message.editCount,
              isEdited: action.message.isEdited,
              linkSafetyState: action.message.linkSafetyState,
            }
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
  /** A local correction that is still newer than this update, if any. */
  correction: LinkSafetyChange | undefined;
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
    correction,
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

/**
 * A copy of the corrections with the one this version supersedes removed. A
 * correction newer than the version being applied is kept: it is still the most
 * recent thing known about that message's links.
 */
function dropSupersededCorrection(
  corrections: LinkSafetyCorrections,
  messageId: string,
  version: string,
): LinkSafetyCorrections {
  const next = new Map(corrections);
  const correction = next.get(messageId);
  if (!correction || !isNotNewerSecurityVersion(version, correction.updatedAt)) {
    next.delete(messageId);
  }
  return next;
}

/** The snapshot written onto the message it replaces, or onto a quote of it. */
function applySnapshotToTimeline(message: Message, snapshot: Message, removed: boolean): Message {
  if (message.id === snapshot.id) {
    return message.isRemoved && !removed ? message : snapshot;
  }
  if (!removed || message.quoted?.id !== snapshot.id) return message;
  return {
    ...message,
    quoted: {
      ...message.quoted,
      bodyText: "",
      isRemoved: true,
      deletedAt: snapshot.deletedAt ?? snapshot.updatedAt,
    },
  };
}

/**
 * An authoritative single-message read, applied to the timeline.
 *
 * This is the resync path: after a reconnect, or after a realtime event this
 * client could not trust, the server's own copy replaces what is drawn.
 */
function applyMessageSnapshot(
  state: MessagesState,
  action: ActionOf<"message_snapshot">,
): MessagesState {
  const removed = action.message.isRemoved || action.message.status === "deleted";
  const rawSnapshot = removed
    ? { ...action.message, bodyText: "", quoted: undefined, reactions: [] }
    : action.message;
  const snapshot = applyLinkSafetyCorrections(rawSnapshot, state.linkSafetyCorrections);
  const linkSafetyCorrections = dropSupersededCorrection(
    state.linkSafetyCorrections,
    rawSnapshot.id,
    rawSnapshot.updatedAt,
  );
  const alreadyPresent = state.messages.some((message) => message.id === snapshot.id);
  const rewritten = state.messages.map((message) =>
    applySnapshotToTimeline(message, snapshot, removed),
  );
  const insertion =
    !alreadyPresent && action.insertIfMissing
      ? insertMessageChronologically(rewritten, snapshot)
      : { messages: rewritten, isNewer: false };
  return {
    ...state,
    messages: insertion.messages,
    replyTo: removed && state.replyTo?.id === snapshot.id ? null : state.replyTo,
    lastMutation: insertion.isNewer ? "ws_append" : "none",
    realtimeError: null,
    linkSafetyCorrections,
  };
}

/**
 * Whether a refetched reference preview is older than the one already drawn.
 *
 * The second clause is the one that matters for RF-21: a preview this client
 * already knows was condemned must not be replaced by an unversioned answer that
 * says otherwise, because the correction that condemned it may simply not have
 * reached the endpoint that produced this one yet.
 */
function refreshedReferenceIsStale(
  current: NonNullable<Message["reference"]>,
  refreshed: NonNullable<Message["reference"]>,
): boolean {
  if (!current.available || !refreshed.available) return false;
  if (
    current.updatedAt &&
    refreshed.updatedAt &&
    isOlderSecurityVersion(refreshed.updatedAt, current.updatedAt)
  ) {
    return true;
  }
  return (
    current.linkSafetyState === "malicious" &&
    !refreshed.updatedAt &&
    refreshed.linkSafetyState !== "malicious"
  );
}

function applyReferencesRefreshed(
  state: MessagesState,
  action: ActionOf<"references_refreshed">,
): MessagesState {
  return {
    ...state,
    messages: state.messages.map((message) => {
      const reference = action.references[message.id];
      if (!reference || !message.reference) return message;
      return refreshedReferenceIsStale(message.reference, reference)
        ? message
        : { ...message, reference };
    }),
  };
}

function applyAttachmentStatus(
  state: MessagesState,
  action: ActionOf<"attachment_status">,
): MessagesState {
  // RF-22 verdict for an attachment shown inside a message (RF-32).
  //
  // Patched in place rather than refetched: the event already carries the
  // authoritative new status, and the status is the only thing that changed
  // — nothing here decides what may be downloaded. That gate is
  // file-service's, applied to every content and preview request, so a
  // client that got this wrong would still be refused the bytes.
  //
  // Messages that carry no matching attachment are returned unchanged by
  // identity, so an event for another conversation's file — or one this
  // timeline has never seen — allocates nothing and rerenders nothing.
  let changed = false;
  const messages = state.messages.map((message) => {
    if (!message.attachments?.some((item) => item.id === action.attachmentId)) {
      return message;
    }
    changed = true;
    return {
      ...message,
      attachments: message.attachments.map((item) =>
        item.id === action.attachmentId ? { ...item, status: action.status } : item,
      ),
    };
  });
  return changed ? { ...state, messages, lastMutation: "none" } : state;
}

function applyAttachmentsReconciled(
  state: MessagesState,
  action: ActionOf<"attachments_reconciled">,
): MessagesState {
  // Preview reconciliation (RF-31/#464 pattern, applied to the inline
  // thread rather than the details panel). There is no WebSocket event
  // for "the preview finished" — only attachment_status above, and that
  // fires on the scan verdict, before the render even starts — so a
  // message posted with previewStatus "pending" would otherwise show the
  // icon fallback forever until the thread is reloaded. This patches in
  // whatever a polled listing found, by id.
  //
  // Keyed by id and applied field by field, exactly like attachment_status:
  // a listing for one destination can carry attachments this timeline has
  // never rendered (older messages outside the loaded page), and those
  // update nothing.
  if (action.attachments.length === 0) return state;
  const byId = new Map(action.attachments.map((attachment) => [attachment.id, attachment]));
  let changed = false;
  const messages = state.messages.map((message) => {
    if (!message.attachments?.some((item) => byId.has(item.id))) {
      return message;
    }
    let messageChanged = false;
    const attachments = message.attachments.map((item) => {
      const fresh = byId.get(item.id);
      if (!fresh || (fresh.status === item.status && fresh.previewStatus === item.previewStatus)) {
        return item;
      }
      messageChanged = true;
      return { ...item, status: fresh.status, previewStatus: fresh.previewStatus };
    });
    if (!messageChanged) return message;
    changed = true;
    return { ...message, attachments };
  });
  return changed ? { ...state, messages, lastMutation: "none" } : state;
}

function applyReactionError(
  state: MessagesState,
  action: ActionOf<"reaction_error">,
): MessagesState {
  // Server-level errors (rate limit, feature unavailable) aren't scoped to a
  // single message, so every optimistic toggle still in flight is reverted.
  if (state.pendingReactions.size === 0) {
    return { ...state, actionError: action.error };
  }
  const messages = state.messages.map((message) => {
    const pending = state.pendingReactions.get(message.id);
    return pending ? { ...message, reactions: pending.confirmed } : message;
  });
  return { ...state, messages, actionError: action.error, pendingReactions: new Map() };
}

function applyFavoriteSet(state: MessagesState, action: ActionOf<"favorite_set">): MessagesState {
  const index = state.messages.findIndex((message) => message.id === action.messageId);
  if (index < 0) return state;
  const messages = [...state.messages];
  messages[index] = { ...messages[index], isFavorited: action.isFavorited };
  return { ...state, messages };
}

function applyReactionOptimistic(
  state: MessagesState,
  action: ActionOf<"reaction_optimistic">,
): MessagesState {
  const index = state.messages.findIndex((message) => message.id === action.messageId);
  if (index < 0) return state;
  const message = state.messages[index];
  const previous = pendingFor(state, action.messageId, message.reactions);
  // The toggle is read off what the reader is looking at, so a second toggle of
  // the same emoji supersedes the first rather than stacking with it.
  const reacted =
    message.reactions.find((item) => item.emoji === action.emoji)?.reactedByMe ?? false;
  const intents = new Map(previous.intents).set(action.emoji, reacted ? "removed" : "added");
  const pending = { confirmed: previous.confirmed, intents };
  const messages = [...state.messages];
  messages[index] = { ...message, reactions: applyPendingReactions(pending) };
  return {
    ...state,
    messages,
    pendingReactions: withPending(state, action.messageId, pending),
    actionError: null,
  };
}

/** Undoes one intent — a refused send, or a confirmation that never came. */
function applyReactionRevert(
  state: MessagesState,
  action: ActionOf<"reaction_revert">,
): MessagesState {
  const previous = state.pendingReactions.get(action.messageId);
  if (!previous?.intents.has(action.emoji)) return { ...state, actionError: action.error };
  const intents = new Map(previous.intents);
  intents.delete(action.emoji);
  const pending = { confirmed: previous.confirmed, intents };
  const index = state.messages.findIndex((message) => message.id === action.messageId);
  const reactions = applyPendingReactions(pending);
  const messages =
    index < 0
      ? state.messages
      : state.messages.map((message, i) => (i === index ? { ...message, reactions } : message));
  return {
    ...state,
    messages,
    pendingReactions: withPending(state, action.messageId, pending),
    actionError: action.error,
  };
}

/**
 * A refetched message replaces what the server had said, and the intents still
 * in flight are re-applied on top of it — a resync must not swallow a toggle the
 * reader is still waiting on.
 */
function applyReactionSnapshot(
  state: MessagesState,
  action: ActionOf<"reaction_snapshot">,
): MessagesState {
  const index = state.messages.findIndex((message) => message.id === action.messageId);
  if (index < 0) return state;
  const previous = state.pendingReactions.get(action.messageId);
  const pending = { confirmed: action.reactions, intents: previous?.intents ?? new Map() };
  const messages = [...state.messages];
  messages[index] = { ...messages[index], reactions: applyPendingReactions(pending) };
  return {
    ...state,
    messages,
    pendingReactions: withPending(state, action.messageId, pending),
    lastMutation: "none",
    realtimeError: null,
    actionError: null,
  };
}

/** Loading a conversation and paging through it. */
function reduceHistory(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "loading":
      // Reset cursor and loadingMore so stale pagination state does not carry over.
      return {
        ...state,
        status: "loading",
        sendError: null,
        sending: false,
        loadingMore: false,
        nextCursor: "",
        lastMutation: "none",
        realtimeError: null,
        actionError: null,
        pendingReactions: new Map(),
        replyTo: null,
      };
    case "loaded":
      return applyLoaded(state, action);
    case "error":
      return { ...state, status: "error", sending: false, lastMutation: "none" };
    case "prepending":
      return { ...state, loadingMore: true, lastMutation: "none" };
    case "prepended":
      return applyPrepended(state, action);
    case "prepend_error":
      return { ...state, loadingMore: false, lastMutation: "none" };
    default:
      return undefined;
  }
}

/** Sending, and what the server refused. */
function reduceComposer(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "sending":
      return { ...state, sending: true, sendError: null };
    case "sent":
      return applySent(state, action);
    case "send_error":
      return { ...state, sending: false, sendError: action.error };
    case "message_blocked":
      return applyMessageBlocked(state, action);
    default:
      return undefined;
  }
}

/** RF-21 verdicts about links, and the snapshots that carry them. */
function reduceLinkSafety(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "link_safety_changed":
      return applyLinkSafetyChanged(state, action);
    case "security_snapshots_refreshed":
      return applySecuritySnapshotsRefreshed(state, action);
    case "references_refreshed":
      return applyReferencesRefreshed(state, action);
    default:
      return undefined;
  }
}

/** What the socket delivered about messages in this conversation. */
function reduceRealtime(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "ws_received":
      return applyWsReceived(state, action);
    case "message_updated":
      return applyMessageUpdated(state, action);
    case "message_snapshot":
      return applyMessageSnapshot(state, action);
    case "attachment_status":
      return applyAttachmentStatus(state, action);
    case "attachments_reconciled":
      return applyAttachmentsReconciled(state, action);
    case "ws_fetch_error":
      return { ...state, realtimeError: action.error, lastMutation: "none" };
    case "ws_subscription_ready":
      return { ...state, realtimeError: null };
    default:
      return undefined;
  }
}

/** Editing and deleting a message this reader wrote. */
function reduceEditing(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "edit_optimistic":
      return applyEditOptimistic(state, action);
    case "edit_confirmed":
      return applyEditConfirmed(state, action);
    case "edit_revert":
      return applyEditRevert(state, action);
    case "delete_error":
      return { ...state, actionError: action.error };
    default:
      return undefined;
  }
}

/** Reactions: optimistic toggles, their confirmations and their refusals. */
function reduceReactions(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "reaction_optimistic":
      return applyReactionOptimistic(state, action);
    case "reaction_revert":
      return applyReactionRevert(state, action);
    case "reaction_updated":
      return applyReactionEvent(state, action.event, action.actorIsMe);
    case "reaction_snapshot":
      return applyReactionSnapshot(state, action);
    case "reaction_error":
      return applyReactionError(state, action);
    case "reaction_error_clear":
      return { ...state, actionError: null };
    default:
      return undefined;
  }
}

/** Reply target and favourites — per-reader state beside the list. */
function reduceConversation(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "reply_set":
      return { ...state, replyTo: action.message };
    case "reply_clear":
      return { ...state, replyTo: null };
    case "favorite_set":
      return applyFavoriteSet(state, action);
    case "favorite_error":
      // Reuses the transient banner without touching reaction snapshots.
      return { ...state, actionError: action.error };
    default:
      return undefined;
  }
}

/**
 * The conversation's state machine.
 *
 * Every action belongs to exactly one subject — history, composing, link
 * safety, realtime delivery, editing, reactions, or the reader's own selection —
 * so the reducer asks each in turn and the first one that recognises the action
 * answers. A sub-reducer returns undefined for an action that is not its
 * business, which is what keeps the groups disjoint and this function a
 * dispatcher rather than a second copy of the switch.
 */
function reducer(state: MessagesState, action: Action): MessagesState {
  return (
    reduceHistory(state, action) ??
    reduceComposer(state, action) ??
    reduceLinkSafety(state, action) ??
    reduceRealtime(state, action) ??
    reduceEditing(state, action) ??
    reduceReactions(state, action) ??
    reduceConversation(state, action) ??
    state
  );
}

// ── Hook ──────────────────────────────────────────────────────────────────────

interface UseMessagesOptions {
  kind: "channel" | "dm";
  targetId: string;
  bodyFormat?: "v2" | "v3";
  currentUserId: string;
  /** Direct-navigation target resolved through the authorized single-message GET. */
  focusMessageId?: string;
  onOwnReactionConfirmed?: (emoji: string) => void;
  /** RF-05: called on a pin.updated event for the active target (refetch pins). */
  onPinUpdated?: (event: WSPinUpdatedEvent) => void;
  /**
   * Issue #398: called on a members.added event for the active target.
   *
   * Routed through this hook rather than a second socket because the connection
   * and its subscriptions already live here; opening another one to hear about
   * membership would double the WebSocket count per conversation.
   */
  onMembersAdded?: (event: WSMembersAddedEvent) => void;
  /**
   * RF-22: called on an attachment.status event for the active target.
   *
   * Routed through this hook for the same reason members.added is — the
   * connection and its subscriptions already live here — and filtered to the
   * active target, so a verdict for another conversation cannot make this one
   * refetch.
   */
  onAttachmentStatus?: (event: WSAttachmentStatusEvent) => void;
  /**
   * Typing indicator: called on a typing.updated event for the active target,
   * including the local user's own echo — self-filtering is left to the
   * caller (useTypingIndicator), the same way actorIsMe is computed per
   * caller for reactions rather than dropped here.
   */
  onTypingUpdated?: (event: WSTypingUpdatedEvent) => void;
  onMessageRemoved?: () => void;
}

export interface UseMessagesResult {
  state: MessagesState;
  /**
   * Posts a message. `attachmentIds` (RF-32) are references to files already
   * uploaded to this destination — nothing is re-uploaded here — and a message
   * with one is valid even when the body is empty.
   */
  sendMessage: (
    body: string,
    referencedMessageId?: string,
    attachmentIds?: string[],
  ) => Promise<SendResult>;
  retry: () => void;
  loadMore: () => void;
  selectReply: (message: Message) => void;
  cancelReply: () => void;
  toggleReaction: (messageId: string, emoji: string) => void;
  /**
   * Declares this user's typing intent for the active target. See
   * ChatWebSocketActions.sendTyping — returns false when the shared socket is
   * not open.
   */
  sendTyping: (isTyping: boolean) => boolean;
  toggleFavorite: (messageId: string, isFavorited: boolean) => void;
  /**
   * RF-21 "Verificar novamente" (issue #135): asks the server to re-read what it
   * already knows about one message's unverified links. It never starts a new
   * scan. Resolves to the message's state afterwards, or `undefined` when the
   * request itself failed — in both cases the caller simply re-enables its
   * button.
   */
  reconcileLinkSafety: (messageId: string) => Promise<LinkSafetyRecheck | undefined>;
  editMessageLocal: (
    messageId: string,
    body: string,
    bodyFormat: Message["bodyFormat"],
  ) => Promise<Message>;
  deleteMessageLocal: (messageId: string) => Promise<void>;
}

export function useMessages({
  kind,
  targetId,
  bodyFormat = kind === "channel" ? "v3" : "v2",
  currentUserId,
  focusMessageId,
  onOwnReactionConfirmed,
  onPinUpdated,
  onMembersAdded,
  onAttachmentStatus,
  onTypingUpdated,
  onMessageRemoved,
}: UseMessagesOptions): UseMessagesResult {
  const [state, dispatch] = useReducer(reducer, initialState);

  useEffect(() => {
    if (!state.actionError) return;
    const timer = window.setTimeout(() => dispatch({ type: "reaction_error_clear" }), 5_000);
    return () => window.clearTimeout(timer);
  }, [state.actionError]);

  // stateRef holds values that stable callbacks (loadMore, sendMessage, load) read
  // after async gaps, so they always see the current target and pagination state.
  // useLayoutEffect (no deps) fires synchronously after every render, before any
  // microtask, ensuring the ref is up-to-date before any async resolution can run.
  const stateRef = useRef({
    target: `${kind}:${targetId}`,
    nextCursor: state.nextCursor,
    loadingMore: state.loadingMore,
    messages: state.messages,
    replyTo: state.replyTo,
  });
  useLayoutEffect(() => {
    stateRef.current.target = `${kind}:${targetId}`;
    stateRef.current.nextCursor = state.nextCursor;
    stateRef.current.loadingMore = state.loadingMore;
    stateRef.current.messages = state.messages;
    stateRef.current.replyTo = state.replyTo;
  });
  useLayoutEffect(() => {
    pendingReactionsRef.current = state.pendingReactions;
  }, [state.pendingReactions]);

  const abortRef = useRef<AbortController | null>(null);
  const loadMoreAbortRef = useRef<AbortController | null>(null);
  const wsFallbackAbortRefs = useRef<Map<string, AbortController>>(new Map());
  const deletedMessageTombstonesRef = useRef<Map<string, string | null>>(new Map());
  const deletedMessageTombstoneTargetRef = useRef(`${kind}:${targetId}`);
  // Nested by message and then by emoji, so one confirmation clears one timer.
  // A message can carry several toggles at once and each waits on its own.
  const pendingReactionTimersRef = useRef<Map<string, Map<string, number>>>(new Map());
  /**
   * A read-only mirror of the reducer's pending intents, for the one decision
   * that has to be made before dispatching: whether an incoming event confirms
   * an addition this client asked for, and so counts as a use.
   */
  const pendingReactionsRef = useRef(initialState.pendingReactions);
  const pendingSendIdentityRef = useRef<{ signature: string; key: string } | null>(null);
  const onMessageRemovedRef = useRef(onMessageRemoved);
  useLayoutEffect(() => {
    onMessageRemovedRef.current = onMessageRemoved;
  }, [onMessageRemoved]);
  /** Stops waiting on one toggle, or on every toggle of a message when no emoji is named. */
  const clearReactionTimer = useCallback((messageId: string, emoji?: string) => {
    const timers = pendingReactionTimersRef.current.get(messageId);
    if (!timers) return;
    if (emoji === undefined) {
      for (const timer of timers.values()) window.clearTimeout(timer);
      pendingReactionTimersRef.current.delete(messageId);
      return;
    }
    const timer = timers.get(emoji);
    if (timer === undefined) return;
    window.clearTimeout(timer);
    timers.delete(emoji);
    if (timers.size === 0) pendingReactionTimersRef.current.delete(messageId);
  }, []);

  const abortWsFallbacks = useCallback(() => {
    for (const ctrl of wsFallbackAbortRefs.current.values()) ctrl.abort();
    wsFallbackAbortRefs.current.clear();
  }, []);

  const startWsFallback = useCallback((key: string) => {
    wsFallbackAbortRefs.current.get(key)?.abort();
    const ctrl = new AbortController();
    wsFallbackAbortRefs.current.set(key, ctrl);
    return ctrl;
  }, []);

  const cancelWsFallback = useCallback((key: string) => {
    const ctrl = wsFallbackAbortRefs.current.get(key);
    ctrl?.abort();
    wsFallbackAbortRefs.current.delete(key);
  }, []);

  const finishWsFallback = useCallback((key: string, ctrl: AbortController) => {
    if (wsFallbackAbortRefs.current.get(key) === ctrl) {
      wsFallbackAbortRefs.current.delete(key);
    }
  }, []);

  const sanitizeTombstonedMessage = useCallback((message: Message): Message => {
    if (deletedMessageTombstonesRef.current.has(message.id)) {
      return {
        ...message,
        bodyText: "",
        quoted: undefined,
        reactions: [],
        isRemoved: true,
        status: "deleted",
        deletedAt: deletedMessageTombstonesRef.current.get(message.id) ?? message.deletedAt,
      };
    }
    if (message.quoted && deletedMessageTombstonesRef.current.has(message.quoted.id)) {
      return {
        ...message,
        quoted: {
          ...message.quoted,
          bodyText: "",
          isRemoved: true,
          deletedAt:
            deletedMessageTombstonesRef.current.get(message.quoted.id) ?? message.quoted.deletedAt,
        },
      };
    }
    return message;
  }, []);

  const isCurrentTarget = useCallback((loadKey: string) => {
    return stateRef.current.target === loadKey;
  }, []);

  const isMessageRendered = useCallback((messageId: string) => {
    return stateRef.current.messages.some((message) => message.id === messageId);
  }, []);

  const load = useCallback(
    (id: string, k: "channel" | "dm") => {
      const loadKey = `${k}:${id}`;
      abortRef.current?.abort();
      loadMoreAbortRef.current?.abort();
      abortWsFallbacks();
      if (deletedMessageTombstoneTargetRef.current !== loadKey) {
        deletedMessageTombstonesRef.current.clear();
        deletedMessageTombstoneTargetRef.current = loadKey;
      }
      const ctrl = new AbortController();
      abortRef.current = ctrl;

      dispatch({ type: "loading" });

      const fetchFn: () => Promise<MessagePage> =
        k === "channel"
          ? () => fetchChannelMessages(id, undefined, ctrl.signal)
          : () => fetchDMMessages(id, undefined, ctrl.signal);

      const fetchPage = async (): Promise<MessagePage> => {
        const page = await fetchFn();
        if (!focusMessageId || page.messages.some((message) => message.id === focusMessageId)) {
          return page;
        }
        try {
          const focused =
            k === "channel"
              ? await fetchChannelMessage(id, focusMessageId, ctrl.signal)
              : await fetchDMMessage(id, focusMessageId, ctrl.signal);
          return {
            ...page,
            messages: insertMessageChronologically(page.messages, focused).messages,
          };
        } catch {
          // Invalid, removed, or inaccessible focus IDs never reveal which case occurred.
          return page;
        }
      };

      fetchPage().then(
        (page) => {
          if (!isCurrentTarget(loadKey)) return;
          dispatch({
            type: "loaded",
            page: { ...page, messages: page.messages.map(sanitizeTombstonedMessage) },
          });
        },
        (err: unknown) => {
          if (!isCurrentTarget(loadKey)) return;
          if (err instanceof Error && err.name === "AbortError") return;
          dispatch({ type: "error" });
        },
      );

      return () => {
        ctrl.abort();
        loadMoreAbortRef.current?.abort();
        abortWsFallbacks();
        for (const timers of pendingReactionTimersRef.current.values()) {
          for (const timer of timers.values()) window.clearTimeout(timer);
        }
        pendingReactionTimersRef.current.clear();
      };
    },
    [abortWsFallbacks, focusMessageId, isCurrentTarget, sanitizeTombstonedMessage],
  );

  useEffect(() => {
    if (!targetId) return;
    return load(targetId, kind);
  }, [kind, targetId, load]);

  const referencedMessageIDs = useMemo(
    () => state.messages.filter((message) => message.reference).map((message) => message.id),
    [state.messages],
  );
  const referencedMessageIDsKey = referencedMessageIDs.join(",");

  useEffect(() => {
    if (!targetId || !referencedMessageIDsKey) return;
    const loadKey = `${kind}:${targetId}`;
    const allMessageIDs = referencedMessageIDsKey.split(",");
    const messageIDs = allMessageIDs.slice(-referenceRevalidationBatchSize);
    const overflowMessageIDs = allMessageIDs.slice(0, -referenceRevalidationBatchSize);
    if (overflowMessageIDs.length > 0) {
      dispatch({
        type: "references_refreshed",
        references: Object.fromEntries(
          overflowMessageIDs.map((messageID) => [messageID, { available: false }]),
        ),
      });
    }
    let generation = 0;
    let activeController: AbortController | null = null;
    let disposed = false;
    let revalidationScheduled = false;

    const revalidate = () => {
      generation += 1;
      const currentGeneration = generation;
      activeController?.abort();
      const controller = new AbortController();
      activeController = controller;
      const request =
        kind === "channel"
          ? resolveChannelMessageReferences(targetId, messageIDs, controller.signal)
          : resolveDMMessageReferences(targetId, messageIDs, controller.signal);
      void request.then(
        (references) => {
          if (disposed || currentGeneration !== generation || !isCurrentTarget(loadKey)) return;
          dispatch({
            type: "references_refreshed",
            references: Object.fromEntries(
              messageIDs.map((messageID) => [
                messageID,
                references[messageID] ?? { available: false },
              ]),
            ),
          });
        },
        () => {
          if (disposed || currentGeneration !== generation || !isCurrentTarget(loadKey)) return;
          dispatch({
            type: "references_refreshed",
            references: Object.fromEntries(
              messageIDs.map((messageID) => [messageID, { available: false }]),
            ),
          });
        },
      );
    };
    const scheduleRevalidation = () => {
      if (revalidationScheduled) return;
      revalidationScheduled = true;
      queueMicrotask(() => {
        revalidationScheduled = false;
        if (!disposed) revalidate();
      });
    };

    const timer = window.setInterval(revalidate, referenceRevalidationMs);
    window.addEventListener("focus", scheduleRevalidation);
    const onVisibilityChange = () => {
      if (document.visibilityState === "visible") scheduleRevalidation();
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      disposed = true;
      generation += 1;
      activeController?.abort();
      window.clearInterval(timer);
      window.removeEventListener("focus", scheduleRevalidation);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [isCurrentTarget, kind, referencedMessageIDsKey, targetId]);

  const retry = useCallback(() => {
    if (targetId) load(targetId, kind);
  }, [kind, targetId, load]);

  const loadMore = useCallback(() => {
    const { nextCursor, loadingMore } = stateRef.current;
    if (!nextCursor || loadingMore) return;

    // Update the in-flight flag synchronously — before any async work — so that a
    // second loadMore() call in the same microtask tick (e.g., two IO callbacks
    // fired before the next React render) fails the guard above and does not
    // dispatch a duplicate fetch. The flag is cleared in both success and error paths.
    stateRef.current.loadingMore = true;

    const loadKey = `${kind}:${targetId}`;

    loadMoreAbortRef.current?.abort();
    const ctrl = new AbortController();
    loadMoreAbortRef.current = ctrl;

    dispatch({ type: "prepending" });

    const fetchFn: () => Promise<MessagePage> =
      kind === "channel"
        ? () => fetchChannelMessages(targetId, nextCursor, ctrl.signal)
        : () => fetchDMMessages(targetId, nextCursor, ctrl.signal);

    fetchFn().then(
      (page) => {
        stateRef.current.loadingMore = false;
        if (!isCurrentTarget(loadKey)) return;
        dispatch({
          type: "prepended",
          page: { ...page, messages: page.messages.map(sanitizeTombstonedMessage) },
        });
      },
      (err: unknown) => {
        stateRef.current.loadingMore = false;
        if (!isCurrentTarget(loadKey)) return;
        if (err instanceof Error && err.name === "AbortError") return;
        dispatch({ type: "prepend_error" });
      },
    );
  }, [kind, targetId, isCurrentTarget, sanitizeTombstonedMessage]);

  const sendMessage = useCallback(
    async (
      body: string,
      referencedMessageId?: string,
      attachmentIds?: string[],
    ): Promise<SendResult> => {
      // An attachment is content: a message carrying one is sendable with an
      // empty body, and the server applies the same rule.
      if (!targetId || (!body.trim() && !attachmentIds?.length)) return { status: "stale" };

      // Sending a message is unambiguous proof the person is here, and it goes
      // over HTTP — so nothing about it would otherwise reach the presence
      // tracker, which only sees WebSocket frames (issue #444).
      markPresenceActivity();

      const sendKey = `${kind}:${targetId}`;
      const parentMessageId = stateRef.current.replyTo?.id;
      dispatch({ type: "sending" });

      try {
        const signature = JSON.stringify({
          target: sendKey,
          bodyFormat,
          body,
          parentMessageId,
          referencedMessageId,
          attachmentIds: attachmentIds ?? [],
        });
        if (pendingSendIdentityRef.current?.signature !== signature) {
          pendingSendIdentityRef.current = { signature, key: randomId() };
        }
        const options = {
          parentMessageId,
          referencedMessageId,
          attachmentIds,
          idempotencyKey: pendingSendIdentityRef.current.key,
          ...(kind === "dm" ? { bodyFormat } : {}),
        };
        const sendFn =
          kind === "channel"
            ? () => postChannelMessage(targetId, body, options)
            : () => postDMMessage(targetId, body, options);

        const msg = await sendFn();

        if (stateRef.current.target !== sendKey) return { status: "stale" };
        pendingSendIdentityRef.current = null;
        dispatch({ type: "sent", message: sanitizeTombstonedMessage(msg) });
        return { status: "sent" };
      } catch (err: unknown) {
        // Stale failure: silently discard — do not update state for a previous target.
        if (stateRef.current.target !== sendKey) return { status: "stale" };
        dispatch({ type: "send_error", error: sendErrorMessage(err) });
        // Re-throw for current-target failures so callers can preserve the draft.
        throw err;
      }
    },
    [bodyFormat, kind, sanitizeTombstonedMessage, targetId],
  );

  // Handle incoming message.created WS events.
  //
  // Primary path: use evt.payload (full DTO from server) to insert the message
  // directly — no additional GET required.
  //
  // Fallback path: if payload is absent (old server version during rolling deploy),
  // fall back to a targeted GET to avoid silent message loss.
  //
  // Target check: events for other channels/DMs are ignored (defence-in-depth on
  // top of the WS hook's own filter).
  // RF-21: the author's message was refused.
  //
  // Without this the composer's "checking links…" bubble had no event that would
  // ever change it — the backend took the message to a terminal state and told
  // nobody — so the author was left believing a send was still in flight.
  //
  // The event is addressed to the author alone and carries no content, so there
  // is nothing to render from it: the pending bubble is removed and the send
  // error explains why. Removing rather than rewriting is deliberate — the
  // message was never published, so leaving a husk of it in the transcript would
  // suggest otherwise.
  const handleWsMessageBlocked = useCallback((evt: WSMessageBlockedEvent) => {
    dispatch({
      type: "message_blocked",
      messageId: evt.message_id,
      reason: blockedMessageReason(evt.reason),
    });
  }, []);

  // RF-21: a published message's link-safety state changed (issue #135).
  //
  // This is the convergence path for a reconciliation that landed after the
  // message was delivered — the notice disappearing when a verdict finally
  // arrives, or the links being withdrawn when one turns out to be malicious.
  // Unlike message.blocked it never removes the message: it was published, and it
  // stays published.
  //
  // The event's own `link_safety.state` is preferred and the envelope's is not
  // consulted for the value, so a payload the server stripped simply carries the
  // conservative fallback normalizeLinkSafety produces for an unknown value.
  const handleWsLinkSafetyChanged = useCallback((evt: WSMessageLinkSafetyChangedEvent) => {
    dispatch({
      type: "link_safety_changed",
      messageId: evt.message_id,
      state: normalizeLinkSafety(evt.link_safety?.state),
      updatedAt: evt.link_safety.updated_at,
    });
  }, []);

  const handleWsMessageCreated = useCallback(
    (evt: WSMessageCreatedEvent) => {
      const loadKey = `${kind}:${targetId}`;

      // Double-check target (ws hook already filters, but guard here too).
      if (evt.target_id !== targetId) return;

      if (evt.payload) {
        cancelWsFallback(`created:${evt.message_id}`);
        // Build Message from the full DTO carried in the event.
        const p = evt.payload;
        const removed = p.is_removed || p.status === "deleted" || Boolean(p.deleted_at);
        const msg: Message = {
          id: p.id,
          senderId: p.sender_id,
          senderDisplayName: p.sender_display_name,
          senderEmail: p.sender_email ?? "",
          senderAvatarUrl: safeAvatarUrl(p.sender_avatar_url),
          kind: p.kind as Message["kind"],
          bodyText: removed ? "" : p.body_text,
          bodyFormat: normalizeBodyFormat(p.body_format),
          isRemoved: removed,
          status: removed ? "deleted" : "active",
          // RF-21 (issue #135). A published message may carry links the provider
          // could not produce a verdict for; this is what draws the notice. It is
          // rendered, never acted on — nothing in this client fetches a URL.
          linkSafetyState: removed ? "" : normalizeLinkSafety(p.link_safety_state),
          deletedAt: p.deleted_at ?? null,
          createdAt: p.created_at,
          updatedAt: p.updated_at,
          isEdited: Boolean(p.edited_at),
          editCount: 0,
          editedAt: p.edited_at ?? undefined,
          reactions: [],
          // WS create events never carry the caller's favorite state; a message
          // just created cannot be favorited yet.
          isFavorited: false,
          isForwarded: p.is_forwarded === true,
          quoted:
            !removed && p.quoted
              ? {
                  id: p.quoted.id,
                  authorId: p.quoted.author_id,
                  bodyText: p.quoted.body ?? "",
                  bodyFormat: normalizeBodyFormat(p.quoted.body_format),
                  isRemoved: p.quoted.is_removed ?? false,
                  deletedAt: p.quoted.deleted_at ?? null,
                  createdAt: p.quoted.created_at ?? "",
                  updatedAt: p.quoted.updated_at ?? p.quoted.created_at ?? "",
                  linkSafetyState: normalizeLinkSafety(p.quoted.link_safety_state),
                }
              : undefined,
          // Same parser as the HTTP path, so an event and a refetch describe the
          // same attachment. Withheld for a removed message, like the body.
          attachments: removed ? undefined : parseMessageAttachments(p.attachments),
        };
        if (!isCurrentTarget(loadKey)) return;
        dispatch({ type: "ws_received", message: sanitizeTombstonedMessage(msg) });
        return;
      }

      // Fallback: payload absent — fetch the message by ID.
      const fallbackKey = `created:${evt.message_id}`;
      const ctrl = startWsFallback(fallbackKey);
      const fetchFn =
        kind === "channel"
          ? () => fetchChannelMessage(targetId, evt.message_id, ctrl.signal)
          : () => fetchDMMessage(targetId, evt.message_id, ctrl.signal);

      fetchFn().then(
        (msg) => {
          finishWsFallback(fallbackKey, ctrl);
          if (ctrl.signal.aborted) return;
          if (!isCurrentTarget(loadKey)) return;
          dispatch({ type: "ws_received", message: sanitizeTombstonedMessage(msg) });
        },
        (err: unknown) => {
          finishWsFallback(fallbackKey, ctrl);
          if (!isCurrentTarget(loadKey)) return;
          if (err instanceof Error && err.name === "AbortError") return;
          dispatch({ type: "ws_fetch_error", error: realtimeFallbackErrorMessage });
        },
      );
    },
    [
      cancelWsFallback,
      finishWsFallback,
      kind,
      targetId,
      isCurrentTarget,
      sanitizeTombstonedMessage,
      startWsFallback,
    ],
  );

  const fetchReactionSnapshot = useCallback(
    (messageId: string) => {
      const fallbackKey = `reaction:${messageId}`;
      const ctrl = startWsFallback(fallbackKey);
      const loadKey = `${kind}:${targetId}`;
      const fetchFn =
        kind === "channel"
          ? () => fetchChannelMessage(targetId, messageId, ctrl.signal)
          : () => fetchDMMessage(targetId, messageId, ctrl.signal);
      fetchFn().then(
        (message) => {
          finishWsFallback(fallbackKey, ctrl);
          if (ctrl.signal.aborted) return;
          if (!isCurrentTarget(loadKey)) return;
          clearReactionTimer(message.id);
          dispatch({
            type: "reaction_snapshot",
            messageId: message.id,
            reactions: message.reactions,
          });
        },
        (err: unknown) => {
          finishWsFallback(fallbackKey, ctrl);
          if (err instanceof Error && err.name === "AbortError") return;
          if (isCurrentTarget(loadKey)) {
            dispatch({ type: "ws_fetch_error", error: realtimeFallbackErrorMessage });
          }
        },
      );
    },
    [clearReactionTimer, finishWsFallback, isCurrentTarget, kind, startWsFallback, targetId],
  );

  const handleReactionUpdated = useCallback(
    (event: WSReactionUpdatedEvent) => {
      if (event.target_id !== targetId || !isMessageRendered(event.message_id)) return;
      if (!event.reaction) {
        fetchReactionSnapshot(event.message_id);
        return;
      }
      const { reaction } = event;
      const actorIsMe = reaction.actor_user_id === currentUserId;
      // Asked once, of the same predicate the reducer uses, against a mirror of
      // the reducer's own intents. An event that settles nothing leaves the
      // rollback timer running and the emoji history untouched — it has only
      // moved this client's view of what the server holds.
      //
      // The dispatch below settles whatever this answer says was settled, so a
      // redelivered copy finds nothing outstanding: each WS frame is a separate
      // task, and React has committed the mirror by the time the next arrives.
      const confirmation = confirmReactionIntent(pendingReactionsRef.current, reaction, actorIsMe);
      if (confirmation.confirmed) {
        clearReactionTimer(reaction.message_id, reaction.emoji);
        // A removal is not a use; only reaching for an emoji is.
        if (confirmation.intent === "added") onOwnReactionConfirmed?.(reaction.emoji);
      }
      dispatch({ type: "reaction_updated", event, actorIsMe });
    },
    [
      clearReactionTimer,
      currentUserId,
      fetchReactionSnapshot,
      isMessageRendered,
      onOwnReactionConfirmed,
      targetId,
    ],
  );

  const fetchMessageUpdateSnapshot = useCallback(
    (messageId: string, insertIfMissing: boolean) => {
      const fallbackKey = `updated:${messageId}`;
      const ctrl = startWsFallback(fallbackKey);
      const loadKey = `${kind}:${targetId}`;
      const fetchFn =
        kind === "channel"
          ? () => fetchChannelMessage(targetId, messageId, ctrl.signal)
          : () => fetchDMMessage(targetId, messageId, ctrl.signal);
      void fetchFn().then(
        (message) => {
          finishWsFallback(fallbackKey, ctrl);
          if (ctrl.signal.aborted || !isCurrentTarget(loadKey)) return;
          const removed = message.isRemoved || message.status === "deleted";
          if (removed) {
            deletedMessageTombstonesRef.current.set(
              message.id,
              message.deletedAt ?? message.updatedAt,
            );
          }
          const snapshot = sanitizeTombstonedMessage(message);
          dispatch({ type: "message_snapshot", message: snapshot, insertIfMissing });
          if (snapshot.isRemoved) onMessageRemovedRef.current?.();
        },
        (error: unknown) => {
          finishWsFallback(fallbackKey, ctrl);
          if (error instanceof Error && error.name === "AbortError") return;
          if (isCurrentTarget(loadKey)) {
            dispatch({ type: "ws_fetch_error", error: realtimeFallbackErrorMessage });
          }
        },
      );
    },
    [finishWsFallback, isCurrentTarget, kind, sanitizeTombstonedMessage, startWsFallback, targetId],
  );

  const handleMessageUpdated = useCallback(
    (event: WSMessageUpdatedEvent) => {
      if (event.target_id !== targetId) return;
      if (event.message_update) {
        const messageId = event.message_update.message_id;
        if (event.message_update.is_removed || event.message_update.status === "deleted") {
          deletedMessageTombstonesRef.current.set(
            messageId,
            event.message_update.deleted_at ?? event.message_update.updated_at ?? null,
          );
        }
        cancelWsFallback(`updated:${messageId}`);
        const createdFallbackKey = `created:${messageId}`;
        if (wsFallbackAbortRefs.current.has(createdFallbackKey) && !isMessageRendered(messageId)) {
          cancelWsFallback(createdFallbackKey);
          fetchMessageUpdateSnapshot(messageId, true);
        }
        dispatch({ type: "message_updated", event });
        if (event.message_update.is_removed || event.message_update.status === "deleted") {
          onMessageRemovedRef.current?.();
        }
        return;
      }

      const messageId = event.message_id;
      if (!messageId) return;
      const insertIfMissing = wsFallbackAbortRefs.current.has(`created:${messageId}`);
      fetchMessageUpdateSnapshot(messageId, insertIfMissing);
    },
    [cancelWsFallback, fetchMessageUpdateSnapshot, isMessageRendered, targetId],
  );

  const handleReactionError = useCallback((event: WSClientErrorEvent) => {
    const messages: Record<string, string> = {
      rate_limited: "Muitas reações em sequência. Aguarde um minuto e tente novamente.",
      temporarily_unavailable: "Reações temporariamente indisponíveis.",
    };
    // A server-level refusal is not scoped to one toggle, so every wait ends.
    for (const timers of pendingReactionTimersRef.current.values()) {
      for (const timer of timers.values()) window.clearTimeout(timer);
    }
    pendingReactionTimersRef.current.clear();
    dispatch({
      type: "reaction_error",
      error: messages[event.code] ?? "Não foi possível atualizar a reação.",
    });
  }, []);

  const handleSubscriptionError = useCallback((event: WSClientErrorEvent) => {
    dispatch({
      type: "ws_fetch_error",
      error:
        event.code === "room_access_denied"
          ? "Não foi possível acessar as atualizações em tempo real desta conversa."
          : realtimeFallbackErrorMessage,
    });
  }, []);

  /**
   * RF-21 reconnect reconciliation.
   *
   * Realtime tells this client that a withheld message was published or refused.
   * It is best-effort: an author whose socket was down when the verdict landed
   * receives nothing, and — for a refusal in particular — nothing else is ever
   * coming, because the message no longer exists to be fetched. The bubble would
   * say "checking links…" forever.
   *
   * So on every subscription that comes back ready, the messages this client
   * still holds as pending are checked against the server's own answer. Absence
   * of an event is never read as a verdict: this asks, and acts only on what it
   * is told.
   *
   * Nothing happens when there is nothing pending, which is the overwhelmingly
   * common case — no request is made at all.
   */
  const reconcilePendingLinkScans = useCallback(() => {
    const pendingIds = stateRef.current.messages
      .filter((message) => message.status === "pending_link_scan")
      .slice(-linkSafetyReconcileBatchSize)
      .map((message) => message.id);
    if (pendingIds.length === 0) return;

    const fallbackKey = "link-safety-reconcile";
    // Registered with the websocket fallbacks so a target change aborts it, the
    // same way every other authoritative refetch here is cancelled: an answer
    // about channel A must never be applied to channel B.
    const ctrl = startWsFallback(fallbackKey);
    const loadKey = `${kind}:${targetId}`;
    void fetchLinkSafetyStatuses(pendingIds, ctrl.signal).then(
      (statuses) => {
        finishWsFallback(fallbackKey, ctrl);
        if (ctrl.signal.aborted || !isCurrentTarget(loadKey)) return;
        for (const status of statuses) {
          // Still being scanned, or an id the server would not talk about (which
          // is simply absent from the reply). Either way there is nothing to
          // apply, and the bubble stays as it is.
          if (status.state === "pending") continue;
          if (status.state === "active") {
            // The state alone is not the message. Fetching the authoritative row
            // reuses the same path a missed message.created takes, so a promotion
            // recovered here and one delivered in realtime produce exactly the
            // same result — and neither can duplicate the other, because both
            // replace by id.
            fetchMessageUpdateSnapshot(status.messageId, false);
            continue;
          }
          dispatch({
            type: "message_blocked",
            messageId: status.messageId,
            reason:
              status.state === "blocked" ? blockedMessageReason(status.reason) : "unavailable",
          });
        }
      },
      () => {
        finishWsFallback(fallbackKey, ctrl);
        // A failed reconciliation says nothing about the message. Removing the
        // bubble, promoting it, or reporting it as blocked would all be inventing
        // an answer the server never gave; the next reconnect asks again.
      },
    );
  }, [
    fetchMessageUpdateSnapshot,
    finishWsFallback,
    isCurrentTarget,
    kind,
    startWsFallback,
    targetId,
  ]);

  const refreshAuthoritativeMessageSecurity = useCallback(() => {
    const visible = stateRef.current.messages;
    const snapshotIDs = visible
      .filter(
        (message) =>
          message.status === "active" &&
          (message.linkSafetyState === "inconclusive" ||
            message.quoted?.linkSafetyState === "inconclusive"),
      )
      .map((message) => message.id);
    const referenceIDs = visible
      .filter((message) => message.reference?.available)
      .map((message) => message.id);
    if (snapshotIDs.length === 0 && referenceIDs.length === 0) return;

    const batches = (ids: string[]) =>
      Array.from({ length: Math.ceil(ids.length / referenceRevalidationBatchSize) }, (_, index) =>
        ids.slice(
          index * referenceRevalidationBatchSize,
          (index + 1) * referenceRevalidationBatchSize,
        ),
      );
    const fallbackKey = "message-security-refresh";
    const ctrl = startWsFallback(fallbackKey);
    const loadKey = `${kind}:${targetId}`;
    const snapshotRequests = batches(snapshotIDs).map((messageIDs) =>
      kind === "channel"
        ? fetchChannelMessageSecuritySnapshots(targetId, messageIDs, ctrl.signal)
        : fetchDMMessageSecuritySnapshots(targetId, messageIDs, ctrl.signal),
    );
    const referenceRequests = batches(referenceIDs).map((messageIDs) =>
      kind === "channel"
        ? resolveChannelMessageReferences(targetId, messageIDs, ctrl.signal)
        : resolveDMMessageReferences(targetId, messageIDs, ctrl.signal),
    );

    void Promise.allSettled([
      Promise.all(snapshotRequests).then((parts) => parts.flat()),
      Promise.all(referenceRequests).then((parts) => Object.assign({}, ...parts)),
    ]).then(([snapshotResult, referenceResult]) => {
      finishWsFallback(fallbackKey, ctrl);
      if (ctrl.signal.aborted || !isCurrentTarget(loadKey)) return;
      if (snapshotResult.status === "fulfilled") {
        dispatch({ type: "security_snapshots_refreshed", snapshots: snapshotResult.value });
      }
      if (referenceIDs.length > 0) {
        const references = referenceResult.status === "fulfilled" ? referenceResult.value : {};
        dispatch({
          type: "references_refreshed",
          references: Object.fromEntries(
            referenceIDs.map((messageID) => [
              messageID,
              references[messageID] ?? { available: false },
            ]),
          ),
        });
      }
    });
  }, [finishWsFallback, isCurrentTarget, kind, startWsFallback, targetId]);

  const handleSubscribed = useCallback(() => {
    dispatch({ type: "ws_subscription_ready" });
    reconcilePendingLinkScans();
    refreshAuthoritativeMessageSecurity();
  }, [reconcilePendingLinkScans, refreshAuthoritativeMessageSecurity]);

  // RF-05: keep the pin callback in a ref so it never restarts the socket.
  const onPinUpdatedRef = useRef(onPinUpdated);
  useLayoutEffect(() => {
    onPinUpdatedRef.current = onPinUpdated;
  });
  const handlePinUpdated = useCallback(
    (event: WSPinUpdatedEvent) => {
      if (event.target_type !== kind || event.target_id !== targetId) return;
      onPinUpdatedRef.current?.(event);
    },
    [kind, targetId],
  );

  // Same ref treatment as the pin callback, and for the same reason: the panel
  // recreates its handler on every render, and letting that restart the socket
  // would drop and re-establish every subscription.
  const onMembersAddedRef = useRef(onMembersAdded);
  useLayoutEffect(() => {
    onMembersAddedRef.current = onMembersAdded;
  });
  const handleMembersAdded = useCallback(
    (event: WSMembersAddedEvent) => {
      if (event.target_type !== kind || event.target_id !== targetId) return;
      onMembersAddedRef.current?.(event);
    },
    [kind, targetId],
  );

  // Same ref treatment again, same reason.
  const onAttachmentStatusRef = useRef(onAttachmentStatus);
  useLayoutEffect(() => {
    onAttachmentStatusRef.current = onAttachmentStatus;
  });
  const handleAttachmentStatus = useCallback(
    (event: WSAttachmentStatusEvent) => {
      if (event.target_type !== kind || event.target_id !== targetId) return;
      // The timeline reconciles itself from the event (RF-32) while the caller
      // still gets to refresh whatever else lists this destination's files. No
      // polling is introduced: this is the mechanism that already existed.
      if (event.attachment?.attachment_id) {
        dispatch({
          type: "attachment_status",
          attachmentId: event.attachment.attachment_id,
          status: event.attachment.status,
        });
      }
      onAttachmentStatusRef.current?.(event);
    },
    [kind, targetId],
  );

  /**
   * "Verificar novamente": ask the server to take a second look at one message's
   * unverified links (issue #135).
   *
   * # What it is not
   *
   * It does not start a new scan and must never be presented as doing so. The
   * server searches its own scan history for the URLs it recorded for this
   * message; a new submission is impossible by construction, not merely absent
   * from this call.
   *
   * # Why the reply is applied locally as well as over the websocket
   *
   * A verdict that actually changed something is broadcast, so every reader
   * converges. But the reply is authoritative for *this* reader and costs
   * nothing to apply, which is what makes the button work when realtime is down —
   * the one situation in which a user is most likely to press it. The reducer
   * ignores a state that matches what is already drawn, so the two paths cannot
   * fight.
   *
   * A failure is swallowed on purpose. The outcome the user sees either way is
   * "still not verified", and turning a rate-limit or an outage into a red banner
   * over somebody's message would be alarming about a link nothing is alleging
   * anything against. The state is returned so the caller can re-enable its
   * button.
   */
  const reconcileLinkSafety = useCallback(
    async (messageId: string): Promise<LinkSafetyRecheck | undefined> => {
      try {
        const result = await reconcileMessageLinkSafety(messageId);
        dispatch({
          type: "link_safety_changed",
          messageId,
          state: result.state,
          updatedAt: result.updatedAt,
        });
        // The whole reply is handed back, not just the state: the caller disables
        // its button for the cooldown the server reported, which is what keeps the
        // control from offering an action that would be refused.
        return result;
      } catch {
        return undefined;
      }
    },
    [],
  );

  // Same ref treatment again, same reason.
  const onTypingUpdatedRef = useRef(onTypingUpdated);
  useLayoutEffect(() => {
    onTypingUpdatedRef.current = onTypingUpdated;
  });
  const handleTypingUpdated = useCallback(
    (event: WSTypingUpdatedEvent) => {
      if (event.target_type !== kind || event.target_id !== targetId) return;
      onTypingUpdatedRef.current?.(event);
    },
    [kind, targetId],
  );

  const { toggleReaction: sendReactionToggle, sendTyping } = useChatWebSocket({
    kind,
    targetId,
    onMessageCreated: handleWsMessageCreated,
    onMessageBlocked: handleWsMessageBlocked,
    onMessageLinkSafetyChanged: handleWsLinkSafetyChanged,
    onMessageUpdated: handleMessageUpdated,
    onReactionUpdated: handleReactionUpdated,
    onTypingUpdated: handleTypingUpdated,
    onPinUpdated: handlePinUpdated,
    onMembersAdded: handleMembersAdded,
    onAttachmentStatus: handleAttachmentStatus,
    onReactionError: handleReactionError,
    onSubscriptionError: handleSubscriptionError,
    onSubscribed: handleSubscribed,
  });

  // ── Preview reconciliation for inline attachments (RF-31/#464) ─────────────
  //
  // A message posts with previewStatus "pending" the instant its upload
  // finishes — the malware scan and the render both still have to run. There
  // is no socket event for "the preview finished" (attachment_status above
  // fires on the scan verdict, before the render even starts), so without
  // this the card sits on its icon fallback until the thread is reloaded.
  //
  // Bounded and backed off exactly like the details panel's own
  // reconciliation: the render is normally done within the worker's ~10s
  // poll, so the delay starts there and doubles up to a ceiling, and the
  // window ends after previewReconcileMaxAttempts unchanged polls — giving up
  // costs nothing but a late thumbnail, and one reload opens a fresh window.
  const [reconcile, dispatchReconcile] = useReducer(
    previewReconcileReducer,
    initialPreviewReconcile,
  );

  const previewProgressKey = state.messages
    .flatMap((message) => message.attachments ?? [])
    .map((attachment) => `${attachment.id}:${attachment.status}:${attachment.previewStatus}`)
    .join("|");
  const awaitingPreview = state.messages.some((message) =>
    message.attachments?.some(isPreviewWorkPending),
  );
  const reconcileTargetKey = `${kind}:${targetId}`;
  const reconcileAttempt =
    reconcile.target === reconcileTargetKey && reconcile.progressKey === previewProgressKey
      ? reconcile.attempt
      : 0;
  const previewReconcileActive = awaitingPreview && reconcileAttempt < previewReconcileMaxAttempts;

  useEffect(() => {
    if (!previewReconcileActive) return;
    const onVisibilityChange = () => dispatchReconcile({ type: "resumed" });
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, [previewReconcileActive]);

  useEffect(() => {
    if (!previewReconcileActive) return;
    if (document.visibilityState === "hidden") return;

    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      fetchConversationAttachments(
        { kind, id: targetId },
        messagePreviewReconcileLimit,
        controller.signal,
      ).then(
        (attachments) => {
          if (controller.signal.aborted) return;
          dispatch({ type: "attachments_reconciled", attachments });
          dispatchReconcile({
            type: "polled",
            target: reconcileTargetKey,
            progressKey: previewProgressKey,
          });
        },
        (error: unknown) => {
          if (controller.signal.aborted || isAbort(error)) return;
          // A transient failure leaves the timeline exactly as it is and
          // costs only the next backoff step, never an immediate retry.
          dispatchReconcile({
            type: "polled",
            target: reconcileTargetKey,
            progressKey: previewProgressKey,
          });
        },
      );
    }, previewReconcileDelayMs(reconcileAttempt));

    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [
    previewReconcileActive,
    reconcileAttempt,
    kind,
    targetId,
    reconcileTargetKey,
    previewProgressKey,
    reconcile.round,
  ]);

  const toggleReaction = useCallback(
    (messageId: string, emoji: string) => {
      dispatch({ type: "reaction_optimistic", messageId, emoji });
      if (!sendReactionToggle(messageId, emoji)) {
        dispatch({
          type: "reaction_revert",
          messageId,
          emoji,
          error: "Conexão em tempo real indisponível. Tente novamente.",
        });
        return;
      }
      // One window per (message, emoji): re-toggling the same emoji restarts its
      // own wait, and a toggle of a different emoji on the same message starts a
      // second one beside it.
      clearReactionTimer(messageId, emoji);
      const timer = window.setTimeout(() => {
        pendingReactionTimersRef.current.get(messageId)?.delete(emoji);
        dispatch({
          type: "reaction_revert",
          messageId,
          emoji,
          error: "Não foi possível confirmar a reação. Tente novamente.",
        });
      }, reactionConfirmTimeoutMs);
      const timers = pendingReactionTimersRef.current.get(messageId) ?? new Map<string, number>();
      timers.set(emoji, timer);
      pendingReactionTimersRef.current.set(messageId, timers);
    },
    [clearReactionTimer, sendReactionToggle],
  );

  const selectReply = useCallback((message: Message) => {
    dispatch({ type: "reply_set", message });
  }, []);

  const cancelReply = useCallback(() => {
    dispatch({ type: "reply_clear" });
  }, []);

  // RF-06: REST round-trip confirms before the flag flips — no optimistic
  // update; a failure reuses the transient reaction error banner.
  // ponytail: no in-flight dedupe; a double click just repeats an idempotent call.
  const toggleFavorite = useCallback(
    (messageId: string, isFavorited: boolean) => {
      const apply = isFavorited ? favoriteMessage : unfavoriteMessage;
      void apply(messageId)
        .then(() => {
          if (isMessageRendered(messageId)) {
            dispatch({ type: "favorite_set", messageId, isFavorited });
          }
        })
        .catch(() => {
          dispatch({
            type: "favorite_error",
            error: "Não foi possível atualizar o favorito. Tente novamente.",
          });
        });
    },
    [isMessageRendered],
  );

  const editMessageLocal = useCallback(
    async (messageId: string, body: string, bodyFormat: Message["bodyFormat"]) => {
      const previous = stateRef.current.messages.find((message) => message.id === messageId);
      if (!previous) throw new Error("Mensagem não encontrada.");
      const editKey = `${kind}:${targetId}`;
      const editedAt = new Date().toISOString();
      dispatch({ type: "edit_optimistic", messageId, body, bodyFormat, editedAt });
      try {
        const version = bodyFormat === "v3" ? 3 : bodyFormat === "v2" ? 2 : 1;
        const updated = await editMessageRequest(messageId, body, version);
        if (stateRef.current.target === editKey)
          dispatch({ type: "edit_confirmed", message: updated });
        return updated;
      } catch (error) {
        if (stateRef.current.target === editKey)
          dispatch({ type: "edit_revert", message: previous, optimisticEditedAt: editedAt });
        throw error;
      }
    },
    [kind, targetId],
  );

  const deleteMessageLocal = useCallback(
    async (messageId: string) => {
      const deleteKey = `${kind}:${targetId}`;
      try {
        const deleted = await deleteMessageRequest(messageId);
        if (stateRef.current.target === deleteKey) {
          deletedMessageTombstonesRef.current.set(
            deleted.id,
            deleted.deletedAt ?? deleted.updatedAt,
          );
          dispatch({
            type: "message_snapshot",
            message: sanitizeTombstonedMessage(deleted),
            insertIfMissing: false,
          });
          onMessageRemovedRef.current?.();
        }
      } catch (error) {
        if (stateRef.current.target === deleteKey) {
          dispatch({
            type: "delete_error",
            error: "Não foi possível excluir a mensagem. Tente novamente.",
          });
        }
        throw error;
      }
    },
    [kind, sanitizeTombstonedMessage, targetId],
  );

  return {
    state,
    sendMessage,
    retry,
    loadMore,
    selectReply,
    cancelReply,
    toggleReaction,
    sendTyping,
    toggleFavorite,
    reconcileLinkSafety,
    editMessageLocal,
    deleteMessageLocal,
  };
}
