import { ApiRequestError } from "../lib/api";
import { authenticatedFetch } from "../lib/authClient";

const ADMIN_BASE = import.meta.env.VITE_ADMIN_API_BASE_URL ?? "/api/admin";

export interface AdminUser {
  id: string;
  email: string;
  displayName: string;
  fullName?: string;
  status: string;
  authSource: string;
  createdAt: string;
}

type RawAdminUser = {
  id: string;
  email: string;
  display_name: string;
  full_name?: string;
  status: string;
  auth_source: string;
  created_at: string;
};

function mapUser(raw: RawAdminUser): AdminUser {
  return {
    id: raw.id,
    email: raw.email,
    displayName: raw.display_name,
    fullName: raw.full_name,
    status: raw.status,
    authSource: raw.auth_source,
    createdAt: raw.created_at,
  };
}

/**
 * Lists all users from the admin endpoint.
 *
 * Returns an empty array when the endpoint does not yet exist (404), so the
 * page renders the empty state rather than an error while backend is not deployed.
 * All other non-2xx responses propagate as errors.
 */
export async function listAdminUsers(): Promise<AdminUser[]> {
  try {
    const raw = await authenticatedFetch<RawAdminUser[]>(`${ADMIN_BASE}/users`, {
      method: "GET",
    });
    return (raw ?? []).map(mapUser);
  } catch (err) {
    if (err instanceof ApiRequestError && err.status === 404) {
      return [];
    }
    throw err;
  }
}

/**
 * Updates the status of a user via the admin endpoint.
 *
 * NOTE: The corresponding backend endpoint (PATCH /admin/users/{id}/status) is
 * currently guarded by AdminBootstrapGuard (X-NChat-Admin-Token), which is NOT
 * browser-safe. This function is prepared for when a browser-callable admin
 * JWT/RBAC guard exists (RF-74). Do not trigger this from the UI until that
 * guard is in place.
 *
 * Permitted transitions: active → suspended, suspended → active.
 */
export async function updateUserStatus(
  id: string,
  status: "active" | "suspended",
): Promise<AdminUser> {
  const raw = await authenticatedFetch<RawAdminUser>(`${ADMIN_BASE}/users/${id}/status`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ status }),
  });
  return mapUser(raw);
}
