import HighlightedText from "./HighlightedText";
import { conversationLabel, formatDateTime } from "./searchLabels";
import { openMessage, useOpenSearchResult } from "./searchNavigation";
import type { LinkSearchResult } from "./searchTypes";

interface LinkResultRowProps {
  result: LinkSearchResult;
  query: string;
}

/**
 * A link shared in a message (#1081). The card opens the message it was shared
 * in, through the same MESSAGE_TARGET deep link as a message result — never
 * the URL itself. Leaving NChat stays with the timeline's link chip, where Link
 * Safety decides direct, interstitial or none (docs/api/link-safety.md), so
 * this card has no href of its own to get wrong. The URL is shown whole in the
 * DOM and only clipped visually, so its accessible name is the full address.
 */
export default function LinkResultRow({ result, query }: LinkResultRowProps) {
  const open = useOpenSearchResult();

  return (
    <button
      type="button"
      className="global-search__result"
      onClick={() => openMessage(open, result.conversation, result.messageId)}
    >
      <span className="global-search__icon" aria-hidden="true">
        <span className="material-symbols-outlined">link</span>
      </span>
      <span className="global-search__result-body">
        <span className="global-search__result-title">
          <span className="global-search__sr-only">Link</span>{" "}
          <HighlightedText text={result.hostname} query={query} />
        </span>{" "}
        <span className="global-search__result-url">
          <HighlightedText text={result.url} query={query} />
        </span>{" "}
        <span className="global-search__result-sub">
          {conversationLabel(result.conversation)} · {result.senderDisplayName} ·{" "}
          <time dateTime={result.createdAt}>{formatDateTime(result.createdAt)}</time>
        </span>
      </span>
    </button>
  );
}
