/**
 * Workspace user administration client (issue #425).
 *
 * The base is /api/auth, not /api/admin: user accounts and invites are the
 * auth-service's domain, and /api/auth is the only prefix the gateways forward
 * to it. /api/admin is routed to admin-service, which serves no user endpoints
 * — that mismatch is what made this screen answer 404.
 *
 * The workspace is never sent. The backend derives it from the caller's session
 * and rejects a caller who administers none, so there is no identifier here for
 * a client to aim at another workspace.
 */

import { ApiRequestError } from "../lib/api";
import { authenticatedFetch } from "../lib/authClient";

const ADMIN_BASE = import.meta.env.VITE_ADMIN_API_BASE_URL ?? "/api/auth/admin";

/** Page size requested by the admin table. The server caps it at 100. */
export const ADMIN_USERS_PAGE_SIZE = 50;

export interface AdminUser {
  id: string;
  email: string;
  displayName: string;
  fullName?: string;
  status: string;
  authSource: string;
  createdAt: string;
}

/**
 * Raised when the server answered 2xx but the body is not the agreed shape.
 *
 * It reuses ApiRequestError so callers keep one error type to handle. The
 * status is the HTTP status that actually came back — usually 200, which is
 * the whole point: the transport succeeded and the *contract* did not, and
 * those must not be confused with each other or with an empty result.
 */
export const ERR_INVALID_RESPONSE = "invalid_response";

