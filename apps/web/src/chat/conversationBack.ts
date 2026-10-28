/**
 * The conversation header's Back (issue #1124): where it leads from the entry
 * on screen, and the gesture itself.
 *
 * The destination is read from the browser history the app itself recorded
 * (lib/historyTrail), never guessed from `history.length` or handed blindly to
 * `history.back()`: Back travels only to an entry this document saw the app
 * create, so it can never leave the app, reach the sign-in, or land on a page
 * that was there before the app was. Travelling is a real `history.go(-n)`, so
 * the URL, the browser's own Back/Forward and the search's restorable entry
 * stay exactly what they would be had the user pressed the browser's button
 * `n` times.
 *
 * Before anything is walked, the trail has to be this identity's: the one the
 * sidebar confirmed, owning the trail, with no rotated token waiting to be
 * confirmed (BackSession). Until then nothing on it is a destination — not a
 * conversation, not the search, not the favourites: Back opens the list and
 * leaves the trail as it is, for the confirmed identity to use or discard.
 *
 * Walking back from the entry on screen:
 *  - entries of the same conversation — a `?message=` jump, a replaced entry —
 *    are not a destination: they are where the reader already is;
 *  - a conversation no longer in the sidebar — left, removed, access revoked —
 *    is skipped, so Back never offers what the user can no longer open (the
 *    server still decides access for whatever the route asks for);
 *  - a conversation whose availability nobody can vouch for yet — the sidebar
 *    loading or failed — ends the walk without travelling: Back neither opens it
 *    nor skips past it to something further back. It opens the conversation
 *    list and leaves the trail as it is, so the next press, once the sidebar
 *    answers, finds the same destination;
 *  - the search and the favourites are surfaces the user came *through* and can
 *    return to with their state, so they are a destination too;
 *  - anything else — the chat's home, the profile, the sign-in, an entry from
 *    before a reload or a deep link — ends the walk: there is no previous
 *    conversation, and Back opens the conversation list instead.
 * From search or favourites, the same walk also accepts a known Home entry
 * and skips consecutive entries of the surface already on screen.
 */

import { useCallback, useEffect, useRef } from "react";
import { useLocation, useNavigate } from "react-router";

import {
  claimHistoryTrail,
  historyTrail,
  historyTrailOwner,
  type TrailEntry,
} from "../lib/historyTrail";
import { normalizeChatTargetId } from "./chatTargetId";
import type { SidebarState } from "./useChatSidebar";
import { safeDecodeURIComponent } from "./useConversationTarget";

interface ConversationRef {
  kind: "channel" | "dm";
  id: string;
}

/** What the sidebar can say about a conversation: confirmed either way, or not yet. */
export type Availability = "available" | "unavailable" | "unknown";

const CONVERSATION_PATH = /^\/chat\/(channel|dm)\/([^/]+)$/;
const RETURN_SURFACES = new Set(["/chat/search", "/chat/favorites"]);

function withoutTrailingSlash(pathname: string): string {
  return pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
}

export function isReturnSurface(pathname: string): boolean {
  return RETURN_SURFACES.has(withoutTrailingSlash(pathname));
}

/** The conversation a path opens, identified the way ChatMessageArea identifies it. */
function conversationAt(pathname: string): ConversationRef | null {
  const match = CONVERSATION_PATH.exec(withoutTrailingSlash(pathname));
  if (!match) return null;
  return {
    kind: match[1] as ConversationRef["kind"],
    id: normalizeChatTargetId(safeDecodeURIComponent(match[2])),
  };
}

/**
 * Who is asking, as far as the trail is concerned (CQ-05): the identity the
 * sidebar confirmed (null while it cannot say), the identity the trail was
 * claimed for, and whether a rotated token is still waiting to be confirmed.
 */
export interface BackSession {
  confirmed: string | null;
  owner: string | null;
  pending: boolean;
}

/** Whether the trail on record may be walked at all: it is the confirmed identity's, settled. */
export function sessionOwnsTrail({ confirmed, owner, pending }: BackSession): boolean {
  return confirmed !== null && !pending && owner === confirmed;
}

/**
 * How many entries back the previous destination is, null when there is none,
 * or "unknown" when nobody can say yet: the session is not settled as the
 * trail's owner, or the walk reached a conversation the sidebar cannot vouch
 * for. `displayedKey` is the entry the user is looking at, which is not always
 * the history's current one: a move whose render is still suspended has
 * already created its entry, and Back from what is on screen has to count it.
 */
export function conversationBackDelta(
  trail: readonly TrailEntry[],
  displayedKey: string,
  session: BackSession,
  availability: (conversation: ConversationRef) => Availability,
): number | "unknown" | null {
  // First, before any entry is read: no destination of any kind — conversation,
  // search, favourites — is chosen from a trail this session may not own.
  if (!sessionOwnsTrail(session)) return "unknown";
  return walkBack(trail, displayedKey, availability);
}

