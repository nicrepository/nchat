import { authenticatedFetch } from "../lib/authClient";

const MEDIA_BASE = import.meta.env.VITE_MEDIA_API_BASE_URL ?? "/api/media";

interface CallTokenEnvelope {
  data: {
    token: string;
    expiresAt: string;
  };
}

export async function issueCallToken(
  callId: string,
): Promise<{ token: string; expiresAt: string }> {
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
