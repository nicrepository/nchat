/**
 * The session boundary of the draft store (issue #929, third and fifth
 * reviews).
 *
 * `clearAllDrafts` — logout, account switch, a token refresh that renews the
 * session — ends everything the store was holding. Two different things have
 * to notice that, and they notice it differently:
 *
 *  - **an operation already under way** asks, synchronously, whether it still
 *    belongs to the session it started in. That is the *generation*: a ref,
 *    monotonic, read by an upload's callbacks and by a recording's, and never
 *    a reason to render. A request that answers for a generation that has
 *    passed writes nothing and cleans up after itself.
 *  - **a composer still on screen** has mirrors of its own — a TipTap
 *    document, an upload queue, a recorder — holding content of the session
 *    that just ended. Nothing in those is an operation yet, so no generation
 *    check can reach them: they have to be *told*. That is the *reset
 *    revision*, React state, bumped only here and only by a clear.
 *
 * The distinction matters. Without the reset, a file queued before the clear
 * would start uploading afterwards and legitimately capture the *new*
 * generation; text left in the editor would be sent as though it had been
 * written in the new session. The generation cannot see either, because
 * neither had started anything.
 *
 * Store-wide, not per draftKey: a clear ends every conversation at once, so a
 * single revision says all there is to say, and a composer of any
 * conversation reads the same one.
 */

import { useCallback, useMemo, useRef, useState } from "react";

/**
 * A generation of the draft store. Monotonic, never reused, never persisted:
 * it only says which session an in-flight operation belongs to.
 */
export type DraftGeneration = number;

export interface DraftBoundaryApi {
  /** The generation an operation starting now belongs to. Synchronous. */
  captureGeneration: () => DraftGeneration;
  /** Whether `generation` is still the one the store is in. Synchronous. */
  isGenerationCurrent: (generation: DraftGeneration) => boolean;
  /**
   * Bumped once per `clearAllDrafts`. A mounted composer compares it with
   * the value it last acted on and, when it moved, abandons everything its
   * mirrors were holding for the session that ended.
   *
   * This is the *reactive* half — what a render and a layout effect depend
   * on. A guard asking "may this go out?" must not read it: a render-time
   * value is one commit old the moment the session ends, which is exactly
   * the turn in which something may still try to send. `hasUnobservedReset`
   * is the authority for that.
   */
  resetRevision: number;
  /** The reset revision as it is right now, whatever has been rendered. */
  getResetRevision: () => number;
  /** Whether a session has ended since `observed` — read synchronously. */
  hasUnobservedReset: (observed: number) => boolean;
  /** Ends the current session: a new generation, and a reset for the mirrors. */
  endSession: () => void;
}

export function useDraftBoundary(): DraftBoundaryApi {
  const generationRef = useRef<DraftGeneration>(1);
  // Both halves of the reset: the ref every guard reads, and the state that
  // makes the mirrors converge. They move together, the ref first.
  const resetRef = useRef(0);
  const [resetRevision, setResetRevision] = useState(0);

  const captureGeneration = useCallback(() => generationRef.current, []);
  const isGenerationCurrent = useCallback(
    (generation: DraftGeneration) => generation === generationRef.current,
    [],
  );
  const getResetRevision = useCallback(() => resetRef.current, []);
  const hasUnobservedReset = useCallback((observed: number) => observed !== resetRef.current, []);

  const endSession = useCallback(() => {
    // The refs first and synchronously: from this statement on, every
    // callback of the session being ended is invalid and nothing may be
    // sent from what it left behind — whatever order the rest of the
    // teardown runs in, and whenever React gets around to rendering.
    generationRef.current += 1;
    resetRef.current += 1;
    setResetRevision(resetRef.current);
  }, []);

  return useMemo(
    () => ({
      captureGeneration,
      isGenerationCurrent,
      resetRevision,
      getResetRevision,
      hasUnobservedReset,
      endSession,
    }),
    [
      captureGeneration,
      isGenerationCurrent,
      resetRevision,
      getResetRevision,
      hasUnobservedReset,
      endSession,
    ],
  );
}
