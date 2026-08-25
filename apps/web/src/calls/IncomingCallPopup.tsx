import { useRef, useState } from "react";

import { initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
import "./CallPresentation.css";

interface IncomingCallPopupProps {
  name: string;
  avatarUrl?: string;
  callType: "audio" | "video";
  onAccept: () => unknown;
  onReject: () => unknown;
  identityStatus?: "loading" | "ready" | "error";
  onRetryIdentity?: () => unknown;
}

export default function IncomingCallPopup({
  name,
  avatarUrl,
  callType,
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
          <strong className="incoming-call__name">{name}</strong>
          <span className="incoming-call__type">
            {callType === "video" ? "Chamada de vídeo" : "Chamada de voz"}
          </span>
        </div>
      </div>
      <div className="incoming-call__actions">
        {identityStatus === "loading" ? (
          <span role="status">Preparando chamada…</span>
        ) : identityStatus === "error" ? (
          <>
            <span role="alert">Não foi possível preparar a chamada.</span>
            <button
              type="button"
              className="incoming-call__retry"
              disabled={retrying}
              onClick={retryIdentity}
            >
              Tentar novamente
            </button>
          </>
        ) : (
          <>
            <button
              type="button"
              className="incoming-call__reject"
              aria-label={`Recusar chamada de ${name}`}
              onClick={onReject}
            >
              Recusar
            </button>
            <button
              type="button"
              className="incoming-call__accept"
              aria-label={
                callType === "video"
                  ? `Atender com câmera a chamada de vídeo de ${name}`
                  : `Atender chamada de ${name}`
              }
              onClick={onAccept}
            >
              {callType === "video" ? "Atender com câmera" : "Atender"}
            </button>
          </>
        )}
      </div>
    </aside>
  );
}
