/**
 * Where a conversation opens (#492 cases A/B/C), decided as a pure function
 * (issue #834).
 *
 * Priority: an explicit deep link (focusMessageId, owned by the jump effect) >
 * a saved history anchor > the first unread message > the bottom.
 *
 * A saved anchor or an unread boundary outside the currently loaded window
 * asks for another page instead of guessing — the existing loadMore/
 * beforeCursor pagination, reused rather than a new endpoint. That search is
 * capped at MAX_BOUNDARY_SEARCH_PAGES: exhausting it while more history
 * remains falls back to the bottom rather than rendering a wrong boundary
 * (#492 item 9 — a safe fallback, never an infinite search or a crash).
 *
 * Pure and total on purpose: every one of these endings is reachable from a
 * plain object, so none of them needs a rendered timeline to be exercised.
 */

import {
  findFirstUnreadBoundary,
  MAX_BOUNDARY_SEARCH_PAGES,
  type ViewportPhase,
} from "../../chatViewportState";
import type { ViewportAnchor } from "../../chatViewportPersistence";
import type { Message } from "../../chatTypes";

export type OpenPosition =
  /** A deep link owns positioning; resolution only records that it did. */
  | { kind: "deep-link" }
  /** Put the reader back where they left this conversation. */
  | { kind: "anchor"; messageId: string }
  /** Land on the "Novas mensagens" separator above this message. */
  | { kind: "first-unread"; messageId: string }
  /** The newest message. */
  | { kind: "bottom" }
  /** Not answerable from the loaded window yet — fetch one more page. */
  | { kind: "need-more-history" };

export interface OpenPositionInput {
  messages: Message[];
  currentUserId: string;
  hasMore: boolean;
  /** The sidebar's unread_count for this target as of opening it. */
  unreadCountAtOpen: number;
  /** The anchor this conversation was left at, if any. */
  initialAnchor: ViewportAnchor | null;
  /** A `?message=` deep link, which outranks everything else. */
  focusMessageId?: string;
  /** How many extra pages the bounded backward search has already asked for. */
  searchAttempts: number;
}

/** Whether the bounded backward search may ask for one more page. */
function canSearchFurther(input: OpenPositionInput): boolean {
  return input.hasMore && input.searchAttempts < MAX_BOUNDARY_SEARCH_PAGES;
}

/**
 * Case B: a saved reading position. Null when this conversation has none, or
 * has one that was saved at the bottom — both fall through to the unread
 * check below rather than being an answer of their own.
 */
function fromSavedAnchor(input: OpenPositionInput): OpenPosition | null {
  const anchor = input.initialAnchor;
  if (!anchor || anchor.atBottom || !anchor.anchorMessageId) return null;
  const messageId = anchor.anchorMessageId;
  if (input.messages.some((message) => message.id === messageId)) {
    return { kind: "anchor", messageId };
  }
  if (canSearchFurther(input)) return { kind: "need-more-history" };
  // The anchored message no longer exists and the search is exhausted — fall
  // through to the unread/bottom fallback rather than guessing.
  return null;
}

/**
 * Case C: the first message this reader has not seen. Null when there is
 * nothing unread, or when the boundary is still out of reach and the search
 * cap has been spent — never a guessed boundary.
 */
function fromFirstUnread(input: OpenPositionInput): OpenPosition | null {
  if (input.unreadCountAtOpen <= 0) return null;
  const boundary = findFirstUnreadBoundary(
    input.messages,
    input.currentUserId,
    input.unreadCountAtOpen,
  );
  if (boundary) return { kind: "first-unread", messageId: boundary.messageId };
  if (canSearchFurther(input)) return { kind: "need-more-history" };
  if (input.hasMore) return null;
  // Whole history loaded and still short of unreadCountAtOpen (a stale
  // count/race) — anchor at the oldest loaded message rather than guessing
  // further.
  return { kind: "first-unread", messageId: input.messages[0].id };
}

/** The one decision, in priority order. */
export function resolveOpenPosition(input: OpenPositionInput): OpenPosition {
  if (input.focusMessageId) return { kind: "deep-link" };
  return fromSavedAnchor(input) ?? fromFirstUnread(input) ?? { kind: "bottom" };
}

// ── What this render of the conversation has to do about it ─────────────────

/** Where the one instant positioning scroll should land, once decided. */
export type ScrollTarget = { messageId: string | null } | undefined;

/**
 * What the opening resolution asks of *this* render.
 *
 * The three are exhaustive and mutually exclusive, which is the point: the hook
 * that applies them never has to re-derive why it is doing what it is doing,
 * and every path — including the two that do nothing — is reachable from a
 * plain object in a test.
 */
export type OpenPositionResolution =
  /** Nothing to do: already settled, nothing loaded, or a page already asked for. */
  | { kind: "wait" }
  /** Ask for one more page, and record the length that asked for it. */
  | { kind: "search"; searchedLength: number }
  /** The position is decided; these are the values it decides. */
  | {
      kind: "settle";
      /** Null unless the conversation opens on an unread boundary. */
      firstUnreadMessageId: string | null;
      scrollTarget: ScrollTarget;
      phase: ViewportPhase;
    };

export interface ResolutionInput extends OpenPositionInput {
  /** Whether the opening position has already been settled. */
  resolved: boolean;
  /**
   * The messages.length that last asked for a page. Comparing against it is
   * what guarantees at most one request per distinct length, so an incidental
   * extra render can never double-fetch — and it belongs to the decision, not
   * to the hook, because "should I ask again" is the same question as "what
   * should happen now".
   */
  searchedForLength: number;
}

/** A deep link positions itself; every other answer names where to land. */
function scrollTargetFor(position: SettledPosition): ScrollTarget {
  if (position.kind === "deep-link") return undefined;
  if (position.kind === "bottom") return { messageId: null };
  return { messageId: position.messageId };
}

function phaseFor(position: SettledPosition): ViewportPhase {
  if (position.kind === "first-unread") return "AT_FIRST_UNREAD";
  if (position.kind === "bottom") return "AT_BOTTOM";
  return "READING_HISTORY";
}

/** Every answer that positions the viewport itself, rather than fetching more. */
type SettledPosition = Exclude<OpenPosition, { kind: "need-more-history" }>;

export function decideOpenPositionResolution(input: ResolutionInput): OpenPositionResolution {
  if (input.resolved || input.messages.length === 0) return { kind: "wait" };
  const position = resolveOpenPosition(input);
  if (position.kind !== "need-more-history") {
    return {
      kind: "settle",
      firstUnreadMessageId: position.kind === "first-unread" ? position.messageId : null,
      scrollTarget: scrollTargetFor(position),
      phase: phaseFor(position),
    };
  }
  if (input.searchedForLength === input.messages.length) return { kind: "wait" };
  return { kind: "search", searchedLength: input.messages.length };
}
