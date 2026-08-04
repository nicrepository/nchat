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
  parseDMConversationType,
  type AddMembersResult,
  type Channel,
  type ChannelDetails,
  type ChannelMemberProfile,
  type GroupDetails,
  type GroupParticipantProfile,
  type DMCandidate,
  type DirectDetails,
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
  /** Declared as the two documented values; validated as `unknown` anyway. */
  type?: unknown;
  name: string;
  /** Absent on group DMs and on pre-counterpart server responses. */
  counterpart?: SidebarDMCounterpartResponse;
}

interface SidebarResponse {
  current_user_id: string;
  workspace: { id: string; name: string; slug: string; max_upload_bytes?: number };
  channels: SidebarChannelResponse[];
  dm_conversations: SidebarDMResponse[];
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

/**
 * Maps one sidebar conversation, or drops it.
 *
 * A conversation whose `type` this build does not recognise is not rendered at
 * all. The alternative — treating "anything that is not a group" as a 1:1 — is
 * what a `?:` here would mean, and it is actively unsafe: a 1:1 is the variant
 * that gets a profile control and a request for a counterpart, so an unknown
 * type would be handed a panel describing a person the conversation may not
 * have.
 *
 * Dropping the single row rather than failing the whole payload matches the
 * other list parsers in this file (channel members, group participants): the
 * sidebar is a list of independent items, and one unrecognised conversation is
 * no reason to leave the user without the rest of theirs.
 */
function mapSidebarDM(dm: SidebarDMResponse): DMConversation | undefined {
  const type = parseDMConversationType(dm.type);
  if (type === undefined) return undefined;
  return {
    id: dm.id,
    type,
    name: dm.name,
    participants: [],
    counterpart: type === "group" ? undefined : mapSidebarCounterpart(dm.counterpart),
  };
}

function mapSidebarDMs(raw: SidebarDMResponse[] | undefined): DMConversation[] {
  return (raw ?? []).map(mapSidebarDM).filter((dm): dm is DMConversation => dm !== undefined);
}

// ── Exported API ──────────────────────────────────────────────────────────────

export async function fetchChannels(): Promise<Channel[]> {
  const sidebar = await fetchSidebar();
  return (sidebar.channels ?? []).map(mapSidebarChannel);
}

export async function fetchDMs(): Promise<DMConversation[]> {
  const sidebar = await fetchSidebar();
  return mapSidebarDMs(sidebar.dm_conversations);
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
}> {
  const sidebar = await fetchSidebar();
  const channels = (sidebar.channels ?? []).map(mapSidebarChannel);
  const dms = mapSidebarDMs(sidebar.dm_conversations);
  return { currentUserId: sidebar.current_user_id ?? "", channels, dms };
}

/**
 * The workspace's effective attachment size limit, in bytes, or null when the
 * server did not publish one (RF-32, issue #458).
 *
 * It rides on the sidebar payload the app already loads rather than on a
 * dedicated request: the value is workspace-scoped policy, and the sidebar —
 * which resolves the workspace from the caller's own session — is where the
 * workspace itself comes from. The limit therefore always belongs to the
 * workspace the session is in, and there is no path by which one workspace's
 * policy can be read for another.
 *
 * A missing, non-integer, zero or negative value returns null rather than a
 * compiled-in default. Substituting one would restate the administrative policy
 * as a number this client invented, and a workspace whose limit is not 250 MiB
 * would then be shown the wrong figure. Null means "unknown", the caller skips
 * its pre-flight check, and file-service — which re-reads the policy on every
 * upload — remains the only thing that decides.
 */
export async function fetchWorkspaceUploadLimit(): Promise<number | null> {
  const sidebar = await fetchSidebar();
  const raw = sidebar.workspace?.max_upload_bytes;
  if (typeof raw !== "number" || !Number.isSafeInteger(raw) || raw <= 0) {
    return null;
  }
  return raw;
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

/**
 * Searches people who can still be added to a channel (issue #398).
 *
 * Contextual on purpose. The workspace-wide search cannot know who is already
 * in this channel, and the details panel's member list is a presence-filtered,
 * capped preview — an offline member is simply not in it. Deciding eligibility
 * from that made existing members show up as selectable. The server excludes
 * current members in SQL, so the answer is complete regardless of what the
 * panel happens to be showing.
 *
 * Only the query and an optional limit are sent; the workspace and the actor
 * come from the session, and the channel is the path.
 */
export async function searchChannelMemberCandidates(
  channelId: string,
  query: string,
  signal?: AbortSignal,
): Promise<DMCandidate[]> {
  const params = new URLSearchParams({ query: query.trim(), limit: "20" });
  const response = await authenticatedFetch<DMCandidateEnvelope>(
    `${CHAT_BASE}/channels/${encodeURIComponent(channelId)}/member-candidates?${params}`,
    { method: "GET", signal },
  );
  return response.data.candidates.map((candidate) => ({
    userId: candidate.user_id,
    displayName: candidate.display_name,
  }));
}

/**
 * Searches people who can still be added to a group (issue #398).
 *
 * The group counterpart, and needed for the same reason with a sharper edge:
 * the group panel shows at most 30 participants, so in a larger group everyone
 * past the 30th was invisible to the client-side exclusion and came back as
 * selectable.
 */
export async function searchGroupParticipantCandidates(
  conversationId: string,
  query: string,
  signal?: AbortSignal,
): Promise<DMCandidate[]> {
  const params = new URLSearchParams({ query: query.trim(), limit: "20" });
  const response = await authenticatedFetch<DMCandidateEnvelope>(
    `${CHAT_BASE}/dm/${encodeURIComponent(conversationId)}/member-candidates?${params}`,
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
 * the endpoint rejects a body that carries them. No role, actor or workspace is
 * ever sent from the browser. The slug is trimmed and lowercased here because
 * the server does the same before matching it against its pattern — this is
 * shaping, never a check. Authorization is not consulted on this side at all: a
 * caller the server refuses gets a 403, which the dialog surfaces as such rather
 * than as a generic failure.
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

// ── Channel details (issue #435) ─────────────────────────────────────────────

interface ChannelDetailsMemberResponse {
  user_id?: unknown;
  display_name?: unknown;
  avatar_url?: unknown;
  role?: unknown;
  presence?: unknown;
}

interface ChannelDetailsEnvelope {
  data: {
    id?: unknown;
    slug?: unknown;
    display_name?: unknown;
    type?: unknown;
    created_at?: unknown;
    member_count?: unknown;
    online_member_count?: unknown;
    online_members?: unknown;
    can_manage_members?: unknown;
  };
}

function mapChannelMember(raw: unknown): ChannelMemberProfile | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const member = raw as ChannelDetailsMemberResponse;
  if (typeof member.user_id !== "string" || member.user_id === "") return undefined;
  const displayName = typeof member.display_name === "string" ? member.display_name : "";
  // presence is only accepted as one of the values the domain defines; anything
  // else (including a missing field) leaves it absent rather than "offline",
  // which the UI must not claim on the server's behalf.
  const presence =
    member.presence === "online" || member.presence === "away" || member.presence === "offline"
      ? member.presence
      : undefined;
  return {
    userId: member.user_id,
    displayName,
    avatarUrl: safeAvatarUrl(member.avatar_url),
    role: member.role === "moderator" ? "moderator" : "member",
    presence,
  };
}

function nonNegativeCount(raw: unknown): number {
  return typeof raw === "number" && Number.isFinite(raw) && raw >= 0 ? raw : 0;
}

/**
 * Fetches the channel-details payload for the panel.
 *
 * The server is the authority on visibility: a channel the caller cannot read
 * answers 404 and this rejects, so the panel shows its error state instead of a
 * partially-rendered channel.
 */
export async function fetchChannelDetails(
  channelId: string,
  signal?: AbortSignal,
): Promise<ChannelDetails> {
  const res = await authenticatedFetch<ChannelDetailsEnvelope>(
    `${CHAT_BASE}/channels/${encodeURIComponent(channelId)}/details`,
    { method: "GET", signal },
  );
  const data = res.data;
  const onlineMembers = Array.isArray(data.online_members)
    ? data.online_members
        .map(mapChannelMember)
        .filter((member): member is ChannelMemberProfile => member !== undefined)
        // Defence in depth, not the fix: the server already filters by presence
        // before applying its limit, and this only stops a member the server did
        // not vouch for as online from reaching an "online members" list. It can
        // never restore an online member the server left out, which is exactly
        // why the correction itself lives server-side.
        .filter((member) => member.presence === "online")
    : [];
  return {
    id: typeof data.id === "string" ? data.id : channelId,
    slug: typeof data.slug === "string" ? data.slug : "",
    name: typeof data.display_name === "string" ? data.display_name : "",
    type: data.type === "private" ? "private" : "public",
    createdAt: typeof data.created_at === "string" ? data.created_at : "",
    // Both totals are the server's. Deriving either from onlineMembers.length
    // would under-report: that array is a capped preview, and the channel's size
    // does not shrink when a member disconnects.
    memberCount: nonNegativeCount(data.member_count),
    onlineCount: nonNegativeCount(data.online_member_count),
    onlineMembers,
    // Strict `=== true`, exactly like can_write: an absent, null or truthy-ish
    // field must never read as permission. A server that predates issue #398
    // therefore leaves the action hidden, which is the safe direction, and the
    // POST re-checks the real decision regardless of what this says.
    canManageMembers: data.can_manage_members === true,
  };
}

// ── Add members (issue #398) ─────────────────────────────────────────────────

interface AddMembersEnvelope {
  data: {
    added?: unknown;
    already_members?: unknown;
    member_count?: unknown;
  };
}

function mapAddMembersResult(envelope: AddMembersEnvelope): AddMembersResult {
  const data = envelope.data;
  return {
    added: nonNegativeCount(data.added),
    alreadyMembers: nonNegativeCount(data.already_members),
    memberCount: nonNegativeCount(data.member_count),
  };
}

/**
 * Adds workspace members to an existing channel.
 *
 * Only the picked user IDs are sent. The workspace, the actor, the membership
 * role and every eligibility verdict are derived from the session server-side,
 * and chat-service rejects a body that carries them — so there is no field here
 * through which the browser could claim a privilege. Nothing is checked on this
 * side: de-duplication, the batch cap, who may add and who may be added are all
 * decided by the server, and a caller it refuses gets a 403 or 404 rather than a
 * locally-invented failure.
 *
 * The returned counts are the server's post-commit truth. The caller updates its
 * member counter from `memberCount` instead of incrementing a local guess, which
 * is what keeps the panel correct when someone else added people concurrently.
 */
export async function addChannelMembers(
  channelId: string,
  userIds: string[],
  signal?: AbortSignal,
): Promise<AddMembersResult> {
  const response = await authenticatedFetch<AddMembersEnvelope>(
    `${CHAT_BASE}/channels/${encodeURIComponent(channelId)}/members`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ user_ids: userIds }),
      signal,
    },
  );
  return mapAddMembersResult(response);
}

