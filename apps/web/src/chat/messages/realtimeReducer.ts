/**
 * What the socket delivered about the messages in this conversation.
 *
 * Every transition here is idempotent by id, because delivery is at-least-once
 * and unordered: a repeated create is a no-op, a snapshot replaces in place, and
 * an attachment verdict patches the one attachment it names.
 */

import { compareTimelinePositions, insertMessageChronologically } from "./messageOrder";
import type { Message } from "../chatTypes";
import { applyLinkSafetyCorrections, dropSupersededCorrection } from "./linkSafetyCorrections";
import type { Action, ActionOf, MessagesState } from "./types";

function applyWsReceived(state: MessagesState, action: ActionOf<"ws_received">): MessagesState {
  const received = applyLinkSafetyCorrections(action.message, state.linkSafetyCorrections);
  const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
  linkSafetyCorrections.delete(action.message.id);
  // Dedup: if the message is already present (e.g. our own POST response
  // arrived before the WS event), this is normally a pure no-op.
  const existingIndex = state.messages.findIndex((m) => m.id === received.id);
  if (existingIndex >= 0) {
    return applyWsRedelivery(state, received, existingIndex, linkSafetyCorrections);
  }

  // Insert in stable (createdAt, id) order to handle out-of-order delivery.
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

/**
 * A create event for a message this timeline already holds.
 *
 * RF-21: the one case where "already present" is not a no-op.
 *
 * A message whose links were still being scanned was returned to its own sender
 * as pending_link_scan and shown to nobody else. When the scan clears, the
 * backend promotes it and broadcasts message.created with the same id — so
 * discarding the event by id, as this used to do unconditionally, left the
 * sender looking at "checking links…" forever while everyone else saw the
 * message.
 *
 * The event carries the authoritative published row, so it replaces the local
 * one in place: same position, no duplicate, no re-sort. Only this transition is
 * special-cased; every other repeat delivery stays the no-op it was, which is
 * what keeps at-least-once outbox delivery safe.
 */
function applyWsRedelivery(
  state: MessagesState,
  received: Message,
  existingIndex: number,
  linkSafetyCorrections: MessagesState["linkSafetyCorrections"],
): MessagesState {
  const existing = state.messages[existingIndex];
  const promoted =
    existing.status === "pending_link_scan" && received.status !== "pending_link_scan";
  if (!promoted) return { ...state, linkSafetyCorrections, realtimeError: null };
  const messages = [...state.messages];
  messages[existingIndex] = received;
  return { ...state, messages, linkSafetyCorrections, realtimeError: null };
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

/** A server copy prepared for the timeline: withdrawn content stripped, corrections applied. */
interface PreparedSnapshot {
  snapshot: Message;
  removed: boolean;
}

function prepareSnapshot(
  message: Message,
  corrections: MessagesState["linkSafetyCorrections"],
): PreparedSnapshot {
  const removed = message.isRemoved || message.status === "deleted";
  const raw = removed ? { ...message, bodyText: "", quoted: undefined, reactions: [] } : message;
  return { snapshot: applyLinkSafetyCorrections(raw, corrections), removed };
}

/** The result of applying a batch of server copies to the timeline. */
export interface SnapshotBatch {
  messages: Message[];
  linkSafetyCorrections: MessagesState["linkSafetyCorrections"];
  /** True when a copy the timeline did not hold landed after its last message. */
  appended: boolean;
}

/**
 * Authoritative server copies, applied to the timeline in one pass.
 *
 * Every copy replaces its message in place, a withdrawn one also blanks the
 * quotes of it, and — with `insertIfMissing` — the ones the timeline did not
 * hold are merged in chronological order. Linear in the timeline plus the batch:
 * an index by id instead of a scan per copy, one rewrite of the list, and a sort
 * that only has to merge two runs that are already ordered.
 */
export function applyMessageSnapshots(
  state: MessagesState,
  messages: Message[],
  insertIfMissing: boolean,
): SnapshotBatch {
  let linkSafetyCorrections = state.linkSafetyCorrections;
  const byId = new Map<string, PreparedSnapshot>();
  for (const message of messages) {
    byId.set(message.id, prepareSnapshot(message, state.linkSafetyCorrections));
    if (linkSafetyCorrections.has(message.id)) {
      linkSafetyCorrections = dropSupersededCorrection(
        linkSafetyCorrections,
        message.id,
        message.updatedAt,
      );
    }
  }
  const rewritten = state.messages.map((message) => {
    const own = byId.get(message.id);
    const current = own ? applySnapshotToTimeline(message, own.snapshot, own.removed) : message;
    const quoted = current.quoted ? byId.get(current.quoted.id) : undefined;
    return quoted ? applySnapshotToTimeline(current, quoted.snapshot, quoted.removed) : current;
  });
  const held = new Set(state.messages.map(({ id }) => id));
  const missing = insertIfMissing
    ? [...byId.values()].map(({ snapshot }) => snapshot).filter(({ id }) => !held.has(id))
    : [];
  return mergeChronologically(rewritten, missing, linkSafetyCorrections);
}

function mergeChronologically(
  messages: Message[],
  missing: Message[],
  linkSafetyCorrections: MessagesState["linkSafetyCorrections"],
): SnapshotBatch {
  if (missing.length === 0) return { messages, linkSafetyCorrections, appended: false };
  const last = messages.at(-1);
  const appended = missing.some(
    (message) => last === undefined || compareTimelinePositions(message, last) > 0,
  );
  return {
    messages: [...messages, ...missing].sort(compareTimelinePositions),
    linkSafetyCorrections,
    appended,
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
  const batch = applyMessageSnapshots(state, [action.message], action.insertIfMissing);
  const removed = action.message.isRemoved || action.message.status === "deleted";
  return {
    ...state,
    messages: batch.messages,
    replyTo: removed && state.replyTo?.id === action.message.id ? null : state.replyTo,
    lastMutation: batch.appended ? "ws_append" : "none",
    realtimeError: null,
    linkSafetyCorrections: batch.linkSafetyCorrections,
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

/** The attachments of one message, refreshed from a polled listing. */
function reconcileMessageAttachments(
  message: Message,
  byId: Map<string, ActionOf<"attachments_reconciled">["attachments"][number]>,
): Message {
  if (!message.attachments?.some((item) => byId.has(item.id))) return message;
  let changed = false;
  const attachments = message.attachments.map((item) => {
    const fresh = byId.get(item.id);
    if (!fresh || (fresh.status === item.status && fresh.previewStatus === item.previewStatus)) {
      return item;
    }
    changed = true;
    return { ...item, status: fresh.status, previewStatus: fresh.previewStatus };
  });
  return changed ? { ...message, attachments } : message;
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
    const next = reconcileMessageAttachments(message, byId);
    if (next !== message) changed = true;
    return next;
  });
  return changed ? { ...state, messages, lastMutation: "none" } : state;
}

/** What the socket delivered about messages in this conversation. */
export function reduceRealtime(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "ws_received":
      return applyWsReceived(state, action);
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
