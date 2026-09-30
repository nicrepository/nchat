import { useState } from "react";

import { getOrCreateDirectDM } from "../chat/chatApi";
import { presenceLabel, usePresence } from "../chat/presence";
import HighlightedText from "./HighlightedText";
import SearchAvatar from "./SearchAvatar";
import { useOpenSearchResult } from "./searchNavigation";
import type { UserSearchResult } from "./searchTypes";

interface UserResultRowProps {
  result: UserSearchResult;
  query: string;
}

/**
 * Opens a DM with this person, reusing the app's real "start a conversation"
 * flow (getOrCreateDirectDM). The profile is not the primary action (#879).
 * Presence is shown only when this session actually knows it — the store says
 * "unknown" for anyone the server has not reported, and that is not a status
 * worth printing.
 */
export default function UserResultRow({ result, query }: UserResultRowProps) {
  const open = useOpenSearchResult();
  const presence = usePresence(result.id);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState("");

  async function start() {
    if (starting) return;
    setStarting(true);
    setError("");
    try {
      const { conversationId } = await getOrCreateDirectDM(result.id);
      open(`/chat/dm/${encodeURIComponent(conversationId)}`);
    } catch {
      setError("Não foi possível abrir a conversa. Tente novamente.");
      setStarting(false);
    }
  }

  return (
    <div className="global-search__result-group">
      <button type="button" className="global-search__result" onClick={start} disabled={starting}>
        <SearchAvatar seed={result.id} name={result.displayName} url={result.avatarUrl} />
        <span className="global-search__result-body">
          <span className="global-search__result-title">
            <HighlightedText text={result.displayName} query={query} />
          </span>{" "}
          {presence !== "unknown" && (
            <span className="global-search__result-sub">{presenceLabel(presence)}</span>
          )}
        </span>
      </button>
      {error && (
        <span className="global-search__result-error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}