/**
 * Adds workspace members to an existing group conversation.
 *
 * A separate route from the channel one because a group is a DM conversation,
 * not a channel; the payload and the response are identical by design, so the
 * selector that produces them does not need to know which it is talking to. A
 * 1:1 conversation is answered 404 by the server — adding a third person would
 * convert it into a group, which this flow deliberately does not do.
 */
export async function addGroupParticipants(
  conversationId: string,
  userIds: string[],
  signal?: AbortSignal,
): Promise<AddMembersResult> {
  const response = await authenticatedFetch<AddMembersEnvelope>(
    `${CHAT_BASE}/dm/${encodeURIComponent(conversationId)}/members`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ user_ids: userIds }),
      signal,
    },
  );
  return mapAddMembersResult(response);
}

// ── Group details (issues #441, #398) ────────────────────────────────────────

interface GroupParticipantResponse {
  user_id?: unknown;
  display_name?: unknown;
  avatar_url?: unknown;
  presence?: unknown;
}

interface GroupDetailsEnvelope {
  data: {
    id?: unknown;
    type?: unknown;
    name?: unknown;
    created_at?: unknown;
    participant_count?: unknown;
    participants?: unknown;
    can_manage_members?: unknown;
  };
}

function mapGroupParticipant(raw: unknown): GroupParticipantProfile | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const participant = raw as GroupParticipantResponse;
  if (typeof participant.user_id !== "string" || participant.user_id === "") return undefined;
  // Presence is decoration for a group: an unknown or missing value leaves the
  // field absent, and the participant is still rendered. Unlike the channel
  // panel, nothing here filters the list by it.
  const presence =
    participant.presence === "online" ||
    participant.presence === "away" ||
    participant.presence === "offline"
      ? participant.presence
      : undefined;
  return {
    userId: participant.user_id,
    displayName: typeof participant.display_name === "string" ? participant.display_name : "",
    avatarUrl: safeAvatarUrl(participant.avatar_url),
    presence,
  };
}

