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

import { acquireChatSocket, type ChatSocketHandle, type ChatSocketStatus } from "./chatSocket";
import { normalizeChatTargetId } from "./chatTargetId";

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
  onMessageUpdated?: (event: WSMessageUpdatedEvent) => void;
  onReactionUpdated?: (event: WSReactionUpdatedEvent) => void;
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
  onMessageUpdated,
  onReactionUpdated,
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
  const onMessageUpdatedRef = useRef(onMessageUpdated);
  const onReactionRef = useRef(onReactionUpdated);
  const onPinRef = useRef(onPinUpdated);
  const onMembersRef = useRef(onMembersAdded);
  const onAttachmentStatusRef = useRef(onAttachmentStatus);
  const onConversationAvailableRef = useRef(onConversationAvailable);
  const onReactionErrorRef = useRef(onReactionError);
  const onSubscriptionErrorRef = useRef(onSubscriptionError);
  const onSubscribedRef = useRef(onSubscribed);
  const socketRef = useRef<ChatSocketHandle | null>(null);
  const [connectionStatus, setConnectionStatus] = useState<ChatSocketStatus>("connecting");
  const desiredTargetsRef = useRef(subscriptionTargets);
  const subscriptionControlRef = useRef<SubscriptionControl | null>(null);
  useLayoutEffect(() => {
    onMessageRef.current = onMessageCreated;
    onMessageUpdatedRef.current = onMessageUpdated;
    onReactionRef.current = onReactionUpdated;
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

  useEffect(() => {
    const primaryTarget = JSON.parse(primaryTargetSignature) as WSSubscriptionTarget | null;
    if (!primaryTarget) return;
    const primaryTargetKey = subscriptionTargetKey(primaryTarget);
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

    const sendSubscribe = (generation: number, targetKeys?: Iterable<string>) => {
      const control = currentSubscriptionControl(generation);
      if (!control || !handle?.isOpen()) return;
      const keys = targetKeys ?? control.expected.keys();
      for (const key of keys) {
        const target = control.expected.get(key);
        if (!target) continue;
        handle.send({
          type: "subscribe",
          target_type: target.kind,
          target_id: target.targetId,
        });
      }
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
        sendSubscribe(generation, activeControl.pending);
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
        // One resubscribe per generation: this is also the resynchronisation
        // point, since the acknowledgement is what the caller waits on.
        sendSubscribe(generation);
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
        if (!control.expected.has(incomingTargetKey)) return;
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
      if (releasing?.isOpen()) {
        // The connection outlives this hook, so its targets must be dropped
        // explicitly — releasing alone would leave the server subscribed.
        for (const target of control?.expected.values() ?? desiredTargetsRef.current) {
          releasing.send({
            type: "unsubscribe",
            target_type: target.kind,
            target_id: target.targetId,
          });
        }
      }
      releasing?.release();
    };
  }, [primaryTargetSignature]);

  useEffect(() => {
    const control = subscriptionControlRef.current;
    const handle = socketRef.current;
    if (!control || !handle?.isOpen()) return;

    const targets = JSON.parse(subscriptionSignature) as WSSubscriptionTarget[];
    const nextTargets = new Map(targets.map((target) => [subscriptionTargetKey(target), target]));
    const hadPendingTargets = control.pending.size > 0;
    for (const [key, target] of control.expected) {
      if (nextTargets.has(key)) continue;
      control.expected.delete(key);
      control.pending.delete(key);
      control.confirmed.delete(key);
      handle.send({
        type: "unsubscribe",
        target_type: target.kind,
        target_id: target.targetId,
      });
    }
    for (const [key, target] of nextTargets) {
      if (control.expected.has(key)) continue;
      control.expected.set(key, target);
      control.pending.add(key);
      handle.send({
        type: "subscribe",
        target_type: target.kind,
        target_id: target.targetId,
      });
    }
    if (control.pending.size === 0) {
      if (control.timer !== null) window.clearTimeout(control.timer);
      control.timer = null;
      control.attempts = 0;
      if (hadPendingTargets && control.primaryAcknowledgement) {
        onSubscribedRef.current?.(control.primaryAcknowledgement);
      }
    }
  }, [subscriptionSignature]);

  return { toggleReaction, connectionStatus };
}
