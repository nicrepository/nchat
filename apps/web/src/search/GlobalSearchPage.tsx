/**
 * GlobalSearchPage — "Busca global" (RF-15).
 *
 * Nested under /chat/search, the same routing shape as FavoritesPage: a
 * full-content page rendered into ChatShell's <Outlet/>. Consumes the three
 * search-service endpoints via useGlobalSearch, with independent
 * loading/error/empty/pagination state per tab.
 */

import "./GlobalSearchPage.css";

import ChannelResultRow from "./ChannelResultRow";
import MessageResultRow from "./MessageResultRow";
import SearchResultList from "./SearchResultList";
import UserResultRow from "./UserResultRow";
import { useGlobalSearch } from "./useGlobalSearch";
import type { SearchTab } from "./searchTypes";

const TABS: Array<{ id: SearchTab; label: string }> = [
  { id: "messages", label: "Mensagens" },
  { id: "users", label: "Usuários" },
  { id: "channels", label: "Canais" },
];

export default function GlobalSearchPage() {
  const { state, setQuery, setActiveTab, loadMore, retryTab } = useGlobalSearch();

  return (
    <div className="global-search" data-testid="global-search">
      <header className="global-search__header">
        <h1 className="global-search__title">Busca global</h1>
        <div className="global-search__field">
          <span className="material-symbols-outlined" aria-hidden="true">
            search
          </span>
          <label htmlFor="global-search-input" className="global-search__sr-label">
            Buscar mensagens, pessoas e canais
          </label>
          <input
            id="global-search-input"
            type="search"
            autoComplete="off"
            autoFocus
            placeholder="Buscar mensagens, pessoas e canais"
            value={state.query}
            onChange={(event) => setQuery(event.target.value)}
          />
        </div>
      </header>

      <div className="global-search__tabs" role="tablist" aria-label="Tipo de resultado">
        {TABS.map((tab) => {
          const isActive = state.activeTab === tab.id;
          return (
            <button
              key={tab.id}
              type="button"
              role="tab"
              id={`global-search-tab-${tab.id}`}
              aria-selected={isActive}
              aria-controls={`global-search-panel-${tab.id}`}
              className={`global-search__tab${isActive ? " global-search__tab--active" : ""}`}
              onClick={() => setActiveTab(tab.id)}
            >
              {tab.label}
            </button>
          );
        })}
      </div>

      {!state.activeQuery && (
        <div className="global-search__status" data-testid="global-search-initial">
          Digite um termo para buscar.
        </div>
      )}

      {state.activeQuery && (
        <div
          id={`global-search-panel-${state.activeTab}`}
          role="tabpanel"
          aria-labelledby={`global-search-tab-${state.activeTab}`}
          aria-live="polite"
        >
          {state.activeTab === "messages" && (
            <SearchResultList
              status={state.messages.status}
              items={state.messages.items}
              errorKind={state.messages.errorKind}
              hasMore={state.messages.hasMore}
              loadingMore={state.messages.loadingMore}
              loadMoreError={state.messages.loadMoreError}
              emptyMessage="Nenhuma mensagem encontrada."
              listLabel="Mensagens encontradas"
              itemKey={(item) => item.id}
              renderItem={(item) => <MessageResultRow result={item} query={state.activeQuery} />}
              onRetry={() => retryTab("messages")}
              onLoadMore={() => loadMore("messages")}
            />
          )}
          {state.activeTab === "users" && (
            <SearchResultList
              status={state.users.status}
              items={state.users.items}
              errorKind={state.users.errorKind}
              hasMore={state.users.hasMore}
              loadingMore={state.users.loadingMore}
              loadMoreError={state.users.loadMoreError}
              emptyMessage="Nenhuma pessoa encontrada."
              listLabel="Pessoas encontradas"
              itemKey={(item) => item.id}
              renderItem={(item) => <UserResultRow result={item} query={state.activeQuery} />}
              onRetry={() => retryTab("users")}
              onLoadMore={() => loadMore("users")}
            />
          )}
          {state.activeTab === "channels" && (
            <SearchResultList
              status={state.channels.status}
              items={state.channels.items}
              errorKind={state.channels.errorKind}
              hasMore={state.channels.hasMore}
              loadingMore={state.channels.loadingMore}
              loadMoreError={state.channels.loadMoreError}
              emptyMessage="Nenhum canal encontrado."
              listLabel="Canais encontrados"
              itemKey={(item) => item.id}
              renderItem={(item) => <ChannelResultRow result={item} query={state.activeQuery} />}
              onRetry={() => retryTab("channels")}
              onLoadMore={() => loadMore("channels")}
            />
          )}
        </div>
      )}
    </div>
  );
}
