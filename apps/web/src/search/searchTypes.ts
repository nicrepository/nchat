/**
 * Types for the global search feature (RF-15, issue #900).
 *
 * Wire shapes mirror the search-service contract exactly (see
 * services/search-service/internal/domain/search.go) — no field is invented
 * beyond what the backend actually returns.
 */

import type { AttachmentPreviewStatus, AttachmentStatus } from "../chat/chatTypes";

/** The five result kinds, in the fixed order the overview shows them. */
export const SEARCH_CATEGORIES = ["messages", "users", "channels", "groups", "files"] as const;
export type SearchCategory = (typeof SEARCH_CATEGORIES)[number];
export type SearchTab = "all" | SearchCategory;

/** The two conversation route shapes: /chat/channel/:id and /chat/dm/:id. */
export type ConversationKind = "channel" | "dm";
export type ConversationType = "public" | "private" | "direct" | "group";

// ── Wire shapes (search-service JSON) ──────────────────────────────────────────

/**
 * GET /api/search/messages, the pre-#900 contract: public channel messages
 * only. Read solely as a rollout fallback when /v2/messages does not exist
 * (see searchApi.ts and docs/api/search.md).
 */
export interface LegacyMessageResultResponse {
  id: string;
  channel_id: string;
  channel_name: string;
  sender_id: string;
  sender_display_name: string;
  body_text: string;
  created_at: string;
  score: number;
}

/** GET /api/search/v2/messages. */
export interface MessageResultResponse {
  id: string;
  conversation_kind: string;
  conversation_id: string;
  conversation_type: string;
  conversation_name: string;
  sender_id: string;
  sender_display_name: string;
  sender_avatar_url?: string | null;
  body_text: string;
  created_at: string;
  score: number;
}

export interface UserResultResponse {
  id: string;
  display_name: string;
  avatar_url?: string | null;
}

export interface ChannelResultResponse {
  id: string;
  slug: string;
  display_name: string;
  type: string;
  description?: string | null;
  member_count: number;
  is_general: boolean;
}

export interface GroupResultResponse {
  id: string;
  title: string;
  participant_count: number;
  last_message_at?: string | null;
}

export interface FileResultResponse {
  id: string;
  filename: string;
  content_type: string;
  size: number;
  status: string;
  preview_status: string;
  message_id: string;
  conversation_kind: string;
  conversation_id: string;
  conversation_type: string;
  conversation_name: string;
  created_at: string;
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

/** Where a message or file lives — exactly what its route needs. */
export interface ConversationRef {
  kind: ConversationKind;
  id: string;
  type: ConversationType;
  name: string;
}

export interface MessageSearchResult {
  id: string;
  conversation: ConversationRef;
  senderId: string;
  senderDisplayName: string;
  senderAvatarUrl: string | null;
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
  isPrivate: boolean;
  description: string | null;
  memberCount: number;
  isGeneral: boolean;
}

export interface GroupSearchResult {
  id: string;
  title: string;
  participantCount: number;
  lastMessageAt: string | null;
}

export interface FileSearchResult {
  id: string;
  filename: string;
  contentType: string;
  size: number;
  status: AttachmentStatus;
  previewStatus: AttachmentPreviewStatus;
  messageId: string;
  conversation: ConversationRef;
  createdAt: string;
}

export interface SearchResultByCategory {
  messages: MessageSearchResult;
  users: UserSearchResult;
  channels: ChannelSearchResult;
  groups: GroupSearchResult;
  files: FileSearchResult;
}

export interface SearchResultPage<T> {
  items: T[];
  nextCursor: string | null;
  hasMore: boolean;
}

/** Classification of a failed search request, for status-specific UI copy. */
export type SearchErrorKind = "bad_request" | "forbidden" | "server_error" | "unknown";
