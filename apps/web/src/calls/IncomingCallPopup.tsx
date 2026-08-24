import { useRef, useState } from "react";

import { initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
import "./CallPresentation.css";

interface IncomingCallPopupProps {
  name: string;
  avatarUrl?: string;
  targetKind: "user" | "dm" | "channel";
  callType: "audio" | "video";
  participantCount?: number;
  onAccept: () => unknown;
  onReject: () => unknown;
  identityStatus?: "loading" | "ready" | "error";
  onRetryIdentity?: () => unknown;
}

const kindLabel = { user: "DM", dm: "Grupo", channel: "Canal" } as const;

export default function IncomingCallPopup({
  name,
  avatarUrl,
  targetKind,
  callType,
  participantCount,
  onAccept,
  onReject,
  identityStatus = "ready",
  onRetryIdentity,
}: IncomingCallPopupProps) {
  const retryRef = useRef<Promise<unknown> | null>(null);
  const [retrying, setRetrying] = useState(false);
  const retryIdentity = () => {
    if (retryRef.current || !onRetryIdentity) return;
    const operation = Promise.resolve(onRetryIdentity());
    retryRef.current = operation;
    setRetrying(true);
    const clearIfCurrent = () => {
      if (retryRef.current === operation) {
        retryRef.current = null;
        setRetrying(false);
      }
    };
    operation.then(clearIfCurrent, clearIfCurrent);
  };
  return (
    <aside className="incoming-call" role="dialog" aria-modal="false" aria-label="Chamada recebida">
      <div className="incoming-call__identity">
        <div className="incoming-call__avatar" aria-hidden="true">
          <PersonAvatarImage
            src={avatarUrl}
            initials={initialsFrom(name)}
            imgClassName="incoming-call__avatar-img"
          />
        </div>
        <div>
          <strong>{name}</strong>
          <span>
            {kindLabel[targetKind]} · chamada de {callType === "video" ? "vídeo" : "áudio"}
          </span>
          {participantCount !== undefined && (
            <span>
              {participantCount} participante{participantCount === 1 ? "" : "s"}
            </span>
          )}
        </div>
      </div>
      <div className="incoming-call__actions">
        {identityStatus === "loading" ? (
          <span role="status">Preparando chamada…</span>
        ) : identityStatus === "error" ? (
          <>
            <span role="alert">Não foi possível preparar a chamada.</span>
            <button type="button" disabled={retrying} onClick={retryIdentity}>
              Tentar novamente
            </button>
          </>
        ) : (
          <>
            <button type="button" className="incoming-call__reject" onClick={onReject}>
              Recusar
            </button>
            <button type="button" className="incoming-call__accept" onClick={onAccept}>
              {callType === "video" ? "Atender com câmera" : "Atender"}
            </button>
          </>
        )}
      </div>
    </aside>
  );
}
