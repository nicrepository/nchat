import { adminFetch, setCSRFToken } from "./client";

export interface AdminIdentity {
  user_id: string;
  email: string;
  display_name: string;
  avatar_url: string;
}

export type AdminEnvironment = "DEVELOPMENT" | "STAGING" | "PRODUCTION";

export interface AdminBootstrap {
  identity: AdminIdentity;
  capabilities: string[];
  environment: AdminEnvironment;
  build: { service: string; version: string; commit: string };
  session: { idle_expires_at: string; absolute_expires_at: string };
  csrf_token: string;
}

export interface AuditEvent {
  id: string;
  occurred_at: string;
  actor_user_id: string;
  actor_email: string;
  action: string;
  resource: string;
  result: string;
  correlation_id: string;
}

/**
 * Exchanges a just-proven NChat identity for an administrative session.
 *
 * The access token is passed in as an argument and never stored: it lives for
 * the duration of this call and is dropped by the caller immediately
 * afterwards. From here on the console's credential is the HttpOnly cookie the
 * server sets in response.
 */
export async function createAdminSession(accessToken: string): Promise<AdminBootstrap> {
  const bootstrap = await adminFetch<AdminBootstrap>("/session", {
    method: "POST",
    headers: { Authorization: `Bearer ${accessToken}` },
  });
  setCSRFToken(bootstrap.csrf_token);
  return bootstrap;
}

export async function fetchAdminBootstrap(): Promise<AdminBootstrap> {
  const bootstrap = await adminFetch<AdminBootstrap>("/bootstrap");
  setCSRFToken(bootstrap.csrf_token);
  return bootstrap;
}

export async function destroyAdminSession(): Promise<void> {
  try {
    await adminFetch<void>("/session", { method: "DELETE" });
  } finally {
    setCSRFToken(null);
  }
}

/**
 * Reads the administrative audit trail.
 *
 * `userID` narrows it to the events performed *on* that account. The narrowing
 * happens in the database, before the limit — the point of the parameter is
 * finding an event the platform-wide window no longer reaches, which a
 * client-side filter over the last fifty rows could never do.
 */
export async function listAuditEvents(
  limit?: number,
  userID?: string,
  signal?: AbortSignal,
): Promise<AuditEvent[]> {
  const params = new URLSearchParams();
  if (limit !== undefined) params.set("limit", String(limit));
  if (userID) params.set("user_id", userID);
  const suffix = params.toString();
  const body = await adminFetch<{ events: AuditEvent[] }>(
    `/audit/events${suffix ? `?${suffix}` : ""}`,
    { signal },
  );
  return body.events ?? [];
}
