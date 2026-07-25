/**
 * Chat API client.
 *
 * Fetches sidebar data from GET /api/chat/sidebar using authenticatedFetch.
 * Tokens are never stored or passed via URL; auth is handled by the existing
 * session/cookie mechanism in authenticatedFetch.
 *
 * chatFixtures.ts is TEST-ONLY and must never be imported here.
 * User-scoped data is never cached across calls. The immutable, non-sensitive
 * reaction allowlist is the sole exception and is deduplicated for the app lifetime.
 */

import { authenticatedFetch } from "../lib/authClient";
import { ApiRequestError } from "../lib/api";
import { onAuthChange } from "../lib/authSession";
import {
  normalizeBodyFormat,
  type Channel,
  type DMCandidate,
  type DirectDMResult,
  type MessageBodyFormat,
  type MentionCandidate,
  type DMConversation,
  type FavoriteItem,
  type FavoritesPage,
  type Message,
  type MessageEditHistoryEntry,
  type MessagePage,
  type PinnedItem,
} from "./chatTypes";

const CHAT_BASE = import.meta.env.VITE_CHAT_API_BASE_URL ?? "/api/chat";

// ── API response shapes ───────────────────────────────────────────────────────

interface SidebarChannelResponse {
  id: string;
  slug: string;
  display_name: string;
  type: "public" | "private";
  is_general: boolean;
  /** Optional only while pre-RF-08 sidebar responses remain in rolling deploys. */
  can_write?: unknown;
}

interface SidebarDMCounterpartResponse {
  user_id?: unknown;
  display_name?: unknown;
  avatar_url?: unknown;
}

interface SidebarDMResponse {
  id: string;
  type: "direct" | "group";
  name: string;
  /** Absent on group DMs and on pre-counterpart server responses. */
  counterpart?: SidebarDMCounterpartResponse;
}

interface SidebarResponse {
  current_user_id: string;
  workspace: { id: string; name: string; slug: string };
  channels: SidebarChannelResponse[];
  dm_conversations: SidebarDMResponse[];
  /** Absent on servers that predate RF-01 channel creation; absent means "no". */
  can_create_channel?: unknown;
}

interface SidebarEnvelope {
  data: SidebarResponse;
}

interface DMCandidateEnvelope {
  data: { candidates: Array<{ user_id: string; display_name: string }> };
}

interface DirectDMEnvelope {
  data: { conversation_id: string; created: boolean };
}

interface GroupDMEnvelope {
  data: { conversation_id: string };
}

interface CreateChannelEnvelope {
  data: { id: string; slug: string; display_name: string; type: "public" | "private" };
}

interface AllowedReactionEmojisEnvelope {
  data: { emojis: unknown };
}

let allowedReactionEmojisRequest: Promise<string[]> | null = null;

export function resetAllowedReactionEmojisCache(): void {
  allowedReactionEmojisRequest = null;
}

// ponytail: authSession already owns session-change notifications; reuse it
// instead of adding chat-specific logout plumbing.
onAuthChange(resetAllowedReactionEmojisCache);

// ── Internal fetch (no cross-request caching) ─────────────────────────────────

async function fetchSidebar(): Promise<SidebarResponse> {
  const res = await authenticatedFetch<SidebarEnvelope>(`${CHAT_BASE}/sidebar`, {
    method: "GET",
  });
  return res.data;
}

function mapSidebarChannel(ch: SidebarChannelResponse): Channel {
  return {
    id: ch.id,
    name: ch.display_name || ch.slug,
    type: ch.type,
    canWrite: ch.can_write === true,
  };
}

/** Guards against a hostile payload turning an attribute into a memory hog. */
const maxAvatarUrlLength = 512;

/**
 * Accepts an avatar URL only when it points at this very origin.
 *
 * Same-origin is the policy, not merely a consequence of the CSP: the deployed
 * `img-src 'self'` would block a third-party image anyway, and loading one
 * would leak the viewer's IP and referer to whoever hosts it — an avatar is a
 * perfectly good tracking pixel. Rejecting cross-origin here means the UI never
 * renders an <img> that is destined to fail, so the initials fallback is a real
 * fallback rather than the normal outcome.
 *
 * `javascript:`, `data:`, `blob:`, `file:` and every other scheme are excluded
 * by the http(s) check; backslashes and control characters are excluded before
 * parsing because browsers normalise them in ways that can escape the origin.
 *
 * This is the render-time boundary. auth-service applies a stricter, separate
 * rule at persistence time (root-relative only), because it cannot know the
 * browser origin — the two checks guard different things and are not duplicates.
 */