/** The walk itself, for a trail already known to be this session's. */
function walkBack(
  trail: readonly TrailEntry[],
  displayedKey: string,
  availability: (conversation: ConversationRef) => Availability,
): number | "unknown" | null {
  const start = trail.findIndex((entry) => entry.key === displayedKey);
  const shownPath = start < 0 ? "" : withoutTrailingSlash(trail[start].pathname);
  const shown = conversationAt(shownPath);
  const fromSurface = isReturnSurface(shownPath);
  if (!shown && !fromSurface) return null;
  for (let steps = start + 1; steps < trail.length; steps += 1) {
    const { pathname } = trail[steps];
    const conversation = conversationAt(pathname);
    if (!conversation) {
      const path = withoutTrailingSlash(pathname);
      if (fromSurface && path === shownPath) continue;
      return isReturnSurface(path) || (fromSurface && path === "/chat") ? steps : null;
    }
    if (shown && conversation.kind === shown.kind && conversation.id === shown.id) continue;
    const known = availability(conversation);
    if (known !== "unavailable") return known === "available" ? steps : "unknown";
  }
  return null;
}

/**
 * The identity the sidebar was confirmed for — user and workspace — or null
 * while it cannot say. What a screen's state is owned by (#1124): never an
 * access decision, which stays with the server.
 */
export function confirmedIdentity(state: SidebarState): string | null {
  return state.status === "ready" ? `${state.currentUserId}:${state.workspaceId}` : null;
}

/**
 * The header's Back for the shell: travel to the previous destination, or —
 * with none — call `showNavigation`, which opens the conversation list. True
 * when it travels.
 *
 * A second press before the first one's move is on screen is ignored: the
 * history has already moved, and counting again from the entry still on screen
 * would travel twice as far.
 *
 * The trail is claimed for every identity the sidebar is confirmed for as soon
 * as it is — not at the press — so a trail recorded under one user is never the
 * first one another user's sidebar claims (historyTrail). Never while a
 * rotated token is being confirmed (`confirming`), nor when one started after
 * the render (CQ-13 R6): a sidebar loaded for the previous token claims nothing
 * for the current one until the server says the token is that identity's.
 */
export function useConversationBack(
  sidebar: SidebarState,
  identityPending: () => boolean,
  confirming: boolean,
  showNavigation: () => void,
): () => boolean {
  const { key } = useLocation();
  const navigate = useNavigate();
  const travelling = useRef(false);
  useEffect(() => {
    travelling.current = false;
  }, [key]);
  const identity = confirmedIdentity(sidebar);
  const settled = confirming ? null : identity;
  useEffect(() => {
    if (settled !== null && !identityPending()) claimHistoryTrail(settled);
  }, [settled, identityPending]);
  return useCallback(() => {
    if (travelling.current) return false;
    // Read at the press, not from the last render: a rotation starts between
    // renders, and the press must already see it pending.
    const session = { confirmed: identity, owner: historyTrailOwner(), pending: identityPending() };
    const delta = conversationBackDelta(historyTrail(), key, session, ({ kind, id }) => {
      if (sidebar.status !== "ready") return "unknown";
      const rows = kind === "channel" ? sidebar.channels : sidebar.dms;
      return rows.some((item) => item.id === id) ? "available" : "unavailable";
    });
    if (typeof delta !== "number") {
      showNavigation();
      return false;
    }
    travelling.current = true;
    void navigate(-delta);
    return true;
  }, [key, sidebar, identity, identityPending, navigate, showNavigation]);
}

/**
 * The button's side of the gesture: the press, and the focus after it.
 *
 * Details open over the conversation are not this button's: where they cover
 * it, the panel's own close takes its place, at the same spot, and is what Back
 * means there (ConversationDetailsPanel); where they sit beside it, the header
 * offers no Back at all.
 *
 * Arriving by Back is arriving: the composer's own first-focus rule applies as
 * for a conversation picked in the sidebar. The one case it does not cover is a
 * channel ↔ DM trip, where the header itself is swapped and the pressed button
 * with it — focus would drop to <body>, so it goes to the new header's Back.
 * Anything that already holds focus keeps it.
 */
export function useBackButton(goBack: (() => boolean) | undefined, destination: string) {
  const ref = useRef<HTMLButtonElement>(null);
  const travelled = useRef(false);
  useEffect(() => {
    if (!travelled.current) return;
    travelled.current = false;
    if (document.activeElement === document.body) ref.current?.focus();
  }, [destination]);
  const onBack = useCallback(() => {
    if (goBack?.() === true) travelled.current = true;
  }, [goBack]);
  return { ref, onBack: goBack ? onBack : undefined };
}
