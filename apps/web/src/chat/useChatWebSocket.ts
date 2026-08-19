/**
 * useChatWebSocket — realtime message delivery over the tab's shared chat
 * connection.
 *
 * The socket itself lives in chatSocket.ts: this hook owns only the
 * subscription state for its targets. Two components use this hook at once
 * (the sidebar and the message list) and each used to open its own connection,
 * which is what exhausted the server's per-user connection budget (issue #449).
 *
 * Auth: chatSocket passes the Bearer access token as a credential subprotocol
 * plus a fixed public protocol selected by the server, so browser clients
 * (which cannot set arbitrary HTTP headers on the upgrade request) authenticate
 * without putting the token in the URL query string. The server validates the
 * token but does not echo it back in the response.
 *
 * Security notes:
 * - Token is passed as Sec-WebSocket-Protocol, not in the URL query string.
 * - onMessage callback is kept in a ref so re-renders don't restart anything.
 * - Target filtering is applied here AND in the caller (defence-in-depth).
 * - Events from a superseded socket generation are dropped, so a late callback
 *   can neither apply an event twice nor resubscribe on a dead connection.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import {
  acquireChatSocket,
  releaseConsumerSubscriptions,
  resendConsumerSubscriptions,
  setConsumerSubscriptions,
  type ChatSocketHandle,
  type ChatSocketStatus,
} from "./chatSocket";
import { normalizeChatTargetId } from "./chatTargetId";

/** Distinguishes one mounted hook from another for subscription ownership. */
let nextConsumerId = 0;

const SUBSCRIPTION_RETRY_BASE_DELAY_MS = 250;
const SUBSCRIPTION_RETRY_MAX_DELAY_MS = 2_000;
const MAX_SUBSCRIPTION_RECOVERY_ATTEMPTS = 3;

export interface WSMessagePayload {
  id: string;
  workspace_id: string;
  channel_id?: string;
  dm_conversation_id?: string;
  sender_id: string;
  sender_display_name: string;
  sender_email?: string;
  kind: string;
  body_text: string;
  /** Missing means legacy v1 during rolling deploys. */
  body_format?: "v1" | "v2" | "v3";
  status: string;
  /**
   * RF-21 link safety (issue #135), a separate axis from `status`. Absent on a
   * message with no links and on any pre-#135 server; the value is narrowed by
   * the client that reads it.
   */
  link_safety_state?: unknown;
  is_removed: boolean;
  created_at: string;
  updated_at: string;
  edited_at?: string | null;
  deleted_at?: string | null;
  quoted?: WSQuotePayload;
  /** Missing means a pre-RF-08 server during rolling deploys. */
  is_forwarded?: unknown;
  /**
   * RF-32 attachment metadata, so a subscriber renders a message carrying a
   * file without a follow-up GET. Absent on a text-only message and on any
   * pre-RF-32 server; the shape is validated by the client that reads it.
   */
  attachments?: unknown;
}

export interface WSQuotePayload {
  id: string;
  author_id: string;
  body?: string;
  body_format?: "v1" | "v2" | "v3";
  is_removed?: boolean;
  deleted_at?: string | null;
  created_at: string;
  updated_at?: string;
  /** Absent on a pre-#135 server; treated as "unknown" by the reader. */
  link_safety_state?: string;
}

export interface WSMessageCreatedEvent {
  /** Optional for compatibility with older servers during rolling deploys. */
  schema_version?: number;
  type: "message.created";
  workspace_id: string;
  target_type: "channel" | "dm";
  target_id: string;
  message_id: string;
  event_id: string;
  created_at: string;
  /** Full message DTO — use this to insert the message without a follow-up GET. */
  payload?: WSMessagePayload;
}

export interface WSReactionUpdatedEvent {
  type: "reaction.updated";
  target_type: "channel" | "dm";
  target_id: string;
  message_id: string;
  reaction?: {
    message_id: string;
    actor_user_id: string;
    emoji: string;
    added: boolean;
    reactions: Array<{ emoji: string; count: number }>;
  };
}

/**
 * Someone's typing state in a channel or DM changed.
 *
 * IsTyping is exactly what the server accepted from the typing user's own
 * typing.start/typing.stop — never inferred, never the local user's own
 * keystrokes echoed back with a different meaning. No draft, no character
 * count, no body: this answers "is this person typing right now" and nothing
 * about what they wrote.
 */
