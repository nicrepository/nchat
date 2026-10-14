import { useCallback, useEffect, useReducer, useRef } from "react";

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
  | ({ type: "FETCH_SUCCESS" | "MORE_SUCCESS"; page: SearchResultPage<{ id: string }> } & Slot)
  | ({ type: "FETCH_ERROR" | "MORE_ERROR"; errorKind: SearchErrorKind } & Slot)
  | { type: "RETRY"; scope: SearchScope; category: SearchCategory };

function idleList<T>(): ListState<T> {
  return {
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
function appendPage(list: AnyList, page: SearchResultPage<{ id: string }>): AnyList {
  const seen = new Set(list.items.map((item) => item.id));
  return {
    ...list,
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

function reducer(state: GlobalSearchState, action: Action): GlobalSearchState {
  switch (action.type) {
    case "SET_QUERY": {
      const typing = action.query.trim() !== state.activeQuery;
      // Entering the window invalidates everything the old query produced;
      // staying in it (another keystroke) changes nothing else. Leaving it by
      // typing back to the committed query searches that query again.
      if (typing === state.typing) return { ...state, query: action.query };
      return { ...invalidated(state), query: action.query, typing };
    }
    case "COMMIT_QUERY":
      return { ...invalidated(state), activeQuery: action.query, typing: false };
    case "SET_ACTIVE_TAB":
      return { ...state, activeTab: action.tab };
    case "FETCH_START":
      return patchSlot(state, action, (list) => ({ ...list, status: "loading", errorKind: null }));
    case "FETCH_SUCCESS":
      return patchSlot(state, action, (list) => ({
        ...list,
        status: "ready",
        items: action.page.items,
        cursor: action.page.nextCursor,
        hasMore: action.page.hasMore,
      }));
    case "FETCH_ERROR":
      return patchSlot(state, action, (list) => ({
        ...list,
        status: "error",
        errorKind: action.errorKind,
      }));
    case "MORE_START":
      return patchSlot(state, action, (list) => ({
        ...list,
        loadingMore: true,
        loadMoreError: null,
      }));
    case "MORE_SUCCESS":
      return patchSlot(state, action, (list) => appendPage(list, action.page));
    case "MORE_ERROR":
      // Items and cursor stay: a failed "load more" never erases what is shown.
      return patchSlot(state, action, (list) => ({
        ...list,
        loadingMore: false,
        loadMoreError: action.errorKind,
      }));
    case "RETRY":
      return patchSlot(state, action, () => idleList());
  }
}

/** The slots the current view needs; "Tudo" needs every category's overview. */
function slotsInView(tab: SearchTab): Array<[SearchScope, SearchCategory]> {
  return tab === "all"
    ? SEARCH_CATEGORIES.map((category) => ["overview", category])
    : [["tabs", tab]];
}

export interface UseGlobalSearchResult {
  state: GlobalSearchState;
  setQuery: (query: string) => void;
  setActiveTab: (tab: SearchTab) => void;
  loadMore: (category: SearchCategory) => void;
  retry: (scope: SearchScope, category: SearchCategory) => void;
}

/**
 * Orchestrates the global search page: debounced query commit, lazy fetch of
 * exactly the slots the visible view needs (only once per committed query),
 * abort of superseded requests, and cursor pagination per category.
 *
 * Switching tabs does not abort anything: a request for the same query is
 * still valid and simply fills its slot for when the reader comes back.
 */
export function useGlobalSearch(restored?: RestoredSearch): UseGlobalSearchResult {
  const [state, dispatch] = useReducer(reducer, restored, initialState);
  const stateRef = useRef(state);
  const controllersRef = useRef(new Map<string, AbortController>());

  useEffect(() => {
    stateRef.current = state;
  });

  const abortAll = useCallback(() => {
    controllersRef.current.forEach((controller) => controller.abort());
    controllersRef.current.clear();
  }, []);

  const run = useCallback((scope: SearchScope, category: SearchCategory, cursor: string | null) => {
    const { activeQuery: query, generation } = stateRef.current;
    const key = `${scope}:${category}`;
    controllersRef.current.get(key)?.abort();
    const controller = new AbortController();
    controllersRef.current.set(key, controller);

    const slot = { scope, category, generation };
    const more = cursor !== null;
    dispatch({ type: more ? "MORE_START" : "FETCH_START", ...slot });
    searchCategory(category, query, {
      limit: scope === "overview" ? OVERVIEW_LIMIT : PAGE_LIMIT,
      cursor: cursor ?? undefined,
      signal: controller.signal,
    }).then(
      (page) => {
        if (controller.signal.aborted) return;
        dispatch({ type: more ? "MORE_SUCCESS" : "FETCH_SUCCESS", ...slot, page });
      },
      (error: unknown) => {
        if (controller.signal.aborted) return;
        const errorKind = classifySearchError(error);
        dispatch({ type: more ? "MORE_ERROR" : "FETCH_ERROR", ...slot, errorKind });
      },
    );
  }, []);

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
    state.activeQuery && !state.typing
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

  const setActiveTab = useCallback(
    (tab: SearchTab) => dispatch({ type: "SET_ACTIVE_TAB", tab }),
    [],
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

  return { state, setQuery, setActiveTab, loadMore, retry };
}
