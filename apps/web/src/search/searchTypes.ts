/**
 * Types for the global search feature (RF-15).
 *
 * Wire shapes mirror the search-service contract exactly (see
 * services/search-service/internal/domain/search.go) — no field is invented
 * beyond what the backend actually returns.
 */

export type SearchTab = "messages" | "users" | "channels";

// ── Wire shapes (search-service JSON) ──────────────────────────────────────────

export interface MessageResultResponse {
  id: string;
  channel_id: string;
  channel_name: string;
  sender_id: string;
  sender_display_name: string;
  body_text: string;
  created_at: string;
  score: number;
}

export interface UserResultResponse {
  id: string;
  display_name: string;
  avatar_url: string | null;
}

export interface ChannelResultResponse {
  id: string;
  slug: string;
  display_name: string;
  is_general: boolean;
}

export interface SearchPaginationResponse {
  limit: number;
  next_cursor: string | null;
  has_more: boolean;
}

/** The inner shape written by each search-service handler (search_handler.go). */
export interface SearchPage<T> {
  data: T[];
  pagination: SearchPaginationResponse;
}

/**
 * Every nchat service wraps its JSON body in a shared {"data": ...} envelope
 * (see libs/go/platform/httputil.WriteJSON), so the actual wire response is
 * this envelope wrapping the search-service's own SearchPage.
 */
export interface SearchEnvelope<T> {
  data: SearchPage<T>;
}

// ── Domain shapes (client-side) ─────────────────────────────────────────────────

export interface MessageSearchResult {
  id: string;
  channelId: string;
  channelName: string;
  senderId: string;
  senderDisplayName: string;
  bodyText: string;
  createdAt: string;
  score: number;
}

export interface UserSearchResult {
  id: string;
  displayName: string;
  avatarUrl: string | null;
}

export interface ChannelSearchResult {
  id: string;
  slug: string;
  displayName: string;
  isGeneral: boolean;
}

export interface SearchResultPage<T> {
  items: T[];
  nextCursor: string | null;
  hasMore: boolean;
}

/** Classification of a failed search request, for status-specific UI copy. */
export type SearchErrorKind = "bad_request" | "forbidden" | "server_error" | "unknown";
