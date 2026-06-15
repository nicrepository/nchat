/**
 * Chat API client.
 *
 * Fetches sidebar data from GET /api/chat/sidebar using authenticatedFetch.
 * Tokens are never stored or passed via URL; auth is handled by the existing
 * session/cookie mechanism in authenticatedFetch.
 *
 * chatFixtures.ts is TEST-ONLY and must never be imported here.
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
  participant_ids: string[];
}

interface SidebarResponse {
  workspace: { id: string; name: string; slug: string };
  channels: SidebarChannelResponse[];
  dm_conversations: SidebarDMResponse[];
}

interface SidebarEnvelope {
  data: SidebarResponse;
}

// ── Module-level request deduplication ───────────────────────────────────────

let _inflight: Promise<SidebarResponse> | null = null;

async function fetchSidebar(): Promise<SidebarResponse> {
  if (_inflight) return _inflight;
  _inflight = authenticatedFetch<SidebarEnvelope>(`${CHAT_BASE}/sidebar`, {
    method: "GET",
  }).then(
    (res) => {
      _inflight = null;
      return res.data;
    },
    (err: unknown) => {
      _inflight = null;
      throw err;
    },
  );
  return _inflight;
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
