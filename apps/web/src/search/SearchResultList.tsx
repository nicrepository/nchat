import type { ReactNode } from "react";

import type { SearchErrorKind } from "./searchTypes";

interface SearchResultListProps<T> {
  status: "idle" | "loading" | "ready" | "error";
  items: T[];
  errorKind: SearchErrorKind | null;
  hasMore: boolean;
  loadingMore: boolean;
  loadMoreError: SearchErrorKind | null;
  emptyMessage: string;
  listLabel: string;
  renderItem: (item: T) => ReactNode;
  itemKey: (item: T) => string;
  onRetry: () => void;
  onLoadMore: () => void;
}

function errorMessage(kind: SearchErrorKind | null): string {
  switch (kind) {
    case "forbidden":
      return "Você não tem permissão para ver esses resultados.";
    case "bad_request":
      return "Não foi possível interpretar essa busca.";
    case "server_error":
      return "O servidor de busca está indisponível no momento.";
    default:
      return "Não foi possível carregar os resultados.";
  }
}

export default function SearchResultList<T>({
  status,
  items,
  errorKind,
  hasMore,
  loadingMore,
  loadMoreError,
  emptyMessage,
  listLabel,
  renderItem,
  itemKey,
  onRetry,
  onLoadMore,
}: SearchResultListProps<T>) {
  if (status === "idle") return null;

  if (status === "loading") {
    return (
      <div className="global-search__status" role="status">
        Buscando…
      </div>
    );
  }

  if (status === "error") {
    return (
      <div className="global-search__status" role="alert">
        <span>{errorMessage(errorKind)}</span>
        <button type="button" className="global-search__link-btn" onClick={onRetry}>
          Tentar novamente
        </button>
      </div>
    );
  }

  if (items.length === 0) {
    return (
      <div className="global-search__status" data-testid="global-search-empty">
        {emptyMessage}
      </div>
    );
  }

  return (
    <>
      <ul className="global-search__list" aria-label={listLabel}>
        {items.map((item) => (
          <li key={itemKey(item)} className="global-search__item">
            {renderItem(item)}
          </li>
        ))}
      </ul>

      {hasMore && (
        <button
          type="button"
          className="global-search__load-more"
          onClick={onLoadMore}
          disabled={loadingMore}
        >
          {loadingMore ? "Carregando…" : "Carregar mais"}
        </button>
      )}

      {loadMoreError && (
        <div className="global-search__status" role="alert">
          {errorMessage(loadMoreError)}
        </div>
      )}
    </>
  );
}
