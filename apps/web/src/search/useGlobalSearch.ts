import { useCallback, useEffect, useReducer, useRef, useState } from "react";

import { classifySearchError, searchCategory } from "./searchApi";
import {
  SEARCH_CATEGORIES,
  type SearchCategory,
  type SearchErrorKind,
  type SearchResultByCategory,
  type SearchResultPage,
  type SearchTab,
} from "./searchTypes";

export const SEARCH_DEBOUNCE_MS = 300;
export const PAGE_LIMIT = 20;
/** How many results each section of "Tudo" shows. */
export const OVERVIEW_LIMIT = 5;

/**
 * Each category is loaded twice over, into separate slots: a small top-N for
 * the "Tudo" overview and a paginated list for its own tab. They answer
 * different requests (limit 5 vs 20 plus cursors), so neither is derived from
 * the other.
 */
export type SearchScope = "overview" | "tabs";

export interface ListState<T> {
  pages: number;
  restorePages: number;
  status: "idle" | "loading" | "ready" | "error";
  items: T[];
  cursor: string | null;
  hasMore: boolean;
  loadingMore: boolean;
  errorKind: SearchErrorKind | null;
  loadMoreError: SearchErrorKind | null;
}

export type SearchLists = { [C in SearchCategory]: ListState<SearchResultByCategory[C]> };

export interface GlobalSearchState {
  revalidationsLeft: number;
  query: string;
  activeQuery: string;
  activeTab: SearchTab;
  /**
   * The debounce window: the field no longer says what activeQuery says, and
   * the new query has not been committed yet. The old query's results were
   * dropped the moment this became true — they are not the answer to what the
   * field shows — and nothing is requested until the commit.
   */
  typing: boolean;
  /**
   * Bumped on every committed query and whenever the field leaves the
   * committed one. Every response carries the generation it
   * was requested under and is dropped when that is no longer current, so a
   * late answer for an old query can never land on the new one — aborting is
   * the fast path, this is the guarantee.
   */
  generation: number;
  overview: SearchLists;
  tabs: SearchLists;
}

export interface RestoredSearch {
  query: string;
  tab: SearchTab;
}

interface Slot {
  scope: SearchScope;
  category: SearchCategory;
  generation: number;
}

type Action =
  | { type: "SET_QUERY"; query: string }
  | { type: "COMMIT_QUERY"; query: string }
  | { type: "SET_ACTIVE_TAB"; tab: SearchTab }
  | ({ type: "FETCH_START" | "MORE_START" } & Slot)
  | ({
      type: "FETCH_SUCCESS" | "MORE_SUCCESS";
      page: SearchResultPage<{ id: string }>;
      pages?: number;
    } & Slot)
  | ({ type: "FETCH_ERROR" | "MORE_ERROR"; errorKind: SearchErrorKind } & Slot)
  | { type: "RETRY"; scope: SearchScope; category: SearchCategory }
  | { type: "CANCEL_RECOVERY"; abortAutomatic?: boolean };

function idleList<T>(): ListState<T> {
  return {
    pages: 0,
    restorePages: 0,
    status: "idle",
    items: [],
    cursor: null,
    hasMore: false,
    loadingMore: false,
    errorKind: null,
    loadMoreError: null,
  };
}

function idleLists(): SearchLists {
  return {
    messages: idleList(),
    users: idleList(),
    channels: idleList(),
    groups: idleList(),
    files: idleList(),
    links: idleList(),
  };
}

function initialState(restored: RestoredSearch | undefined): GlobalSearchState {
  const query = restored?.query.trim() ?? "";
  return {
    revalidationsLeft: 1,
    query,
    activeQuery: query,
    activeTab: restored?.tab ?? "all",
    typing: false,
    generation: 0,
    overview: idleLists(),
    tabs: idleLists(),
  };
}

type AnyList = ListState<{ id: string }>;

function patchSlot(
  state: GlobalSearchState,
  slot: Omit<Slot, "generation"> & { generation?: number },
  update: (list: AnyList) => AnyList,
): GlobalSearchState {
  if (slot.generation !== undefined && slot.generation !== state.generation) return state;
  const lists = state[slot.scope] as unknown as Record<SearchCategory, AnyList>;
  return {
    ...state,
    [slot.scope]: { ...lists, [slot.category]: update(lists[slot.category]) },
  };
}