function safeAvatarUrl(raw: unknown): string | undefined {
  if (typeof raw !== "string") return undefined;
  const value = raw.trim();
  if (value === "" || value.length > maxAvatarUrlLength) return undefined;
  // eslint-disable-next-line no-control-regex -- control characters are exactly what must be rejected.
  if (/[\u0000-\u0020\u007f]/.test(value)) return undefined;
  // Browsers treat "\" as "/", so "/\evil.test" and "\\evil.test" can leave the
  // origin even though URL parsing may report otherwise.
  if (value.includes("\\")) return undefined;

  const origin = currentOrigin();
  if (!origin) return undefined;
  try {
    const parsed = new URL(value, origin);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
    if (parsed.origin !== origin) return undefined;
    if (parsed.username !== "" || parsed.password !== "") return undefined;
    // The original string is returned, never the resolved absolute form, so a
    // relative path stays relative and nothing is rewritten behind the caller.
    return value;
  } catch {
    return undefined;
  }
}

/**
 * Returns the origin to validate against, or "" when there is no document —
 * server-side rendering, a worker, or a bare unit test. With no origin there is
 * no way to prove an avatar is same-origin, so callers drop it and fall back to
 * initials rather than guessing.
 */
function currentOrigin(): string {
  if (typeof window === "undefined") return "";
  const origin = window.location?.origin;
  // Some environments expose the literal "null" origin (sandboxed frames).
  return typeof origin === "string" && origin !== "" && origin !== "null" ? origin : "";
}

/**
 * A counterpart is only usable when the server sent both an ID and a name;
 * anything partial is dropped so the UI falls back to `name` + initials rather
 * than rendering a half-built identity. Servers that predate the counterpart
 * field simply yield undefined.
 */
function mapSidebarCounterpart(raw: SidebarDMCounterpartResponse | undefined) {
  if (!raw || typeof raw.user_id !== "string" || typeof raw.display_name !== "string") {
    return undefined;
  }
  if (raw.user_id === "" || raw.display_name === "") return undefined;
  return {
    userId: raw.user_id,
    displayName: raw.display_name,
    avatarUrl: safeAvatarUrl(raw.avatar_url),
  };
}

function mapSidebarDM(dm: SidebarDMResponse): DMConversation {
  return {
    id: dm.id,
    type: dm.type === "group" ? "group" : "1:1",
    name: dm.name,
    participants: [],
    counterpart: dm.type === "group" ? undefined : mapSidebarCounterpart(dm.counterpart),
  };
}

// ── Exported API ──────────────────────────────────────────────────────────────

export async function fetchChannels(): Promise<Channel[]> {
  const sidebar = await fetchSidebar();
  return (sidebar.channels ?? []).map(mapSidebarChannel);
}

export async function fetchDMs(): Promise<DMConversation[]> {
  const sidebar = await fetchSidebar();
  return (sidebar.dm_conversations ?? []).map(mapSidebarDM);
}

/**
 * Fetches the full sidebar in a single request and returns both channels and DMs.
 * Prefer this over calling fetchChannels() + fetchDMs() separately to avoid
 * making two HTTP requests per load.
 */
export async function fetchSidebarData(): Promise<{
  currentUserId: string;
  channels: Channel[];
  dms: DMConversation[];
  canCreateChannel: boolean;
}> {
  const sidebar = await fetchSidebar();
  const channels = (sidebar.channels ?? []).map(mapSidebarChannel);
  const dms = (sidebar.dm_conversations ?? []).map(mapSidebarDM);
  return {
    currentUserId: sidebar.current_user_id ?? "",
    channels,
    dms,
    // Strict equality, so anything but a literal true — absent field, string,
    // number — reads as "not allowed". The endpoint decides regardless.
    canCreateChannel: sidebar.can_create_channel === true,
  };
}