export interface WSTypingUpdatedEvent {
  type: "typing.updated";
  target_type: "channel" | "dm";
  target_id: string;
  typing?: {
    user_id: string;
    /**
     * The typing user's display name, resolved server-side once per
     * WebSocket connection. Absent when the server couldn't resolve one
     * (rare) — the caller falls back to its own heuristic in that case.
     */
    user_display_name?: string;
    is_typing: boolean;
    updated_at?: string;
  };
}

/**
 * RF-21: the author's message was refused by the link-safety check.
 *
 * Recipient-scoped rather than target-scoped — the server addresses it to one
 * user — so it carries no target and is routed before the subscription guard,
 * exactly like conversation.available. The payload is the message id and a
 * fixed reason; no body, no URL, no scan id, no provider detail.
 */
export interface WSMessageBlockedEvent {
  type: "message.blocked";
  message_id: string;
  reason?: string;
}

/**
 * RF-21: what is known about a published message's links changed (issue #135).
 *
 * Target-scoped, unlike message.blocked, and that difference is the whole point:
 * the message *was* delivered, so everyone holding it has to converge. It arrives
 * when a reconciliation obtains a verdict for a link whose scan had finished
 * without one — in either direction:
 *
 *   inconclusive -> safe       the "could not verify" notice goes away
 *   inconclusive -> malicious  the links stop being usable
 *
 * It carries a message id, state, and update time. No URL, scan id, or provider
 * text. The timestamp orders it against delayed create/edit events; the reducer
 * can retain it until the corresponding message arrives without inventing a
 * partial message.
 */
export interface WSMessageLinkSafetyChangedEvent {
  type: "message.link_safety_changed";
  target_type: "channel" | "dm";
  target_id: string;
  message_id: string;
  link_safety: {
    message_id: string;
    state: string;
    updated_at: string;
  };
}

export interface WSMessageUpdatedEvent {
  type: "message.updated";
  target_type: "channel" | "dm";
  target_id: string;
  /** Present on route-only events relayed through another server instance. */
  message_id?: string;
  message_update?: {
    message_id: string;
    channel_id?: string;
    dm_id?: string;
    body: string;
    body_format: "v1" | "v2" | "v3";
    link_safety_state?: unknown;
    edited_at: string;
    edit_count: number;
    is_edited: boolean;
    status?: "active" | "deleted";
    is_removed?: boolean;
    deleted_at?: string | null;
    updated_at?: string;
  };
}

function isMessageUpdatedEvent(
  value: Record<string, unknown>,
): value is Record<string, unknown> & WSMessageUpdatedEvent {
  const update = value["message_update"];
  if (update === undefined) return typeof value["message_id"] === "string";
  if (!update || typeof update !== "object") return false;
  const payload = update as Record<string, unknown>;
  return (
    typeof payload["message_id"] === "string" &&
    typeof payload["body"] === "string" &&
    (payload["body_format"] === "v1" ||
      payload["body_format"] === "v2" ||
      payload["body_format"] === "v3") &&
    typeof payload["edited_at"] === "string" &&
    typeof payload["edit_count"] === "number" &&
    typeof payload["is_edited"] === "boolean" &&
    (payload["link_safety_state"] === undefined ||
      typeof payload["link_safety_state"] === "string") &&
    (payload["status"] === undefined ||
      payload["status"] === "active" ||
      payload["status"] === "deleted") &&
    (payload["is_removed"] === undefined || typeof payload["is_removed"] === "boolean") &&
    (payload["deleted_at"] === undefined ||
      payload["deleted_at"] === null ||
      typeof payload["deleted_at"] === "string") &&
    (payload["updated_at"] === undefined || typeof payload["updated_at"] === "string")
  );
}

export interface WSPinUpdatedEvent {
  type: "pin.updated";
  target_type: "channel" | "dm";
  target_id: string;
  message_id: string;
  pin?: {
    message_id: string;
    actor_user_id: string;
    pinned: boolean;
  };
}

/**
 * A channel or group gained participants (issue #398).
 *
 * Names nobody by design: the server broadcasts only counts, because who may
 * see a roster is a per-reader decision the details endpoint makes and a
 * fan-out to every subscriber cannot. Receipt means "your view of this target
 * is stale", so the handler refetches — exactly like pin.updated.
 */
