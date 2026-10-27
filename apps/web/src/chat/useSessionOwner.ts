/**
 * Whose server data a screen may hold and show (#1124: CQ-07, CQ-09, CQ-10).
 *
 * The one source is the identity the sidebar confirmed, as ChatShell hands it
 * to its routes (`sessionIdentity`): null while the sidebar loads, has failed,
 * or a rotated token is being confirmed. A screen that loads private data —
 * the search, the favourites — keys it on `owner`, the identity last
 * confirmed while it was on screen:
 *  - no identity confirmed yet (`owner` null): nothing is requested and nothing
 *    is shown. Credentials alone are no owner — a token can be another
 *    account's (a rotation through the shared refresh cookie) — so what a
 *    request made with them returns could not be told apart from the next
 *    identity's;
 *  - the identity in doubt again (`settled` false): what the screen holds is
 *    kept off screen, and nothing it requested before is trusted after;
 *  - another identity confirmed: `owner` changes, which a screen keyed on it
 *    remounts on — its state, and its requests in flight, go with the old one.
 *
 * Settled during render, so no committed frame shows one identity's state
 * under another. Never an access decision: that stays with the server.
 */

import { useState } from "react";
import { useOutletContext } from "react-router";

import type { ChatOutletContext } from "./ChatShell";

export interface SessionOwner {
  /** The identity last confirmed while the screen was on, null before any. */
  owner: string | null;
  /** The owner is the confirmed identity right now: no doubt pending. */
  settled: boolean;
  /** No identity because the sidebar failed to load: only a retry brings one. */
  failed: boolean;
  retry?: () => void;
}

export function useSessionOwner(): SessionOwner {
  const context = useOutletContext<ChatOutletContext | null>();
  const identity = context?.sessionIdentity ?? null;
  const [owner, setOwner] = useState(identity);
  if (identity !== null && identity !== owner) setOwner(identity);
  return {
    owner: identity ?? owner,
    settled: identity !== null,
    // Not loading, and no ready sidebar (ChatShell's stand-in has no user):
    // the sidebar failed. A token being confirmed keeps the sidebar ready.
    failed: identity === null && context?.sidebarLoading === false && context.currentUserId === "",
    retry: context?.refreshConversations,
  };
}
