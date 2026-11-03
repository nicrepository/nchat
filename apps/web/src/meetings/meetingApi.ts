import { authenticatedFetch, type RequestSender } from "../lib/authClient";
import { ApiRequestError, apiFetch } from "../lib/api";
import type { Candidate, CreateMeetingPayload, MeetingResult, MeetingTarget } from "./meetingTypes";

// Match the read-cursor transport: refresh may install another account's token.
// The caller retries only after the shell confirms identity, retaining its key/ID.
function meetingMutationSender(): RequestSender {
  let dispatched = false;
  return (url, init, parse) => {
    if (dispatched)
      return Promise.reject(
        new ApiRequestError(401, "identity_unconfirmed", "Meeting mutation not replayed"),
      );
    dispatched = true;
    return apiFetch(url, init, parse);
  };
}

const base = import.meta.env.VITE_CHAT_API_BASE_URL ?? "/api/chat";
export async function meetingCandidates(
  target: MeetingTarget,
  q: string,
  cursor: string,
  signal: AbortSignal,
) {
  const params = new URLSearchParams({ q, limit: "50" });
  if (cursor) params.set("cursor", cursor);
  const response = await authenticatedFetch<{ data: { items: Candidate[]; next_cursor?: string } }>(
    `${base}/meeting-candidates/${target.kind}/${encodeURIComponent(target.id)}?${params}`,
    { signal },
  );
  return response.data;
}
export function createMeeting(payload: CreateMeetingPayload, key: string) {
  return authenticatedFetch<{ meeting: MeetingResult; replayed: boolean }>(
    `${base}/meetings`,
    {
      method: "POST",
      headers: { "Idempotency-Key": key },
      body: JSON.stringify(payload),
    },
    async (response) => {
      const { data } = (await response.json()) as { data: MeetingResult };
      if (!data?.id || !["scheduled", "in_progress", "ended", "cancelled"].includes(data.status))
        throw new ApiRequestError(0, "invalid_response", "Invalid response");
      return { meeting: data, replayed: response.status === 200 };
    },
    meetingMutationSender(),
  );
}
export async function inviteAll(meetingId: string) {
  const response = await authenticatedFetch<{ data: { added: number } }>(
    `${base}/meetings/${encodeURIComponent(meetingId)}/participants/invite-all`,
    { method: "POST", body: "{}" },
    undefined,
    meetingMutationSender(),
  );
  if (!Number.isSafeInteger(response?.data?.added) || response.data.added < 0)
    throw new ApiRequestError(0, "invalid_response", "Invalid response");
  return response.data.added;
}

export function meetingError(error: unknown, bulk = false): string {
  if (!(error instanceof ApiRequestError))
    return "Não foi possível confirmar a operação. Tente novamente com segurança.";
  if (error.code === "identity_unconfirmed")
    return "Aguarde a confirmação da sessão e tente novamente. Seus dados estão preservados.";
  const messages: Record<number, string> = {
    0: "Resposta não confirmada. Verifique sua conexão e tente novamente; a mesma intenção será recuperada sem duplicar a reunião.",
    400: bulk
      ? "Não foi possível convidar todos. O limite é de 500 elegíveis; acima disso, utilize seleção parcial ao agendar. Você pode finalizar com a seleção original."
      : "Revise os dados e a seleção. Alguma pessoa pode não estar mais disponível.",
    401: "Sua sessão expirou. Entre novamente para continuar.",
    403: "Você não tem permissão para realizar esta operação.",
    404: "A conversa ou reunião não está mais disponível.",
    409: "A intenção está em conflito com o estado no servidor. Preserve estes dados e verifique antes de iniciar outro agendamento.",
    413: "A seleção excede o tamanho permitido. Remova algumas pessoas.",
    429: "Muitas solicitações. Aguarde um minuto e tente novamente.",
    500: "O serviço não confirmou a operação. Tente novamente com segurança.",
    503: "Serviço temporariamente indisponível. Tente novamente com segurança.",
  };
  return (
    messages[error.status] ??
    "Não foi possível confirmar a operação. Tente novamente com segurança."
  );
}

export function candidateError(error: unknown): string {
  if (error instanceof ApiRequestError && [401, 403, 404, 429].includes(error.status))
    return meetingError(error);
  return "Não foi possível buscar participantes. Verifique a conexão e tente novamente.";
}
