/**
 * A composer's view of its draft's lifecycle above its own mount (issue
 * #929, second review).
 *
 * Two different questions, deliberately kept apart:
 *
 *  - **pending** — a send of this draft has not been answered yet. The
 *    composer presents the draft as going out and issues nothing new.
 *  - **stale mirrors** — the authoritative draft has moved (a consumed send
 *    snapshot, an upload's terminal result, a finalized recording) and this
 *    instance's editor, upload queue and recorder have not caught up. A
 *    send stops being pending the instant the server answers; the mirrors
 *    are still behind for one commit after that, and nothing may be sent
 *    from that window.
 *
 * `canIssueSend` answers both synchronously, from the store's own refs, so
 * the send path never depends on a render having happened. The disabled
 * button is the UX; this is the invariant.
 *
 * The revision seen at mount is the baseline: a composer is seeded from the
 * authoritative draft, so it starts already reconciled and never replays a
 * change that happened before it existed.
 */

import { useCallback, useRef, useState } from "react";

import type { ConversationDraftsApi } from "./useConversationDrafts";

/** Why a composer's mirrors are being asked to converge. */
export type MirrorConvergence =
  /** The authoritative draft moved; re-read it. */
  | "changed"
  /**
   * The session ended (`clearAllDrafts`): everything these mirrors hold
   * belongs to a session that is over, and is abandoned rather than
   * re-read — there is nothing left to read.
   */
  | "cleared";

export interface DraftMirrors {
  /** A send of this draft is still awaiting an answer. */
  sendPending: boolean;
  /**
   * Nothing may go out of this draft right now: a send of it is still
   * open, or the authoritative draft has moved and this instance has not
   * re-read it yet. What the composer locks on.
   */
  blocked: boolean;
  /** Whether a send may be issued for this draft right now. Synchronous. */
  canIssueSend: () => boolean;
  /** The authoritative draft to converge on, and the acknowledgement of having done so. */
  reconcile: (
    converge: (drafts: ConversationDraftsApi, draftKey: string, reason: MirrorConvergence) => void,
  ) => void;
}

export function useDraftMirrors(
  drafts: ConversationDraftsApi,
  draftKey: string | null,
): DraftMirrors {
  const authoritative = draftKey ? (drafts.mirrorRevisions.get(draftKey) ?? 0) : 0;
  const [observed, setObserved] = useState(authoritative);
  const observedRef = useRef(observed);
  // The session this composer's mirrors belong to. A clear moves it, and
  // until this instance has abandoned what it was holding it may send
  // nothing — the draft it would send from is a session old.
  const [observedReset, setObservedReset] = useState(drafts.resetRevision);
  const observedResetRef = useRef(observedReset);

  const canIssueSend = useCallback(() => {
    if (!draftKey) return true;
    // Asked of the store, not of what this render captured: a session can
    // end between the render and this call (issue #929, sixth review).
    if (drafts.hasUnobservedReset(observedResetRef.current)) return false;
    return (
      !drafts.hasPendingSend(draftKey) &&
      !drafts.hasUnreconciledMirror(draftKey, observedRef.current)
    );
  }, [drafts, draftKey]);

  /**
   * Runs `converge` — the composer's own store → mirrors step — while this
   * instance is behind, and records that it has caught up. Called from the
   * composer's layout effect, so the mirrors are current before anything
   * can interact with the commit that announced the change.
   */
  const reconcile = useCallback(
    (
      converge: (
        drafts: ConversationDraftsApi,
        draftKey: string,
        reason: MirrorConvergence,
      ) => void,
    ) => {
      if (!draftKey) return;
      // A clear comes first and stands alone: the draft it ended is gone,
      // so there is nothing to re-read afterwards, and the revision of a
      // draft that no longer exists says nothing.
      if (drafts.hasUnobservedReset(observedResetRef.current)) {
        observedResetRef.current = drafts.getResetRevision();
        observedRef.current = drafts.getMirrorRevision(draftKey);
        converge(drafts, draftKey, "cleared");
        setObservedReset(observedResetRef.current);
        setObserved(observedRef.current);
        return;
      }
      const current = drafts.getMirrorRevision(draftKey);
      if (current === observedRef.current) return;
      observedRef.current = current;
      converge(drafts, draftKey, "changed");
      setObserved(current);
    },
    [drafts, draftKey],
  );

  const sendPending = draftKey
    ? (drafts.sendLifecycle.get(draftKey)?.pendingSends ?? 0) > 0
    : false;
  return {
    sendPending,
    blocked: sendPending || authoritative !== observed || drafts.resetRevision !== observedReset,
    canIssueSend,
    reconcile,
  };
}
