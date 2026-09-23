/**
 * The send lifecycle of a conversation's draft (issue #929, review finding 1).
 *
 * A send outlives the composer that started it: the reader can leave the
 * conversation and come back while the request is still open, and the
 * composer mounted then is a fresh instance with no memory of it. So the
 * question has to be answerable above the composer, per draft, from the
 * moment a send is issued until it settles.
 *
 * It answers one question — "is a send of this draft still in flight?" — so
 * a remounted composer neither posts the same snapshot again nor presents
 * it as sendable. What a settled send did to the draft is a different
 * concern, and belongs to the mirror revision (see useDraftMirrorSync): a
 * send stops being *pending* the moment the server answers, while the
 * mirrors of the composer on screen stay *unreconciled* until they have
 * re-read the draft the acknowledgement left behind.
 *
 * The pending count is React state keyed by draftKey, and changes only on
 * begin/settle — never on a keystroke or any other draft mutation. It is
 * deliberately not part of `summaries` (issue #845), which stays
 * presence-only for the sidebar and must not re-render it for a send.
 *
 * Attempts are identified, not counted: settling S1 removes S1 and only S1,
 * so a stale settlement can never release a newer attempt, and settling an
 * attempt twice is a no-op.
 */

import { useCallback, useMemo, useRef, useState } from "react";

import type { SendSnapshot } from "./draftSendSnapshot";

/** What a mounted composer reads for its draft. Absent means "no send in flight". */
export interface DraftSendLifecycle {
  /** Sends of this draft still awaiting an answer. */
  pendingSends: number;
}

/** One issued send, handed back on settlement so only this attempt is released. */
export interface SendAttempt {
  readonly id: number;
  readonly snapshot: SendSnapshot;
}

export type SendOutcome = "sent" | "unsent";

export interface DraftSendLifecycleApi {
  sendLifecycle: ReadonlyMap<string, DraftSendLifecycle>;
  /** Synchronous, for the send path's own guard — never behind a render. */
  hasPendingSend: (draftKey: string) => boolean;
  beginSend: (snapshot: SendSnapshot) => SendAttempt;
  /**
   * Releases `attempt`. On "sent", `consume(snapshot)` runs first — and
   * with it the mirror revision it announces — so the render that sees
   * this draft stop being pending already sees mirrors that are behind,
   * and the send path stays closed until they converge. A second
   * settlement of the same attempt, or one of an attempt this store never
   * issued, changes nothing.
   */
  settleSend: (attempt: SendAttempt, outcome: SendOutcome) => void;
  /** Logout / account switch: forgets every attempt. */
  clearSendLifecycle: () => void;
}

export function useDraftSendLifecycle(
  consume: (snapshot: SendSnapshot) => void,
): DraftSendLifecycleApi {
  const [sendLifecycle, setSendLifecycle] = useState<ReadonlyMap<string, DraftSendLifecycle>>(
    new Map(),
  );
  // The attempts themselves stay out of React state: their ids are the
  // store's bookkeeping, and the exposed value only needs the count.
  const pendingRef = useRef(new Map<number, SendAttempt>());
  const nextIdRef = useRef(1);

  const update = useCallback((draftKey: string, delta: number) => {
    setSendLifecycle((prev) => {
      const pendingSends = (prev.get(draftKey)?.pendingSends ?? 0) + delta;
      const next = new Map(prev);
      // A draft with nothing in flight carries no entry at all, so the map
      // does not grow with every conversation sent to during a session.
      if (pendingSends > 0) next.set(draftKey, { pendingSends });
      else next.delete(draftKey);
      return next;
    });
  }, []);

  const hasPendingSend = useCallback((draftKey: string) => {
    for (const attempt of pendingRef.current.values()) {
      if (attempt.snapshot.draftKey === draftKey) return true;
    }
    return false;
  }, []);

  const beginSend = useCallback(
    (snapshot: SendSnapshot): SendAttempt => {
      const attempt: SendAttempt = { id: nextIdRef.current++, snapshot };
      pendingRef.current.set(attempt.id, attempt);
      update(snapshot.draftKey, +1);
      return attempt;
    },
    [update],
  );

  const settleSend = useCallback(
    (attempt: SendAttempt, outcome: SendOutcome) => {
      if (!pendingRef.current.delete(attempt.id)) return;
      // The draft is consumed — and its mirror revision announced — before
      // the lifecycle says the send is over: whoever re-renders on the
      // update below already reads the consumed draft, and the composer
      // still showing the old one knows it is behind.
      if (outcome === "sent") consume(attempt.snapshot);
      update(attempt.snapshot.draftKey, -1);
    },
    [consume, update],
  );

  const clearSendLifecycle = useCallback(() => {
    pendingRef.current.clear();
    setSendLifecycle((prev) => (prev.size === 0 ? prev : new Map()));
  }, []);

  return useMemo(
    () => ({ sendLifecycle, hasPendingSend, beginSend, settleSend, clearSendLifecycle }),
    [sendLifecycle, hasPendingSend, beginSend, settleSend, clearSendLifecycle],
  );
}
