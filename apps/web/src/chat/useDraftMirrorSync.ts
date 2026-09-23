/**
 * Mirror synchronisation for a conversation's draft (issue #929, second
 * review).
 *
 * The draft store is the authority over unsent content, but the composer
 * keeps operational mirrors of it — the TipTap document, the upload queue,
 * the recorder — and those are seeded once, at mount. Anything that changes
 * the authoritative draft from *outside* the mounted composer therefore has
 * to be announced, or the mirrors quietly diverge: an acknowledgement of a
 * send issued by a previous instance, an upload that finishes after the
 * reader navigated away and back, a recording finalized after its recorder
 * was replaced.
 *
 * One monotonic revision per draftKey answers that. It is deliberately
 * *sparse*: it moves only on authoritative changes a mounted mirror must
 * re-read — a consumed send snapshot, an upload's terminal result, a
 * finalized recording — never on a keystroke, an upload progress report or
 * any other mutation the mirror itself performed. The draft's content stays
 * where it was (a Map in a ref); only this counter is React state, so the
 * sidebar and the editor keep their per-keystroke performance.
 *
 * It is also readable synchronously. A composer's send path asks
 * `hasUnreconciledMirror(key, observed)` before issuing anything: between
 * the acknowledgement and the commit where the mirrors converge there is a
 * moment in which nothing is pending any more and the mirrors still hold
 * what the send carried, and no send may be issued from it.
 */

import { useCallback, useMemo, useRef, useState } from "react";

export interface DraftMirrorSyncApi {
  /** Revisions by draftKey; the value a mounted composer renders against. */
  mirrorRevisions: ReadonlyMap<string, number>;
  /** The authoritative revision, readable without waiting for a render. */
  getMirrorRevision: (draftKey: string) => number;
  /** Whether `observed` is behind the authoritative revision of this draft. */
  hasUnreconciledMirror: (draftKey: string, observed: number) => boolean;
  /** Announces an authoritative change that mounted mirrors must re-read. */
  notifyMirrors: (draftKey: string) => void;
  /** Logout / account switch: forgets every revision. */
  clearMirrorSync: () => void;
}

export function useDraftMirrorSync(): DraftMirrorSyncApi {
  // The ref is the authority and is written first, so a guard asking about
  // a change never has to wait for the render the state update schedules;
  // the state carries the same map, for the composers that render from it.
  const [mirrorRevisions, setMirrorRevisions] = useState<ReadonlyMap<string, number>>(
    () => new Map(),
  );
  const revisionsRef = useRef<ReadonlyMap<string, number>>(mirrorRevisions);

  const getMirrorRevision = useCallback((draftKey: string) => {
    return revisionsRef.current.get(draftKey) ?? 0;
  }, []);

  const hasUnreconciledMirror = useCallback(
    (draftKey: string, observed: number) => (revisionsRef.current.get(draftKey) ?? 0) !== observed,
    [],
  );

  const notifyMirrors = useCallback((draftKey: string) => {
    const next = new Map(revisionsRef.current);
    next.set(draftKey, (next.get(draftKey) ?? 0) + 1);
    revisionsRef.current = next;
    setMirrorRevisions(next);
  }, []);

  const clearMirrorSync = useCallback(() => {
    if (revisionsRef.current.size === 0) return;
    const empty: ReadonlyMap<string, number> = new Map();
    revisionsRef.current = empty;
    setMirrorRevisions(empty);
  }, []);

  return useMemo(
    () => ({
      mirrorRevisions,
      getMirrorRevision,
      hasUnreconciledMirror,
      notifyMirrors,
      clearMirrorSync,
    }),
    [mirrorRevisions, getMirrorRevision, hasUnreconciledMirror, notifyMirrors, clearMirrorSync],
  );
}