export interface WSMembersAddedEvent {
  type: "members.added";
  target_type: "channel" | "dm";
  target_id: string;
  members?: {
    actor_user_id: string;
    added_count: number;
    member_count: number;
  };
}

/**
 * A conversation the current user can now see (issue #398).
 *
 * The only user-scoped event in this protocol. It arrives *without* a
 * subscription to its target, by design: it is sent to someone who has just
 * been added to a channel or group and therefore cannot be subscribed to it
 * yet. That is why its routing below sits before the subscription guard.
 *
 * It is an invalidation hint carrying no identities and granting no access —
 * the client reacts by refetching the sidebar, which the server re-authorises.
 */
export interface WSConversationAvailableEvent {
  type: "conversation.available";
  target_type: "channel" | "dm";
  target_id: string;
}

/**
 * An attachment's antimalware verdict changed (RF-22).
 *
 * Produced by file-service and relayed over the same bus and the same
 * subscription fan-out as pin.updated, so it arrives only for a target this
 * client may already read. Like pin.updated it is an invalidation hint: the
 * handler refetches the authoritative list rather than patching a status in
 * place, which is what keeps a refetch and an event arriving together from
 * disagreeing.
 *
 * The payload carries no filename, no size and no scanner detail — the server
 * is deliberately not describing files over a broadcast.
 */
export interface WSAttachmentStatusEvent {
  type: "attachment.status";
  target_type: "channel" | "dm";
  target_id: string;
  attachment?: {
    attachment_id: string;
    /** One of the three states file-service persists. */
    status: "pending_scan" | "clean" | "rejected";
    updated_at: string;
  };
}

export interface WSClientErrorEvent {
  type: "error";
  operation?: string;
  code: string;
  retry_after?: number;
}

export interface WSSubscribedEvent {
  type: "subscribed";
  operation: "subscribe";
  target_type: "channel" | "dm";
  target_id: string;
}

export interface WSSubscriptionTarget {
  kind: "channel" | "dm";
  targetId: string;
}

interface UseChatWebSocketOptions {
  kind: "channel" | "dm";
  targetId: string;
  additionalTargets?: readonly WSSubscriptionTarget[];
  onMessageCreated: (event: WSMessageCreatedEvent) => void;
  onMessageBlocked?: (event: WSMessageBlockedEvent) => void;
  onMessageLinkSafetyChanged?: (event: WSMessageLinkSafetyChangedEvent) => void;
  onMessageUpdated?: (event: WSMessageUpdatedEvent) => void;
  onReactionUpdated?: (event: WSReactionUpdatedEvent) => void;
  onTypingUpdated?: (event: WSTypingUpdatedEvent) => void;
  onPinUpdated?: (event: WSPinUpdatedEvent) => void;
  onMembersAdded?: (event: WSMembersAddedEvent) => void;
  onAttachmentStatus?: (event: WSAttachmentStatusEvent) => void;
  onConversationAvailable?: (event: WSConversationAvailableEvent) => void;
  onReactionError?: (event: WSClientErrorEvent) => void;
  onSubscriptionError?: (event: WSClientErrorEvent) => void;
  onSubscribed?: (event: WSSubscribedEvent) => void;
}

export interface ChatWebSocketActions {
  toggleReaction: (messageId: string, emoji: string) => boolean;
  /**
   * Declares this user's typing intent for the primary target. Returns false
   * when the shared socket is not open, the same "nothing sent, caller may
   * retry" contract toggleReaction uses. Never sends the composer's content —
   * only the boolean state.
   */
  sendTyping: (isTyping: boolean) => boolean;
  /** Shared connection state, for discreet feedback and diagnosis. */
  connectionStatus: ChatSocketStatus;
}

interface SubscriptionControl {
  generation: number;
  expected: Map<string, WSSubscriptionTarget>;
  pending: Set<string>;
  confirmed: Set<string>;
  primaryAcknowledgement: WSSubscribedEvent | null;
  attempts: number;
  timer: number | null;
}

function subscriptionTargetKey({ kind, targetId }: WSSubscriptionTarget): string {
  return `${kind}:${targetId}`;
}

