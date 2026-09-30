import HighlightedText from "./HighlightedText";
import { participantCount } from "./searchLabels";
import { useOpenSearchResult } from "./searchNavigation";
import type { ChannelSearchResult } from "./searchTypes";

interface ChannelResultRowProps {
  result: ChannelSearchResult;
  query: string;
}

/** Opens the channel by id — the `channel/:id` route param, not the slug. */
export default function ChannelResultRow({ result, query }: ChannelResultRowProps) {
  const open = useOpenSearchResult();

  return (
    <button
      type="button"
      className="global-search__result"
      onClick={() => open(`/chat/channel/${encodeURIComponent(result.id)}`)}
    >
      <span className="global-search__icon" aria-hidden="true">
        <span className="material-symbols-outlined">{result.isPrivate ? "lock" : "tag"}</span>
      </span>
      <span className="global-search__result-body">
        <span className="global-search__result-title">
          <span className="global-search__sr-only">
            {result.isPrivate ? "Canal privado" : "Canal"}
          </span>{" "}
          <HighlightedText text={result.displayName} query={query} />
        </span>{" "}
        {result.description && (
          <>
            <span className="global-search__result-snippet">{result.description}</span>{" "}
          </>
        )}
        <span className="global-search__result-sub">{participantCount(result.memberCount)}</span>
      </span>
      {result.isGeneral && <span className="global-search__badge">Geral</span>}
    </button>
  );
}
