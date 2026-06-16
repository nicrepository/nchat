/**
 * Chat API client.
 *
 * Fetches sidebar data from GET /api/chat/sidebar using authenticatedFetch.
 * Tokens are never stored or passed via URL; auth is handled by the existing
 * session/cookie mechanism in authenticatedFetch.
 *
 * chatFixtures.ts is TEST-ONLY and must never be imported here.
 * There is intentionally no module-level request cache: each call is independent
 * so that a session change cannot cause one user to receive another user's data.
 */

import { authenticatedFetch } from "../lib/authClient";
import type { Channel, DMConversation, Message, MessagePage } from "./chatTypes";

const CHAT_BASE = import.meta.env.VITE_CHAT_API_BASE_URL ?? "/api/chat";

// ── API response shapes ───────────────────────────────────────────────────────

interface SidebarChannelResponse {
  id: string;
  slug: string;
  display_name: string;
  type: "public" | "private";
  is_general: boolean;
}

interface SidebarDMResponse {
  id: string;
  type: "direct" | "group";
  name: string;
}

interface SidebarResponse {
  workspace: { id: string; name: string; slug: string };
  channels: SidebarChannelResponse[];
  dm_conversations: SidebarDMResponse[];
}

interface SidebarEnvelope {
  data: SidebarResponse;
}

// ── Internal fetch (no cross-request caching) ─────────────────────────────────

async function fetchSidebar(): Promise<SidebarResponse> {
  const res = await authenticatedFetch<SidebarEnvelope>(`${CHAT_BASE}/sidebar`, {
    method: "GET",
  });
  return res.data;
}

// ── Exported API ──────────────────────────────────────────────────────────────

export async function fetchChannels(): Promise<Channel[]> {
  const sidebar = await fetchSidebar();
  return (sidebar.channels ?? []).map((ch) => ({
    id: ch.id,
    name: ch.display_name || ch.slug,
    type: ch.type,
  }));
}

export async function fetchDMs(): Promise<DMConversation[]> {
  const sidebar = await fetchSidebar();
  return (sidebar.dm_conversations ?? []).map((dm) => ({
    id: dm.id,
    type: dm.type === "group" ? ("group" as const) : ("1:1" as const),
    name: dm.name,
    participants: [],
  }));
}

/**
 * Fetches the full sidebar in a single request and returns both channels and DMs.
 * Prefer this over calling fetchChannels() + fetchDMs() separately to avoid
 * making two HTTP requests per load.
 */
export async function fetchSidebarData(): Promise<{
  channels: Channel[];
  dms: DMConversation[];
}> {
  const sidebar = await fetchSidebar();
  const channels = (sidebar.channels ?? []).map((ch) => ({
    id: ch.id,
    name: ch.display_name || ch.slug,
    type: ch.type,
  }));
  const dms = (sidebar.dm_conversations ?? []).map((dm) => ({
    id: dm.id,
    type: dm.type === "group" ? ("group" as const) : ("1:1" as const),
    name: dm.name,
    participants: [],
  }));
  return { channels, dms };
}

// ── Message API response shapes ───────────────────────────────────────────────

interface MessageResponse {
  id: string;
  sender_id: string;
  kind: string;
  body_text?: string;
  is_removed?: boolean;
  status: string;
  created_at: string;
  updated_at: string;
}

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

// ── Internal helpers ──────────────────────────────────────────────────────────

function mapMessage(r: MessageResponse): Message {
  return {
    id: r.id,
    senderId: r.sender_id,
    kind: (r.kind === "system" ? "system" : "user") as Message["kind"],
    bodyText: r.body_text ?? "",
    isRemoved: r.is_removed ?? false,
    status: (r.status === "deleted" ? "deleted" : "active") as Message["status"],
    createdAt: r.created_at,
    updatedAt: r.updated_at,
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
  signal?: AbortSignal,
): Promise<Message> {
  const res = await authenticatedFetch<MessageEnvelope>(messagesPath("channel", channelId), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ body_text: bodyText }),
    signal,
  });
  return mapMessage(res.data);
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

export async function postDMMessage(
  conversationId: string,
  bodyText: string,
  signal?: AbortSignal,
): Promise<Message> {
  const res = await authenticatedFetch<MessageEnvelope>(messagesPath("dm", conversationId), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ body_text: bodyText }),
    signal,
  });
  return mapMessage(res.data);
}