/**
 * Fetches the group-details payload for the panel.
 *
 * The server is the authority: a conversation the caller does not participate
 * in — and a 1:1 conversation, which has no panel in this release — answers 404
 * and this rejects, so the panel shows its error state rather than a
 * half-rendered group.
 */
export async function fetchGroupDetails(
  conversationId: string,
  signal?: AbortSignal,
): Promise<GroupDetails> {
  const res = await authenticatedFetch<GroupDetailsEnvelope>(
    `${CHAT_BASE}/dm/${encodeURIComponent(conversationId)}/details`,
    { method: "GET", signal },
  );
  const data = res.data;
  const participants = Array.isArray(data.participants)
    ? data.participants
        .map(mapGroupParticipant)
        .filter((participant): participant is GroupParticipantProfile => participant !== undefined)
    : [];
  return {
    id: typeof data.id === "string" ? data.id : conversationId,
    name: typeof data.name === "string" ? data.name : "",
    createdAt: typeof data.created_at === "string" ? data.created_at : "",
    // The server's total. Deriving it from the preview would under-report a
    // group with more participants than the cap allows.
    participantCount: nonNegativeCount(data.participant_count),
    participants,
    // Strict `=== true` (issue #398): an absent or malformed field leaves the
    // add action hidden, so a server that predates it does not enable a flow it
    // cannot authorize.
    canManageMembers: data.can_manage_members === true,
  };
}

