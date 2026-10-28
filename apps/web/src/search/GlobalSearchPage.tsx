/**
 * GlobalSearchPage — "Busca global" (RF-15, issue #900).
 *
 * Nested under /chat/search, a full-content page rendered into ChatShell's
 * <Outlet/>. Opening it, its shortcut and the focus hand-back belong to #550;
 * this page owns what is inside: the query, the six tabs — "Tudo" by default —
 * and the result cards. No avatar of the signed-in user is drawn here: the
 * search is for finding things, not for identity.
 *
 * What it shows is the confirmed identity's (CQ-07, CQ-10, CQ-11 — see
 * useSessionOwner). A search left for a result is recorded on its history
 * entry with the identity that left it, and is brought back only for that
 * identity — whichever Back, contextual or the browser's, lands here, and
 * whenever. Until an identity is confirmed there is no field and no request:
 * the page says it is confirming the session, and offers a retry if the
 * sidebar failed. While the identity is in doubt again, the search keeps what
 * the reader typed and the tab, shows none of it, drops the results it had and
 * requests nothing; confirmed the same, it revalidates the loaded page depth;
 * confirmed another user, workspace or session, the view starts over.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useLocation, useNavigate, useOutletContext, type NavigateOptions } from "react-router";

import "./GlobalSearchPage.css";
import "../chat/ChatMessageArea.css";

import AttachmentViewerHost from "../chat/AttachmentViewerHost";
import SessionPending from "../chat/SessionPending";
import { useSessionOwner, type SessionOwner } from "../chat/useSessionOwner";
import type { ChatOutletContext } from "../chat/ChatShell";
import { BackButton } from "../chat/message-area/ConversationHeader";
import { useSessionPresentation } from "../chat/useSessionPresentation";
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
  const session = useSessionOwner();
  const [entrySession] = useState(session.sessionKey);
  const goBack = useOutletContext<ChatOutletContext | null>()?.goBack;
  if (session.owner === null) return <SearchPending session={session} goBack={goBack} />;
  // Keyed on its owner: another identity's search is a new mount.
  return <SearchView
    key={`${session.sessionKey}:${session.owner}`}
    owner={session.owner}
    session={session}
    restoreAllowed={entrySession === session.sessionKey}
  />;
}

/** The page while nobody's search may be on screen. */
function SearchPending({ session, goBack }: { session: SessionOwner; goBack?: () => boolean }) {
  return (
    <main
      className="global-search"
      data-testid="global-search"
      aria-busy="true"
      onKeyDown={(event) => closeSearch(event, goBack)}
    >
      <div className="global-search__inner">
        <header className="global-search__header">
          {goBack && <BackButton onBack={goBack} />}
          <h1 className="global-search__sr-only">Busca global</h1>
        </header>
        <SessionPending session={session} className="global-search__initial" />
      </div>
    </main>
  );
}

function closeSearch(event: KeyboardEvent<HTMLElement>, goBack: (() => boolean) | undefined) {
  if (event.key !== "Escape" || event.defaultPrevented) return;
  // A viewer rendered in a portal owns Escape outside the search's DOM subtree.
  if (!event.currentTarget.contains(event.target as Node)) return;
  event.preventDefault();
  goBack?.();
}

/**
 * The search itself, for `owner`. Not settled — its owner in doubt again — it
 * stays mounted, so the same user's confirmed rotation finds the query and tab
 * as they left them, but shows none of it and searches nothing.
 */
function SearchView({ owner, session, restoreAllowed }: {
  owner: string;
  session: SessionOwner;
  restoreAllowed: boolean;
}) {
  const { settled } = session;
  const goBack = useOutletContext<ChatOutletContext | null>()?.goBack;
  const location = useLocation();
  const navigate = useNavigate();
  // Read once: a search the reader left for a result, brought back by Back.
  const [restored] = useState(() =>
    restoreAllowed ? readRestoredSearch(location.state, owner) : undefined,
  );
  const { state, setQuery, setActiveTab, loadMore, retry, recover, cancelRecovery } = useGlobalSearch(restored, settled);
  const inputRef = useRef<HTMLInputElement>(null);
  const rootRef = useRef<HTMLElement>(null);
  const loading = useMemo(() => isLoading(state), [state]);
  const scope = state.activeTab === "all" ? "overview" : "tabs";
  const visible = state.activeTab === "all" ? SEARCH_CATEGORIES : [state.activeTab];
  const presentationReady = !state.activeQuery || (
    !state.typing && visible.every((category) => state[scope][category].status === "ready")
  );
  const currentList = state.activeTab === "all" ? null : state.tabs[state.activeTab];
  const recoveryPending = !!currentList && currentList.hasMore && currentList.restorePages > currentList.pages;
  const abandonReading = useSessionPresentation(rootRef, settled && presentationReady, !settled, {
    pending: recoveryPending, scope: `${state.activeQuery}:${state.activeTab}`, onAbandon: cancelRecovery,
    unavailable: !!currentList && !currentList.hasMore && currentList.restorePages > currentList.pages,
  });
  // The field takes focus on every entry into the search: the first open, and
  // a repeat open while already here — the shell replaces the history entry
  // rather than stacking one (issue #550), which gives it a new key.
  // Shown again after a confirmation, the field is where the reader was.
  useEffect(() => {
    inputRef.current?.focus();
  }, [location.key]);
  useEffect(() => {
    if (settled && presentationReady && !recoveryPending && document.activeElement === document.body) inputRef.current?.focus();
  }, [settled, presentationReady, recoveryPending]);

  // Record this search on its own history entry, then leave for the result.
  const { pathname, search, state: historyState } = location;
  const { activeQuery, activeTab } = state;
  const openResult = useCallback(
    (to: string, options?: NavigateOptions) => {
      navigate(`${pathname}${search}`, {
        replace: true,
        state: withRestoredSearch(historyState, { query: activeQuery, tab: activeTab }, owner),
      });
      navigate(to, options);
    },
    [navigate, pathname, search, historyState, activeQuery, activeTab, owner],
  );

  function clearQuery() {
    setQuery("");
    inputRef.current?.focus();
  }

  const view = panelView(state);

  if (!settled) return <SearchPending session={session} goBack={goBack} />;

  return (
    <SearchNavigationContext.Provider value={openResult}>
      <AttachmentViewerHost onFocusFallback={() => inputRef.current?.focus()}>
        <main
          ref={rootRef}
          tabIndex={-1}
          className="global-search"
          data-testid="global-search"
          onKeyDown={(event) => closeSearch(event, goBack)}
        >
          <div className="global-search__inner">
            <header className="global-search__header" data-testid="global-search-header">
              <h1 className="global-search__sr-only">Busca global</h1>
              {goBack && <BackButton onBack={goBack} />}
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
                  onLoadMore={() => { abandonReading(); loadMore(state.activeTab as SearchCategory); }}
                  onRecover={() => recover(state.activeTab as SearchCategory)}
                  onCancelRecovery={abandonReading}
                />
              )}
            </section>
          </div>
        </main>
      </AttachmentViewerHost>
    </SearchNavigationContext.Provider>
  );
}
