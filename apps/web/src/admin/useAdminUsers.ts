/**
 * Loading state for the admin users table (issues #425, #433).
 *
 * Two rules live here so the page does not have to restate them:
 *
 *  1. The table is filled only from a successful response. Every failure — HTTP,
 *     network, or a 200 whose envelope does not match the contract — becomes an
 *     `error` state carrying its kind, and none of them can produce a users
 *     array. That is what stopped this screen from reporting a broken
 *     deployment as "no users".
 *  2. The listing is paged. A failure while fetching a later page must not
 *     discard the rows already on screen, and no request is ever retried on its
 *     own — a spontaneous retry after a 429 spends the budget that is already
 *     refusing us.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import {
  ADMIN_USERS_PAGE_SIZE,
  type AdminErrorKind,
  type AdminUser,
  classifyAdminError,
  listAdminUsers,
} from "./adminUsersApi";

export type AdminUsersState =
  | { kind: "loading" }
  | { kind: "error"; error: AdminErrorKind }
  | { kind: "success"; users: AdminUser[] };

export interface AdminUsersQuery {
  state: AdminUsersState;
  /** Refetches from the first page. Used by retry and after a successful invite. */
  reload: () => void;
  /** Fetches and appends the next page. No-op while one is in flight. */
  loadMore: () => void;
  loadingMore: boolean;
  /** Set when fetching a later page failed; the loaded rows are still shown. */
  loadMoreError: AdminErrorKind | null;
  hasMore: boolean;
}

/**
 * Merges a page into the accumulated rows, dropping ids already present.
 *
 * The keyset cursor should not produce overlaps, but a membership changing
 * between two page fetches can shift a row across the boundary. Deduplicating
 * costs one pass and removes a whole class of "React key is not unique".
 */
function appendUnique(existing: AdminUser[], incoming: AdminUser[]): AdminUser[] {
  const seen = new Set(existing.map((u) => u.id));
  return existing.concat(incoming.filter((u) => !seen.has(u.id)));
}

export function useAdminUsers(): AdminUsersQuery {
  const [state, setState] = useState<AdminUsersState>({ kind: "loading" });
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadMoreError, setLoadMoreError] = useState<AdminErrorKind | null>(null);
  // Bumping this refetches the first page. Retry and post-invite refresh both
  // go through it, so the table is only ever filled from the canonical source.
  const [reloadToken, setReloadToken] = useState(0);
  // Guards against a second page being requested while one is in flight —
  // React state updates are async, so `loadingMore` alone can be observed stale
  // by two clicks in the same tick.
  const inFlight = useRef(false);

  useEffect(() => {
    const controller = new AbortController();

    listAdminUsers({ limit: ADMIN_USERS_PAGE_SIZE, signal: controller.signal })
      .then((page) => {
        if (controller.signal.aborted) return;
        setState({ kind: "success", users: page.users });
        setNextCursor(page.nextCursor);
        setHasMore(page.hasMore);
        setLoadMoreError(null);
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        setState({ kind: "error", error: classifyAdminError(err) });
        setNextCursor(null);
        setHasMore(false);
      });

    return () => controller.abort();
  }, [reloadToken]);

  // The loading state is set here rather than inside the effect: a synchronous
  // setState in an effect body causes a cascading render, and the initial mount
  // already starts in "loading" anyway.
  const reload = useCallback(() => {
    inFlight.current = false;
    setState({ kind: "loading" });
    setLoadMoreError(null);
    setLoadingMore(false);
    setReloadToken((n) => n + 1);
  }, []);

  const loadMore = useCallback(() => {
    if (inFlight.current || !nextCursor) return;
    inFlight.current = true;
    setLoadingMore(true);
    setLoadMoreError(null);

    listAdminUsers({ limit: ADMIN_USERS_PAGE_SIZE, cursor: nextCursor })
      .then((page) => {
        // Appending onto whatever is on screen, not onto a captured copy: the
        // rows may have been replaced by a reload while this was in flight.
        setState((current) =>
          current.kind === "success"
            ? { kind: "success", users: appendUnique(current.users, page.users) }
            : current,
        );
        setNextCursor(page.nextCursor);
        setHasMore(page.hasMore);
      })
      .catch((err: unknown) => {
        // The already-loaded rows stay. Only the "load more" affordance reports
        // the failure, and it is the operator who decides to try again.
        setLoadMoreError(classifyAdminError(err));
      })
      .finally(() => {
        inFlight.current = false;
        setLoadingMore(false);
      });
  }, [nextCursor]);

  return { state, reload, loadMore, loadingMore, loadMoreError, hasMore };
}
