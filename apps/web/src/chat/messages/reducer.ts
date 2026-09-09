/**
 * The conversation's state machine.
 *
 * Every action belongs to exactly one subject — history, composing, link
 * safety, realtime delivery, editing, reactions, or the reader's own selection —
 * so the reducer asks each in turn and the first one that recognises the action
 * answers. A sub-reducer returns undefined for an action that is not its
 * business, which is what keeps the groups disjoint and this function a
 * dispatcher rather than a second copy of every switch.
 */

import { reduceComposer } from "./composerReducer";
import { reduceConversation } from "./conversationReducer";
import { reduceEditing } from "./editingReducer";
import { reduceHistory } from "./historyReducer";
import { reduceLinkSafety } from "./linkSafetyReducer";
import { reduceReactions } from "./reactionsReducer";
import { reduceRealtime } from "./realtimeReducer";
import { reduceReferences } from "./referencesReducer";
import { reduceSecuritySnapshots } from "./securitySnapshotReducer";
import type { Action, MessagesState } from "./types";

/**
 * RF-21 verdicts about links, and the authoritative reads that reconcile them.
 * One subject, three deliveries: an event, a page of snapshots, a page of
 * reference previews.
 */
function reduceLinkSafetyGroup(state: MessagesState, action: Action): MessagesState | undefined {
  return (
    reduceLinkSafety(state, action) ??
    reduceSecuritySnapshots(state, action) ??
    reduceReferences(state, action)
  );
}

export function reducer(state: MessagesState, action: Action): MessagesState {
  return (
    reduceHistory(state, action) ??
    reduceComposer(state, action) ??
    reduceLinkSafetyGroup(state, action) ??
    reduceRealtime(state, action) ??
    reduceEditing(state, action) ??
    reduceReactions(state, action) ??
    reduceConversation(state, action) ??
    state
  );
}
