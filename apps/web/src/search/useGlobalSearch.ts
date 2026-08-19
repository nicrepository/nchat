import { useCallback, useEffect, useReducer, useRef } from "react";

import { searchChannels, searchMessages, searchUsers, classifySearchError } from "./searchApi";
import type {
  ChannelSearchResult,
  MessageSearchResult,
  SearchErrorKind,
  SearchResultPage,
  SearchTab,
  UserSearchResult,
} from "./searchTypes";

const DEBOUNCE_MS = 300;
const PAGE_LIMIT = 20;

interface TabState<T> {
  status: "idle" | "loading" | "ready" | "error";
  items: T[];
  cursor: string | null;
  hasMore: boolean;
  loadingMore: boolean;
  errorKind: SearchErrorKind | null;
  loadMoreError: SearchErrorKind | null;
}

function idleTab<T>(): TabState<T> {
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

export interface GlobalSearchState {
  query: string;
  activeQuery: string;
  activeTab: SearchTab;
  messages: TabState<MessageSearchResult>;
  users: TabState<UserSearchResult>;
  channels: TabState<ChannelSearchResult>;
}

type Action =
  | { type: "SET_QUERY"; query: string }
  | { type: "COMMIT_QUERY"; query: string }
  | { type: "SET_ACTIVE_TAB"; tab: SearchTab }
  | { type: "FETCH_START"; tab: SearchTab }
  | { type: "FETCH_SUCCESS"; tab: SearchTab; page: SearchResultPage<unknown> }
  | { type: "FETCH_ERROR"; tab: SearchTab; errorKind: SearchErrorKind }
  | { type: "LOAD_MORE_START"; tab: SearchTab }
  | { type: "LOAD_MORE_SUCCESS"; tab: SearchTab; page: SearchResultPage<unknown> }
  | { type: "LOAD_MORE_ERROR"; tab: SearchTab; errorKind: SearchErrorKind }
  | { type: "RETRY_TAB"; tab: SearchTab };

function initialState(): GlobalSearchState {
  return {
    query: "",
    activeQuery: "",
    activeTab: "messages",
    messages: idleTab(),
    users: idleTab(),
    channels: idleTab(),
  };
}

function updateTab<T>(
  state: GlobalSearchState,
  tab: SearchTab,
  update: (current: TabState<T>) => TabState<T>,
): GlobalSearchState {
  return { ...state, [tab]: update(state[tab] as TabState<T>) };
}

function reducer(state: GlobalSearchState, action: Action): GlobalSearchState {
  switch (action.type) {
    case "SET_QUERY":
      return { ...state, query: action.query };

    case "COMMIT_QUERY":
      // A new committed query invalidates every tab atomically — a cursor is
      // never valid across a query change, per the search-service contract.
      return {
        ...state,
        activeQuery: action.query,
        messages: idleTab(),
        users: idleTab(),
        channels: idleTab(),
      };

    case "SET_ACTIVE_TAB":
      return { ...state, activeTab: action.tab };

    case "FETCH_START":
      return updateTab(state, action.tab, (current) => ({
        ...current,
        status: "loading",
        errorKind: null,
      }));

    case "FETCH_SUCCESS":
      return updateTab(state, action.tab, (current) => ({
        ...current,
        status: "ready",
        items: action.page.items,
        cursor: action.page.nextCursor,
        hasMore: action.page.hasMore,
        errorKind: null,
      }));

    case "FETCH_ERROR":
      return updateTab(state, action.tab, (current) => ({
        ...current,
        status: "error",
        errorKind: action.errorKind,
      }));

    case "LOAD_MORE_START":
      return updateTab(state, action.tab, (current) => ({
        ...current,
        loadingMore: true,
        loadMoreError: null,
      }));

    case "LOAD_MORE_SUCCESS":
      return updateTab(state, action.tab, (current) => ({
        ...current,
        loadingMore: false,
        items: [...current.items, ...action.page.items],
        cursor: action.page.nextCursor,
        hasMore: action.page.hasMore,
        loadMoreError: null,
      }));

    case "LOAD_MORE_ERROR":
      // Items and cursor are left untouched: a failed "load more" must never
      // erase results already on screen.
      return updateTab(state, action.tab, (current) => ({
        ...current,
        loadingMore: false,
        loadMoreError: action.errorKind,
      }));

    case "RETRY_TAB":
      return updateTab(state, action.tab, () => idleTab());

    default:
      return state;
  }
}

const TAB_FETCHERS = {
  messages: searchMessages,
  users: searchUsers,
  channels: searchChannels,
} as const;

export interface UseGlobalSearchResult {
  state: GlobalSearchState;
  setQuery: (query: string) => void;
  setActiveTab: (tab: SearchTab) => void;
  loadMore: (tab: SearchTab) => void;
  retryTab: (tab: SearchTab) => void;
}

/**
 * Orchestrates the global search page: debounced query commit, one fetch per
 * tab (lazy — only the active tab, and only once per committed query), abort
 * of superseded requests, and cursor-based "load more" pagination.
 */
export function useGlobalSearch(): UseGlobalSearchResult {
  const [state, dispatch] = useReducer(reducer, undefined, initialState);
  const controllersRef = useRef<Record<SearchTab, AbortController | null>>({
    messages: null,
    users: null,
    channels: null,
  });

  // ── Debounce: commit the trimmed query after the user pauses typing ──────────
  useEffect(() => {
    const trimmed = state.query.trim();
    if (trimmed === state.activeQuery) return;

    const timer = window.setTimeout(() => {
      dispatch({ type: "COMMIT_QUERY", query: trimmed });
    }, DEBOUNCE_MS);

    return () => window.clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- activeQuery is read, not depended on: it must not restart the debounce timer while it's ticking.
  }, [state.query]);

  // ── Lazy per-tab fetch: only the active tab, only when idle ─────────────────
  // Depends on the active tab's own status (not just which tab/query is active)
  // so that retryTab — which resets a tab back to "idle" without touching
  // activeQuery/activeTab — reliably re-triggers this effect.
  const activeTabStatus = state[state.activeTab].status;
  useEffect(() => {
    if (!state.activeQuery) return;
    const tab = state.activeTab;
    if (activeTabStatus !== "idle") return;

    controllersRef.current[tab]?.abort();
    const controller = new AbortController();
    controllersRef.current[tab] = controller;

    dispatch({ type: "FETCH_START", tab });
    TAB_FETCHERS[tab](state.activeQuery, { limit: PAGE_LIMIT, signal: controller.signal }).then(
      (page) => {
        if (controller.signal.aborted) return;
        dispatch({ type: "FETCH_SUCCESS", tab, page });
      },
      (error: unknown) => {
        if (controller.signal.aborted) return;
        dispatch({ type: "FETCH_ERROR", tab, errorKind: classifySearchError(error) });
      },
    );
  }, [state.activeQuery, state.activeTab, activeTabStatus]);

  useEffect(() => {
    const controllers = controllersRef.current;
    return () => {
      controllers.messages?.abort();
      controllers.users?.abort();
      controllers.channels?.abort();
    };
  }, []);

  const setQuery = useCallback((query: string) => {
    dispatch({ type: "SET_QUERY", query });
  }, []);

  const setActiveTab = useCallback((tab: SearchTab) => {
    dispatch({ type: "SET_ACTIVE_TAB", tab });
  }, []);

  const loadMore = useCallback(
    (tab: SearchTab) => {
      const tabState = state[tab];
      if (tabState.status !== "ready" || !tabState.hasMore || tabState.loadingMore) return;
      if (!tabState.cursor) return;

      dispatch({ type: "LOAD_MORE_START", tab });
      TAB_FETCHERS[tab](state.activeQuery, { limit: PAGE_LIMIT, cursor: tabState.cursor }).then(
        (page) => dispatch({ type: "LOAD_MORE_SUCCESS", tab, page }),
        (error: unknown) =>
          dispatch({ type: "LOAD_MORE_ERROR", tab, errorKind: classifySearchError(error) }),
      );
    },
    [state],
  );

  const retryTab = useCallback((tab: SearchTab) => {
    dispatch({ type: "RETRY_TAB", tab });
  }, []);

  return { state, setQuery, setActiveTab, loadMore, retryTab };
}