/** Appends a page, skipping anything an earlier page already showed. */
function appendPage(list: AnyList, page: SearchResultPage<{ id: string }>, pages = 1): AnyList {
  const seen = new Set(list.items.map((item) => item.id));
  return {
    ...list,
    pages: list.pages + pages,
    loadingMore: false,
    items: [...list.items, ...page.items.filter((item) => !seen.has(item.id))],
    cursor: page.nextCursor,
    hasMore: page.hasMore,
    loadMoreError: null,
  };
}

/** A cursor is never valid across queries, so every slot starts over. */
function invalidated(state: GlobalSearchState): GlobalSearchState {
  return { ...state, generation: state.generation + 1, overview: idleLists(), tabs: idleLists() };
}

/**
 * The session's identity is in doubt (CQ-10): the query and tab are the
 * reader's and stay. Only page counts survive for a bounded, authorized rebuild;
 * old results and cursors, including anything still answering, are discarded.
 */
function searchReducer(
  state: GlobalSearchState,
  action: Action | { type: "SUSPEND" },
): GlobalSearchState {
  if (action.type !== "SUSPEND") return reducer(state, action);
  const next = invalidated(state);
  next.revalidationsLeft = Math.max(0, state.revalidationsLeft - 1);
  for (const scope of ["overview", "tabs"] as const) {
    for (const category of SEARCH_CATEGORIES) {
      next[scope][category].restorePages = Math.max(
        state[scope][category].pages,
        state[scope][category].status === "ready" && !state[scope][category].hasMore
          ? 0
          : state[scope][category].restorePages,
      );
      // The HTTP operation may have been aborted before reporting its final
      // 401. A second interruption must not create another equivalent operation.
      if (state.revalidationsLeft === 0) {
        next[scope][category].status = "error";
        next[scope][category].errorKind = "unknown";
      }
    }
  }
  return next;
}

function reducer(state: GlobalSearchState, action: Action): GlobalSearchState {
  if ("generation" in action && action.generation !== state.generation) return state;
  switch (action.type) {
    case "SET_QUERY": {
      const typing = action.query.trim() !== state.activeQuery;
      // Entering the window invalidates everything the old query produced;
      // staying in it (another keystroke) changes nothing else. Leaving it by
      // typing back to the committed query searches that query again.
      if (typing === state.typing) return { ...state, revalidationsLeft: 1, query: action.query };
      return { ...invalidated(state), revalidationsLeft: 1, query: action.query, typing };
    }
    case "COMMIT_QUERY":
      return { ...invalidated(state), activeQuery: action.query, typing: false };
    case "SET_ACTIVE_TAB":
      return { ...state, revalidationsLeft: 1, activeTab: action.tab };
    case "CANCEL_RECOVERY": {
      let next = state;
      for (const scope of ["overview", "tabs"] as const) {
        for (const category of SEARCH_CATEGORIES) {
          next = patchSlot(next, { scope, category }, (list) => ({
            ...list,
            restorePages: 0,
            status:
              action.abortAutomatic && list.restorePages > list.pages && list.status === "loading"
                ? "idle"
                : list.status,
            loadingMore: list.restorePages > list.pages ? false : list.loadingMore,
          }));
        }
      }
      return next;
    }
    case "FETCH_START":
      return patchSlot(state, action, (list) => ({ ...list, status: "loading", errorKind: null }));
    case "FETCH_SUCCESS":
      return completed(
        patchSlot(state, action, (list) => ({
          ...list,
          status: "ready",
          pages: action.pages ?? 1,
          restorePages:
            list.restorePages > 2 || list.restorePages > (action.pages ?? 1)
              ? list.restorePages
              : 0,
          items: action.page.items,
          cursor: action.page.nextCursor,
          hasMore: action.page.hasMore,
        })),
      );
    case "FETCH_ERROR":
      return patchSlot(state, action, (list) => ({
        ...list,
        status: "error",
        errorKind: action.errorKind,
      }));
    case "MORE_START":
      return patchSlot({ ...state, revalidationsLeft: 1 }, action, (list) => ({
        ...list,
        loadingMore: true,
        loadMoreError: null,
      }));
    case "MORE_SUCCESS":
      return completed(
        patchSlot(state, action, (list) => appendPage(list, action.page, action.pages)),
      );
    case "MORE_ERROR":
      // Items and cursor stay: a failed "load more" never erases what is shown.
      return patchSlot(state, action, (list) => ({
        ...list,
        loadingMore: false,
        loadMoreError: action.errorKind,
      }));
    case "RETRY":
      return patchSlot({ ...state, revalidationsLeft: 1 }, action, (list) => ({
        ...idleList(),
        restorePages: Math.max(list.pages, list.restorePages),
      }));
  }
}

