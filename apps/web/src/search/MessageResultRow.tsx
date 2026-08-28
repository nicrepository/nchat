import { useNavigate } from "react-router";

import { buildMessageSnippet } from "./searchHighlight";
import HighlightedText from "./HighlightedText";
import type { MessageSearchResult } from "./searchTypes";

function formatDateTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleString("pt-BR", { dateStyle: "short", timeStyle: "short" });
}

interface MessageResultRowProps {
  result: MessageSearchResult;
  query: string;
}

/**
 * Navigates to the channel and jumps to the message, reusing the exact
 * mechanism ChatMessageArea already implements for reply/reference jumps
 * (the `?message=` query param it reads at ChatMessageArea.tsx:918).
 */
export default function MessageResultRow({ result, query }: MessageResultRowProps) {
  const navigate = useNavigate();
  const snippet = buildMessageSnippet(result.bodyText, query);

  function open() {
    navigate(
      `/chat/channel/${encodeURIComponent(result.channelId)}?message=${encodeURIComponent(result.id)}`,
    );
  }

  return (
    <button
      type="button"
      className="global-search__result global-search__result--message"
      onClick={open}
    >
      <div className="global-search__result-meta">
        <span className="global-search__result-title">{result.senderDisplayName}</span>
        <span className="global-search__result-sub">
          em #{result.channelName} · {formatDateTime(result.createdAt)}
        </span>
      </div>
      <p className="global-search__result-snippet">
        <HighlightedText text={snippet} query={query} />
      </p>
    </button>
  );
}
