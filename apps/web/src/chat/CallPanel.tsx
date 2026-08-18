import { useEffect, useRef, useState, type RefCallback } from "react";

import "./CallPanel.css";
import { avatarColorFor, initialsFrom } from "./messageDisplay";
import type { CallMediaController, CallMediaStatus } from "./useCallMedia";
import type { CallController } from "./useCallSignaling";
import type { ResourceCallController } from "./useResourceCallSession";

interface CallPanelProps {
  calls: CallController;
  currentUserId: string;
  identityStatus: "loading" | "ready" | "error";
  retryIdentity: () => Promise<void>;
  participantId?: string;
  participantName: string;
  participantAvatarUrl?: string;
  media?: CallMediaController;
  /** RF-24 resource room (channel/group). Renders instead of the RF-23 dialog while active. */
  resource?: ResourceCallController;
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
  identityStatus,
  retryIdentity,
  participantId = "",
  participantName,
  participantAvatarUrl,
  media,
  resource,
}: CallPanelProps) {
  const call = calls.call;
  const dialogCallId = call?.call_id;
  const dialogRef = useRef<HTMLDialogElement>(null);
  const [endingCallId, setEndingCallId] = useState("");
  const [decliningCallId, setDecliningCallId] = useState("");
  const [retryingMediaCallId, setRetryingMediaCallId] = useState("");
  const retryPromiseRef = useRef<Promise<void> | null>(null);
  const [activatingMediaCallId, setActivatingMediaCallId] = useState("");
  const activateMediaPromiseRef = useRef<Promise<void> | null>(null);
  const [retryingIdentityCallId, setRetryingIdentityCallId] = useState("");
  const identityRetryPromiseRef = useRef<Promise<void> | null>(null);
  const terminal = call && call.status in terminalLabels;
  const retryingMedia = Boolean(dialogCallId && retryingMediaCallId === dialogCallId);
  const activatingMedia = Boolean(dialogCallId && activatingMediaCallId === dialogCallId);
  // RF-23's dialog and RF-24's ResourceCallPanel take turns rendering for the
  // very same call_id (e.g. a call that goes ringing → active never changes
  // dialogCallId): this must be in the modal effect's deps below, or a
  // handoff that mounts this <dialog> for the first time on an unchanged
  // dialogCallId never calls showModal() on it — invisible in the DOM.
  const showsResourcePanel = Boolean(resource?.active && call?.status !== "active");

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
      retryPromiseRef.current = null;
      identityRetryPromiseRef.current = null;
      if (dialog.open && typeof dialog.close === "function") dialog.close();
    };
  }, [dialogCallId, showsResourcePanel]);

  const identityReady = identityStatus === "ready" && currentUserId !== "";
  // Computed even when there is no active/ringing call, and read by the RF-24
  // branch below: a direct call ringing in while a resource room is active
  // must never be invisible — RF-24's media ownership does not decide
  // whether the callee gets to see or decline an incoming RF-23 call.
  const incoming = identityReady && call?.status === "ringing" && call.callee_id === currentUserId;
  // Recusar's own in-flight state, independent of calls.pending (which only
  // ever reflects Atender's preflight): Recusar must stay clickable while
  // Atender's own permission preflight is pending. Computed here — before
  // the RF-24 early return — so both the RF-23 dialog below and the RF-24
  // incoming banner (in ResourceCallPanel) share the exact same behaviour
  // instead of drifting apart.
  const declining = call !== null && decliningCallId === call.call_id && !calls.error;
  function declineCall() {
    if (!call || declining) return;
    if (calls.decline()) setDecliningCallId(call.call_id);
  }

  // RF-24 and RF-23 share one LiveKit Room (`media`), so only one of them can
  // ever hold it. Precedence: RF-23 active > RF-24 active. Once signaling
  // confirms this call "active", ChatShell has already begun (or is
  // beginning) the handoff — show the RF-23 dialog immediately, even while
  // that handoff/token/connect is still in flight or resource.active hasn't
  // cleared yet, so the call is never hidden behind ResourceCallPanel and
  // never silently falls back to it. Below "active" (ringing, or no RF-23
  // call at all), RF-24 still owns the panel, and an incoming ringing call
  // stays visible via the banner inside ResourceCallPanel. Checked after
  // every hook above so this early return never changes the hooks called per
  // render.
  if (showsResourcePanel && resource) {
    return (
      <ResourceCallPanel
        resource={resource}
        media={media}
        currentUserId={currentUserId}
        incomingCall={
          incoming && call
            ? {
                participantName,
                video: call.call_type === "video",
                acceptPending: calls.pending,
                declining,
                onAccept: calls.accept,
                onDecline: declineCall,
              }
            : undefined
        }
      />
    );
  }

  if (!call && !calls.error) return null;
  if (!call) {
    return (
      <div className="call-toast" role="alert">
        {calls.error}
      </div>
    );
  }

  const callId = call.call_id;
  const active = call.status === "active";
  const video = call.call_type === "video";
  const ending = endingCallId === call.call_id && !calls.error;
  const activeReady = active && identityReady;
  // A restored/reconciled call never opens the browser permission prompt on
  // its own (RF-23): it stays active in signaling but requires this explicit
  // action before any getUserMedia/LiveKit connection happens.
  const activationNeeded = activeReady && calls.mediaActivationRequired;
  const permissionRecovery =
    activeReady && !activationNeeded && (media?.status === "permission-denied" || retryingMedia);
  const retryingIdentity = retryingIdentityCallId === call.call_id;
  const identityRecovery =
    (call.status === "ringing" || active) && (identityStatus === "error" || retryingIdentity);
  const mediaDisabled =
    !activeReady ||
    calls.pending ||
    !media ||
    !["connected", "reconnecting"].includes(media.status);
  const error = media?.error ?? calls.error;
  const status =
    !identityReady && (call.status === "ringing" || active)
      ? "Preparando chamada…"
      : active && media?.status === "connected"
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

  async function retryMedia() {
    if (retryPromiseRef.current || calls.pending) return;
    setRetryingMediaCallId(callId);
    let operation: Promise<void> | null = null;
    try {
      operation = calls.retryMedia();
      retryPromiseRef.current = operation;
      await operation;
    } finally {
      if (!operation || retryPromiseRef.current === operation) {
        retryPromiseRef.current = null;
        setRetryingMediaCallId("");
      }
    }
  }

  async function activateMedia() {
    if (activateMediaPromiseRef.current || calls.pending) return;
    setActivatingMediaCallId(callId);
    let operation: Promise<void> | null = null;
    try {
      operation = calls.activateMedia();
      activateMediaPromiseRef.current = operation;
      await operation;
    } finally {
      if (!operation || activateMediaPromiseRef.current === operation) {
        activateMediaPromiseRef.current = null;
        setActivatingMediaCallId("");
      }
    }
  }

  async function retryIdentityFromDialog() {
    if (identityRetryPromiseRef.current) return;
    setRetryingIdentityCallId(callId);
    let operation: Promise<void> | null = null;
    try {
      operation = retryIdentity();
      identityRetryPromiseRef.current = operation;
      await operation;
    } finally {
      if (!operation || identityRetryPromiseRef.current === operation) {
        identityRetryPromiseRef.current = null;
        setRetryingIdentityCallId("");
      }
    }
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
        {video && activeReady ? (
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
            {activeReady && (
              <div ref={media?.bindRemoteMedia} className="call-panel__audio-media" />
            )}
          </section>
        )}

        {video && activeReady && (
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

        {identityRecovery ? (
          <div className="call-panel__error" role="alert">
            <span>
              Não foi possível preparar a chamada. Verifique sua conexão e tente novamente.
            </span>
            <button
              type="button"
              autoFocus
              aria-busy={retryingIdentity}
              disabled={retryingIdentity}
              onClick={() => void retryIdentityFromDialog()}
            >
              Tentar novamente
            </button>
          </div>
        ) : permissionRecovery ? (
          <div className="call-panel__error" role="alert">
            <span>{mediaStatusLabels["permission-denied"]}</span>
            <button type="button" disabled={retryingMedia || calls.pending} onClick={retryMedia}>
              Tentar mídia novamente
            </button>
          </div>
        ) : activationNeeded ? (
          <div className="call-panel__error" role={calls.error ? "alert" : "status"}>
            <span>
              {calls.error ??
                (video
                  ? "Ative sua câmera e o microfone para continuar a chamada."
                  : "Ative seu microfone para continuar a chamada.")}
            </span>
            <button
              type="button"
              aria-busy={activatingMedia}
              disabled={activatingMedia || calls.pending}
              onClick={() => void activateMedia()}
            >
              {video ? "Permitir câmera e microfone" : "Permitir microfone"}
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

        {error && !identityRecovery && !permissionRecovery && !activationNeeded && (
          <div className="call-panel__error" role="alert">
            <span>{error}</span>
            {active && (
              <button type="button" disabled={retryingMedia || calls.pending} onClick={retryMedia}>
                Tentar mídia novamente
              </button>
            )}
          </div>
        )}
      </main>

      <footer className="call-panel__controls" aria-label="Controles da chamada">
        {media &&
          (media.mediaLoading || media.audioStarting || media.audioActivationRequired) &&
          identityReady &&
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
        {identityReady && incoming && (
          <IncomingCallActions
            acceptPending={calls.pending}
            declining={declining}
            onAccept={calls.accept}
            onDecline={declineCall}
          />
        )}
        {identityReady && call.status === "ringing" && !incoming && (
          <CallAction
            label="Cancelar chamada"
            icon="call_end"
            variant="danger"
            autoFocus
            disabled={calls.pending}
            onClick={calls.cancel}
          />
        )}
        {activeReady && (
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
              autoFocus
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
  "aria-busy"?: boolean;
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
  "aria-busy": ariaBusy,
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
      aria-busy={ariaBusy}
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

interface IncomingCallActionsProps {
  /** Atender's own gate: calls.pending, shared with every other RF-23 command. */
  acceptPending: boolean;
  /** Recusar's own gate — never calls.pending, so it stays clickable during Atender's preflight. */
  declining: boolean;
  onAccept: () => unknown;
  onDecline: () => unknown;
}

/**
 * Shared by the RF-23 dialog's incoming footer and the RF-24 banner
 * (ResourceCallPanel), so the two can never drift apart on which state gates
 * which button.
 */
function IncomingCallActions({
  acceptPending,
  declining,
  onAccept,
  onDecline,
}: IncomingCallActionsProps) {
  return (
    <>
      <CallAction
        label="Atender"
        icon="call"
        variant="accept"
        autoFocus
        disabled={acceptPending}
        onClick={onAccept}
      />
      <CallAction
        label="Recusar"
        icon="call_end"
        variant="danger"
        aria-busy={declining}
        disabled={declining}
        onClick={onDecline}
      />
    </>
  );
}

// ── RF-24: resource room (channel/group) ────────────────────────────────────

interface IncomingDirectCall {
  participantName: string;
  video: boolean;
  acceptPending: boolean;
  declining: boolean;
  onAccept: () => unknown;
  onDecline: () => unknown;
}

interface ResourceCallPanelProps {
  resource: ResourceCallController;
  media?: CallMediaController;
  currentUserId: string;
  /** RF-23 direct call ringing in while this resource room is active. */
  incomingCall?: IncomingDirectCall;
}

function ResourceCallPanel({
  resource,
  media,
  currentUserId,
  incomingCall,
}: ResourceCallPanelProps) {
  const target = resource.active;
  const dialogRef = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!target || !dialog) return;
    if (!dialog.open && typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
    return () => {
      if (dialog.open && typeof dialog.close === "function") dialog.close();
    };
  }, [target]);

  if (!target) return null;

  const participants = media?.participants ?? [];
  // +1 counts the local participant, who is never in media.participants.
  const participantCount = participants.length + 1;
  const connecting = resource.status === "connecting";
  // RF-25: a transient LiveKit reconnect never changes resource.status (the
  // Room lifecycle itself is untouched), so this must read media.status
  // directly — otherwise the grid silently keeps showing the participant
  // count with no indication that media is currently reconnecting.
  const reconnectingMedia = resource.status === "active" && media?.status === "reconnecting";
  const error = resource.error ?? (resource.status === "active" ? media?.error : null);

  return (
    <dialog
      ref={dialogRef}
      className="call-panel call-panel--grid"
      aria-label={`Chamada em ${target.name}`}
      onCancel={(event) => event.preventDefault()}
      data-testid="resource-call-panel"
    >
      <header className="call-panel__header">
        <div>
          <strong>{target.name}</strong>
          <span>
            Chamada ·{" "}
            {connecting
              ? "Entrando…"
              : `${participantCount} participante${participantCount === 1 ? "" : "s"}`}
          </span>
        </div>
        <span className="call-panel__privacy">
          <span className="material-symbols-outlined" aria-hidden="true">
            lock
          </span>
          Chamada privada
        </span>
      </header>

      {incomingCall && (
        <div
          className="call-panel__incoming"
          role="alert"
          aria-label={`Chamada de ${incomingCall.video ? "vídeo" : "áudio"} de ${incomingCall.participantName}`}
        >
          <span>
            {incomingCall.participantName} está chamando ({incomingCall.video ? "vídeo" : "áudio"})
          </span>
          <div className="call-panel__incoming-actions">
            <IncomingCallActions
              acceptPending={incomingCall.acceptPending}
              declining={incomingCall.declining}
              onAccept={incomingCall.onAccept}
              onDecline={incomingCall.onDecline}
            />
          </div>
        </div>
      )}

      <main className="call-panel__stage call-panel__stage--grid">
        <div className="call-panel__grid" data-testid="call-grid">
          <ParticipantTile
            identity={currentUserId}
            label="Você"
            hasVideo={media?.hasLocalVideo ?? false}
            bindVideo={media?.bindLocalMedia}
          />
          {participants.map((participant) => (
            <ParticipantTile
              key={participant.identity}
              identity={participant.identity}
              label={participant.displayName}
              hasVideo={participant.hasVideo}
              bindVideo={participant.bindVideo}
            />
          ))}
        </div>
        {/* Hidden sink: every remote participant's audio plays here regardless
            of whether their tile currently shows video. */}
        <div ref={media?.bindRemoteAudio} className="call-panel__audio-media" />

        {connecting ? (
          <p className="call-panel__status" role="status" aria-live="polite">
            <span aria-hidden="true" />
            Entrando na chamada…
          </p>
        ) : reconnectingMedia ? (
          <p className="call-panel__status" role="status" aria-live="polite">
            <span aria-hidden="true" />
            {mediaStatusLabels.reconnecting}
          </p>
        ) : (
          error && (
            <div className="call-panel__error" role="alert">
              <span>{error}</span>
              {resource.errorOperation === "leave" ? (
                <button type="button" onClick={() => void resource.leave().catch(() => undefined)}>
                  Tentar sair novamente
                </button>
              ) : (
                <button type="button" onClick={() => void resource.join(target)}>
                  Tentar novamente
                </button>
              )}
            </div>
          )
        )}
      </main>

      <footer className="call-panel__controls" aria-label="Controles da chamada">
        {media && (media.mediaLoading || media.audioStarting || media.audioActivationRequired) && (
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
        <CallAction
          label={media?.microphoneEnabled ? "Desativar microfone" : "Ativar microfone"}
          shortLabel={media?.microphoneEnabled ? "Microfone" : "Microfone desligado"}
          icon={media?.microphoneEnabled ? "mic" : "mic_off"}
          pressed={media?.microphoneEnabled ?? false}
          muted={!media?.microphoneEnabled}
          disabled={connecting || media?.pendingControl === "microphone"}
          onClick={() => void media?.toggleMicrophone()}
        />
        <CallAction
          label="Sair da chamada"
          shortLabel="Sair"
          icon="call_end"
          variant="danger"
          autoFocus
          // leave() can now reject when cleanup itself fails (never a false
          // "left"): resource.error/status already surface that, so this
          // click handler only needs to stop it becoming an unhandled
          // rejection. Re-clicking retries the very same failed attempt.
          onClick={() => void resource.leave().catch(() => undefined)}
        />
        <CallAction
          label={media?.cameraEnabled ? "Desativar câmera" : "Ativar câmera"}
          shortLabel={media?.cameraEnabled ? "Câmera" : "Câmera desligada"}
          icon={media?.cameraEnabled ? "videocam" : "videocam_off"}
          pressed={media?.cameraEnabled ?? false}
          muted={!media?.cameraEnabled}
          disabled={connecting || media?.pendingControl === "camera"}
          onClick={() => void media?.toggleCamera()}
        />
      </footer>
    </dialog>
  );
}

interface ParticipantTileProps {
  identity: string;
  label: string;
  hasVideo: boolean;
  bindVideo?: RefCallback<HTMLDivElement>;
}

function ParticipantTile({ identity, label, hasVideo, bindVideo }: ParticipantTileProps) {
  return (
    <div className="call-panel__tile" data-testid={`participant-tile-${identity}`}>
      <div
        ref={bindVideo}
        className="call-panel__media call-panel__media--tile"
        data-testid={`participant-video-${identity}`}
      />
      {!hasVideo && (
        <div className="call-panel__fallback call-panel__fallback--tile">
          <ParticipantAvatar id={identity} name={label} size="remote" />
        </div>
      )}
      <span className="call-panel__participant-label">{label}</span>
    </div>
  );
}
