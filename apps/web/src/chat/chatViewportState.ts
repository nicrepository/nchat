/**
 * Pure viewport/read-boundary logic for #492 — no React, no DOM, unit-testable
 * in isolation. Eligibility mirrors
 * services/chat-service/internal/storage/conversation_read_state_store.go's
 * UnreadCounts SQL exactly: active, non-own, no kind exclusion (system
 * messages count too). Never infer order from UUID — callers must already
 * pass messages in the backend's own ascending (created_at, id) order.
 */

export type ViewportPhase =
  | "AT_BOTTOM"
  | "AT_FIRST_UNREAD"
  | "READING_HISTORY"
  | "RESTORING_POSITION"
  | "SCROLLING_TO_BOTTOM";

/** Matches the pre-existing ws_append near-bottom threshold this file already used. */
export const BOTTOM_THRESHOLD_PX = 150;

// ponytail: bounded backward search instead of a real read-cursor endpoint.
// Upgrade path: a #687 boundary/cursor endpoint removes the need to search.
export const MAX_BOUNDARY_SEARCH_PAGES = 10;

export function isNearBottom(
  scrollHeight: number,
  scrollTop: number,
  clientHeight: number,
  thresholdPx: number = BOTTOM_THRESHOLD_PX,
): boolean {
  return scrollHeight - scrollTop - clientHeight <= thresholdPx;
}

export function isEligibleUnreadMessage(
  message: { status: string; senderId: string },
  currentUserId: string,
): boolean {
  return message.status === "active" && message.senderId !== currentUserId;
}

export function countEligibleUnread(
  messages: readonly { status: string; senderId: string }[],
  currentUserId: string,
): number {
  let count = 0;
  for (const message of messages) if (isEligibleUnreadMessage(message, currentUserId)) count++;
  return count;
}

export interface UnreadBoundary {
  messageId: string;
  index: number;
}

export function findFirstUnreadBoundary(
  messages: readonly { id: string; status: string; senderId: string }[],
  currentUserId: string,
  unreadCount: number,
): UnreadBoundary | null {
  if (unreadCount <= 0) return null;
  let remaining = unreadCount;
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i];
    if (!isEligibleUnreadMessage(message, currentUserId)) continue;
    remaining--;
    if (remaining === 0) return { messageId: message.id, index: i };
  }
  return null;
}

export function formatPendingCount(n: number): string {
  return n > 99 ? "99+" : String(n);
}

export function goToBottomAccessibleName(pendingCount: number): string {
  if (pendingCount <= 0) return "Ir para o final da conversa";
  return `Ir para o final da conversa, ${formatPendingCount(pendingCount)} novas mensagens`;
}