export function useChatWebSocket({
  kind,
  targetId,
  additionalTargets,
  onMessageCreated,
  onMessageBlocked,
  onMessageLinkSafetyChanged,
  onMessageUpdated,
  onReactionUpdated,
  onTypingUpdated,
  onPinUpdated,
  onMembersAdded,
  onAttachmentStatus,
  onConversationAvailable,
  onReactionError,
  onSubscriptionError,
  onSubscribed,
}: UseChatWebSocketOptions): ChatWebSocketActions {
  const normalizedTargetId = normalizeChatTargetId(targetId);
  const seenTargets = new Set<string>();
  const subscriptionTargets = [
    { kind, targetId: normalizedTargetId },
    ...(additionalTargets ?? []).map(({ kind: targetKind, targetId: additionalTargetId }) => ({
      kind: targetKind,
      targetId: normalizeChatTargetId(additionalTargetId),
    })),
  ].filter(({ kind: targetKind, targetId: subscriptionTargetId }) => {
    const key = `${targetKind}:${subscriptionTargetId}`;
    if (!subscriptionTargetId || seenTargets.has(key)) return false;
    seenTargets.add(key);
    return true;
  });
  const subscriptionSignature = JSON.stringify(subscriptionTargets);
  const primaryTargetSignature = JSON.stringify(subscriptionTargets[0] ?? null);
  // Keep the callback current without restarting the effect.
  const onMessageRef = useRef(onMessageCreated);
  const onMessageBlockedRef = useRef(onMessageBlocked);
  const onLinkSafetyRef = useRef(onMessageLinkSafetyChanged);
  const onMessageUpdatedRef = useRef(onMessageUpdated);
  const onReactionRef = useRef(onReactionUpdated);
  const onTypingRef = useRef(onTypingUpdated);
  const onPinRef = useRef(onPinUpdated);
  const onMembersRef = useRef(onMembersAdded);
  const onAttachmentStatusRef = useRef(onAttachmentStatus);
  const onConversationAvailableRef = useRef(onConversationAvailable);
  const onReactionErrorRef = useRef(onReactionError);
  const onSubscriptionErrorRef = useRef(onSubscriptionError);
  const onSubscribedRef = useRef(onSubscribed);
  const socketRef = useRef<ChatSocketHandle | null>(null);
  // Stable for the life of this hook instance, across effect re-runs and the
  // double mount StrictMode performs. It is what the shared connection records
  // interest under, so two hooks watching one channel are two owners of it
  // rather than two subscribe/unsubscribe pairs racing each other.
  const consumerIdRef = useRef<string | null>(null);
  consumerIdRef.current ??= `chat-ws-${++nextConsumerId}`;
  const [connectionStatus, setConnectionStatus] = useState<ChatSocketStatus>("connecting");
  const desiredTargetsRef = useRef(subscriptionTargets);
  const subscriptionControlRef = useRef<SubscriptionControl | null>(null);
  useLayoutEffect(() => {
    onMessageRef.current = onMessageCreated;
    onMessageBlockedRef.current = onMessageBlocked;
    onLinkSafetyRef.current = onMessageLinkSafetyChanged;
    onMessageUpdatedRef.current = onMessageUpdated;
    onReactionRef.current = onReactionUpdated;
    onTypingRef.current = onTypingUpdated;
    onPinRef.current = onPinUpdated;
    onMembersRef.current = onMembersAdded;
    onAttachmentStatusRef.current = onAttachmentStatus;
    onConversationAvailableRef.current = onConversationAvailable;
    onReactionErrorRef.current = onReactionError;
    onSubscriptionErrorRef.current = onSubscriptionError;
    onSubscribedRef.current = onSubscribed;
  });
  useLayoutEffect(() => {
    desiredTargetsRef.current = JSON.parse(subscriptionSignature) as WSSubscriptionTarget[];
  }, [subscriptionSignature]);

  const toggleReaction = useCallback((messageId: string, emoji: string) => {
    const handle = socketRef.current;
    if (!handle) return false;
    return handle.send({ type: "reaction.toggle", message_id: messageId, emoji });
  }, []);

  // Scoped to the primary target only — the one conversation actually open —
  // same as toggleReaction reading socketRef at call time rather than closing
  // over a stale handle.
  const sendTyping = useCallback(
    (isTyping: boolean) => {
      const handle = socketRef.current;
      if (!handle) return false;
      return handle.send({
        type: isTyping ? "typing.start" : "typing.stop",
        target_type: kind,
        target_id: normalizedTargetId,
      });
    },
    [kind, normalizedTargetId],
  );

  useEffect(() => {
    const primaryTarget = JSON.parse(primaryTargetSignature) as WSSubscriptionTarget | null;
    if (!primaryTarget) return;
    const primaryTargetKey = subscriptionTargetKey(primaryTarget);
    const consumerId = consumerIdRef.current!;
    let closed = false;
    let handle: ChatSocketHandle | null = null;

    // A control belongs to exactly one socket generation. Anything arriving
    // for an older generation — a late acknowledgement, a queued recovery —
    // is dropped rather than applied to the connection that replaced it.
    const currentSubscriptionControl = (generation: number) => {
      const control = subscriptionControlRef.current;
      return !closed && control?.generation === generation ? control : null;
    };

    const clearSubscriptionRecoveryTimer = (control: SubscriptionControl | null) => {
      if (!control || control.timer === null) return;
      window.clearTimeout(control.timer);
      control.timer = null;
    };

    const completeSubscriptionRecovery = (control: SubscriptionControl) => {
      if (control.pending.size > 0) return false;
      clearSubscriptionRecoveryTimer(control);
      control.attempts = 0;
      return true;
    };

    /**
     * Adopts targets the connection reports are already live because another
     * consumer holds them.
     *
     * No frame was sent for those, so no `subscribed` acknowledgement is coming
     * — waiting for one would leave the caller permanently un-resynchronised the
     * moment two views share a conversation. The acknowledgement is synthesised
     * from what the connection already knows to be true.
     */
    const adoptSharedSubscriptions = (control: SubscriptionControl, keys: readonly string[]) => {
      for (const key of keys) {
        if (!control.pending.delete(key)) continue;
        control.confirmed.add(key);
        const target = control.expected.get(key);
        if (!target || key !== primaryTargetKey) continue;
        control.primaryAcknowledgement = {
          type: "subscribed",
          operation: "subscribe",
          target_type: target.kind,
          target_id: target.targetId,
        };
      }
    };

    /** Declares this consumer's targets to the connection, which owns the frames. */
    const declareTargets = (control: SubscriptionControl) => {
      adoptSharedSubscriptions(
        control,
        setConsumerSubscriptions(consumerId, [...control.expected.values()]),
      );
    };

    const scheduleSubscriptionRecovery = (generation: number) => {
      const control = currentSubscriptionControl(generation);
      if (
        !control ||
        !handle?.isOpen() ||
        control.timer !== null ||
        control.attempts >= MAX_SUBSCRIPTION_RECOVERY_ATTEMPTS
      ) {
        return;
      }
      if (control.pending.size === 0) {
        control.confirmed.clear();
        control.primaryAcknowledgement = null;
        for (const key of control.expected.keys()) control.pending.add(key);
      }
      const delay = Math.min(
        SUBSCRIPTION_RETRY_BASE_DELAY_MS * 2 ** control.attempts,
        SUBSCRIPTION_RETRY_MAX_DELAY_MS,
      );
      control.attempts += 1;
      control.timer = window.setTimeout(() => {
        const activeControl = currentSubscriptionControl(generation);
        if (!activeControl || activeControl.generation !== control.generation) return;
        activeControl.timer = null;
        resendConsumerSubscriptions(consumerId, activeControl.pending);
      }, delay);
    };

    handle = acquireChatSocket({
      onStatus: (next) => {
        if (!closed) setConnectionStatus(next);
      },

      onOpen: (generation) => {
        if (closed) return;
        const expected = new Map(
          desiredTargetsRef.current.map((target) => [subscriptionTargetKey(target), target]),
        );
        const control: SubscriptionControl = {
          generation,
          expected,
          pending: new Set(expected.keys()),
          confirmed: new Set(),
          primaryAcknowledgement: null,
          attempts: 0,
          timer: null,
        };
        clearSubscriptionRecoveryTimer(subscriptionControlRef.current);
        subscriptionControlRef.current = control;
        // The connection has already re-established what it owns; this only
        // declares what *this* consumer wants of it, which is also the
        // resynchronisation point since the acknowledgement is what the caller
        // waits on.
        declareTargets(control);
        if (control.pending.size === 0 && control.primaryAcknowledgement) {
          onSubscribedRef.current?.(control.primaryAcknowledgement);
        }
      },

      onMessage: (d, generation) => {
        const control = currentSubscriptionControl(generation);
        if (!control) return;
        const incomingTargetId =
          typeof d["target_id"] === "string" ? normalizeChatTargetId(d["target_id"]) : "";
        const incomingTargetType =
          d["target_type"] === "channel" || d["target_type"] === "dm" ? d["target_type"] : "";
        const incomingTargetKey = `${incomingTargetType}:${incomingTargetId}`;
        const normalizedData = incomingTargetId ? { ...d, target_id: incomingTargetId } : d;
        if (
          d["type"] === "subscribed" &&
          d["operation"] === "subscribe" &&
          control.expected.has(incomingTargetKey)
        ) {
          const acknowledgement = normalizedData as unknown as WSSubscribedEvent;
          const wasPending = control.pending.delete(incomingTargetKey);
          control.confirmed.add(incomingTargetKey);
          if (incomingTargetKey === primaryTargetKey) {
            control.primaryAcknowledgement = acknowledgement;
          }
          if (wasPending && completeSubscriptionRecovery(control)) {
            onSubscribedRef.current?.(control.primaryAcknowledgement ?? acknowledgement);
          }
          return;
        }
        if (d["type"] === "error" && typeof d["code"] === "string") {
          const clientError = d as unknown as WSClientErrorEvent;
          if (d["operation"] === "subscribe") {
            onSubscriptionErrorRef.current?.(clientError);
            if (d["code"] === "room_subscription_unavailable" && handle?.isOpen()) {
              scheduleSubscriptionRecovery(generation);
            }
            return;
          }
          onReactionErrorRef.current?.(clientError);
          return;
        }
        // Routed before the subscription guard on purpose: this event exists
        // precisely for a target the client is not subscribed to yet, so
        // requiring a subscription would drop the one message that tells a
        // newly-added user their sidebar is stale.
        if (d["type"] === "conversation.available" && incomingTargetId && incomingTargetType) {
          onConversationAvailableRef.current?.(
            normalizedData as unknown as WSConversationAvailableEvent,
          );
          return;
        }
        // Routed before the subscription guard for the same reason
        // conversation.available is: it is addressed to a user rather than to a
        // conversation, so it carries no target to match against. Without this
        // the author of a blocked message would never be told, and their
        // composer would sit on "checking links…" forever.
        if (d["type"] === "message.blocked" && typeof d["message_id"] === "string") {
          onMessageBlockedRef.current?.(normalizedData as unknown as WSMessageBlockedEvent);
          return;
        }
        if (!control.expected.has(incomingTargetKey)) return;
        // Routed for any subscribed target rather than only the primary one, for
        // the same reason attachment.status is: a reconciliation lands minutes
        // after the message and quite possibly while the reader is looking at a
        // different conversation. The correction still has to be applied — a
        // message that stopped being safe must not stay drawn as safe just
        // because its tab is in the background.
        if (d["type"] === "message.link_safety_changed" && typeof d["message_id"] === "string") {
          const linkSafety = d["link_safety"];
          if (
            !linkSafety ||
            typeof linkSafety !== "object" ||
            typeof (linkSafety as Record<string, unknown>)["message_id"] !== "string" ||
            (linkSafety as Record<string, unknown>)["message_id"] !== d["message_id"] ||
            typeof (linkSafety as Record<string, unknown>)["state"] !== "string" ||
            typeof (linkSafety as Record<string, unknown>)["updated_at"] !== "string"
          ) {
            return;
          }
          onLinkSafetyRef.current?.(normalizedData as unknown as WSMessageLinkSafetyChangedEvent);
          return;
        }
        if (d["type"] === "message.created") {
          onMessageRef.current(normalizedData as unknown as WSMessageCreatedEvent);
          return;
        }
        // Routed for any subscribed target rather than only the primary one
        // (RF-22). A scan verdict is not a mutating action the user just took:
        // it lands seconds or minutes after an upload, possibly while the user
        // is looking at a different conversation, and the panel that has to
        // reconcile is whichever one the attachment belongs to.
        if (d["type"] === "attachment.status") {
          onAttachmentStatusRef.current?.(normalizedData as unknown as WSAttachmentStatusEvent);
          return;
        }
        // Mutating actions remain scoped to the primary target.
        if (incomingTargetKey !== primaryTargetKey) return;
        if (d["type"] === "message.updated" && isMessageUpdatedEvent(normalizedData)) {
          onMessageUpdatedRef.current?.(normalizedData);
        } else if (d["type"] === "reaction.updated") {
          onReactionRef.current?.(normalizedData as unknown as WSReactionUpdatedEvent);
        } else if (d["type"] === "typing.updated") {
          onTypingRef.current?.(normalizedData as unknown as WSTypingUpdatedEvent);
        } else if (d["type"] === "pin.updated") {
          onPinRef.current?.(normalizedData as unknown as WSPinUpdatedEvent);
        } else if (d["type"] === "members.added") {
          onMembersRef.current?.(normalizedData as unknown as WSMembersAddedEvent);
        }
      },

      onClose: (generation) => {
        const control = currentSubscriptionControl(generation);
        clearSubscriptionRecoveryTimer(control);
        if (subscriptionControlRef.current === control) subscriptionControlRef.current = null;
      },
    });
    socketRef.current = handle;
    // Seed a control for the generation the handle is already on, so an event
    // that lands between acquiring the socket and its open callback is still
    // routed. onOpen replaces it with the control that owns the subscriptions.
    if (!subscriptionControlRef.current) {
      const seeded = new Map(
        desiredTargetsRef.current.map((target) => [subscriptionTargetKey(target), target]),
      );
      subscriptionControlRef.current = {
        generation: handle.generation(),
        expected: seeded,
        pending: new Set(seeded.keys()),
        confirmed: new Set(),
        primaryAcknowledgement: null,
        attempts: 0,
        timer: null,
      };
    }

    return () => {
      closed = true;
      const control = subscriptionControlRef.current;
      clearSubscriptionRecoveryTimer(control);
      subscriptionControlRef.current = null;
      const releasing = handle;
      handle = null;
      socketRef.current = null;
      // Interest is dropped, not the subscription: the connection unsubscribes
      // only if nobody else declared this target. Sending the frame from here is
      // what used to cut the sidebar off from a channel the message list happened
      // to close.
      releaseConsumerSubscriptions(consumerId);
      releasing?.release();
    };
  }, [primaryTargetSignature]);

  useEffect(() => {
    const control = subscriptionControlRef.current;
    const handle = socketRef.current;
    if (!control || !handle?.isOpen()) return;

    const consumerId = consumerIdRef.current!;
    const primaryTarget = JSON.parse(primaryTargetSignature) as WSSubscriptionTarget | null;
    const primaryTargetKey = primaryTarget ? subscriptionTargetKey(primaryTarget) : "";
    const targets = JSON.parse(subscriptionSignature) as WSSubscriptionTarget[];
    const nextTargets = new Map(targets.map((target) => [subscriptionTargetKey(target), target]));
    let awaited = control.pending.size > 0;
    const adopted: string[] = [];
    for (const [key] of control.expected) {
      if (nextTargets.has(key)) continue;
      control.expected.delete(key);
      control.pending.delete(key);
      control.confirmed.delete(key);
    }
    for (const [key, target] of nextTargets) {
      if (control.expected.has(key)) continue;
      control.expected.set(key, target);
      control.pending.add(key);
      awaited = true;
    }
    // One declaration for the whole new set. The connection works out which
    // targets genuinely arrived and which genuinely left; a target this consumer
    // dropped but another still wants produces no unsubscribe at all.
    adopted.push(...setConsumerSubscriptions(consumerId, targets));
    for (const key of adopted) {
      if (!control.pending.delete(key)) continue;
      control.confirmed.add(key);
      const target = control.expected.get(key);
      if (target && key === primaryTargetKey) {
        control.primaryAcknowledgement = {
          type: "subscribed",
          operation: "subscribe",
          target_type: target.kind,
          target_id: target.targetId,
        };
      }
    }
    if (control.pending.size === 0) {
      if (control.timer !== null) window.clearTimeout(control.timer);
      control.timer = null;
      control.attempts = 0;
      if (awaited && control.primaryAcknowledgement) {
        onSubscribedRef.current?.(control.primaryAcknowledgement);
      }
    }
  }, [subscriptionSignature, primaryTargetSignature]);

  return { toggleReaction, sendTyping, connectionStatus };
}
