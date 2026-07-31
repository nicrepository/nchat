import { useEffect } from "react";

import "./CallPanel.css";
import type { CallController } from "./useCallSignaling";

interface CallPanelProps {
  calls: CallController;
  currentUserId: string;
  participantName: string;
}

const terminalLabels = {
  declined: "Chamada recusada",
  cancelled: "Chamada cancelada",
  timed_out: "Chamada não atendida",
  ended: "Chamada encerrada",
} as const;

export default function CallPanel({ calls, currentUserId, participantName }: CallPanelProps) {
  const call = calls.call;
  const terminal = call && call.status in terminalLabels;

  useEffect(() => {
    if (!terminal) return;
    const timer = window.setTimeout(calls.clearTerminal, 4_000);
    return () => window.clearTimeout(timer);
  }, [calls.clearTerminal, terminal]);

  if (!call && !calls.error) return null;
  const incoming = call?.status === "ringing" && call.callee_id === currentUserId;

  return (
    <aside className="call-panel" aria-live="polite" data-testid="call-panel">
      {call && (
        <div>
          <strong>
            {incoming
              ? `${participantName} está chamando`
              : call.status === "ringing"
                ? `Chamando ${participantName}`
                : call.status === "active"
                  ? calls.mediaReady
                    ? `Em chamada com ${participantName}`
                    : `Preparando chamada com ${participantName}`
                  : terminalLabels[call.status as keyof typeof terminalLabels]}
          </strong>
          <span>{call.call_type === "video" ? "Vídeo" : "Áudio"}</span>
        </div>
      )}

      <div className="call-panel__actions">
        {incoming && (
          <>
            <button type="button" onClick={calls.accept} disabled={calls.pending}>
              Atender
            </button>
            <button type="button" onClick={calls.decline} disabled={calls.pending}>
              Recusar
            </button>
          </>
        )}
        {call?.status === "ringing" && !incoming && (
          <button type="button" onClick={calls.cancel} disabled={calls.pending}>
            Cancelar chamada
          </button>
        )}
        {call?.status === "active" && (
          <button type="button" onClick={calls.end} disabled={calls.pending}>
            Encerrar chamada
          </button>
        )}
        {terminal && (
          <button type="button" onClick={calls.clearTerminal}>
            Fechar
          </button>
        )}
      </div>

      {calls.error && (
        <div className="call-panel__error" role="alert">
          {calls.error}
          {call?.status === "active" && (
            <button type="button" onClick={calls.retryMedia}>
              Tentar mídia novamente
            </button>
          )}
        </div>
      )}
    </aside>
  );
}
