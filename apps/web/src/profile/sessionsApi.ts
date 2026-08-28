import { authenticatedFetch } from "../lib/authClient";
import { ApiRequestError } from "../lib/api";

const AUTH_BASE = import.meta.env.VITE_AUTH_API_BASE_URL ?? "/api/auth";

export interface Session {
  id: string;
  createdAt: string;
  lastSeenAt: string;
  ipAddress: string;
  userAgent: string;
  current: boolean;
  revokedAt?: string;
}

interface SessionRowResponse {
  id: string;
  device_id: string | null;
  created_at: string;
  last_seen_at: string;
  idle_expires_at: string;
  absolute_expires_at: string | null;
  revoked_at: string | null;
  ip_address?: string;
  user_agent?: string;
  current: boolean;
}

interface SessionsListResponse {
  data: SessionRowResponse[];
  pagination: { limit: number };
}

export type SessionsApiErrorReason = "unauthorized" | "forbidden" | "unknown";

export class SessionsApiError extends Error {
  readonly reason: SessionsApiErrorReason;
  constructor(reason: SessionsApiErrorReason, message: string) {
    super(message);
    this.name = "SessionsApiError";
    this.reason = reason;
  }
}

function mapSessionsError(error: unknown): SessionsApiError {
  if (error instanceof ApiRequestError) {
    switch (error.status) {
      case 401:
        return new SessionsApiError("unauthorized", "Sua sessão atual não pôde ser confirmada.");
      case 403:
        return new SessionsApiError("forbidden", "Você não tem permissão para esta ação.");
    }
  }
  return new SessionsApiError("unknown", "Não foi possível concluir a operação.");
}

function fromResponse(row: SessionRowResponse): Session {
  return {
    id: row.id,
    createdAt: row.created_at,
    lastSeenAt: row.last_seen_at,
    ipAddress: row.ip_address ?? "",
    userAgent: row.user_agent ?? "",
    current: row.current,
    revokedAt: row.revoked_at ?? undefined,
  };
}

/** Lists the authenticated user's own active sessions, newest first. Never accepts a user id — identity is the session's own. */
export async function listSessions(signal?: AbortSignal): Promise<Session[]> {
  try {
    const res = await authenticatedFetch<SessionsListResponse>(`${AUTH_BASE}/me/sessions`, {
      method: "GET",
      signal,
    });
    return res.data.map(fromResponse);
  } catch (error) {
    throw mapSessionsError(error);
  }
}

/** Revokes one session. Idempotent from the caller's perspective: a 404 (already gone / not this user's) is not surfaced as a distinct case here — the list is always revalidated after, so the row converges to "gone" either way. */
export async function revokeSession(sessionId: string, signal?: AbortSignal): Promise<void> {
  try {
    await authenticatedFetch<void>(`${AUTH_BASE}/me/sessions/${sessionId}`, {
      method: "DELETE",
      signal,
    });
  } catch (error) {
    throw mapSessionsError(error);
  }
}

/** Revokes every session except the caller's current one. */
export async function revokeAllOtherSessions(signal?: AbortSignal): Promise<void> {
  try {
    await authenticatedFetch<void>(`${AUTH_BASE}/me/sessions`, { method: "DELETE", signal });
  } catch (error) {
    throw mapSessionsError(error);
  }
}
