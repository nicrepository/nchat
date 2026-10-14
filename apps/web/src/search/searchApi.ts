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
import { safeAvatarUrl } from "../chat/avatarUrl";
import { parseAttachmentPreviewStatus, parseAttachmentStatus } from "../chat/chatTypes";
import type {
  ChannelResultResponse,
  ConversationKind,
  ConversationRef,
  ConversationType,
  FileResultResponse,
  GroupResultResponse,
  LinkResultResponse,
  MessageResultResponse,
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
  // A route or a method this search-service does not have: a rolling deploy
  // serving a new web against an older service (docs/api/search.md). Never
  // "no results".
  if (error.status === 404 || error.status === 405) return "unavailable";
  if (error.status >= 500) return "server_error";
  return "unknown";
}

export interface SearchRequestOptions {
  limit?: number;
  cursor?: string;
  signal?: AbortSignal;
}

/**
 * One page, always as a POST with q/limit/cursor in the body (#1081). The
 * query typed in the field may be a whole URL, a token or any other secret —
 * no rule can tell which — so it never travels in a URL: not on the first
 * page, not on the next, not on a retry. A search-service that does not take
 * the body (404/405, an older release mid-rollout) leaves the category
 * "unavailable"; there is no GET to fall back to (docs/api/search.md).
 */
async function fetchSearchPage<TResponse, TResult>(
  path: string,
  query: string,
  mapItem: (item: TResponse) => TResult,
  { limit, cursor, signal }: SearchRequestOptions,
): Promise<SearchResultPage<TResult>> {
  const body = JSON.stringify({ q: query, limit, cursor: cursor || undefined });
  const response = await authenticatedFetch<SearchEnvelope<TResponse>>(`${SEARCH_BASE}/${path}`, {
    method: "POST",
    body,
    signal,
  });
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
  links: (item: LinkResultResponse) => ({
    id: `${item.message_id}:${item.target_key}`,
    messageId: item.message_id,
    url: item.url,
    hostname: item.hostname,
    conversation: mapConversation(item),
    senderId: item.sender_id,
    senderDisplayName: item.sender_display_name,
    createdAt: item.created_at,
  }),
};

/** Messages are searched on V2: every conversation kind (#900). */
const PATHS: { [C in SearchCategory]: string } = {
  messages: "v2/messages",
  users: "users",
  channels: "channels",
  groups: "groups",
  files: "files",
  links: "links",
};

/** One page of one category. */
export function searchCategory<C extends SearchCategory>(
  category: C,
  query: string,
  options: SearchRequestOptions = {},
): Promise<SearchResultPage<SearchResultByCategory[C]>> {
  const map = MAPPERS[category] as (item: unknown) => SearchResultByCategory[C];
  return fetchSearchPage(PATHS[category], query, map, options);
}
