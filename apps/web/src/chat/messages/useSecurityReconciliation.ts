import { useCallback } from "react";

import {
  fetchLinkSafetyStatuses,
  reconcileMessageLinkSafety,
  type LinkSafetyStatus,
} from "../chatApi";
import type { LinkSafetyRecheck, Message } from "../chatTypes";
import { blockedMessageReason } from "./composerReducer";
import { batchMessageIds, type MessagesGateway } from "./messagesGateway";
import type { AuthoritativeReads } from "./useAuthoritativeReads";
import type { ConversationScope } from "./useConversationScope";
import type { RequestRegistry } from "./useRequestRegistry";
import type { Action } from "./types";

/**
 * How many withheld messages one reconnect may ask about.
 *
 * Matches the server's cap. A client holds a message in this state only between
 * sending it and its scan resolving, so the realistic count is one or two; the
 * bound is here so a long-lived tab that accumulated them cannot turn a
 * reconnect into an unbounded query.
 */
const linkSafetyReconcileBatchSize = 100;

const pendingScanKey = "link-safety-reconcile";
const securityRefreshKey = "message-security-refresh";

interface StatusSink {
  dispatch: (action: Action) => void;
  readMessageSnapshot: AuthoritativeReads["readMessageSnapshot"];
}

/**
 * What the server said about one withheld message, applied.
 *
 * "pending" and an id the server would not talk about are both left alone: the
 * bubble stays as it is rather than being resolved on an answer nobody gave.
 * "active" is re-read rather than promoted from the state alone — the state is
 * not the message, and reusing the same read a missed message.created takes is
 * what makes the two paths converge instead of racing.
 */
function applyLinkSafetyStatus(status: LinkSafetyStatus, sink: StatusSink): void {
  if (status.state === "pending") return;
  if (status.state === "active") {
    sink.readMessageSnapshot(status.messageId, false);
    return;
  }
  sink.dispatch({
    type: "message_blocked",
    messageId: status.messageId,
    reason: status.state === "blocked" ? blockedMessageReason(status.reason) : "unavailable",
  });
}

/** Messages whose own or whose quote's links the server never resolved. */
function inconclusiveMessageIDs(messages: Message[]): string[] {
  return messages
    .filter(
      (message) =>
        message.status === "active" &&
        (message.linkSafetyState === "inconclusive" ||
          message.quoted?.linkSafetyState === "inconclusive"),
    )
    .map((message) => message.id);
}

function referencingMessageIDs(messages: Message[]): string[] {
  return messages.filter((message) => message.reference?.available).map((message) => message.id);
}

export interface SecurityReconciliation {
  /** RF-21 reconnect reconciliation of messages this client still holds as pending. */
  reconcilePendingLinkScans(): void;
  /** RF-21 reconnect refresh of the authoritative security state of what is drawn. */
  refreshAuthoritativeMessageSecurity(): void;
  /** RF-21 "Verificar novamente" for one message (issue #135). */
  reconcileLinkSafety(messageId: string): Promise<LinkSafetyRecheck | undefined>;
}

interface Options {
  scope: ConversationScope;
  gateway: MessagesGateway;
  dispatch: (action: Action) => void;
  fallbacks: RequestRegistry;
  readMessageSnapshot: AuthoritativeReads["readMessageSnapshot"];
}

export function useSecurityReconciliation({
  scope,
  gateway,
  dispatch,
  fallbacks,
  readMessageSnapshot,
}: Options): SecurityReconciliation {
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
    const pendingIds = scope
      .messages()
      .filter((message) => message.status === "pending_link_scan")
      .slice(-linkSafetyReconcileBatchSize)
      .map((message) => message.id);
    if (pendingIds.length === 0) return;

    // Registered with the websocket fallbacks so a target change aborts it, the
    // same way every other authoritative refetch here is cancelled: an answer
    // about channel A must never be applied to channel B.
    const controller = fallbacks.start(pendingScanKey);
    const loadKey = scope.key;
    void fetchLinkSafetyStatuses(pendingIds, controller.signal).then(
      (statuses) => {
        fallbacks.finish(pendingScanKey, controller);
        if (controller.signal.aborted || !scope.isCurrent(loadKey)) return;
        for (const status of statuses) {
          applyLinkSafetyStatus(status, { dispatch, readMessageSnapshot });
        }
      },
      () => {
        fallbacks.finish(pendingScanKey, controller);
        // A failed reconciliation says nothing about the message. Removing the
        // bubble, promoting it, or reporting it as blocked would all be inventing
        // an answer the server never gave; the next reconnect asks again.
      },
    );
  }, [dispatch, fallbacks, readMessageSnapshot, scope]);

  const refreshAuthoritativeMessageSecurity = useCallback(() => {
    const visible = scope.messages();
    const snapshotIDs = inconclusiveMessageIDs(visible);
    const referenceIDs = referencingMessageIDs(visible);
    if (snapshotIDs.length === 0 && referenceIDs.length === 0) return;

    const controller = fallbacks.start(securityRefreshKey);
    const loadKey = scope.key;
    const snapshotRequests = batchMessageIds(snapshotIDs).map((messageIDs) =>
      gateway.fetchSecuritySnapshots(messageIDs, controller.signal),
    );
    const referenceRequests = batchMessageIds(referenceIDs).map((messageIDs) =>
      gateway.resolveReferences(messageIDs, controller.signal),
    );

    void Promise.allSettled([
      Promise.all(snapshotRequests).then((parts) => parts.flat()),
      Promise.all(referenceRequests).then((parts) => Object.assign({}, ...parts)),
    ]).then(([snapshotResult, referenceResult]) => {
      fallbacks.finish(securityRefreshKey, controller);
      if (controller.signal.aborted || !scope.isCurrent(loadKey)) return;
      if (snapshotResult.status === "fulfilled") {
        dispatch({ type: "security_snapshots_refreshed", snapshots: snapshotResult.value });
      }
      if (referenceIDs.length === 0) return;
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
    });
  }, [dispatch, fallbacks, gateway, scope]);

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
    [dispatch],
  );

  return {
    reconcilePendingLinkScans,
    refreshAuthoritativeMessageSecurity,
    reconcileLinkSafety,
  };
}
