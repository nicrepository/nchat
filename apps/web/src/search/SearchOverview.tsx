import SearchResultRow from "./SearchResultRow";
import { SearchEmpty, SearchError, SearchSkeleton } from "./SearchResultList";
import { resultCount, TAB_LABELS } from "./searchLabels";
import { SEARCH_CATEGORIES, type SearchCategory, type SearchResultByCategory } from "./searchTypes";
import type { ListState, SearchLists } from "./useGlobalSearch";

interface SectionProps<C extends SearchCategory> {
  category: C;
  list: ListState<SearchResultByCategory[C]>;
  query: string;
  onSeeAll: (category: SearchCategory) => void;
  onRetry: (category: SearchCategory) => void;
}

function OverviewSection<C extends SearchCategory>({
  category,
  list,
  query,
  onSeeAll,
  onRetry,
}: SectionProps<C>) {
  // A category with nothing to show takes no room at all.
  if (list.status === "ready" && list.items.length === 0) return null;

  const label = TAB_LABELS[category];
  const headingId = `global-search-section-${category}`;
  return (
    <section className="global-search__section" aria-labelledby={headingId}>
      <header className="global-search__section-header">
        <h2 id={headingId} className="global-search__section-title">
          {label}
        </h2>
        {list.status === "ready" && !list.hasMore && (
          <span className="global-search__count">{resultCount(list.items.length)}</span>
        )}
      </header>
      {list.status === "error" && (
        <SearchError kind={list.errorKind} onRetry={() => onRetry(category)} />
      )}
      {(list.status === "idle" || list.status === "loading") && <SearchSkeleton rows={2} />}
      {list.status === "ready" && (
        <>
          <ul className="global-search__list" aria-label={label}>
            {list.items.map((item) => (
              <li key={item.id} className="global-search__item">
                <SearchResultRow category={category} result={item} query={query} />
              </li>
            ))}
          </ul>
          {list.hasMore && (
            <button
              type="button"
              className="global-search__see-all"
              aria-label={`Ver todos os resultados em ${label}`}
              onClick={() => onSeeAll(category)}
            >
              Ver todos
            </button>
          )}
        </>
      )}
    </section>
  );
}

/**
 * "Tudo": a short, fixed-order sample of each category. Sections never
 * reorder by score, so the layout does not jump as they resolve, and each one
 * loads, fails and retries on its own — one unavailable category never turns
 * the others into an error or into "no results".
 */
export default function SearchOverview({
  lists,
  query,
  onSeeAll,
  onRetry,
}: {
  lists: SearchLists;
  query: string;
  onSeeAll: (category: SearchCategory) => void;
  onRetry: (category: SearchCategory) => void;
}) {
  const nothingFound = SEARCH_CATEGORIES.every(
    (category) => lists[category].status === "ready" && lists[category].items.length === 0,
  );
  if (nothingFound) return <SearchEmpty query={query} />;

  return (
    <>
      {SEARCH_CATEGORIES.map((category) => (
        <OverviewSection
          key={category}
          category={category}
          list={lists[category] as ListState<SearchResultByCategory[typeof category]>}
          query={query}
          onSeeAll={onSeeAll}
          onRetry={onRetry}
        />
      ))}
    </>
  );
}
