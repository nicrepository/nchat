/**
 * GlobalSearchPage — "Busca global" (RF-15, issue #900).
 *
 * Nested under /chat/search, a full-content page rendered into ChatShell's
 * <Outlet/>. Opening it, its shortcut and the focus hand-back belong to #550;
 * this page owns what is inside: the query, the six tabs — "Tudo" by default —
 * and the result cards. No avatar of the signed-in user is drawn here: the
 * search is for finding things, not for identity.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useLocation, useNavigate, type NavigateOptions } from "react-router";

import "./GlobalSearchPage.css";

import AttachmentViewerHost from "../chat/AttachmentViewerHost";
import SearchOverview from "./SearchOverview";
import SearchResultList, { SearchSkeleton } from "./SearchResultList";
import SearchTabs from "./SearchTabs";
import { SEARCH_PANEL_ID, tabId } from "./searchLabels";
import {
  readRestoredSearch,
  SearchNavigationContext,
  withRestoredSearch,
} from "./searchNavigation";
import { SEARCH_CATEGORIES, type SearchCategory } from "./searchTypes";
import { useGlobalSearch, type GlobalSearchState } from "./useGlobalSearch";

const FIELD_LABEL = "Buscar mensagens, pessoas, canais, grupos e arquivos";

/**
 * What the panel shows. "pending" is the debounce window: the field says
 * something the results were not searched for, so no result is on screen.
 */
function panelView(state: GlobalSearchState): "initial" | "pending" | "results" {
  if (!state.query.trim()) return "initial";
  return state.typing ? "pending" : "results";
}

function isLoading(state: GlobalSearchState): boolean {
  if (!state.activeQuery || state.typing) return false;
  const scope = state.activeTab === "all" ? "overview" : "tabs";
  const categories: readonly SearchCategory[] =
    state.activeTab === "all" ? SEARCH_CATEGORIES : [state.activeTab];
  return categories.some((category) => {
    const status = state[scope][category].status;
    return status === "idle" || status === "loading";
  });
}

export default function GlobalSearchPage() {
  const location = useLocation();
  const navigate = useNavigate();
  // Read once: a search the reader left for a result, brought back by Back.
  const [restored] = useState(() => readRestoredSearch(location.state));
  const { state, setQuery, setActiveTab, loadMore, retry } = useGlobalSearch(restored);
  const inputRef = useRef<HTMLInputElement>(null);
  // The field takes focus on every entry into the search: the first open, and
  // a repeat open while already here — the shell replaces the history entry
  // rather than stacking one (issue #550), which gives it a new key.
  useEffect(() => inputRef.current?.focus(), [location.key]);

  // Record this search on its own history entry, then leave for the result.
  const { pathname, search, state: historyState } = location;
  const { activeQuery, activeTab } = state;
  const openResult = useCallback(
    (to: string, options?: NavigateOptions) => {
      navigate(`${pathname}${search}`, {
        replace: true,
        state: withRestoredSearch(historyState, { query: activeQuery, tab: activeTab }),
      });
      navigate(to, options);
    },
    [navigate, pathname, search, historyState, activeQuery, activeTab],
  );

  function closeSearch(event: KeyboardEvent<HTMLElement>) {
    if (event.key !== "Escape" || event.defaultPrevented) return;
    event.preventDefault();
    navigate(-1);
  }

  function clearQuery() {
    setQuery("");
    inputRef.current?.focus();
  }

  const loading = useMemo(() => isLoading(state), [state]);
  const view = panelView(state);

  return (
    <SearchNavigationContext.Provider value={openResult}>
      <AttachmentViewerHost onFocusFallback={() => inputRef.current?.focus()}>
        <main className="global-search" data-testid="global-search" onKeyDown={closeSearch}>
          <div className="global-search__inner">
            <header className="global-search__header" data-testid="global-search-header">
              <h1 className="global-search__sr-only">Busca global</h1>
              <div className="global-search__field">
                <span className="material-symbols-outlined" aria-hidden="true">
                  search
                </span>
                <label htmlFor="global-search-input" className="global-search__sr-only">
                  {FIELD_LABEL}
                </label>
                <input
                  id="global-search-input"
                  type="search"
                  autoComplete="off"
                  ref={inputRef}
                  placeholder="Buscar no NChat"
                  maxLength={512}
                  value={state.query}
                  onChange={(event) => setQuery(event.target.value)}
                />
                {state.query && (
                  <button
                    type="button"
                    className="global-search__clear"
                    aria-label="Limpar busca"
                    onClick={clearQuery}
                  >
                    <span className="material-symbols-outlined" aria-hidden="true">
                      close
                    </span>
                  </button>
                )}
              </div>
            </header>

            <SearchTabs active={state.activeTab} onChange={setActiveTab} />

            <p className="global-search__sr-only" role="status">
              {loading ? "Buscando…" : ""}
            </p>

            <section
              id={SEARCH_PANEL_ID}
              role="tabpanel"
              aria-labelledby={tabId(state.activeTab)}
              // APG tabs: the panel is the next Tab stop after the tab list, so
              // the initial and empty states — no control inside — are reachable.
              tabIndex={0}
              className="global-search__panel"
            >
              {view === "initial" && (
                <div className="global-search__initial" data-testid="global-search-initial">
                  <h2 className="global-search__initial-title">Buscar no NChat</h2>
                  <p>Pesquise mensagens, pessoas, canais, grupos e arquivos.</p>
                </div>
              )}
              {view === "pending" && (
                <div data-testid="global-search-pending">
                  <SearchSkeleton rows={3} />
                </div>
              )}
              {view === "results" && state.activeTab === "all" && (
                <SearchOverview
                  lists={state.overview}
                  query={state.activeQuery}
                  onSeeAll={setActiveTab}
                  onRetry={(category) => retry("overview", category)}
                />
              )}
              {view === "results" && state.activeTab !== "all" && (
                <SearchResultList
                  category={state.activeTab}
                  list={state.tabs[state.activeTab]}
                  query={state.activeQuery}
                  onRetry={() => retry("tabs", state.activeTab as SearchCategory)}
                  onLoadMore={() => loadMore(state.activeTab as SearchCategory)}
                />
              )}
            </section>
          </div>
        </main>
      </AttachmentViewerHost>
    </SearchNavigationContext.Provider>
  );
}
