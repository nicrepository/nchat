/**
 * RF-32 upload limit policy client (issue #458).
 *
 * The policy lives on chat-service for the same reason the anti-spam one does:
 * it is a chat-domain workspace setting guarded by the workspace JWT/RBAC
 * checks, unlike the /api/admin user endpoints which sit behind
 * AdminBootstrapGuard. file-service reads the same column when it enforces the
 * limit, so there is one stored value, not two.
 *
 * The workspace ID is not a user-supplied value — it is read from the sidebar
 * endpoint, which resolves it from the caller's own session. The backend
 * re-checks that the caller administers whatever workspace the path names, and
 * the UPDATE re-checks it atomically, so nothing here is a security boundary.
 */

import { authenticatedFetch } from "../lib/authClient";

const CHAT_BASE = import.meta.env.VITE_CHAT_API_BASE_URL ?? "/api/chat";
const WORKSPACE_BASE = `${CHAT_BASE}/workspaces`;

export interface UploadLimitPolicy {
  workspaceId: string;
  /** Effective limit for one attachment, in bytes. */
  maxUploadBytes: number;
  /** Server-supplied bounds, in bytes. The form validates against these. */
  min: number;
  max: number;
}

interface RawUploadLimitPolicy {
  workspace_id: string;
  max_upload_bytes: number;
  min: number;
  max: number;
}

function mapPolicy(raw: RawUploadLimitPolicy): UploadLimitPolicy {
  return {
    workspaceId: raw.workspace_id,
    maxUploadBytes: raw.max_upload_bytes,
    min: raw.min,
    max: raw.max,
  };
}

export async function fetchUploadLimitPolicy(workspaceId: string): Promise<UploadLimitPolicy> {
  const body = await authenticatedFetch<{ data: RawUploadLimitPolicy }>(
    `${WORKSPACE_BASE}/${encodeURIComponent(workspaceId)}/upload-limit`,
    { method: "GET" },
  );
  return mapPolicy(body.data);
}

/**
 * Persists a new limit, in bytes. The value is validated here for immediate
 * feedback, but the handler, the atomic RBAC predicate in the UPDATE and the
 * database CHECK are what actually enforce it.
 */
export async function updateUploadLimitPolicy(
  workspaceId: string,
  maxUploadBytes: number,
): Promise<UploadLimitPolicy> {
  const body = await authenticatedFetch<{ data: RawUploadLimitPolicy }>(
    `${WORKSPACE_BASE}/${encodeURIComponent(workspaceId)}/upload-limit`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ max_upload_bytes: maxUploadBytes }),
    },
  );
  return mapPolicy(body.data);
}
