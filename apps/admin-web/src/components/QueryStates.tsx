import type { QueryStatus } from "../lib/useAdminQuery";

interface QueryStatesProps {
  status: QueryStatus;
  message: string;
  /** What to say when the request succeeded and returned nothing. */
  empty: string;
  isEmpty: boolean;
  onRetry: () => void;
  /** Rows of the loading skeleton, so it occupies roughly the real height. */
  skeletonRows?: number;
}

/**
 * The four answers a listing can give that are not "here are the rows".
 *
 * They are one component because keeping them apart is the point: a permission
 * failure, a network failure, a broken response and an empty result look
 * identical if a screen collapses them into "nada encontrado", and only one of
 * them means the operator should try again.
 *
 * Returns null when the data is present, so a caller renders it above the table
 * and gets nothing when there is nothing to say.
 */
export default function QueryStates({
  status,
  message,
  empty,
  isEmpty,
  onRetry,
  skeletonRows = 3,
}: QueryStatesProps) {
  if (status === "loading") {
    return (
      <div className="admin-skeleton" role="status" aria-live="polite">
        <span className="admin-visually-hidden">Carregando…</span>
        {Array.from({ length: skeletonRows }, (_, index) => (
          <span key={index} className="admin-skeleton__row" aria-hidden="true" />
        ))}
      </div>
    );
  }
  if (status === "forbidden") {
    return (
      <p role="alert" className="admin-alert">
        {message || "Você não tem permissão para esta seção."}
      </p>
    );
  }
  if (status === "network" || status === "error") {
    return (
      <div role="alert" className="admin-alert">
        <p>{message}</p>
        <button type="button" className="admin-button" onClick={onRetry}>
          Tentar novamente
        </button>
      </div>
    );
  }
  if (isEmpty) {
    return <p className="admin-empty">{empty}</p>;
  }
  return null;
}
