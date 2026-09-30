import HighlightedText from "./HighlightedText";
import SearchAvatar from "./SearchAvatar";
import { formatDateTime, participantCount } from "./searchLabels";
import { useOpenSearchResult } from "./searchNavigation";
import type { GroupSearchResult } from "./searchTypes";

interface GroupResultRowProps {
  result: GroupSearchResult;
  query: string;
}

/** A group is a dm conversation: it opens on the same `/chat/dm/:id` route. */
export default function GroupResultRow({ result, query }: GroupResultRowProps) {
  const open = useOpenSearchResult();

  return (
    <button
      type="button"
      className="global-search__result"
      onClick={() => open(`/chat/dm/${encodeURIComponent(result.id)}`)}
    >
      <SearchAvatar seed={result.id} name={result.title} />
      <span className="global-search__result-body">
        <span className="global-search__result-title">
          <span className="global-search__sr-only">Grupo</span>{" "}
          <HighlightedText text={result.title} query={query} />
        </span>{" "}
        <span className="global-search__result-sub">
          {participantCount(result.participantCount)}
          {result.lastMessageAt && (
            <>
              {" · última atividade "}
              <time dateTime={result.lastMessageAt}>{formatDateTime(result.lastMessageAt)}</time>
            </>
          )}
        </span>
      </span>
    </button>
  );
}
