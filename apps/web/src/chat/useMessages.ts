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
import { ApiRequestError } from "../lib/api";
import { randomId } from "../lib/randomId";
import {
  normalizeBodyFormat,
  normalizeLinkSafety,
  parseMessageAttachments,
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
  pendingReactions: Map<string, Message["reactions"]>;
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
  | { type: "reaction_revert"; messageId: string; error: string }
  | { type: "ws_fetch_error"; error: string }
  | { type: "attachment_status"; attachmentId: string; status: ChannelAttachment["status"] }
  | { type: "ws_subscription_ready" };

/** Applies a local toggle to a message's reaction list, mirroring server semantics for count/reactedByMe. */
function toggleOptimisticReaction(
  reactions: Message["reactions"],
  emoji: string,
): Message["reactions"] {
  const index = reactions.findIndex((item) => item.emoji === emoji);
  if (index < 0) {
    return [...reactions, { emoji, count: 1, reactedByMe: true }];
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

function reducer(state: MessagesState, action: Action): MessagesState {
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
    case "error":
      return { ...state, status: "error", sending: false, lastMutation: "none" };
    case "sending":
      return { ...state, sending: true, sendError: null };
    case "sent": {
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
    case "message_blocked": {
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
    case "link_safety_changed": {
      // Only a message already on screen, and only a real change. A correction
      // for something this view never held is a no-op, and so is one that agrees
      // with what is already drawn — which is what makes at-least-once delivery
      // of this event free.
      //
      // Nothing here inserts a message and nothing changes `status`: if the
      // message has not arrived yet, only a versioned correction is retained for
      // its eventual create event. Nothing here fetches a URL either — see
      // MessageBubble, where this state is rendered and never acted on.
      const previousCorrection = state.linkSafetyCorrections.get(action.messageId);
      if (
        previousCorrection?.state === action.state &&
        previousCorrection?.updatedAt === action.updatedAt
      ) {
        return state;
      }
      if (
        previousCorrection &&
        isOlderSecurityVersion(action.updatedAt, previousCorrection.updatedAt)
      ) {
        return state;
      }
      // A quote/reference can be the only visible copy of the source. Its
      // version is still authoritative: retaining an older correction here
      // would let a later stale snapshot re-apply it.
      const hasNewerVisibleVersion = state.messages.some(
        (message) =>
          (message.id === action.messageId &&
            isOlderSecurityVersion(action.updatedAt, message.updatedAt)) ||
          (message.quoted?.id === action.messageId &&
            isOlderSecurityVersion(
              action.updatedAt,
              message.quoted.updatedAt ?? message.quoted.createdAt,
            )) ||
          (message.reference?.available &&
            message.reference.messageId === action.messageId &&
            isOlderSecurityVersion(
              action.updatedAt,
              message.reference.updatedAt ?? message.reference.createdAt,
            )),
      );
      if (
        hasNewerVisibleVersion ||
        (state.replyTo?.id === action.messageId &&
          isOlderSecurityVersion(action.updatedAt, state.replyTo.updatedAt))
      ) {
        return state;
      }
      const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
      linkSafetyCorrections.set(action.messageId, {
        state: action.state,
        updatedAt: action.updatedAt,
      });
      const malicious = action.state === "malicious";
      const messages = state.messages.map((message) => {
        let next = message;
        if (
          message.id === action.messageId &&
          !isOlderSecurityVersion(action.updatedAt, message.updatedAt) &&
          ((message.linkSafetyState ?? "") !== (action.state ?? "") ||
            (malicious && message.bodyText !== ""))
        ) {
          next = {
            ...next,
            linkSafetyState: action.state,
            bodyText: malicious ? "" : next.bodyText,
            updatedAt: action.updatedAt,
          };
        }
        if (
          message.quoted?.id === action.messageId &&
          !isOlderSecurityVersion(
            action.updatedAt,
            message.quoted.updatedAt ?? message.quoted.createdAt,
          ) &&
          (message.quoted.linkSafetyState !== action.state ||
            (malicious && message.quoted.bodyText !== ""))
        ) {
          next = {
            ...next,
            quoted: {
              ...message.quoted,
              linkSafetyState: action.state ?? "",
              bodyText: malicious ? "" : message.quoted.bodyText,
              updatedAt: action.updatedAt,
            },
          };
        }
        if (
          message.reference?.available &&
          message.reference.messageId === action.messageId &&
          !isOlderSecurityVersion(
            action.updatedAt,
            message.reference.updatedAt ?? message.reference.createdAt,
          ) &&
          (message.reference.linkSafetyState !== action.state ||
            (malicious && message.reference.bodyText !== ""))
        ) {
          next = {
            ...next,
            reference: {
              ...message.reference,
              linkSafetyState: action.state ?? "",
              bodyText: malicious ? "" : message.reference.bodyText,
              updatedAt: action.updatedAt,
            },
          };
        }
        return next;
      });
      const replyTo =
        state.replyTo?.id === action.messageId &&
        !isOlderSecurityVersion(action.updatedAt, state.replyTo.updatedAt)
          ? applyLinkSafetyCorrection(state.replyTo, {
              state: action.state,
              updatedAt: action.updatedAt,
            })
          : state.replyTo;
      // lastMutation stays "none": nothing was added or removed, so the list must
      // not scroll. A notice appearing above a message the reader is looking at
      // should not move the conversation under them.
      return {
        ...state,
        messages,
        replyTo,
        linkSafetyCorrections,
        lastMutation: "none",
      };
    }
    case "security_snapshots_refreshed": {
      const snapshots = new Map(action.snapshots.map((snapshot) => [snapshot.messageId, snapshot]));
      const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
      let changed = false;
      const messages = state.messages.map((message) => {
        const snapshot = snapshots.get(message.id);
        if (!snapshot) return message;
        if (!snapshot.available) {
          linkSafetyCorrections.delete(message.id);
          changed = true;
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
        const correction = linkSafetyCorrections.get(message.id);
        const correctionWins =
          correction && isNotNewerSecurityVersion(snapshot.updatedAt, correction.updatedAt);
        if (!correctionWins) linkSafetyCorrections.delete(message.id);
        const snapshotWins = !isOlderSecurityVersion(snapshot.updatedAt, message.updatedAt);
        const effectiveState = correctionWins
          ? correction.state
          : snapshotWins
            ? snapshot.linkSafetyState
            : message.linkSafetyState;
        const effectiveUpdatedAt = correctionWins
          ? correction.updatedAt
          : snapshotWins
            ? snapshot.updatedAt
            : message.updatedAt;
        const effectiveStatus = snapshotWins ? snapshot.status : message.status;
        const removed = effectiveStatus === "deleted";
        const malicious = effectiveState === "malicious";
        let next: Message = {
          ...message,
          status: effectiveStatus,
          linkSafetyState: effectiveState,
          bodyText: removed || malicious ? "" : message.bodyText,
          isRemoved: removed,
          updatedAt: effectiveUpdatedAt,
          ...(removed ? { quoted: undefined, reactions: [] } : {}),
        };
        if (!removed && message.quoted && snapshot.quoted?.messageId === message.quoted.id) {
          const quoteCurrentVersion = message.quoted.updatedAt ?? message.quoted.createdAt;
          const quoteCorrection = linkSafetyCorrections.get(message.quoted.id);
          const quoteCorrectionWins =
            quoteCorrection &&
            isNotNewerSecurityVersion(snapshot.quoted.updatedAt, quoteCorrection.updatedAt) &&
            !isOlderSecurityVersion(quoteCorrection.updatedAt, quoteCurrentVersion);
          if (!quoteCorrectionWins) linkSafetyCorrections.delete(message.quoted.id);
          const quoteSnapshotWins = !isOlderSecurityVersion(
            snapshot.quoted.updatedAt,
            quoteCurrentVersion,
          );
          const quoteState = quoteCorrectionWins
            ? (quoteCorrection.state ?? "unknown")
            : quoteSnapshotWins
              ? snapshot.quoted.linkSafetyState
              : message.quoted.linkSafetyState;
          const quoteUpdatedAt = quoteCorrectionWins
            ? quoteCorrection.updatedAt
            : quoteSnapshotWins
              ? snapshot.quoted.updatedAt
              : quoteCurrentVersion;
          const quoteRemoved = quoteSnapshotWins
            ? snapshot.quoted.status === "deleted"
            : message.quoted.isRemoved;
          next = {
            ...next,
            quoted: {
              ...message.quoted,
              linkSafetyState: quoteState,
              updatedAt: quoteUpdatedAt,
              isRemoved: quoteRemoved,
              bodyText: quoteRemoved || quoteState === "malicious" ? "" : message.quoted.bodyText,
            },
          };
        }
        changed = true;
        return next;
      });
      if (!changed) return state;
      const replySnapshot = state.replyTo ? snapshots.get(state.replyTo.id) : undefined;
      const replyTo =
        replySnapshot && (!replySnapshot.available || replySnapshot.status === "deleted")
          ? null
          : state.replyTo;
      return { ...state, messages, replyTo, linkSafetyCorrections, lastMutation: "none" };
    }
    case "send_error":
      return { ...state, sending: false, sendError: action.error };
    case "prepending":
      return { ...state, loadingMore: true, lastMutation: "none" };
    case "prepended": {
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
    case "prepend_error":
      return { ...state, loadingMore: false, lastMutation: "none" };
    case "ws_received": {
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
    case "edit_optimistic": {
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
    case "edit_confirmed": {
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
    case "edit_revert":
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
    case "message_updated": {
      const update = action.event.message_update;
      if (!update) return state;
      const removed = update.is_removed === true || update.status === "deleted";
      const deletedAt = update.deleted_at ?? update.updated_at ?? null;
      const updateVersion = update.updated_at ?? update.edited_at;
      const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
      const correction = linkSafetyCorrections.get(update.message_id);
      if (!correction || !isNotNewerSecurityVersion(updateVersion, correction.updatedAt)) {
        linkSafetyCorrections.delete(update.message_id);
      }
      return {
        ...state,
        messages: state.messages.map((message) => {
          if (message.id === update.message_id) {
            if (correction && isNotNewerSecurityVersion(updateVersion, correction.updatedAt)) {
              return message;
            }
            if (message.isRemoved || removed) {
              return removed
                ? {
                    ...message,
                    bodyText: "",
                    quoted: undefined,
                    reactions: [],
                    status: "deleted" as const,
                    isRemoved: true,
                    deletedAt,
                    updatedAt: update.updated_at ?? deletedAt ?? message.updatedAt,
                  }
                : message;
            }
            const linkSafetyState =
              update.link_safety_state === undefined
                ? message.linkSafetyState
                : normalizeLinkSafety(update.link_safety_state);
            return update.edit_count < message.editCount
              ? message
              : {
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
          if (removed && message.quoted?.id === update.message_id) {
            return {
              ...message,
              quoted: { ...message.quoted, bodyText: "", isRemoved: true, deletedAt },
            };
          }
          return message;
        }),
        replyTo: removed && state.replyTo?.id === update.message_id ? null : state.replyTo,
        lastMutation: "none",
        realtimeError: null,
        linkSafetyCorrections,
      };
    }
    case "message_snapshot": {
      const removed = action.message.isRemoved || action.message.status === "deleted";
      const rawSnapshot = removed
        ? { ...action.message, bodyText: "", quoted: undefined, reactions: [] }
        : action.message;
      const correction = state.linkSafetyCorrections.get(rawSnapshot.id);
      const snapshot = applyLinkSafetyCorrections(rawSnapshot, state.linkSafetyCorrections);
      const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
      if (!correction || !isNotNewerSecurityVersion(rawSnapshot.updatedAt, correction.updatedAt)) {
        linkSafetyCorrections.delete(rawSnapshot.id);
      }
      const alreadyPresent = state.messages.some((message) => message.id === snapshot.id);
      let insertedAsNewer = false;
      let messages = state.messages.map((message) => {
        if (message.id === snapshot.id) return message.isRemoved && !removed ? message : snapshot;
        if (removed && message.quoted?.id === snapshot.id) {
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
        return message;
      });
      if (!alreadyPresent && action.insertIfMissing) {
        const insertion = insertMessageChronologically(messages, snapshot);
        messages = insertion.messages;
        insertedAsNewer = insertion.isNewer;
      }
      return {
        ...state,
        messages,
        replyTo: removed && state.replyTo?.id === snapshot.id ? null : state.replyTo,
        lastMutation: insertedAsNewer ? "ws_append" : "none",
        realtimeError: null,
        linkSafetyCorrections,
      };
    }
    case "references_refreshed":
      return {
        ...state,
        messages: state.messages.map((message) => {
          const reference = action.references[message.id];
          if (!reference || !message.reference) return message;
          if (
            message.reference.available &&
            reference.available &&
            ((message.reference.updatedAt &&
              reference.updatedAt &&
              isOlderSecurityVersion(reference.updatedAt, message.reference.updatedAt)) ||
              (message.reference.linkSafetyState === "malicious" &&
                !reference.updatedAt &&
                reference.linkSafetyState !== "malicious"))
          ) {
            return message;
          }
          return { ...message, reference };
        }),
      };
    case "delete_error":
      return { ...state, actionError: action.error };
    case "ws_fetch_error":
      return { ...state, realtimeError: action.error, lastMutation: "none" };
    case "ws_subscription_ready":
      return { ...state, realtimeError: null };
    case "attachment_status": {
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
    case "reaction_error": {
      // Server-level errors (rate limit, feature unavailable) aren't scoped to a
      // single message, so every optimistic toggle still in flight is reverted.
      if (state.pendingReactions.size === 0) {
        return { ...state, actionError: action.error };
      }
      const messages = state.messages.map((message) => {
        const snapshot = state.pendingReactions.get(message.id);
        return snapshot ? { ...message, reactions: snapshot } : message;
      });
      return { ...state, messages, actionError: action.error, pendingReactions: new Map() };
    }
    case "reaction_error_clear":
      return { ...state, actionError: null };
    case "reply_set":
      return { ...state, replyTo: action.message };
    case "reply_clear":
      return { ...state, replyTo: null };
    case "favorite_set": {
      const index = state.messages.findIndex((message) => message.id === action.messageId);
      if (index < 0) return state;
      const messages = [...state.messages];
      messages[index] = { ...messages[index], isFavorited: action.isFavorited };
      return { ...state, messages };
    }
    case "favorite_error":
      // Reuses the transient banner without touching reaction snapshots.
      return { ...state, actionError: action.error };
    case "reaction_optimistic": {
      const index = state.messages.findIndex((message) => message.id === action.messageId);
      if (index < 0) return state;
      const message = state.messages[index];
      const pendingReactions = new Map(state.pendingReactions);
      if (!pendingReactions.has(action.messageId)) {
        pendingReactions.set(action.messageId, message.reactions);
      }
      const messages = [...state.messages];
      messages[index] = {
        ...message,
        reactions: toggleOptimisticReaction(message.reactions, action.emoji),
      };
      return { ...state, messages, pendingReactions, actionError: null };
    }
    case "reaction_revert": {
      const snapshot = state.pendingReactions.get(action.messageId);
      if (!snapshot) return { ...state, actionError: action.error };
      const index = state.messages.findIndex((message) => message.id === action.messageId);
      const messages =
        index < 0
          ? state.messages
          : state.messages.map((message, i) =>
              i === index ? { ...message, reactions: snapshot } : message,
            );
      const pendingReactions = new Map(state.pendingReactions);
      pendingReactions.delete(action.messageId);
      return { ...state, messages, pendingReactions, actionError: action.error };
    }
    case "reaction_updated": {
      const { reaction } = action.event;
      if (!reaction) return state;
      const index = state.messages.findIndex((message) => message.id === reaction.message_id);
      if (index < 0) return state;
      const message = state.messages[index];
      // Reconcile against the pre-optimistic baseline (not the current, possibly
      // still-unconfirmed optimistic guess) so an update from another actor doesn't
      // inherit our own not-yet-confirmed toggle as ground truth.
      const baseline = state.pendingReactions.get(reaction.message_id) ?? message.reactions;
      const previous = new Map(baseline.map((item) => [item.emoji, item.reactedByMe]));
      const reactions = reaction.reactions.map((item) => ({
        ...item,
        reactedByMe:
          action.actorIsMe && item.emoji === reaction.emoji
            ? reaction.added
            : (previous.get(item.emoji) ?? false),
      }));
      const messages = [...state.messages];
      messages[index] = { ...message, reactions };
      const pendingReactions = new Map(state.pendingReactions);
      pendingReactions.delete(reaction.message_id);
      return {
        ...state,
        messages,
        pendingReactions,
        lastMutation: "none",
        realtimeError: null,
        actionError: null,
      };
    }
    case "reaction_snapshot": {
      const index = state.messages.findIndex((message) => message.id === action.messageId);
      if (index < 0) return state;
      const messages = [...state.messages];
      messages[index] = { ...messages[index], reactions: action.reactions };
      const pendingReactions = new Map(state.pendingReactions);
      pendingReactions.delete(action.messageId);
      return {
        ...state,
        messages,
        pendingReactions,
        lastMutation: "none",
        realtimeError: null,
        actionError: null,
      };
    }
  }
}

// ── Hook ──────────────────────────────────────────────────────────────────────

interface UseMessagesOptions {
  kind: "channel" | "dm";
  targetId: string;
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

  const abortRef = useRef<AbortController | null>(null);
  const loadMoreAbortRef = useRef<AbortController | null>(null);
  const wsFallbackAbortRefs = useRef<Map<string, AbortController>>(new Map());
  const deletedMessageTombstonesRef = useRef<Map<string, string | null>>(new Map());
  const deletedMessageTombstoneTargetRef = useRef(`${kind}:${targetId}`);
  const pendingReactionTimersRef = useRef<Map<string, number>>(new Map());
  const pendingSendIdentityRef = useRef<{ signature: string; key: string } | null>(null);
  const onMessageRemovedRef = useRef(onMessageRemoved);
  useLayoutEffect(() => {
    onMessageRemovedRef.current = onMessageRemoved;
  }, [onMessageRemoved]);
  const clearReactionTimer = useCallback((messageId: string) => {
    const timer = pendingReactionTimersRef.current.get(messageId);
    if (timer !== undefined) {
      window.clearTimeout(timer);
      pendingReactionTimersRef.current.delete(messageId);
    }
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
        for (const timer of pendingReactionTimersRef.current.values()) window.clearTimeout(timer);
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
    [kind, sanitizeTombstonedMessage, targetId],
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
      const actorIsMe = event.reaction.actor_user_id === currentUserId;
      if (actorIsMe) onOwnReactionConfirmed?.(event.reaction.emoji);
      clearReactionTimer(event.message_id);
      dispatch({
        type: "reaction_updated",
        event,
        actorIsMe,
      });
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
    for (const timer of pendingReactionTimersRef.current.values()) window.clearTimeout(timer);
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

  const toggleReaction = useCallback(
    (messageId: string, emoji: string) => {
      dispatch({ type: "reaction_optimistic", messageId, emoji });
      if (!sendReactionToggle(messageId, emoji)) {
        dispatch({
          type: "reaction_revert",
          messageId,
          error: "Conexão em tempo real indisponível. Tente novamente.",
        });
        return;
      }
      clearReactionTimer(messageId);
      const timer = window.setTimeout(() => {
        pendingReactionTimersRef.current.delete(messageId);
        dispatch({
          type: "reaction_revert",
          messageId,
          error: "Não foi possível confirmar a reação. Tente novamente.",
        });
      }, reactionConfirmTimeoutMs);
      pendingReactionTimersRef.current.set(messageId, timer);
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