function contractError(detail: string): ApiRequestError {
  return new ApiRequestError(200, ERR_INVALID_RESPONSE, `Invalid API response: ${detail}`);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requireString(raw: Record<string, unknown>, key: string, index: number): string {
  const value = raw[key];
  if (typeof value !== "string") {
    throw contractError(`user[${index}].${key} must be a string`);
  }
  return value;
}

/**
 * Maps one row, rejecting anything that does not match the DTO.
 *
 * Every field the table renders is required. `full_name` is the one optional
 * field, but when present it must still be a string — a number or an object
 * there would reach the DOM as unrenderable output rather than an error.
 */
function parseUser(value: unknown, index: number): AdminUser {
  if (!isRecord(value)) {
    throw contractError(`user[${index}] must be an object`);
  }
  const fullName = value.full_name;
  if (fullName !== undefined && fullName !== null && typeof fullName !== "string") {
    throw contractError(`user[${index}].full_name must be a string when present`);
  }
  return {
    id: requireString(value, "id", index),
    email: requireString(value, "email", index),
    displayName: requireString(value, "display_name", index),
    fullName: typeof fullName === "string" ? fullName : undefined,
    status: requireString(value, "status", index),
    authSource: requireString(value, "auth_source", index),
    createdAt: requireString(value, "created_at", index),
  };
}

/**
 * Unwraps the `{ "data": [...] }` envelope.
 *
 * The previous version was `body?.data ?? []`, which turned a missing
 * envelope, `data: null` and `data: {}` alike into "no users" — the same class
 * of bug as the swallowed 404, one layer further in. Only a genuine array
 * reaches the caller; everything else is an error.
 */
function parseAdminUsers(body: unknown): AdminUser[] {
  if (!isRecord(body)) {
    throw contractError("expected an object with a data property");
  }
  const { data } = body;
  if (!Array.isArray(data)) {
    throw contractError(
      data === undefined ? "missing data array" : `data must be an array, got ${typeName(data)}`,
    );
  }
  return data.map(parseUser);
}

/** One page of the workspace user listing. */
export interface AdminUsersPageResult {
  users: AdminUser[];
  /** Opaque token for the next page; null on the last page. */
  nextCursor: string | null;
  hasMore: boolean;
}

/**
 * Unwraps `{ data: { data: [...], pagination: {...} } }`.
 *
 * The outer `data` is the service-wide envelope; the inner one is the page.
 * Both are required — a body missing either is a broken contract, not an empty
 * workspace, and must not be smoothed over into a valid-looking empty page.
 */
function parseAdminUsersPage(body: unknown): AdminUsersPageResult {
  if (!isRecord(body)) {
    throw contractError("expected an object with a data property");
  }
  const page = body.data;
  if (!isRecord(page)) {
    throw contractError("missing page envelope");
  }
  const users = parseAdminUsers(page);

  const { pagination } = page;
  if (!isRecord(pagination)) {
    throw contractError("missing pagination");
  }
  const { next_cursor: nextCursor, has_more: hasMore } = pagination;
  if (typeof hasMore !== "boolean") {
    throw contractError("pagination.has_more must be a boolean");
  }
  if (nextCursor !== null && typeof nextCursor !== "string") {
    throw contractError("pagination.next_cursor must be a string or null");
  }
  // The two fields must agree. If they do not, one of them is wrong and we
  // cannot tell which — paging on a cursor we do not trust risks an endless
  // loop, so this is an error rather than a guess.
  if (hasMore && !nextCursor) {
    throw contractError("pagination.has_more is true without a next_cursor");
  }
  return { users, nextCursor: nextCursor ?? null, hasMore };
}

function typeName(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  return typeof value;
}

/**
 * How a failed admin request should be presented.
 *
 * The page renders a different state for each, which is the point: a 404 is a
 * broken deployment, a 403 is a permissions problem the user can act on, and
 * neither is "this workspace has no users".
 */
export type AdminErrorKind = "unauthorized" | "forbidden" | "rate-limited" | "error";

/** Classifies a thrown error for the page. Anything unrecognised is a failure,
 * never an empty result. */
export function classifyAdminError(err: unknown): AdminErrorKind {
  if (err instanceof ApiRequestError) {
    if (err.status === 401) return "unauthorized";
    if (err.status === 403) return "forbidden";
    if (err.status === 429) return "rate-limited";
  }
  return "error";
}

/**
 * Lists the users of the caller's workspace.
 *
 * Every failure propagates. An earlier version swallowed 404 and returned [],
 * which is what let a missing endpoint render as "Nenhum usuário disponível"
 * for as long as it did; only a 200 can produce an empty list now.
 */
export async function listAdminUsers(
  options: { limit?: number; cursor?: string | null; signal?: AbortSignal } = {},
): Promise<AdminUsersPageResult> {
  // URLSearchParams, not string concatenation: the cursor is opaque and may
  // contain characters that would otherwise need escaping by hand.
  const params = new URLSearchParams({
    limit: String(options.limit ?? ADMIN_USERS_PAGE_SIZE),
  });
  if (options.cursor) {
    params.set("cursor", options.cursor);
  }
  const body = await authenticatedFetch<unknown>(`${ADMIN_BASE}/users?${params.toString()}`, {
    method: "GET",
    signal: options.signal,
  });
  return parseAdminUsersPage(body);
}

/**
 * Updates the status of a user.
 *
 * NOTE: the browser-callable status route does not exist yet. The backend
 * currently serves PATCH /admin/users/{id}/status behind AdminBootstrapGuard
 * (X-NChat-Admin-Token), which is not browser-safe. This function names the
 * path that route will take once RF-74 puts it behind the same session guard
 * as the listing above. The UI keeps its buttons disabled until then — do not
 * wire this up before the guard exists.
 *
 * Permitted transitions: active → suspended, suspended → active.
 */
export async function updateUserStatus(
  id: string,
  status: "active" | "suspended",
): Promise<AdminUser> {
  const body = await authenticatedFetch<unknown>(
    `${ADMIN_BASE}/users/${encodeURIComponent(id)}/status`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ status }),
    },
  );
  if (!isRecord(body)) {
    throw contractError("expected an object with a data property");
  }
  return parseUser(body.data, 0);
}

export interface InviteUserInput {
  email: string;
  displayName: string;
  fullName?: string;
}

/**
 * Invites a user to the workspace.
 *
 * No role is sent: the payload has no such field and the backend would ignore
 * one, so the UI cannot be used to grant privileges. The actor and the
 * workspace come from the session.
 *
 * The response is not mapped into the table. The invite creates an invitation,
 * not a user account — the caller refetches the canonical list instead of
 * inventing a row for someone who has not accepted yet.
 */
export async function createAdminInvite(input: InviteUserInput): Promise<void> {
  await authenticatedFetch<unknown>(`${ADMIN_BASE}/invites`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      email: input.email.trim(),
      display_name: input.displayName.trim(),
      ...(input.fullName?.trim() ? { full_name: input.fullName.trim() } : {}),
    }),
  });
}
