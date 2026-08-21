import { useState } from "react";
import { useNavigate } from "react-router";

import { getOrCreateDirectDM } from "../chat/chatApi";
import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import HighlightedText from "./HighlightedText";
import type { UserSearchResult } from "./searchTypes";

interface UserResultRowProps {
  result: UserSearchResult;
  query: string;
}

/**
 * Opens a DM with this person, reusing the app's real "start a conversation"
 * flow (getOrCreateDirectDM) — there is no route for another user's profile
 * (see ConversationDetailsPanel.tsx), so no new flow is invented here.
 */
export default function UserResultRow({ result, query }: UserResultRowProps) {
  const navigate = useNavigate();
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState("");

  async function open() {
    if (starting) return;
    setStarting(true);
    setError("");
    try {
      const { conversationId } = await getOrCreateDirectDM(result.id);
      navigate(`/chat/dm/${encodeURIComponent(conversationId)}`);
    } catch {
      setError("Não foi possível abrir a conversa. Tente novamente.");
      setStarting(false);
    }
  }

  return (
    <div className="global-search__result global-search__result--user">
      <button
        type="button"
        className="global-search__result-trigger"
        onClick={open}
        disabled={starting}
      >
        <span
          className={`global-search__avatar global-search__avatar--${avatarColorFor(result.id)}`}
          aria-hidden="true"
        >
          {result.avatarUrl ? (
            <img src={result.avatarUrl} alt="" referrerPolicy="no-referrer" />
          ) : (
            initialsFrom(result.displayName)
          )}
        </span>
        <span className="global-search__result-title">
          <HighlightedText text={result.displayName} query={query} />
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
