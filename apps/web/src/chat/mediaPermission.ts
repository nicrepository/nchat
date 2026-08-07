import type { CallType } from "./callState";

export type MediaPermissionErrorKind =
  | "permission_denied"
  | "device_not_found"
  | "device_unavailable"
  | "constraint_unsupported"
  | "aborted"
  | "security_blocked"
  | "unsupported_browser";

export interface MediaPermissionDenied {
  ok: false;
  kind: MediaPermissionErrorKind;
  message: string;
}

export type MediaPermissionResult = { ok: true } | MediaPermissionDenied;

/**
 * Runs a throwaway getUserMedia preflight tied to the caller's user gesture,
 * so the browser permission decision is known before call.start/call.accept
 * is sent and before LiveKit ever connects. Tracks are stopped immediately;
 * LiveKit re-acquires real tracks once the call is active.
 */
export async function requestMediaPermission(callType: CallType): Promise<MediaPermissionResult> {
  const mediaDevices = navigator.mediaDevices;
  if (!mediaDevices || typeof mediaDevices.getUserMedia !== "function") {
    return {
      ok: false,
      kind: "unsupported_browser",
      message:
        "Este navegador não é compatível com chamadas de áudio e vídeo. Verifique se a conexão usa HTTPS.",
    };
  }
  try {
    const stream = await mediaDevices.getUserMedia({ audio: true, video: callType === "video" });
    stream.getTracks().forEach((track) => track.stop());
    return { ok: true };
  } catch (error) {
    return { ok: false, ...permissionError(error, callType) };
  }
}

function permissionError(
  error: unknown,
  callType: CallType,
): { kind: MediaPermissionErrorKind; message: string } {
  const name =
    error && typeof error === "object" && "name" in error && typeof error.name === "string"
      ? error.name
      : "";
  const devices = callType === "video" ? "a câmera e o microfone" : "o microfone";
  switch (name) {
    case "NotAllowedError":
    case "PermissionDeniedError":
      return {
        kind: "permission_denied",
        message: `Acesso a ${devices} foi negado ou bloqueado. Libere a permissão nas configurações do navegador e tente novamente.`,
      };
    case "NotFoundError":
    case "DevicesNotFoundError":
      return {
        kind: "device_not_found",
        message: `Nenhum dispositivo de ${devices} compatível foi encontrado.`,
      };
    case "NotReadableError":
    case "TrackStartError":
      return {
        kind: "device_unavailable",
        message: `Não foi possível acessar ${devices}. Verifique se outro aplicativo está usando o dispositivo.`,
      };
    case "OverconstrainedError":
    case "ConstraintNotSatisfiedError":
      return {
        kind: "constraint_unsupported",
        message:
          "A configuração de mídia solicitada não é compatível com o dispositivo disponível.",
      };
    case "AbortError":
      return {
        kind: "aborted",
        message: "A solicitação de acesso à mídia foi interrompida. Tente novamente.",
      };
    case "SecurityError":
      return {
        kind: "security_blocked",
        message:
          "O navegador bloqueou o acesso à mídia neste contexto. Verifique se a conexão usa HTTPS.",
      };
    case "TypeError":
      return {
        kind: "unsupported_browser",
        message:
          "Este navegador não é compatível com chamadas de áudio e vídeo. Verifique se a conexão usa HTTPS.",
      };
    default:
      return {
        kind: "permission_denied",
        message: `Não foi possível acessar ${devices}. Tente novamente.`,
      };
  }
}
