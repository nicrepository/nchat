import { useNavigate } from "react-router";

import HighlightedText from "./HighlightedText";
import type { ChannelSearchResult } from "./searchTypes";

interface ChannelResultRowProps {
  result: ChannelSearchResult;
  query: string;
}

/** Navigates using the channel's id — confirmed the `channel/:id` route param, not slug. */
export default function ChannelResultRow({ result, query }: ChannelResultRowProps) {
  const navigate = useNavigate();

  function open() {
    navigate(`/chat/channel/${encodeURIComponent(result.id)}`);
  }

  return (
    <button
      type="button"
      className="global-search__result global-search__result--channel"
      onClick={open}
    >
      <div className="global-search__result-meta">
        <span className="global-search__result-title">
          #<HighlightedText text={result.displayName} query={query} />
        </span>
        <span className="global-search__result-sub">{result.slug}</span>
      </div>
      {result.isGeneral && <span className="global-search__badge">Geral</span>}
    </button>
  );
}
