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

/**
 * @param sessionScopeKey identifies whose data the table is showing. Any change
 * invalidates everything in flight and starts over; `null` means there is no
 * session, so nothing is fetched and nothing is shown. The hook never inspects
 * the value — it only compares it — so the caller may make it as precise as the
 * identifiers it actually has.
 */
/** Everything a response owns, tagged with the scope it was fetched for. */
interface ScopedData {
  scope: string | null;
  state: AdminUsersState;
  nextCursor: string | null;
  hasMore: boolean;
  loadingMore: boolean;
  loadMoreError: AdminErrorKind | null;
}

function emptyFor(scope: string | null): ScopedData {
  return {
    scope,
    state: { kind: "loading" },
    nextCursor: null,
    hasMore: false,
    loadingMore: false,
    loadMoreError: null,
  };
}

export function useAdminUsers(sessionScopeKey: string | null): AdminUsersQuery {
  // The data carries the scope it belongs to, so a scope change empties the
  // table by derivation during render rather than by a reset that lands a frame
  // later. Nothing is mutated to achieve it — the stored rows simply stop being
  // the rows this scope is asking for.
  const [data, setData] = useState<ScopedData>(() => emptyFor(sessionScopeKey));
  const current = data.scope === sessionScopeKey ? data : emptyFor(sessionScopeKey);
  const { state, nextCursor, hasMore, loadingMore, loadMoreError } = current;
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

  /**
   * True when this response still belongs to the round the UI is waiting for.
   *
   * The generation catches a reload; the scope is enforced by the state write
   * itself, which refuses to touch data belonging to a different scope.
   */
  const isCurrent = (generation: number) => generation === generationRef.current;

  /**
   * Applies an update only if the stored data still belongs to `scope`.
   *
   * This is the second half of staleness: a response issued for one session
   * must not write into another's table even if it arrives while the
   * generation happens to line up.
   */
  const applyScoped = (scope: string | null, update: (prev: ScopedData) => ScopedData) => {
    setData((prev) => {
      const base = prev.scope === scope ? prev : emptyFor(scope);
      return update(base);
    });
  };

  /**
   * Abandon everything in flight. Incrementing the generation is what makes
   * responses already on their way irrelevant; aborting is the courtesy that
   * stops the work early where the transport supports it.
   */
  const invalidate = () => {
    generationRef.current += 1;
    initialControllerRef.current?.abort();
    loadMoreControllerRef.current?.abort();
    loadMoreControllerRef.current = null;
  };

  useEffect(() => {
    // No session, nothing to ask for. A request here would only earn a 401.
    if (sessionScopeKey === null) return;

    const generation = generationRef.current;
    const scope = sessionScopeKey;
    const controller = new AbortController();
    initialControllerRef.current = controller;

    listAdminUsers({ limit: ADMIN_USERS_PAGE_SIZE, signal: controller.signal })
      .then((page) => {
        if (controller.signal.aborted || !isCurrent(generation)) return;
        applyScoped(scope, () => ({
          scope,
          state: { kind: "success", users: page.users },
          nextCursor: page.nextCursor,
          hasMore: page.hasMore,
          loadingMore: false,
          loadMoreError: null,
        }));
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted || !isCurrent(generation)) return;
        applyScoped(scope, () => ({
          ...emptyFor(scope),
          state: { kind: "error", error: classifyAdminError(err) },
        }));
      });

    // Unmount, a reload, or a scope change aborts whatever is still open. The
    // generation bump is what stops an already-resolved response from applying.
    return () => {
      generationRef.current += 1;
      controller.abort();
      loadMoreControllerRef.current?.abort();
    };
  }, [reloadToken, sessionScopeKey]);

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
    invalidate();
    setData((prev) => emptyFor(prev.scope));
    setReloadToken((n) => n + 1);
  }, []);

  const loadMore = useCallback(() => {
    // Nothing to fetch, or something already is. `loadingMore` is React state
    // and can be read stale by two clicks in the same tick, so the in-flight
    // controller is the authority.
    if (loadMoreControllerRef.current || !nextCursor || !hasMore) return;
    if (state.kind !== "success") return;

    const generation = generationRef.current;
    const scope = sessionScopeKey;
    const controller = new AbortController();
    loadMoreControllerRef.current = controller;
    applyScoped(scope, (prev) => ({ ...prev, loadingMore: true, loadMoreError: null }));

    listAdminUsers({ limit: ADMIN_USERS_PAGE_SIZE, cursor: nextCursor, signal: controller.signal })
      .then((page) => {
        if (controller.signal.aborted || !isCurrent(generation)) return;
        // Appending onto whatever is on screen, not onto a captured copy: the
        // rows may have changed while this was in flight. applyScoped refuses
        // to append onto another scope's list.
        setData((prev) => {
          if (prev.scope !== scope || prev.state.kind !== "success") return prev;
          return {
            ...prev,
            state: { kind: "success", users: appendUnique(prev.state.users, page.users) },
            nextCursor: page.nextCursor,
            hasMore: page.hasMore,
          };
        });
      })
      .catch((err: unknown) => {
        // A stale failure is not the user's problem: the list it belonged to is
        // gone, and showing its error would report a fault in a request nobody
        // is waiting for.
        if (controller.signal.aborted || !isCurrent(generation)) return;
        // The already-loaded rows stay. Only the "load more" affordance reports
        // the failure, and it is the operator who decides to try again.
        setData((prev) =>
          prev.scope === scope ? { ...prev, loadMoreError: classifyAdminError(err) } : prev,
        );
      })
      .finally(() => {
        // Only the request that owns the slot may release it; a stale one must
        // not clear a spinner belonging to the current generation.
        if (loadMoreControllerRef.current === controller) {
          loadMoreControllerRef.current = null;
        }
        if (!isCurrent(generation)) return;
        setData((prev) => (prev.scope === scope ? { ...prev, loadingMore: false } : prev));
      });
  }, [nextCursor, hasMore, state.kind, sessionScopeKey]);

  return { state, reload, loadMore, loadingMore, loadMoreError, hasMore };
}
