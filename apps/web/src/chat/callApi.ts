import { authenticatedFetch } from "../lib/authClient";

const MEDIA_BASE = import.meta.env.VITE_MEDIA_API_BASE_URL ?? "/api/media";

interface CallTokenEnvelope {
  data: {
    token: string;
    expiresAt: string;
    serverUrl: string;
  };
}

export async function issueCallToken(
  callId: string,
): Promise<{ token: string; expiresAt: string; serverUrl: string }> {
  const response = await authenticatedFetch<CallTokenEnvelope>(
    `${MEDIA_BASE}/media/livekit/token`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ call_id: callId }),
    },
  );
  return response.data;
}

/** RF-24 resource room kind. Never "call" — a direct call keeps using issueCallToken. */
export type ResourceCallKind = "channel" | "dm";

/**
 * Issues a LiveKit token for a channel or group resource room (RF-24). The
 * room itself is never named here — the server derives it from resourceId,
 * so a later joiner always lands in the same room as everyone already in it.
 */
export async function issueResourceCallToken(
  kind: ResourceCallKind,
  resourceId: string,
): Promise<{ token: string; expiresAt: string; serverUrl: string }> {
  const response = await authenticatedFetch<CallTokenEnvelope>(
    `${MEDIA_BASE}/media/livekit/token`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ resource_kind: kind, resource_id: resourceId }),
    },
  );
  return response.data;
}
