/**
 * Leaving the search for a result (issue #900).
 *
 * The page provides `openResult`, which first records the current query and
 * tab on the search's own history entry and then navigates. Back therefore
 * lands on the same search, with the same results, without the query ever
 * reaching the URL — where it would sit in history, in server access logs and
 * in anything the address is pasted into. A row rendered outside the page
 * falls back to plain navigation.
 */

import { createContext, useContext } from "react";
import { useNavigate, type NavigateOptions } from "react-router";

import { withMessageJump } from "../chat/useConversationTarget";
import type { RestoredSearch } from "./useGlobalSearch";
import { SEARCH_CATEGORIES, type ConversationRef, type SearchTab } from "./searchTypes";

export type OpenResult = (to: string, options?: NavigateOptions) => void;

export const SearchNavigationContext = createContext<OpenResult | null>(null);

export function useOpenSearchResult(): OpenResult {
  const navigate = useNavigate();
  return useContext(SearchNavigationContext) ?? navigate;
}

export function conversationPath(conversation: ConversationRef): string {
  return `/chat/${conversation.kind}/${encodeURIComponent(conversation.id)}`;
}

/**
 * Opens a conversation on one message through the RF-09 deep link, marked as
 * an explicit jump request (#896/#880) so the timeline travels to that message
 * — MESSAGE_TARGET — instead of restoring a saved position or the tail.
 */
export function openMessage(open: OpenResult, conversation: ConversationRef, messageId: string) {
  open(`${conversationPath(conversation)}?message=${encodeURIComponent(messageId)}`, {
    state: withMessageJump(undefined),
  });
}

const STATE_KEY = "globalSearch";
const MAX_RESTORED_QUERY = 512;
const TABS: readonly string[] = ["all", ...SEARCH_CATEGORIES];

/**
 * Records `search` on a history state, owned by `owner` — the identity the
 * session had confirmed when it was left (CQ-07). The identity, user and
 * workspace ids, is what the sidebar already holds: no token, no credential,
 * and like the query it never reaches the URL.
 */
export function withRestoredSearch(
  state: unknown,
  search: RestoredSearch,
  owner: string | null,
): Record<string, unknown> {
  const base = typeof state === "object" && state !== null ? state : {};
  return { ...base, [STATE_KEY]: { ...search, owner } };
}

function savedSearch(state: unknown): Record<string, unknown> | undefined {
  if (typeof state !== "object" || state === null) return undefined;
  const saved = (state as Record<string, unknown>)[STATE_KEY];
  return typeof saved === "object" && saved !== null
    ? (saved as Record<string, unknown>)
    : undefined;
}

/**
 * Reads a search back from history state for `owner`, accepting only
 * well-formed values. A search is only ever restored for the identity that
 * left it: one saved under another identity, under none confirmed, or before
 * searches had owners is nobody's to restore (CQ-07).
 */
export function readRestoredSearch(
  state: unknown,
  owner: string | null,
): RestoredSearch | undefined {
  const saved = savedSearch(state);
  if (!saved || owner === null || saved.owner !== owner) return undefined;
  const { query, tab } = saved;
  if (typeof query !== "string" || query.length > MAX_RESTORED_QUERY) return undefined;
  if (typeof tab !== "string" || !TABS.includes(tab)) return undefined;
  return { query, tab: tab as SearchTab };
}
