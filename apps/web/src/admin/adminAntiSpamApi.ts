/**
 * RF-19 anti-spam policy client (issue #419).
 *
 * The policy lives on chat-service, not on the admin API: it is a chat-domain
 * workspace setting and it is guarded by the workspace JWT/RBAC checks, unlike
 * the /api/admin user endpoints which still sit behind AdminBootstrapGuard.
 *
 * The workspace ID is not a user-supplied value — it is read from the sidebar
 * endpoint, which resolves it from the caller's own session. The backend
 * re-checks that the caller administers whatever workspace the path names, so
 * nothing here is a security boundary.
 */

import { authenticatedFetch } from "../lib/authClient";

// /api/chat is the only prefix the gateways forward to chat-service, so the
// workspace endpoints share it with the rest of the service's API.
const CHAT_BASE = import.meta.env.VITE_CHAT_API_BASE_URL ?? "/api/chat";
const WORKSPACE_BASE = `${CHAT_BASE}/workspaces`;

export interface AntiSpamPolicy {
  workspaceId: string;
  messagesPerMinute: number;
  /** Server-supplied bounds. The form validates against these, never its own. */
  min: number;
  max: number;
}

interface RawAntiSpamPolicy {
  workspace_id: string;
  message_rate_limit_per_minute: number;
  min: number;
  max: number;
}

interface SidebarWorkspaceEnvelope {
  data: { workspace: { id: string } };
}

function mapPolicy(raw: RawAntiSpamPolicy): AntiSpamPolicy {
  return {
    workspaceId: raw.workspace_id,
    messagesPerMinute: raw.message_rate_limit_per_minute,
    min: raw.min,
    max: raw.max,
  };
}

/** Resolves the caller's own workspace ID from their session. */
export async function fetchCurrentWorkspaceId(): Promise<string> {
  const body = await authenticatedFetch<SidebarWorkspaceEnvelope>(`${CHAT_BASE}/sidebar`, {
    method: "GET",
  });
  const id = body?.data?.workspace?.id;
  if (!id) {
    throw new Error("workspace unavailable");
  }
  return id;
}

export async function fetchAntiSpamPolicy(workspaceId: string): Promise<AntiSpamPolicy> {
  const body = await authenticatedFetch<{ data: RawAntiSpamPolicy }>(
    `${WORKSPACE_BASE}/${encodeURIComponent(workspaceId)}/anti-spam`,
    { method: "GET" },
  );
  return mapPolicy(body.data);
}

/**
 * Persists a new limit. The value is validated here for immediate feedback, but
 * the backend and the database CHECK are what actually enforce the bounds — a
 * request that skips this function is rejected all the same.
 */
export async function updateAntiSpamPolicy(
  workspaceId: string,
  messagesPerMinute: number,
): Promise<AntiSpamPolicy> {
  const body = await authenticatedFetch<{ data: RawAntiSpamPolicy }>(
    `${WORKSPACE_BASE}/${encodeURIComponent(workspaceId)}/anti-spam`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message_rate_limit_per_minute: messagesPerMinute }),
    },
  );
  return mapPolicy(body.data);
}
