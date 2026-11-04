import { useCallback, useEffect, useRef } from "react";

import type { Message } from "../chatTypes";
import type { CatchUpJournal } from "./catchUpJournal";
import type { MessagesGateway } from "./messagesGateway";
import { compareTimelinePositions } from "./messageOrder";
import type { ConversationScope } from "./useConversationScope";
import { realtimeFallbackErrorMessage } from "./useAuthoritativeReads";
import { isAbortError, type RequestRegistry } from "./useRequestRegistry";
import type { Action, MessagesStatus } from "./types";

/**
 * How many pages of *new* messages a catch-up reads before it stops looking for
 * the timeline it holds. A longer gap is not stitched with a hole in it: the
 * timeline is replaced by what was read, and older history is paged in from the
 * server on demand — so nothing drawn is ever older than the database.
 */
export const catchUpMaxPages = 5;

/**
 * The waits before each new attempt while a reconciliation is still owed. The
 * last one repeats: the obligation is not dropped when the quick retries are
 * spent (CQ947-02), it is pursued at most once a minute for as long as the
 * conversation is on screen, and any resubscription retries it at once.
 */
export const catchUpRetryDelaysMs = [1_000, 4_000, 15_000, 30_000, 60_000] as const;

function retryDelay(attempt: number): number {
  return catchUpRetryDelaysMs[Math.min(attempt, catchUpRetryDelaysMs.length - 1)];
}

const catchUpKey = "timeline-catch-up";

interface TimelineRead {
  /** Every message read, oldest first. */
  messages: Message[];
  nextCursor: string;
  /** True when the read must replace the timeline rather than be stitched onto it. */
  replace: boolean;
}

/** True once a page reaches the oldest message the timeline holds. */
function reachesOldest(page: Message[], oldest: Message | undefined): boolean {
  const first = page[0];
  return (
    first !== undefined && oldest !== undefined && compareTimelinePositions(first, oldest) <= 0
  );
}

/**
 * Reads the conversation back from the newest page until it covers every
 * message the timeline holds.
 *
 * Two bounds, for two different things. New messages are bounded by
 * catchUpMaxPages: a gap longer than that replaces the timeline. The held part
 * is bounded by the timeline itself — one page per held message at worst, even
 * if the server answers with short pages — so an edit, a deletion or a reaction
 * anywhere in what the reader has loaded converges, not only in a recent window.
 */
async function readTimeline(
  gateway: MessagesGateway,
  held: Message[],
  signal: AbortSignal,
): Promise<TimelineRead> {
  const heldIds = new Set(held.map(({ id }) => id));
  const pages: Message[][] = [];
  let cursor: string | undefined;
  let met = false;
  for (let read = 0; read < catchUpMaxPages + held.length; read += 1) {
    const page = await gateway.fetchPage(cursor, signal);
    pages.unshift(page.messages);
    cursor = page.nextCursor;
    met ||= page.messages.some(({ id }) => heldIds.has(id));
    const done = !cursor || page.messages.length === 0 || reachesOldest(page.messages, held[0]);
    if (done || (!met && read + 1 >= catchUpMaxPages)) {
      // Replaced only when the page bound stopped the walk. A history that
      // simply ended is stitched: a held message absent from it — the reader's
      // own pending_link_scan message, which no read path returns — stays.
      return { messages: pages.flat(), nextCursor: cursor, replace: !done };
    }
  }
  return { messages: pages.flat(), nextCursor: cursor ?? "", replace: true };
}

/**
 * Resolves true after `ms`, or false as soon as `signal` aborts. Either way the
 * other one is released: the signal outlives every wait of an obligation, and
 * `once` only drops a listener that fired (CQ947-R2-03).
 */
function pause(ms: number, signal: AbortSignal): Promise<boolean> {
  return new Promise((resolve) => {
    const abort = () => {
      window.clearTimeout(timer);
      resolve(false);
    };
    const timer = window.setTimeout(() => {
      signal.removeEventListener("abort", abort);
      resolve(true);
    }, ms);
    signal.addEventListener("abort", abort, { once: true });
  });
}

function isRemovedMessage(message: Message): boolean {
  return message.isRemoved || message.status === "deleted";
}

