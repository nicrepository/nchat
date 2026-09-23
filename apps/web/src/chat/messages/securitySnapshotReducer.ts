/**
 * RF-21 reconciliation: the authoritative security state of a page of messages,
 * read back from the server and merged with any correction still in flight.
 *
 * This is what a reconnect uses. It is a reconciliation, not a delivery: the
 * snapshot has no ordering relationship with the corrections this client holds,
 * so every field is resolved by comparing versions, and a correction that lost
 * is dropped because the snapshot now carries it.
 */

import type { Message, MessageSecuritySnapshot } from "../chatTypes";
import { mergeSnapshotLinks } from "../messageLinks";
import {
  bodyUnderAggregate,
  isNotNewerSecurityVersion,
  isOlderSecurityVersion,
} from "./linkSafetyCorrections";
import type { Action, ActionOf, LinkSafetyCorrections, MessagesState } from "./types";

type AvailableSnapshot = Extract<MessageSecuritySnapshot, { available: true }>;
type QuoteLinkSafety = NonNullable<Message["quoted"]>["linkSafetyState"];

/**
 * Which version of a message's security state wins: a local correction the
 * snapshot has not caught up with, the snapshot itself, or what is already
 * drawn. A correction that lost is dropped, because the snapshot now carries it.
 */
function resolveSnapshotVersion(
  message: Message,
  snapshot: AvailableSnapshot,
  corrections: LinkSafetyCorrections,
): {
  state: Message["linkSafetyState"];
  updatedAt: string;
  status: Message["status"];
  /** True when the snapshot is at least as new as the drawn message. */
  snapshotWins: boolean;
} {
  const correction = corrections.get(message.id);
  const correctionWins = Boolean(
    correction && isNotNewerSecurityVersion(snapshot.updatedAt, correction.updatedAt),
  );
  if (!correctionWins) corrections.delete(message.id);
  const snapshotWins = !isOlderSecurityVersion(snapshot.updatedAt, message.updatedAt);
  const status = snapshotWins ? snapshot.status : message.status;
  if (correction && correctionWins) {
    return { state: correction.state, updatedAt: correction.updatedAt, status, snapshotWins };
  }
  if (snapshotWins) {
    return { state: snapshot.linkSafetyState, updatedAt: snapshot.updatedAt, status, snapshotWins };
  }
  return { state: message.linkSafetyState, updatedAt: message.updatedAt, status, snapshotWins };
}

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
  // Issue #807: the per-link state is merged occurrence by occurrence under
  // the realtime version order, so a snapshot read before an update landed
  // cannot regress it; the occurrence set follows the newer message version.
  // The body follows the same rule as every other aggregate correction: kept
  // as the server projected it whenever the per-link model is present.
  const links = removed
    ? undefined
    : mergeSnapshotLinks(message.links, snapshot.links, resolved.snapshotWins);
  const next: Message = {
    ...message,
    status: resolved.status,
    linkSafetyState: resolved.state,
    bodyText: removed
      ? ""
      : bodyUnderAggregate({ bodyText: message.bodyText, links }, resolved.state),
    isRemoved: removed,
    updatedAt: resolved.updatedAt,
    links,
    ...(removed ? { quoted: undefined, reactions: [] } : {}),
  };
  const quoted = message.quoted;
  if (removed || !quoted || snapshot.quoted?.messageId !== quoted.id) return next;
  return applySnapshotToQuote(next, quoted, snapshot.quoted, corrections);
}

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

/** The authoritative security snapshots a reconnect reads back. */
export function reduceSecuritySnapshots(
  state: MessagesState,
  action: Action,
): MessagesState | undefined {
  return action.type === "security_snapshots_refreshed"
    ? applySecuritySnapshotsRefreshed(state, action)
    : undefined;
}
