interface PaginationProps {
  /** How many rows the current page shows. */
  count: number;
  hasMore: boolean;
  canGoBack: boolean;
  busy: boolean;
  onNext: () => void;
  onPrevious: () => void;
}

/**
 * Keyset pagination controls.
 *
 * There is no page number and no total, because the listing has neither: it
 * pages by cursor so a page costs its own rows, and computing a total would
 * mean counting the whole table on every request. The navigation is a real
 * landmark with an accessible name, and the position is announced as text
 * rather than implied by which button is greyed out.
 */
export default function Pagination({
  count,
  hasMore,
  canGoBack,
  busy,
  onNext,
  onPrevious,
}: PaginationProps) {
  return (
    <nav className="admin-pagination" aria-label="Paginação">
      <p className="admin-pagination__status" role="status">
        {count} {count === 1 ? "registro" : "registros"} nesta página
        {hasMore ? " · há mais páginas" : " · última página"}
      </p>
      <div className="admin-pagination__actions">
        <button
          type="button"
          className="admin-button admin-button--ghost"
          onClick={onPrevious}
          disabled={!canGoBack || busy}
        >
          Página anterior
        </button>
        <button
          type="button"
          className="admin-button admin-button--ghost"
          onClick={onNext}
          disabled={!hasMore || busy}
        >
          Próxima página
        </button>
      </div>
    </nav>
  );
}