export async function searchDMCandidates(
  query: string,
  signal?: AbortSignal,
): Promise<DMCandidate[]> {
  const params = new URLSearchParams({ query: query.trim(), limit: "20" });
  const response = await authenticatedFetch<DMCandidateEnvelope>(
    `${CHAT_BASE}/dm-candidates?${params}`,
    { method: "GET", signal },
  );
  return response.data.candidates.map((candidate) => ({
    userId: candidate.user_id,
    displayName: candidate.display_name,
  }));
}

export async function getOrCreateDirectDM(
  otherUserId: string,
  signal?: AbortSignal,
): Promise<DirectDMResult> {
  const response = await authenticatedFetch<DirectDMEnvelope>(`${CHAT_BASE}/dms`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ other_user_id: otherUserId }),
    signal,
  });
  return {
    conversationId: response.data.conversation_id,
    created: response.data.created,
  };
}

/**
 * Creates an ad-hoc group DM (RF-02) and returns its conversation ID.
 *
 * Only the other participants are sent: the caller, the workspace and every
 * membership field are derived from the session server-side, and the endpoint
 * rejects them outright. The title is omitted — not sent as an empty string —
 * when it is blank, so the server stores NULL and applies its own fallback name.
 * De-duplication, participant eligibility and the count limits are enforced by
 * chat-service; the shaping done here is for the request body, never a check.
 */
export async function createGroupDM(
  participantUserIds: string[],
  title: string,
  signal?: AbortSignal,
): Promise<string> {
  const trimmedTitle = title.trim();
  const response = await authenticatedFetch<GroupDMEnvelope>(`${CHAT_BASE}/dms/group`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      participant_user_ids: participantUserIds,
      ...(trimmedTitle ? { title: trimmedTitle } : {}),
    }),
    signal,
  });
  return response.data.conversation_id;
}

/**
 * Creates a channel (RF-01) and returns it as the sidebar models one.
 *
 * Only the three caller-owned fields are sent: the workspace, the creator, the
 * general flag and the position are derived from the session server-side, and
 * the endpoint rejects a body that carries them. The slug is trimmed and
 * lowercased here because the server does the same before matching it against
 * its pattern — this is shaping, never a check. Permission is not consulted on
 * this side at all: a caller without the workspace role gets a 403, which the
 * dialog surfaces as such rather than as a generic failure.
 */
export async function createChannel(
  input: { slug: string; displayName: string; type: "public" | "private" },
  signal?: AbortSignal,
): Promise<Channel> {
  const response = await authenticatedFetch<CreateChannelEnvelope>(`${CHAT_BASE}/channels`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      slug: input.slug.trim().toLowerCase(),
      display_name: input.displayName.trim(),
      type: input.type,
    }),
    signal,
  });
  const created = response.data;
  return {
    id: created.id,
    name: created.display_name || created.slug,
    type: created.type,
    // The creator of a channel can write to it, but the sidebar refetch is what
    // makes this authoritative; nothing here is trusted for a write decision.
    canWrite: true,
  };
}

export function fetchAllowedReactionEmojis(): Promise<string[]> {
  if (!allowedReactionEmojisRequest) {
    const request = authenticatedFetch<AllowedReactionEmojisEnvelope>(
      `${CHAT_BASE}/reactions/allowed-emojis`,
      { method: "GET" },
    ).then((response) => {
      const emojis = response.data.emojis;
      return Array.isArray(emojis)
        ? emojis.filter((emoji): emoji is string => typeof emoji === "string")
        : [];
    });
    allowedReactionEmojisRequest = request;
    void request.catch(() => {
      if (allowedReactionEmojisRequest === request) allowedReactionEmojisRequest = null;
    });
  }
  return allowedReactionEmojisRequest;
}

// ── Message API response shapes ───────────────────────────────────────────────

interface MessageResponse {
  id: string;
  sender_id: string;
  sender_display_name?: string;
  sender_email?: string;
  kind: string;
  body_text?: string;
  body_format?: string;
  is_removed?: boolean;
  status: string;
  deleted_at?: string | null;
  created_at: string;
  updated_at: string;
  edited_at?: string | null;
  edit_count?: number;
  is_edited?: boolean;
  reactions?: Array<{ emoji: string; count: number; reacted_by_me: boolean }>;
  is_favorited?: boolean;
  /** Current servers always send this; optionality accepts pre-RF-08 responses. */
  is_forwarded?: unknown;
  quoted?: QuoteResponse;
  reference?: ReferenceResponse;
}

