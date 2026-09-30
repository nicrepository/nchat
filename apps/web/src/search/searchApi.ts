/**
 * Global search API client (RF-15, issue #900).
 *
 * Talks to the search-service endpoints via the existing authenticatedFetch
 * (Bearer token injection + 401 refresh-and-retry already handled there).
 * Wire shapes match services/search-service/internal/domain/search.go exactly
 * — no field beyond what the backend returns is read or forwarded, and the
 * request carries the query only: who is asking is the session, never a
 * parameter.
 */

import { ApiRequestError } from "../lib/api";
import { authenticatedFetch } from "../lib/authClient";
import { safeAvatarUrl } from "../chat/chatApi";
import { parseAttachmentPreviewStatus, parseAttachmentStatus } from "../chat/chatTypes";
import type {
  ChannelResultResponse,
  ConversationKind,
  ConversationRef,
  ConversationType,
  FileResultResponse,
  GroupResultResponse,
  LegacyMessageResultResponse,
  MessageResultResponse,
  MessageSearchResult,
  SearchCategory,
  SearchEnvelope,
  SearchErrorKind,
  SearchResultByCategory,
  SearchResultPage,
  UserResultResponse,
} from "./searchTypes";

const SEARCH_BASE = import.meta.env.VITE_SEARCH_API_BASE_URL ?? "/api/search";

/** Maps an ApiRequestError's HTTP status to a UI-facing error kind. */
export function classifySearchError(error: unknown): SearchErrorKind {
  if (!(error instanceof ApiRequestError)) return "unknown";
  if (error.status === 400) return "bad_request";
  if (error.status === 403) return "forbidden";
  if (error.status >= 500) return "server_error";
  return "unknown";
}

export interface SearchRequestOptions {
  limit?: number;
  cursor?: string;
  signal?: AbortSignal;
}

function buildSearchParams(query: string, limit?: number, cursor?: string): URLSearchParams {
  const params = new URLSearchParams({ q: query });
  if (limit !== undefined) params.set("limit", String(limit));
  if (cursor) params.set("cursor", cursor);
  return params;
}

async function fetchSearchPage<TResponse, TResult>(
  path: string,
  query: string,
  mapItem: (item: TResponse) => TResult,
  options: SearchRequestOptions = {},
): Promise<SearchResultPage<TResult>> {
  const params = buildSearchParams(query, options.limit, options.cursor);
  const response = await authenticatedFetch<SearchEnvelope<TResponse>>(
    `${SEARCH_BASE}/${path}?${params}`,
    { method: "GET", signal: options.signal },
  );
  const page = response.data;
  return {
    items: page.data.map(mapItem),
    nextCursor: page.pagination.next_cursor,
    hasMore: page.pagination.has_more,
  };
}

const CONVERSATION_TYPES: readonly string[] = ["public", "private", "direct", "group"];

/**
 * A conversation reference from the wire. An unknown kind falls back to the
 * route that revalidates hardest (dm, which requires membership); an unknown
 * type to the most restrictive label.
 */
function mapConversation(item: {
  conversation_kind: string;
  conversation_id: string;
  conversation_type: string;
  conversation_name: string;
}): ConversationRef {
  const kind: ConversationKind = item.conversation_kind === "channel" ? "channel" : "dm";
  const fallbackType: ConversationType = kind === "channel" ? "private" : "direct";
  return {
    kind,
    id: item.conversation_id,
    type: CONVERSATION_TYPES.includes(item.conversation_type)
      ? (item.conversation_type as ConversationType)
      : fallbackType,
    name: item.conversation_name,
  };
}

