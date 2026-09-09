/**
 * Cross-target reference previews, refreshed from the authorized batch endpoint.
 *
 * A refresh is a reconciliation, not a delivery: it can answer with a preview
 * older than the one already drawn, so what it returns is only accepted when it
 * is not a step backwards.
 */

import type { Message } from "../chatTypes";
import { isOlderSecurityVersion } from "./linkSafetyCorrections";
import type { Action, ActionOf, MessagesState } from "./types";

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

/** Reference previews read back from the server. */
export function reduceReferences(state: MessagesState, action: Action): MessagesState | undefined {
  return action.type === "references_refreshed"
    ? applyReferencesRefreshed(state, action)
    : undefined;
}