interface Options {
  scope: ConversationScope;
  gateway: MessagesGateway;
  journal: CatchUpJournal;
  fallbacks: RequestRegistry;
  status: MessagesStatus;
  notifyRemoved: () => void;
}

/**
 * How one attempt ended. Only "applied" settles the obligation: a failed read,
 * and a read that cannot be applied because too much happened while it was in
 * flight, both leave it owed. "stale" means the attempt no longer matters — the
 * conversation changed, the view went away, or a newer attempt took over.
 */
type Outcome = "applied" | "rejected" | "failed" | "stale";

/**
 * Issue #947: brings a loaded timeline back in line with the database after the
 * socket may have missed events — a reconnect, the server's
 * conversation.sync_required, or the first subscription of a conversation.
 *
 * Persistence is the source of truth, so nothing is replayed from the wire: the
 * timeline is read back through the same authorized listing the first page came
 * from, and handed to the reducer with the journal of what the server said
 * meanwhile (catchUpReducer.ts).
 *
 * Asking for a catch-up creates an obligation, and only an applied read settles
 * it. Attempts are one at a time — a new request aborts the one in flight and
 * starts over — and a failed or rejected attempt waits out a backoff that ends at
 * once a minute, for as long as the conversation stays on screen. The failure is
 * reported until a read is applied; no other event clears it.
 *
 * A subscription acknowledged before the first page has landed owes a
 * catch-up: something persisted after the database answered that page and
 * before the subscription existed is in neither. It runs once that page is
 * committed — never against the previous conversation's state, which is what
 * the render that moves the route still carries.
 */
export function useTimelineCatchUp({
  scope,
  gateway,
  journal,
  fallbacks,
  status,
  notifyRemoved,
}: Options): () => void {
  const owed = useRef<string | null>(null);

  const apply = useCallback(
    (read: TimelineRead, entries: Action[]) => {
      const drawn = new Map(scope.messages().map((message) => [message.id, message]));
      for (const message of read.messages.filter(isRemovedMessage)) {
        scope.rememberDeleted(message.id, message.deletedAt ?? message.updatedAt);
      }
      const page = { messages: read.messages.map(scope.sanitize), nextCursor: read.nextCursor };
      journal.dispatch({ type: "caught_up", page, replace: read.replace, journal: entries });
      const withdrawn = read.messages.some(
        (message) => isRemovedMessage(message) && drawn.get(message.id)?.isRemoved === false,
      );
      if (withdrawn) notifyRemoved();
    },
    [journal, notifyRemoved, scope],
  );

  const attempt = useCallback(
    async (signal: AbortSignal, current: () => boolean): Promise<Outcome> => {
      const recording = journal.open();
      try {
        const read = await readTimeline(gateway, scope.messages(), signal);
        if (!current()) return "stale";
        if (recording.overflowed) return "rejected";
        apply(read, recording.entries);
        return "applied";
      } catch (error) {
        return !current() || isAbortError(error) ? "stale" : "failed";
      } finally {
        journal.close(recording);
      }
    },
    [apply, gateway, journal, scope],
  );

  const run = useCallback(() => {
    const controller = fallbacks.start(catchUpKey);
    const loadKey = scope.key;
    const current = () => !controller.signal.aborted && scope.isCurrent(loadKey);

    const pursue = async () => {
      for (let tries = 0; ; tries += 1) {
        const outcome = await attempt(controller.signal, current);
        if (outcome === "applied" || outcome === "stale") return;
        if (outcome === "failed") {
          journal.dispatch({ type: "catch_up_failed", error: realtimeFallbackErrorMessage });
        }
        if (!(await pause(retryDelay(tries), controller.signal))) return;
      }
    };
    void pursue().finally(() => fallbacks.finish(catchUpKey, controller));
  }, [attempt, fallbacks, journal, scope]);

  // The catch-up a subscription owed while the first page was loading, run
  // once that page is committed and the scope's mirror holds it.
  useEffect(() => {
    if (status !== "ready" || owed.current !== scope.key || !scope.isLoaded(scope.key)) return;
    owed.current = null;
    run();
  }, [run, scope, status]);

  return useCallback(() => {
    if (scope.isLoaded(scope.key)) {
      run();
      return;
    }
    owed.current = scope.key;
  }, [run, scope]);
}
