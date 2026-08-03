import { useEffect, useRef, useState } from "react";

import "./CallPanel.css";
import { avatarColorFor, initialsFrom } from "./messageDisplay";
import type { CallMediaController, CallMediaStatus } from "./useCallMedia";
import type { CallController } from "./useCallSignaling";

interface CallPanelProps {
  calls: CallController;
  currentUserId: string;
  participantId?: string;
  participantName: string;
  participantAvatarUrl?: string;
  media?: CallMediaController;
}

const terminalLabels = {
  declined: "Chamada recusada",
  cancelled: "Chamada cancelada",
  timed_out: "Chamada não atendida",
  ended: "Chamada encerrada",
} as const;

const mediaStatusLabels: Partial<Record<CallMediaStatus, string>> = {
  idle: "Preparando mídia…",
  connecting: "Conectando mídia…",
  reconnecting: "Reconectando mídia…",
  "permission-denied":
    "Acesso à câmera ou ao microfone foi bloqueado. Ajuste a permissão no navegador e tente novamente.",
  error: "Falha na conexão de mídia.",
};

export default function CallPanel({
  calls,
  currentUserId,
  participantId = "",
  participantName,
  participantAvatarUrl,
  media,
}: CallPanelProps) {
  const call = calls.call;
  const dialogCallId = call?.call_id;
  const dialogRef = useRef<HTMLDialogElement>(null);
  const [endingCallId, setEndingCallId] = useState("");
  const [retryingMediaCallId, setRetryingMediaCallId] = useState("");
  const retryProgressRef = useRef(false);
  const terminal = call && call.status in terminalLabels;
  const retryingMedia = Boolean(dialogCallId && retryingMediaCallId === dialogCallId);
  const mediaStatus = media?.status;
  const mediaLoading = media?.mediaLoading ?? false;

  useEffect(() => {
    if (!terminal) return;
    const timer = window.setTimeout(calls.clearTerminal, 4_000);
    return () => window.clearTimeout(timer);
  }, [calls.clearTerminal, terminal]);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialogCallId || !dialog) return;
    if (!dialog.open && typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
    return () => {
      if (dialog.open && typeof dialog.close === "function") dialog.close();
    };
  }, [dialogCallId]);

  useEffect(() => {
    if (!retryingMedia) {
      retryProgressRef.current = false;
      return;
    }
    if (
      mediaLoading ||
      mediaStatus === "idle" ||
      mediaStatus === "connecting" ||
      mediaStatus === "reconnecting"
    ) {
      retryProgressRef.current = true;
      return;
    }
    if (
      retryProgressRef.current &&
      (mediaStatus === "connected" ||
        mediaStatus === "permission-denied" ||
        mediaStatus === "error")
    ) {
      retryProgressRef.current = false;
      setRetryingMediaCallId("");
    }
  }, [mediaLoading, mediaStatus, retryingMedia]);

  if (!call && !calls.error) return null;
  if (!call) {
    return (
      <div className="call-toast" role="alert">
        {calls.error}
      </div>
    );
  }

  const incoming = call.status === "ringing" && call.callee_id === currentUserId;
  const callId = call.call_id;
  const active = call.status === "active";
  const video = call.call_type === "video";
  const ending = endingCallId === call.call_id && !calls.error;
  const permissionRecovery = active && (media?.status === "permission-denied" || retryingMedia);
  const mediaDisabled = !active || !media || !["connected", "reconnecting"].includes(media.status);
  const error = media?.error ?? calls.error;
  const status =
    active && media?.status === "connected"
      ? media.hasRemoteMedia
        ? "Em chamada"
        : "Aguardando participante"
      : active && media
        ? mediaStatusLabels[media.status]
        : incoming
          ? `${participantName} está chamando`
          : call.status === "ringing"
            ? `Chamando ${participantName}…`
            : terminalLabels[call.status as keyof typeof terminalLabels];

  function endCall() {
    if (!ending && calls.end()) setEndingCallId(callId);
  }

  function retryMedia() {
    if (retryingMedia || calls.pending) return;
    retryProgressRef.current = false;
    setRetryingMediaCallId(callId);
    calls.retryMedia();
  }

  return (
    <dialog
      ref={dialogRef}
      className="call-panel"
      aria-label={`Chamada de ${video ? "vídeo" : "áudio"} com ${participantName}`}
      onCancel={(event) => event.preventDefault()}
      data-testid="call-panel"
    >
      <header className="call-panel__header">
        <div>
          <strong>NChat</strong>
          <span>{video ? "Chamada de vídeo" : "Chamada de áudio"}</span>
        </div>
        <span className="call-panel__privacy">
          <span className="material-symbols-outlined" aria-hidden="true">
            lock
          </span>
          Chamada privada
        </span>
      </header>

      <main className={`call-panel__stage${video ? "" : " call-panel__stage--audio"}`}>
        {video && active ? (
          <section className="call-panel__remote" aria-label={`Vídeo de ${participantName}`}>
            <div
              ref={media?.bindRemoteMedia}
              className="call-panel__media call-panel__media--remote"
              data-testid="call-remote-media"
            />
            {!media?.hasRemoteVideo && (
              <div className="call-panel__fallback">
                <ParticipantAvatar
                  id={participantId}
                  name={participantName}
                  src={participantAvatarUrl}
                  size="remote"
                />
                <strong>{participantName}</strong>
                <span>A câmera de {participantName} está desligada</span>
              </div>
            )}
            <span className="call-panel__participant-label">{participantName}</span>
          </section>
        ) : (
          <section className="call-panel__audio-person">
            <ParticipantAvatar
              id={participantId}
              name={participantName}
              src={participantAvatarUrl}
              size="remote"
            />
            <h1>{participantName}</h1>
            {active && <div ref={media?.bindRemoteMedia} className="call-panel__audio-media" />}
          </section>
        )}

        {video && active && (
          <aside className="call-panel__local" aria-label="Sua pré-visualização">
            <div
              ref={media?.bindLocalMedia}
              className="call-panel__media call-panel__media--local"
              data-testid="call-local-media"
            />
            {!media?.hasLocalVideo && (
              <div className="call-panel__local-fallback">
                <span className="material-symbols-outlined" aria-hidden="true">
                  videocam_off
                </span>
                <span>Sua câmera está desligada</span>
              </div>
            )}
            <span className="call-panel__participant-label">Você</span>
          </aside>
        )}

        {permissionRecovery ? (
          <div className="call-panel__error" role="alert">
            <span>{mediaStatusLabels["permission-denied"]}</span>
            <button type="button" disabled={retryingMedia || calls.pending} onClick={retryMedia}>
              Tentar mídia novamente
            </button>
          </div>
        ) : (
          status && (
            <p className="call-panel__status" role="status" aria-live="polite">
              <span aria-hidden="true" />
              {status}
            </p>
          )
        )}

        {error && !permissionRecovery && (
          <div className="call-panel__error" role="alert">
            <span>{error}</span>
            {active && (
              <button type="button" onClick={calls.retryMedia}>
                Tentar mídia novamente
              </button>
            )}
          </div>
        )}
      </main>

      <footer className="call-panel__controls" aria-label="Controles da chamada">
        {media &&
          (media.mediaLoading || media.audioStarting || media.audioActivationRequired) &&
          (active || call.status === "ringing") && (
            <CallAction
              label={
                media.mediaLoading
                  ? "Carregando áudio da chamada"
                  : media.audioStarting
                    ? "Ativando áudio da chamada"
                    : "Ativar áudio da chamada"
              }
              shortLabel={
                media.mediaLoading
                  ? "Carregando áudio"
                  : media.audioStarting
                    ? "Ativando áudio"
                    : "Ativar áudio"
              }
              icon="volume_up"
              disabled={media.mediaLoading || media.audioStarting}
              onClick={() => void media.activateAudio()}
            />
          )}
        {incoming && (
          <>
            <CallAction
              label="Atender"
              icon="call"
              variant="accept"
              autoFocus
              disabled={calls.pending}
              onClick={calls.accept}
            />
            <CallAction
              label="Recusar"
              icon="call_end"
              variant="danger"
              disabled={calls.pending}
              onClick={calls.decline}
            />
          </>
        )}
        {call.status === "ringing" && !incoming && (
          <CallAction
            label="Cancelar chamada"
            icon="call_end"
            variant="danger"
            autoFocus
            disabled={calls.pending}
            onClick={calls.cancel}
          />
        )}
        {active && (
          <>
            <CallAction
              label={media?.microphoneEnabled ? "Desativar microfone" : "Ativar microfone"}
              shortLabel={media?.microphoneEnabled ? "Microfone" : "Microfone desligado"}
              icon={media?.microphoneEnabled ? "mic" : "mic_off"}
              pressed={media?.microphoneEnabled ?? false}
              muted={!media?.microphoneEnabled}
              disabled={mediaDisabled || media?.pendingControl === "microphone"}
              onClick={() => void media?.toggleMicrophone()}
            />
            <CallAction
              label="Encerrar chamada"
              shortLabel="Encerrar"
              icon="call_end"
              variant="danger"
              disabled={calls.pending || ending}
              onClick={endCall}
            />
            {video && (
              <CallAction
                label={media?.cameraEnabled ? "Desativar câmera" : "Ativar câmera"}
                shortLabel={media?.cameraEnabled ? "Câmera" : "Câmera desligada"}
                icon={media?.cameraEnabled ? "videocam" : "videocam_off"}
                pressed={media?.cameraEnabled ?? false}
                muted={!media?.cameraEnabled}
                disabled={mediaDisabled || media?.pendingControl === "camera"}
                onClick={() => void media?.toggleCamera()}
              />
            )}
          </>
        )}
        {terminal && (
          <CallAction label="Fechar" icon="close" autoFocus onClick={calls.clearTerminal} />
        )}
      </footer>
    </dialog>
  );
}