interface QuoteResponse {
  id: string;
  author_id: string;
  body?: string;
  body_format?: MessageBodyFormat;
  is_removed?: boolean;
  deleted_at?: string;
  created_at: string;
}

interface ReferenceResponse {
  available: boolean;
  message_id?: string;
  target_type?: "channel" | "dm";
  target_id?: string;
  target_label?: string;
  author_display_name?: string;
  body?: string;
  body_format?: MessageBodyFormat;
  created_at?: string;
}

interface MessageReferenceResolutionsEnvelope {
  data: {
    references: Array<{ message_id: string; reference: ReferenceResponse }>;
  };
}

interface FavoriteResponse {
  message: MessageResponse;
  channel_id?: string;
  dm_conversation_id?: string;
  favorited_at: string;
}

interface FavoritesEnvelope {
  data: { favorites: FavoriteResponse[]; next_cursor?: string };
}

interface PinResponse {
  message: MessageResponse;
  pinned_by_user_id: string;
  pinned_at: string;
}

interface PinsEnvelope {
  data: { pins: PinResponse[]; total_count?: number };
}

export type PinTarget = { kind: "channel" | "dm"; id: string };

interface MessageListData {
  messages: MessageResponse[];
  next_cursor?: string;
}

interface MessageListEnvelope {
  data: MessageListData;
}

interface MessageEnvelope {
  data: MessageResponse;
}

interface MessageEditHistoryResponse {
  body: string;
  body_format: string;
  versioned_at: string;
}

interface MessageEditHistoryEnvelope {
  data: { history?: MessageEditHistoryResponse[]; offset?: number };
}

export type MessageEditErrorReason = "forbidden" | "not_found" | "window_expired" | "rate_limited";

export class MessageEditError extends Error {
  readonly status: number;
  readonly reason: MessageEditErrorReason;

  constructor(status: number, reason: MessageEditErrorReason, message: string) {
    super(message);
    this.name = "MessageEditError";
    this.status = status;
    this.reason = reason;
  }
}

interface MentionCandidateResponse {
  type: "user" | "channel";
  id: string;
  label: string;
}

interface MentionEnvelope {
  data: { users: MentionCandidateResponse[]; channels: MentionCandidateResponse[] };
}

// Fail-safe shape guards for the mentions response: the backend contract can
// drift or return partial data, and a malformed candidate must not crash the
// composer — callers fall back to an empty list instead of throwing.
function isMentionCandidateResponse(value: unknown): value is MentionCandidateResponse {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Record<string, unknown>;
  return (
    (candidate.type === "user" || candidate.type === "channel") &&
    typeof candidate.id === "string" &&
    typeof candidate.label === "string"
  );
}

function isMentionEnvelope(value: unknown): value is MentionEnvelope {
  if (typeof value !== "object" || value === null) return false;
  const data = (value as { data?: unknown }).data;
  if (typeof data !== "object" || data === null) return false;
  const { users, channels } = data as Record<string, unknown>;
  return (
    Array.isArray(users) &&
    Array.isArray(channels) &&
    users.every(isMentionCandidateResponse) &&
    channels.every(isMentionCandidateResponse)
  );
}

// ── Internal helpers ──────────────────────────────────────────────────────────

function mapMessage(r: MessageResponse): Message {
  const isRemoved = r.is_removed === true || r.status === "deleted" || Boolean(r.deleted_at);
  return {
    id: r.id,
    senderId: r.sender_id,
    senderDisplayName: r.sender_display_name ?? "",
    senderEmail: r.sender_email ?? "",
    kind: (r.kind === "system" ? "system" : "user") as Message["kind"],
    bodyText: isRemoved ? "" : (r.body_text ?? ""),
    bodyFormat: normalizeBodyFormat(r.body_format),
    isRemoved,
    status: isRemoved ? "deleted" : "active",
    deletedAt: r.deleted_at ?? null,
    createdAt: r.created_at,
    updatedAt: r.updated_at,
    isEdited: r.is_edited ?? (r.edit_count ?? 0) > 0,
    editCount: r.edit_count ?? 0,
    editedAt: r.edited_at ?? undefined,
    reactions: (r.reactions ?? []).map((reaction) => ({
      emoji: reaction.emoji,
      count: reaction.count,
      reactedByMe: reaction.reacted_by_me,
    })),
    isFavorited: r.is_favorited ?? false,
    isForwarded: r.is_forwarded === true,
    quoted: !isRemoved && r.quoted ? mapQuote(r.quoted) : undefined,
    reference: !isRemoved && r.reference ? mapReference(r.reference) : undefined,
  };
}