/** The slots the current view needs; "Tudo" needs every category's overview. */
function slotsInView(tab: SearchTab): Array<[SearchScope, SearchCategory]> {
  return tab === "all"
    ? SEARCH_CATEGORIES.map((category) => ["overview", category])
    : [["tabs", tab]];
}

/** One healthy category must not renew the budget while another keeps rotating. */
function completed(state: GlobalSearchState): GlobalSearchState {
  const ready = slotsInView(state.activeTab).every(([scope, category]) => {
    const list = state[scope][category];
    return list.status === "ready" && !list.loadingMore;
  });
  return ready ? { ...state, revalidationsLeft: 1 } : state;
}

export interface UseGlobalSearchResult {
  state: GlobalSearchState;
  setQuery: (query: string) => void;
  setActiveTab: (tab: SearchTab) => void;
  loadMore: (category: SearchCategory) => void;
  retry: (scope: SearchScope, category: SearchCategory) => void;
  recover: (category: SearchCategory) => void;
  cancelRecovery: () => void;
}

/**
 * Orchestrates the global search page: debounced query commit, lazy fetch of
 * exactly the slots the visible view needs (only once per committed query),
 * abort of superseded requests, and cursor pagination per category.
 *
 * Switching tabs keeps ordinary requests for the same query, but cancels an
 * unfinished session recovery together with its reading intention.
 *
 * Not `enabled` — the session's identity in doubt (CQ-10) — nothing is
 * requested, and turning disabled drops every result and every request in
 * flight: once enabled again, visible slots rebuild at most two pages with
 * fresh cursors. Explicit recovery adds at most five per action; hidden slots stay lazy.
 */