const MAPPERS: { [C in SearchCategory]: (item: never) => SearchResultByCategory[C] } = {
  messages: (item: MessageResultResponse) => ({
    id: item.id,
    conversation: mapConversation(item),
    senderId: item.sender_id,
    senderDisplayName: item.sender_display_name,
    senderAvatarUrl: safeAvatarUrl(item.sender_avatar_url) ?? null,
    bodyText: item.body_text,
    createdAt: item.created_at,
    score: item.score,
  }),
  users: (item: UserResultResponse) => ({
    id: item.id,
    displayName: item.display_name,
    // Same render-time boundary as every other avatar: same-origin http(s) only.
    avatarUrl: safeAvatarUrl(item.avatar_url) ?? null,
  }),
  channels: (item: ChannelResultResponse) => ({
    id: item.id,
    slug: item.slug,
    displayName: item.display_name,
    // Anything but an explicit "public" is shown with the lock: the label
    // must never promise a channel is open when the server did not say so.
    isPrivate: item.type !== "public",
    description: item.description?.trim() || null,
    memberCount: item.member_count,
    isGeneral: item.is_general,
  }),
  groups: (item: GroupResultResponse) => ({
    id: item.id,
    title: item.title,
    participantCount: item.participant_count,
    lastMessageAt: item.last_message_at ?? null,
  }),
  files: (item: FileResultResponse) => ({
    id: item.id,
    filename: item.filename,
    contentType: item.content_type,
    size: item.size,
    status: parseAttachmentStatus(item.status),
    previewStatus: parseAttachmentPreviewStatus(item.preview_status),
    messageId: item.message_id,
    conversation: mapConversation(item),
    createdAt: item.created_at,
  }),
};

// ── Messages: V2 with a rollout fallback (#900) ────────────────────────────────
//
// /v2/messages carries every conversation kind. A search-service from before
// #900 does not have it and answers 404 — its catch-all for an unknown route —
// and then, and only then, the page comes from the legacy /messages: public
// channel messages in the channel-only shape, mapped into the same
// MessageSearchResult. 401 is the auth client's to handle, and 403, 5xx, a
// network failure or an abort are real failures shown as such, never a
// fallback. Nothing outside this module knows which endpoint answered.

/** Marks a next-page cursor issued by the legacy endpoint, so it goes back there. */
const LEGACY_CURSOR_PREFIX = "legacy:";

function mapLegacyMessage(item: LegacyMessageResultResponse): MessageSearchResult {
  return {
    id: item.id,
    // The legacy endpoint only ever searched public channels.
    conversation: {
      kind: "channel",
      id: item.channel_id,
      type: "public",
      name: item.channel_name ?? "",
    },
    senderId: item.sender_id,
    senderDisplayName: item.sender_display_name,
    senderAvatarUrl: null,
    bodyText: item.body_text,
    createdAt: item.created_at,
    score: item.score,
  };
}

async function searchLegacyMessages(
  query: string,
  options: SearchRequestOptions,
): Promise<SearchResultPage<MessageSearchResult>> {
  const page = await fetchSearchPage<LegacyMessageResultResponse, MessageSearchResult>(
    "messages",
    query,
    mapLegacyMessage,
    options,
  );
  return {
    // A row with no channel has no route; it is dropped, never guessed.
    items: page.items.filter(
      (item) => typeof item.conversation.id === "string" && item.conversation.id !== "",
    ),
    nextCursor: page.nextCursor === null ? null : `${LEGACY_CURSOR_PREFIX}${page.nextCursor}`,
    hasMore: page.hasMore,
  };
}

function isMissingEndpoint(error: unknown): boolean {
  return error instanceof ApiRequestError && error.status === 404;
}

async function searchMessages(
  query: string,
  options: SearchRequestOptions = {},
): Promise<SearchResultPage<MessageSearchResult>> {
  const { cursor } = options;
  if (cursor?.startsWith(LEGACY_CURSOR_PREFIX)) {
    return searchLegacyMessages(query, {
      ...options,
      cursor: cursor.slice(LEGACY_CURSOR_PREFIX.length),
    });
  }
  try {
    return await fetchSearchPage(
      "v2/messages",
      query,
      MAPPERS.messages as (item: unknown) => MessageSearchResult,
      options,
    );
  } catch (error) {
    // A V2 cursor means V2 answered this search before; it is never replayed
    // against the legacy endpoint, whose cursors and result set differ.
    if (!isMissingEndpoint(error) || cursor) throw error;
    return searchLegacyMessages(query, options);
  }
}

/** One page of one category. */
export function searchCategory<C extends SearchCategory>(
  category: C,
  query: string,
  options?: SearchRequestOptions,
): Promise<SearchResultPage<SearchResultByCategory[C]>> {
  if (category === "messages") {
    return searchMessages(query, options) as Promise<SearchResultPage<SearchResultByCategory[C]>>;
  }
  const map = MAPPERS[category] as (item: unknown) => SearchResultByCategory[C];
  return fetchSearchPage(category, query, map, options);
}