function bodyFormatVersion(format: string | undefined): number {
  const normalized = normalizeBodyFormat(format);
  return normalized === "v3" ? 3 : normalized === "v2" ? 2 : 1;
}

function mapMessageEditError(error: unknown): never {
  if (error instanceof ApiRequestError) {
    const reasonByStatus: Partial<Record<number, MessageEditErrorReason>> = {
      403: "forbidden",
      404: "not_found",
      409: "window_expired",
      429: "rate_limited",
    };
    const reason = reasonByStatus[error.status];
    if (reason) throw new MessageEditError(error.status, reason, error.message);
  }
  throw error;
}

function mapQuote(r: QuoteResponse): Message["quoted"] {
  return {
    id: r.id,
    authorId: r.author_id,
    bodyText: r.body ?? "",
    bodyFormat: normalizeBodyFormat(r.body_format),
    isRemoved: r.is_removed ?? false,
    deletedAt: r.deleted_at ?? null,
    createdAt: r.created_at ?? "",
  };
}

function mapReference(r: ReferenceResponse): NonNullable<Message["reference"]> {
  if (
    !r.available ||
    !r.message_id ||
    (r.target_type !== "channel" && r.target_type !== "dm") ||
    !r.target_id ||
    !r.created_at
  ) {
    return { available: false };
  }
  return {
    available: true,
    messageId: r.message_id,
    targetType: r.target_type,
    targetId: r.target_id,
    targetLabel: r.target_label ?? "",
    authorDisplayName: r.author_display_name ?? "",
    bodyText: r.body ?? "",
    bodyFormat: normalizeBodyFormat(r.body_format),
    createdAt: r.created_at,
  };
}

/** Returns the base path for the message collection of a channel or DM. */
export function messagesPath(kind: "channel" | "dm", id: string): string {
  const segment = kind === "channel" ? "channels" : "dm";
  return `${CHAT_BASE}/${segment}/${encodeURIComponent(id)}/messages`;
}

function buildMessagesUrl(base: string, beforeCursor?: string): string {
  if (!beforeCursor) return base;
  return `${base}?before=${encodeURIComponent(beforeCursor)}`;
}

// ── Exported message API ──────────────────────────────────────────────────────

export async function fetchChannelMessages(
  channelId: string,
  beforeCursor?: string,
  signal?: AbortSignal,
): Promise<MessagePage> {
  const url = buildMessagesUrl(messagesPath("channel", channelId), beforeCursor);
  const res = await authenticatedFetch<MessageListEnvelope>(url, { method: "GET", signal });
  return {
    messages: (res.data.messages ?? []).map(mapMessage),
    nextCursor: res.data.next_cursor ?? "",
  };
}

export async function postChannelMessage(
  channelId: string,
  bodyText: string,
  parentMessageId?: string,
  referencedMessageId?: string,
  signal?: AbortSignal,
): Promise<Message> {
  const res = await authenticatedFetch<MessageEnvelope>(messagesPath("channel", channelId), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      body_text: bodyText,
      body_format: "v3",
      ...(parentMessageId ? { parent_message_id: parentMessageId } : {}),
      ...(referencedMessageId ? { referenced_message_id: referencedMessageId } : {}),
    }),
    signal,
  });
  return mapMessage(res.data);
}

export async function forwardChannelMessage(
  destinationChannelId: string,
  sourceMessageId: string,
  idempotencyKey: string,
  signal?: AbortSignal,
): Promise<Message> {
  const response = await authenticatedFetch<MessageEnvelope>(
    `${messagesPath("channel", destinationChannelId)}/forward`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": idempotencyKey,
      },
      body: JSON.stringify({ source_message_id: sourceMessageId }),
      signal,
    },
  );
  return mapMessage(response.data);
}