interface ParticipantAvatarProps {
  id: string;
  name: string;
  src?: string;
  size: "remote";
}

function ParticipantAvatar({ id, name, src, size }: ParticipantAvatarProps) {
  const color = avatarColorFor(id || name);
  return (
    <div
      className={`call-panel__avatar call-panel__avatar--${size} call-panel__avatar--${color}`}
      aria-hidden="true"
    >
      <span>{initialsFrom(name)}</span>
      {src && (
        <img
          key={src}
          src={src}
          alt=""
          referrerPolicy="no-referrer"
          onError={(event) => {
            event.currentTarget.hidden = true;
          }}
        />
      )}
    </div>
  );
}

interface CallActionProps {
  label: string;
  shortLabel?: string;
  icon: string;
  variant?: "default" | "accept" | "danger";
  pressed?: boolean;
  muted?: boolean;
  autoFocus?: boolean;
  disabled?: boolean;
  onClick: () => unknown;
}

function CallAction({
  label,
  shortLabel = label,
  icon,
  variant = "default",
  pressed,
  muted,
  autoFocus,
  disabled,
  onClick,
}: CallActionProps) {
  return (
    <button
      type="button"
      className={`call-panel__control call-panel__control--${variant}${
        muted ? " call-panel__control--muted" : ""
      }`}
      aria-label={label}
      aria-pressed={pressed}
      autoFocus={autoFocus}
      disabled={disabled}
      onClick={onClick}
    >
      <span className="call-panel__control-icon material-symbols-outlined" aria-hidden="true">
        {icon}
      </span>
      <span>{shortLabel}</span>
    </button>
  );
}
