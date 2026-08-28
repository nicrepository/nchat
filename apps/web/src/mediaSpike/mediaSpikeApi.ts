import { apiFetch } from "../lib/api";

const MEDIA_BASE = import.meta.env.VITE_MEDIA_API_BASE_URL ?? "/api/media";

export interface SpikeTokenRequest {
  room: string;
  identity: string;
  name: string;
}

export interface SpikeTokenResponse {
  serverUrl: string;
  token: string;
  room: string;
  identity: string;
  expiresInSeconds: number;
}

interface SpikeTokenEnvelope {
  data: SpikeTokenResponse;
}

export type SpikeTokenRequester = (
  request: SpikeTokenRequest,
  signal?: AbortSignal,
) => Promise<SpikeTokenResponse>;

export async function requestSpikeToken(
  request: SpikeTokenRequest,
  signal?: AbortSignal,
): Promise<SpikeTokenResponse> {
  const response = await apiFetch<SpikeTokenEnvelope>(`${MEDIA_BASE}/spike/token`, {
    method: "POST",
    body: JSON.stringify(request),
    signal,
  });
  return response.data;
}