export async function fetchDMMessages(
  conversationId: string,
  beforeCursor?: string,
  signal?: AbortSignal,
): Promise<MessagePage> {
  const url = buildMessagesUrl(messagesPath("dm", conversationId), beforeCursor);
  const res = await authenticatedFetch<MessageListEnvelope>(url, { method: "GET", signal });
  return {
    messages: (res.data.messages ?? []).map(mapMessage),
    nextCursor: res.data.next_cursor ?? "",
  };
}

export async function resolveChannelMessageReferences(
  channelId: string,
  messageIds: string[],
  signal?: AbortSignal,
): Promise<Record<string, NonNullable<Message["reference"]>>> {
  return resolveMessageReferences("channel", channelId, messageIds, signal);
}

export async function resolveDMMessageReferences(
  conversationId: string,
  messageIds: string[],
  signal?: AbortSignal,
): Promise<Record<string, NonNullable<Message["reference"]>>> {
  return resolveMessageReferences("dm", conversationId, messageIds, signal);
}

async function resolveMessageReferences(
  kind: "channel" | "dm",
  targetId: string,
  messageIds: string[],
  signal?: AbortSignal,
): Promise<Record<string, NonNullable<Message["reference"]>>> {
  const targetSegment = kind === "channel" ? "channels" : "dm";
  const res = await authenticatedFetch<MessageReferenceResolutionsEnvelope>(
    `${CHAT_BASE}/${targetSegment}/${encodeURIComponent(targetId)}/message-references`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message_ids: messageIds }),
      signal,
    },
  );
  return Object.fromEntries(
    (res.data.references ?? []).map((item) => [item.message_id, mapReference(item.reference)]),
  );
}

export async function fetchMentionCandidates(
  channelId: string,
  query: string,
  signal?: AbortSignal,
): Promise<MentionCandidate[]> {
  const url = `${CHAT_BASE}/channels/${encodeURIComponent(channelId)}/mentions?q=${encodeURIComponent(query)}`;
  const res = await authenticatedFetch<MentionEnvelope>(url, { method: "GET", signal });
  if (!isMentionEnvelope(res)) return [];
  return [...res.data.users, ...res.data.channels].map((candidate) => ({
    mentionType: candidate.type,
    id: candidate.id,
    label: candidate.label,
  }));
}

export async function postDMMessage(
  conversationId: string,
  bodyText: string,
  parentMessageId?: string,
  referencedMessageId?: string,
  signal?: AbortSignal,
): Promise<Message> {
  const res = await authenticatedFetch<MessageEnvelope>(messagesPath("dm", conversationId), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      body_text: bodyText,
      body_format: "v2",
      ...(parentMessageId ? { parent_message_id: parentMessageId } : {}),
      ...(referencedMessageId ? { referenced_message_id: referencedMessageId } : {}),
    }),
    signal,
  });
  return mapMessage(res.data);
}

export async function fetchChannelMessage(
  channelId: string,
  messageId: string,
  signal?: AbortSignal,
): Promise<Message> {
  const url = `${messagesPath("channel", channelId)}/${encodeURIComponent(messageId)}`;
  const res = await authenticatedFetch<MessageEnvelope>(url, { method: "GET", signal });
  return mapMessage(res.data);
}

export async function editMessage(
  messageId: string,
  body: string,
  bodyFormat: number,
): Promise<Message> {
  const wireFormat = bodyFormat === 3 ? "v3" : bodyFormat === 2 ? "v2" : "v1";
  try {
    const response = await authenticatedFetch<MessageEnvelope>(
      `${CHAT_BASE}/messages/${encodeURIComponent(messageId)}`,
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ body, body_format: wireFormat }),
      },
    );
    return mapMessage(response.data);
  } catch (error) {
    return mapMessageEditError(error);
  }
}

export async function deleteMessage(messageId: string): Promise<Message> {
  const response = await authenticatedFetch<MessageEnvelope>(
    `${CHAT_BASE}/messages/${encodeURIComponent(messageId)}`,
    { method: "DELETE" },
  );
  return mapMessage(response.data);
}

