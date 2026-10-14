import SearchResultRow from "./SearchResultRow";
import { resultCount, TAB_LABELS } from "./searchLabels";
import type { SearchCategory, SearchErrorKind, SearchResultByCategory } from "./searchTypes";
import type { ListState } from "./useGlobalSearch";

function errorMessage(kind: SearchErrorKind | null): string {
  switch (kind) {
    case "forbidden":
      return "Você não tem permissão para ver esses resultados.";
    case "bad_request":
      return "Não foi possível interpretar essa busca.";
    case "unavailable":
      return "Esta busca ainda não está disponível.";
    default:
      return "Não foi possível buscar agora.";
  }
}

/** Placeholder cards while a list loads. Decorative: the page announces loading once. */
export function SearchSkeleton({ rows }: { rows: number }) {
  return (
    <ul className="global-search__list" aria-hidden="true" data-testid="global-search-skeleton">
      {Array.from({ length: rows }, (_, index) => (
        <li key={index} className="global-search__item global-search__item--skeleton" />
      ))}
    </ul>
  );
}

export function SearchError({
  kind,
  onRetry,
}: {
  kind: SearchErrorKind | null;
  onRetry: () => void;
}) {
  return (
    <div className="global-search__status" role="alert">
      <span>{errorMessage(kind)}</span>
      <button type="button" className="global-search__link-btn" onClick={onRetry}>
        Tentar novamente
      </button>
    </div>
  );
}

export function SearchEmpty({ query }: { query: string }) {
  return (
    <div className="global-search__empty" data-testid="global-search-empty">
      <p className="global-search__empty-title">Nenhum resultado para “{query}”.</p>
      <p>Tente outro termo ou selecione outra categoria.</p>
    </div>
  );
}

/**
 * One category in its own tab: the whole list, paginated by cursor. The count
 * is printed only when it is exact — the last page is here — because the
 * server does not total a search, and a count of the rows on screen would
 * understate one that has more.
 */
export default function SearchResultList<C extends SearchCategory>({
  category,
  list,
  query,
  onRetry,
  onLoadMore,
}: {
  category: C;
  list: ListState<SearchResultByCategory[C]>;
  query: string;
  onRetry: () => void;
  onLoadMore: () => void;
}) {
  if (list.status === "idle" || list.status === "loading") return <SearchSkeleton rows={4} />;
  if (list.status === "error") return <SearchError kind={list.errorKind} onRetry={onRetry} />;
  if (list.items.length === 0) return <SearchEmpty query={query} />;

  return (
    <>
      {!list.hasMore && (
        <p className="global-search__count">
          {resultCount(list.items.length)} para “{query}”
        </p>
      )}
      <ul className="global-search__list" aria-label={TAB_LABELS[category]}>
        {list.items.map((item) => (
          <li key={item.id} className="global-search__item">
            <SearchResultRow category={category} result={item} query={query} />
          </li>
        ))}
      </ul>

      {list.hasMore && (
        <button
          type="button"
          className="global-search__load-more"
          onClick={onLoadMore}
          disabled={list.loadingMore}
        >
          {list.loadingMore ? "Carregando…" : "Carregar mais"}
        </button>
      )}

      {list.loadMoreError && (
        <div className="global-search__status" role="alert">
          {errorMessage(list.loadMoreError)}
        </div>
      )}
    </>
  );
}
