/**
 * Global search API client (RF-15).
 *
 * Talks to the search-service endpoints via the existing authenticatedFetch
 * (Bearer token injection + 401 refresh-and-retry already handled there).
 * Wire shapes match services/search-service/internal/domain/search.go exactly
 * — no field beyond what the backend returns is read or forwarded.
 */

import { ApiRequestError } from "../lib/api";
import { authenticatedFetch } from "../lib/authClient";
import type {
  ChannelResultResponse,
  ChannelSearchResult,
  MessageResultResponse,
  MessageSearchResult,
  SearchEnvelope,
  SearchErrorKind,
  SearchResultPage,
  UserResultResponse,
  UserSearchResult,
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
  options: { limit?: number; cursor?: string; signal?: AbortSignal } = {},
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

function mapMessageResult(item: MessageResultResponse): MessageSearchResult {
  return {
    id: item.id,
    channelId: item.channel_id,
    channelName: item.channel_name,
    senderId: item.sender_id,
    senderDisplayName: item.sender_display_name,
    bodyText: item.body_text,
    createdAt: item.created_at,
    score: item.score,
  };
}

function mapUserResult(item: UserResultResponse): UserSearchResult {
  return {
    id: item.id,
    displayName: item.display_name,
    avatarUrl: item.avatar_url,
  };
}

function mapChannelResult(item: ChannelResultResponse): ChannelSearchResult {
  return {
    id: item.id,
    slug: item.slug,
    displayName: item.display_name,
    isGeneral: item.is_general,
  };
}

export function searchMessages(
  query: string,
  options?: { limit?: number; cursor?: string; signal?: AbortSignal },
): Promise<SearchResultPage<MessageSearchResult>> {
  return fetchSearchPage("messages", query, mapMessageResult, options);
}

export function searchUsers(
  query: string,
  options?: { limit?: number; cursor?: string; signal?: AbortSignal },
): Promise<SearchResultPage<UserSearchResult>> {
  return fetchSearchPage("users", query, mapUserResult, options);
}

export function searchChannels(
  query: string,
  options?: { limit?: number; cursor?: string; signal?: AbortSignal },
): Promise<SearchResultPage<ChannelSearchResult>> {
  return fetchSearchPage("channels", query, mapChannelResult, options);
}