export async function getMessageHistory(
  messageId: string,
  opts: { cursor?: string; limit?: number } = {},
): Promise<{ entries: MessageEditHistoryEntry[]; nextCursor?: string }> {
  const offset = Number.parseInt(opts.cursor ?? "0", 10) || 0;
  const limit = opts.limit ?? 50;
  const params = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  try {
    const response = await authenticatedFetch<MessageEditHistoryEnvelope>(
      `${CHAT_BASE}/messages/${encodeURIComponent(messageId)}/history?${params}`,
      { method: "GET" },
    );
    const history = response.data.history ?? [];
    return {
      entries: history.map((entry) => ({
        body: entry.body,
        bodyFormat: bodyFormatVersion(entry.body_format),
        versionedAt: entry.versioned_at,
      })),
      nextCursor: history.length === limit ? String(offset + history.length) : undefined,
    };
  } catch (error) {
    return mapMessageEditError(error);
  }
}

// ── Favorites API (RF-06) ─────────────────────────────────────────────────────

function favoritePath(messageId: string): string {
  return `${CHAT_BASE}/messages/${encodeURIComponent(messageId)}/favorite`;
}

/** Favorites a message for the authenticated user. Idempotent (204). */
export async function favoriteMessage(messageId: string, signal?: AbortSignal): Promise<void> {
  await authenticatedFetch<void>(favoritePath(messageId), { method: "POST", signal });
}

/** Removes the authenticated user's favorite. Idempotent (204). */
export async function unfavoriteMessage(messageId: string, signal?: AbortSignal): Promise<void> {
  await authenticatedFetch<void>(favoritePath(messageId), { method: "DELETE", signal });
}

function mapFavorite(r: FavoriteResponse): FavoriteItem {
  return {
    message: mapMessage(r.message),
    channelId: r.channel_id ?? "",
    dmConversationId: r.dm_conversation_id ?? "",
    favoritedAt: r.favorited_at,
  };
}

/** Fetches one page of the authenticated user's own favorites, newest first. */
export async function fetchFavorites(
  beforeCursor?: string,
  signal?: AbortSignal,
): Promise<FavoritesPage> {
  const base = `${CHAT_BASE}/favorites`;
  const url = beforeCursor ? `${base}?before=${encodeURIComponent(beforeCursor)}` : base;
  const res = await authenticatedFetch<FavoritesEnvelope>(url, { method: "GET", signal });
  return {
    favorites: (res.data.favorites ?? []).map(mapFavorite),
    nextCursor: res.data.next_cursor ?? "",
  };
}

export async function fetchDMMessage(
  conversationId: string,
  messageId: string,
  signal?: AbortSignal,
): Promise<Message> {
  const url = `${messagesPath("dm", conversationId)}/${encodeURIComponent(messageId)}`;
  const res = await authenticatedFetch<MessageEnvelope>(url, { method: "GET", signal });
  return mapMessage(res.data);
}

// ── Pins API (RF-05) ──────────────────────────────────────────────────────────

function targetBasePath(target: PinTarget): string {
  const segment = target.kind === "channel" ? "channels" : "dm";
  return `${CHAT_BASE}/${segment}/${encodeURIComponent(target.id)}`;
}

function pinPath(target: PinTarget, messageId: string): string {
  return `${targetBasePath(target)}/messages/${encodeURIComponent(messageId)}/pin`;
}

/** Pins a message for the whole channel/DM. Idempotent (204). */
export async function pinMessage(
  target: PinTarget,
  messageId: string,
  signal?: AbortSignal,
): Promise<void> {
  await authenticatedFetch<void>(pinPath(target, messageId), { method: "POST", signal });
}

/** Unpins a channel/DM message. Idempotent (204). */
export async function unpinMessage(
  target: PinTarget,
  messageId: string,
  signal?: AbortSignal,
): Promise<void> {
  await authenticatedFetch<void>(pinPath(target, messageId), { method: "DELETE", signal });
}

function mapPin(r: PinResponse): PinnedItem {
  return {
    message: mapMessage(r.message),
    pinnedByUserId: r.pinned_by_user_id,
    pinnedAt: r.pinned_at,
  };
}

/** Fetches a target's pinned messages, newest pin first. */
export async function fetchPins(target: PinTarget, signal?: AbortSignal): Promise<PinnedItem[]> {
  const url = `${targetBasePath(target)}/pins`;
  const res = await authenticatedFetch<PinsEnvelope>(url, { method: "GET", signal });
  return (res.data.pins ?? []).map(mapPin);
}
