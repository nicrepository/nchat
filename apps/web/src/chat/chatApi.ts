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
import type { Channel, DMConversation } from "./chatTypes";

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