export function useGlobalSearch(restored?: RestoredSearch, enabled = true): UseGlobalSearchResult {
  const [state, dispatch] = useReducer(searchReducer, restored, initialState);
  // Dropped in the render that disables, not after it: no late answer is
  // accepted in between (its generation is already gone).
  const [suspended, setSuspended] = useState(!enabled);
  if (suspended === enabled) {
    setSuspended(!enabled);
    if (!enabled) dispatch({ type: "SUSPEND" });
  }
  const stateRef = useRef(state);
  const controllersRef = useRef(
    new Map<string, { controller: AbortController; pending: boolean }>(),
  );

  useEffect(() => {
    stateRef.current = state;
  });

  const abortAll = useCallback(() => {
    controllersRef.current.forEach(({ controller }) => controller.abort());
    controllersRef.current.clear();
  }, []);

  const run = useCallback(
    (scope: SearchScope, category: SearchCategory, cursor: string | null, batchSize = 1) => {
      const { activeQuery: query, generation } = stateRef.current;
      const key = `${scope}:${category}`;
      if (cursor !== null && controllersRef.current.get(key)?.pending) return;
      controllersRef.current.get(key)?.controller.abort();
      const controller = new AbortController();
      const request = { controller, pending: true };
      controllersRef.current.set(key, request);

      const slot = { scope, category, generation };
      const more = cursor !== null;
      dispatch({ type: more ? "MORE_START" : "FETCH_START", ...slot });
      const fetchPages = async () => {
        const depth = more
          ? Math.min(5, batchSize)
          : Math.min(2, Math.max(1, stateRef.current[scope][category].restorePages));
        const items = new Map<string, { id: string }>();
        const seen = new Set<string>(cursor ? [cursor] : []);
        let next = cursor;
        let pages = 0;
        let page;
        do {
          page = await searchCategory(category, query, {
            limit: scope === "overview" ? OVERVIEW_LIMIT : PAGE_LIMIT,
            cursor: next ?? undefined,
            signal: controller.signal,
          });
          if (controller.signal.aborted) return;
          for (const item of page.items) items.set(item.id, item);
          pages += 1;
          next = page.nextCursor;
          if (!page.hasMore || !next || seen.has(next)) {
            page = { ...page, nextCursor: null, hasMore: false };
            break;
          }
          seen.add(next);
        } while (pages < depth);
        return { page: { ...page, items: [...items.values()] }, pages };
      };
      fetchPages().then(
        (result) => {
          if (controller.signal.aborted) return;
          request.pending = false;
          if (result)
            dispatch({ type: more ? "MORE_SUCCESS" : "FETCH_SUCCESS", ...slot, ...result });
        },
        (error: unknown) => {
          if (controller.signal.aborted) return;
          request.pending = false;
          const errorKind = classifySearchError(error);
          dispatch({ type: more ? "MORE_ERROR" : "FETCH_ERROR", ...slot, errorKind });
        },
      );
    },
    [],
  );

  // ── Debounce: commit the trimmed query after the user pauses typing ──────────
  useEffect(() => {
    const trimmed = state.query.trim();
    if (trimmed === state.activeQuery) return;

    const timer = window.setTimeout(() => {
      abortAll();
      dispatch({ type: "COMMIT_QUERY", query: trimmed });
    }, SEARCH_DEBOUNCE_MS);

    return () => window.clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- activeQuery is read, not depended on: it must not restart the debounce timer while it's ticking.
  }, [state.query]);

  // ── Lazy fetch: every idle slot the current view shows ──────────────────────
  // Keyed on which of those slots are idle, so a retry (which resets a slot to
  // idle) re-triggers this effect as reliably as a new query or tab does.
  const idleSlots =
    enabled && state.activeQuery && !state.typing
      ? slotsInView(state.activeTab)
          .filter(([scope, category]) => state[scope][category].status === "idle")
          .map(([scope, category]) => `${scope}:${category}`)
          .join(",")
      : "";
  useEffect(() => {
    if (!idleSlots) return;
    for (const key of idleSlots.split(",")) {
      const [scope, category] = key.split(":") as [SearchScope, SearchCategory];
      run(scope, category, null);
    }
  }, [idleSlots, state.generation, run]);

  useEffect(() => abortAll, [abortAll]);
  useEffect(() => {
    if (!enabled) abortAll();
  }, [enabled, abortAll]);

  // Aborting here, in the keystroke's own handler, is the earliest point the
  // old query's requests can be stopped; the generation bump in the reducer
  // then drops anything that answers regardless.
  const setQuery = useCallback(
    (query: string) => {
      if (query.trim() !== stateRef.current.activeQuery) abortAll();
      dispatch({ type: "SET_QUERY", query });
    },
    [abortAll],
  );

  const cancelRecovery = useCallback((abortAutomatic = false) => {
    for (const scope of ["overview", "tabs"] as const) {
      for (const category of SEARCH_CATEGORIES) {
        const list = stateRef.current[scope][category];
        if (
          list.restorePages > list.pages &&
          (list.loadingMore || (abortAutomatic && list.status === "loading"))
        ) {
          const key = `${scope}:${category}`;
          controllersRef.current.get(key)?.controller.abort();
          controllersRef.current.delete(key);
        }
      }
    }
    dispatch({ type: "CANCEL_RECOVERY", abortAutomatic });
  }, []);
  const setActiveTab = useCallback(
    (tab: SearchTab) => {
      if (tab !== stateRef.current.activeTab) cancelRecovery(true);
      dispatch({ type: "SET_ACTIVE_TAB", tab });
    },
    [cancelRecovery],
  );

  const loadMore = useCallback(
    (category: SearchCategory) => {
      const list = stateRef.current.tabs[category];
      if (list.status !== "ready" || !list.hasMore || list.loadingMore || !list.cursor) return;
      run("tabs", category, list.cursor);
    },
    [run],
  );

  const retry = useCallback(
    (scope: SearchScope, category: SearchCategory) => dispatch({ type: "RETRY", scope, category }),
    [],
  );

  const recover = useCallback(
    (category: SearchCategory) => {
      const list = stateRef.current.tabs[category];
      if (list.status !== "ready" || !list.hasMore || list.loadingMore || !list.cursor) return;
      const remaining = list.restorePages - list.pages;
      if (remaining > 0) run("tabs", category, list.cursor, Math.min(5, remaining));
    },
    [run],
  );

  return { state, setQuery, setActiveTab, loadMore, retry, recover, cancelRecovery };
}