// ── Direct profile (issue #443) ──────────────────────────────────────────────

interface DirectProfileEnvelope {
  data?: unknown;
}

interface DirectProfileResponse {
  user_id?: unknown;
  display_name?: unknown;
  avatar_url?: unknown;
  presence?: unknown;
  email?: unknown;
  job_title?: unknown;
  department?: unknown;
  timezone?: unknown;
}

/** A profile string the panel will render: text, trimmed, or absent. */
function optionalText(raw: unknown): string | undefined {
  if (typeof raw !== "string") return undefined;
  const value = raw.trim();
  return value === "" ? undefined : value;
}

/**
 * The error code for a 2xx whose body is not the agreed shape.
 *
 * Mirrors adminUsersApi's ERR_INVALID_RESPONSE so the app has one way to say
 * "the transport succeeded and the contract did not", and callers keep handling
 * a single error type.
 */
export const ERR_INVALID_RESPONSE = "invalid_response";

function directProfileContractError(detail: string): ApiRequestError {
  // The status is the one that actually came back — the request was a success,
  // the payload was not, and conflating the two would let a broken contract
  // read as a network failure or a denial.
  return new ApiRequestError(
    200,
    ERR_INVALID_RESPONSE,
    `Invalid direct profile response: ${detail}`,
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Fetches the 1:1 profile payload for the panel.
 *
 * The endpoint names only the conversation: which person is described is the
 * server's decision, derived from the caller's own membership, so there is no
 * user ID to tamper with here and none is sent. A conversation the caller does
 * not participate in — and a group, which has its own endpoint — answers 404
 * and this rejects, so the panel shows its error state rather than a
 * half-rendered profile.
 *
 * Everything the response *claims* about itself is checked rather than assumed,
 * and nothing is repaired:
 *  - `kind` must literally be "direct". A missing, null, "group", "channel" or
 *    unrecognised tag is rejected instead of being replaced with "direct" — the
 *    tag is the only thing distinguishing a profile from a conversation
 *    projection, and inventing it would let a group payload be rendered as a
 *    person.
 *  - `conversation_id` must be present and must equal the conversation that was
 *    asked about. Echoing the request argument back would make a misrouted
 *    response — A's request answered with B's profile — indistinguishable from
 *    a correct one, which is exactly the failure this check exists to catch.
 *  - `profile` must carry a real identity. "Não informado" rows must never be
 *    able to stand in for "the server answered with nothing".
 *
 * The returned value is already the discriminated variant, so no caller has to
 * (or gets to) attach the tag afterwards.
 */
export async function fetchDirectProfile(
  conversationId: string,
  signal?: AbortSignal,
): Promise<DirectDetails> {
  const res = await authenticatedFetch<DirectProfileEnvelope>(
    `${CHAT_BASE}/dm/${encodeURIComponent(conversationId)}/profile`,
    { method: "GET", signal },
  );
  const data = res.data;
  if (!isRecord(data)) {
    throw directProfileContractError("data must be an object");
  }
  if (data.kind !== "direct") {
    throw directProfileContractError(`kind must be "direct"`);
  }
  if (typeof data.conversation_id !== "string" || data.conversation_id === "") {
    throw directProfileContractError("conversation_id must be a non-empty string");
  }
  if (data.conversation_id !== conversationId) {
    // Deliberately without either ID in the message: the mismatch is the fact
    // worth reporting, and repeating identifiers into an error string is how
    // they end up in a log or a toast.
    throw directProfileContractError("conversation_id does not match the requested conversation");
  }
  if (!isRecord(data.profile)) {
    throw directProfileContractError("profile must be an object");
  }
  const raw = data.profile as DirectProfileResponse;
  const userId = typeof raw.user_id === "string" ? raw.user_id : "";
  const displayName = optionalText(raw.display_name);
  if (userId === "") {
    throw directProfileContractError("profile.user_id must be a non-empty string");
  }
  if (displayName === undefined) {
    throw directProfileContractError("profile.display_name must be a non-empty string");
  }
  // Presence is only accepted as one of the values the domain defines; anything
  // else — including a missing field — leaves it absent rather than "offline",
  // which the UI must not claim on the server's behalf.
  const presence =
    raw.presence === "online" || raw.presence === "away" || raw.presence === "offline"
      ? raw.presence
      : undefined;
  return {
    kind: "direct",
    conversationId: data.conversation_id,
    profile: {
      userId,
      displayName,
      avatarUrl: safeAvatarUrl(raw.avatar_url),
      presence,
      email: optionalText(raw.email),
      // No column stores these yet, so today they are always absent. Reading
      // them costs three lines and is what makes "when available" true rather
      // than a promise for a later change.
      jobTitle: optionalText(raw.job_title),
      department: optionalText(raw.department),
      timezone: optionalText(raw.timezone),
    },
  };
}
