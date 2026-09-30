import { buildMessageSnippet } from "./searchHighlight";
import HighlightedText from "./HighlightedText";
import SearchAvatar from "./SearchAvatar";
import { conversationLabel, formatDateTime } from "./searchLabels";
import { openMessage, useOpenSearchResult } from "./searchNavigation";
import type { MessageSearchResult } from "./searchTypes";

interface MessageResultRowProps {
  result: MessageSearchResult;
  query: string;
}

/**
 * Opens the message's own conversation — channel, direct or group — on that
 * exact message, through the timeline's MESSAGE_TARGET deep link. chat-service
 * re-authorizes the conversation and the message on arrival.
 */
export default function MessageResultRow({ result, query }: MessageResultRowProps) {
  const open = useOpenSearchResult();
  const snippet = buildMessageSnippet(result.bodyText, query);

  return (
    <button
      type="button"
      className="global-search__result"
      onClick={() => openMessage(open, result.conversation, result.id)}
    >
      <SearchAvatar
        seed={result.senderId}
        name={result.senderDisplayName}
        url={result.senderAvatarUrl}
      />
      <span className="global-search__result-body">
        <span className="global-search__result-line">
          <span className="global-search__result-title">{result.senderDisplayName}</span>{" "}
          <span className="global-search__result-sub">
            em {conversationLabel(result.conversation)} ·{" "}
            <time dateTime={result.createdAt}>{formatDateTime(result.createdAt)}</time>
          </span>
        </span>{" "}
        <span className="global-search__result-snippet">
          <HighlightedText text={snippet} query={query} />
        </span>
      </span>
    </button>
  );
}
