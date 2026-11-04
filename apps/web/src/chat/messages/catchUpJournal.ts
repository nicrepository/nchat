import { useMemo, useRef } from "react";

import type { Action } from "./types";

/**
 * Issue #947: what the server said while a catch-up read was in flight.
 *
 * Every action the conversation dispatches passes through here on its way to
 * the reducer; while a read is open, the ones that state a server fact are
 * recorded, so the read can be rebased under them (catchUpReducer.ts).
 *
 * Only facts, and only in the form that is safe to replay:
 *
 *  - events, confirmations and single-message reads carry absolute values — an
 *    edit at a version, a reaction list, a link verdict — so applying one again
 *    changes nothing;
 *  - `sent` is recorded as the message it delivered. Its other effects belong
 *    to the composer, which a catch-up has no business repeating;
 *  - `message_blocked` is a terminal fact: a read taken before the refusal
 *    still lists the message (CQ947-R2-01). The rebase keeps only the timeline
 *    of what it replays, so the refusal's send error is not raised twice;
 *  - this reader's unconfirmed intents (an optimistic edit or reaction, their
 *    reverts) are not facts and are never recorded: they live in the reducer's
 *    pending overlays, which the rebase redraws on top.
 */

/**
 * How many facts one read may be rebased under. A read that outlives this many
 * is not applied — the attempt fails and the obligation stays (see
 * useTimelineCatchUp.ts) — so the journal never grows without bound.
 */
export const catchUpJournalLimit = 500;

const recorded = new Set<Action["type"]>([
  "ws_received",
  "message_updated",
  "message_snapshot",
  "reaction_updated",
  "reaction_snapshot",
  "link_safety_changed",
  "link_updated",
  "attachment_status",
  "edit_confirmed",
  "favorite_set",
  "sent",
  "message_blocked",
]);

/** One read's journal. */
export interface Journal {
  readonly entries: Action[];
  /** True once more facts arrived than the read may be rebased under. */
  overflowed: boolean;
}

export interface CatchUpJournal {
  /** The conversation's dispatch, recording facts while a journal is open. */
  dispatch: (action: Action) => void;
  /** Starts a journal for one read, replacing any other. */
  open: () => Journal;
  /** Stops recording into `journal`, if it is still the open one. */
  close: (journal: Journal) => void;
}

/** The replayable form of a recorded fact. */
function asFact(action: Action): Action {
  return action.type === "sent" ? { type: "ws_received", message: action.message } : action;
}

function record(journal: Journal | null, action: Action): void {
  if (!journal || journal.overflowed || !recorded.has(action.type)) return;
  if (journal.entries.length >= catchUpJournalLimit) {
    journal.overflowed = true;
    journal.entries.length = 0;
    return;
  }
  journal.entries.push(asFact(action));
}

export function useCatchUpJournal(dispatch: (action: Action) => void): CatchUpJournal {
  const open = useRef<Journal | null>(null);
  return useMemo<CatchUpJournal>(
    () => ({
      dispatch(action) {
        record(open.current, action);
        dispatch(action);
      },
      open() {
        open.current = { entries: [], overflowed: false };
        return open.current;
      },
      close(journal) {
        if (open.current === journal) open.current = null;
      },
    }),
    [dispatch],
  );
}
