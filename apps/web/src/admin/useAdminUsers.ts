/**
 * Loading state for the admin users table (issue #425).
 *
 * Three rules live here so the page does not have to restate them:
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
 *  3. Only the newest generation of requests may touch state. See below.
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

  /**
   * Which round of requests is current.
   *
   * Every reload starts a new generation, and a response may only touch state
   * if the generation it was issued under is still the current one. Without
   * this, a `loadMore` already in flight when the user hits retry — or when an
   * invite triggers a refresh — resolves *after* the new first page and appends
   * rows from the list that was just thrown away, while also overwriting
   * `nextCursor` and `hasMore` with a position that no longer exists. The
   * result is a table showing a mix of two lists and paging from the wrong
   * place.
   *
   * A generation counter rather than an `isMounted`-style flag, because the
   * problem is not unmounting: the component is very much alive, and the stale
   * response has to be distinguished from the live one. Abort alone is not
   * enough either — aborting is best-effort and a fetch that has already
   * resolved will still run its handlers.
   */
  const generationRef = useRef(0);
  const initialControllerRef = useRef<AbortController | null>(null);
  const loadMoreControllerRef = useRef<AbortController | null>(null);

  /** True when this response is still the one the UI is waiting for. */
  const isCurrent = (generation: number) => generation === generationRef.current;

  useEffect(() => {
    const generation = generationRef.current;
    const controller = new AbortController();
    initialControllerRef.current = controller;

    listAdminUsers({ limit: ADMIN_USERS_PAGE_SIZE, signal: controller.signal })
      .then((page) => {
        if (controller.signal.aborted || !isCurrent(generation)) return;
        setState({ kind: "success", users: page.users });
        setNextCursor(page.nextCursor);
        setHasMore(page.hasMore);
        setLoadMoreError(null);
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted || !isCurrent(generation)) return;
        setState({ kind: "error", error: classifyAdminError(err) });
        setNextCursor(null);
        setHasMore(false);
      });

    // Unmount (or a new generation) aborts whatever is still open.
    return () => {
      controller.abort();
      loadMoreControllerRef.current?.abort();
    };
  }, [reloadToken]);

  /**
   * Starts over from the first page.
   *
   * Incrementing the generation is what makes every response already in flight
   * irrelevant; aborting is the courtesy that stops the work early where the
   * transport supports it. The loading state is set here rather than inside the
   * effect because a synchronous setState in an effect body causes a cascading
   * render, and the initial mount already starts in "loading".
   */
  const reload = useCallback(() => {
    generationRef.current += 1;
    initialControllerRef.current?.abort();
    loadMoreControllerRef.current?.abort();
    loadMoreControllerRef.current = null;

    setState({ kind: "loading" });
    setLoadMoreError(null);
    setLoadingMore(false);
    setNextCursor(null);
    setHasMore(false);
    setReloadToken((n) => n + 1);
  }, []);

  const loadMore = useCallback(() => {
    // Nothing to fetch, or something already is. `loadingMore` is React state
    // and can be read stale by two clicks in the same tick, so the in-flight
    // controller is the authority.
    if (loadMoreControllerRef.current || !nextCursor || !hasMore) return;
    if (state.kind !== "success") return;

    const generation = generationRef.current;
    const controller = new AbortController();
    loadMoreControllerRef.current = controller;
    setLoadingMore(true);
    setLoadMoreError(null);

    listAdminUsers({ limit: ADMIN_USERS_PAGE_SIZE, cursor: nextCursor, signal: controller.signal })
      .then((page) => {
        if (controller.signal.aborted || !isCurrent(generation)) return;
        // Appending onto whatever is on screen, not onto a captured copy: the
        // rows may have changed while this was in flight.
        setState((current) =>
          current.kind === "success"
            ? { kind: "success", users: appendUnique(current.users, page.users) }
            : current,
        );
        setNextCursor(page.nextCursor);
        setHasMore(page.hasMore);
      })
      .catch((err: unknown) => {
        // A stale failure is not the user's problem: the list it belonged to is
        // gone, and showing its error would report a fault in a request nobody
        // is waiting for.
        if (controller.signal.aborted || !isCurrent(generation)) return;
        // The already-loaded rows stay. Only the "load more" affordance reports
        // the failure, and it is the operator who decides to try again.
        setLoadMoreError(classifyAdminError(err));
      })
      .finally(() => {
        // Only the request that owns the slot may release it; a stale one must
        // not clear a spinner belonging to the current generation.
        if (loadMoreControllerRef.current === controller) {
          loadMoreControllerRef.current = null;
        }
        if (isCurrent(generation)) {
          setLoadingMore(false);
        }
      });
  }, [nextCursor, hasMore, state.kind]);

  return { state, reload, loadMore, loadingMore, loadMoreError, hasMore };
}
