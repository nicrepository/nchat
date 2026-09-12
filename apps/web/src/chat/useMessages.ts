/**
 * useMessages — the chat timeline of one channel or DM.
 *
 * This file is composition: it owns the reducer, wires the specialised modules
 * under ./messages, and exposes the public API. Every rule about how a message,
 * a reaction, a link-safety verdict or an attachment changes state lives in
 * those modules, so nothing here has to be read to understand any one of them.
 *
 * Security notes:
 * - No tokens are stored or exposed; authentication is handled by authenticatedFetch.
 * - No author_id is sent from the client; the server derives sender identity from the JWT.
 * - AbortController cancels in-flight list and fallback single-message requests
 *   on target change or unmount — see useConversationScope and useRequestRegistry.
 * - The conversation scope's mirror is updated via useLayoutEffect (no deps)
 *   after every render, synchronously in the same JS task, before any microtask
 *   can run. This ensures stale POST completions are detected reliably
 *   regardless of effect scheduling.
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

import { useCallback, useEffect, useMemo, useReducer } from "react";

import type { LinkSafetyRecheck, Message, MessageAcknowledgement } from "./chatTypes";
import { messagesGateway } from "./messages/messagesGateway";
import { reducer } from "./messages/reducer";
import { initialState } from "./messages/types";
import { useAuthoritativeReads } from "./messages/useAuthoritativeReads";
import { useConversationScope } from "./messages/useConversationScope";
import { useLatestRef } from "./messages/useLatestRef";
import { useMessageLoading } from "./messages/useMessageLoading";
import { useMessageMutations } from "./messages/useMessageMutations";
import { useMessageRealtime } from "./messages/useMessageRealtime";
import { usePreviewReconciliation } from "./messages/usePreviewReconciliation";
import { useReactionEvents } from "./messages/useReactionEvents";
import { useReactionTimers } from "./messages/useReactionTimers";
import { useReactionToggle } from "./messages/useReactionToggle";
import { useReferenceRevalidation } from "./messages/useReferenceRevalidation";
import { useRequestRegistry } from "./messages/useRequestRegistry";
import { useAcknowledgements } from "./messages/useAcknowledgements";
import { useSecurityReconciliation } from "./messages/useSecurityReconciliation";
import type {
  WSAttachmentStatusEvent,
  WSMembersAddedEvent,
  WSPinUpdatedEvent,
  WSTypingUpdatedEvent,
} from "./useChatWebSocket";

export type { LastMutation, MessagesState, SendResult } from "./messages/types";

import type { MessagesState, SendResult } from "./messages/types";

/** How long a transient action banner stays up before it clears itself. */
const actionErrorTimeoutMs = 5_000;

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
    /** Issue #824: ask this message's recipients to confirm receipt. */
    acknowledgementRequired?: boolean,
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
   * Issue #824. The server's acknowledgement summary for each message that asked
   * for confirmation, keyed by message id, and the action that records one.
   *
   * Sparse: a conversation with no such message carries an empty map and costs
   * no requests. The map is the server's answer, never a local state machine —
   * every entry is replaced only by a newer server answer.
   */
  acknowledgements: Record<string, MessageAcknowledgement>;
  /** The message whose confirmation is in flight, if any. */
  acknowledgingId: string | null;
  /** Set when the last confirmation failed; cleared by the next attempt. */
  acknowledgeError: string | null;
  /**
   * Confirms receipt of one message. Explicit by definition — nothing else in
   * this hook calls it, so reading a message can never produce one.
   */
  acknowledge: (messageId: string) => void;
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

  // The action banner is transient by contract: it reports a refusal the reader
  // can do nothing about, so it clears itself rather than accumulating.
  useEffect(() => {
    if (!state.actionError) return;
    const timer = window.setTimeout(
      () => dispatch({ type: "reaction_error_clear" }),
      actionErrorTimeoutMs,
    );
    return () => window.clearTimeout(timer);
  }, [state.actionError]);

  const target = { kind, targetId };
  const scope = useConversationScope(target, state);
  const gateway = useMemo(() => messagesGateway({ kind, targetId }), [kind, targetId]);
  const fallbacks = useRequestRegistry();
  const reactionTimers = useReactionTimers();

  const latestOnMessageRemoved = useLatestRef(onMessageRemoved);
  const notifyRemoved = useCallback(() => {
    latestOnMessageRemoved.current?.();
  }, [latestOnMessageRemoved]);

  const reads = useAuthoritativeReads({
    scope,
    gateway,
    dispatch,
    fallbacks,
    reactionTimers,
    notifyRemoved,
  });
  const reconciliation = useSecurityReconciliation({
    scope,
    gateway,
    dispatch,
    fallbacks,
    readMessageSnapshot: reads.readMessageSnapshot,
  });
  const reactionEvents = useReactionEvents({
    scope,
    dispatch,
    timers: reactionTimers,
    currentUserId,
    pendingReactions: state.pendingReactions,
    readReactionSnapshot: reads.readReactionSnapshot,
    onOwnReactionConfirmed,
  });

  const { retry, loadMore } = useMessageLoading({
    scope,
    gateway,
    dispatch,
    fallbacks,
    reactionTimers,
    nextCursor: state.nextCursor,
    loadingMore: state.loadingMore,
    focusMessageId,
  });
  useReferenceRevalidation({ scope, gateway, dispatch, messages: state.messages });

  const { sendMessage, editMessageLocal, deleteMessageLocal, toggleFavorite } = useMessageMutations(
    { scope, gateway, dispatch, bodyFormat, notifyRemoved },
  );

  // Issue #824. Its own module and its own cache, because what it holds is a
  // different shape from the timeline: a sparse map of server summaries keyed by
  // message, read from a separate authorised endpoint and replaced only by a
  // newer server answer. Folding it into the message reducer would put four
  // action types there for state no message carries.
  const acknowledgements = useAcknowledgements({
    scope,
    messages: state.messages,
    requests: fallbacks,
  });

  const { toggleReaction: sendReactionToggle, sendTyping } = useMessageRealtime({
    scope,
    dispatch,
    fallbacks,
    reads,
    reactions: reactionEvents,
    reconciliation,
    reconcileAcknowledgements: acknowledgements.reconcile,
    reconcileAcknowledgement: acknowledgements.reconcileOne,
    notifyRemoved,
    listeners: { onPinUpdated, onMembersAdded, onAttachmentStatus, onTypingUpdated },
  });

  usePreviewReconciliation({ target, messages: state.messages, dispatch });

  const toggleReaction = useReactionToggle({
    dispatch,
    timers: reactionTimers,
    sendToggle: sendReactionToggle,
  });

  const selectReply = useCallback((message: Message) => {
    dispatch({ type: "reply_set", message });
  }, []);
  const cancelReply = useCallback(() => {
    dispatch({ type: "reply_clear" });
  }, []);

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
    acknowledgements: acknowledgements.summaries,
    acknowledgingId: acknowledgements.pendingId,
    acknowledgeError: acknowledgements.error,
    acknowledge: acknowledgements.acknowledge,
    reconcileLinkSafety: reconciliation.reconcileLinkSafety,
    editMessageLocal,
    deleteMessageLocal,
  };
}
