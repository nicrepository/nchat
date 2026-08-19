import { useCallback, useEffect, useRef, useState } from "react";

import CallControls, { type CallControlProps } from "./CallControls";
import {
  clampPosition,
  positionForCorner,
  snapPosition,
  type FloatingCorner,
  type Point,
} from "./floatingGeometry";
import "./CallPresentation.css";

const MARGIN = 16;
const POSITION_KEY = "nchat.call.floating-corner.v1";
const corners: FloatingCorner[] = ["top-left", "top-right", "bottom-left", "bottom-right"];
// A pointerdown that starts on a control inside the handle (the expand
// button, its icon span, ...) must never be treated as a drag gesture: the
// event still bubbles to the handle's own onPointerDown, so the boundary has
// to reject it there rather than relying on the button to stop propagation.
const INTERACTIVE_SELECTOR = "button, a, input, select, textarea, [contenteditable='true']";

interface FloatingCallWindowProps {
  title: string;
  status: "connecting" | "connected" | "reconnecting" | "failed";
  participantCount: number;
  activeSpeakerName?: string;
  screenShareActive?: boolean;
  startedAt?: string;
  controls: CallControlProps;
  onExpand: () => unknown;
  bindLocalMedia?: React.RefCallback<HTMLDivElement>;
  bindRemoteMedia?: React.RefCallback<HTMLDivElement>;
  testId?: string;
  activationRequired?: boolean;
  activationLabel?: string;
  onActivate?: () => unknown;
  identityStatus?: "loading" | "ready" | "error";
  onRetryIdentity?: () => unknown;
  error?: string | null;
  onRetry?: () => unknown;
}

const statuses = {
  connecting: "Conectando",
  connected: "Em chamada",
  reconnecting: "Reconectando",
  failed: "Falha na chamada",
} as const;

function storedCorner(): FloatingCorner {
  try {
    const value = localStorage.getItem(POSITION_KEY);
    return corners.includes(value as FloatingCorner) ? (value as FloatingCorner) : "bottom-right";
  } catch {
    return "bottom-right";
  }
}

export default function FloatingCallWindow({
  title,
  status,
  participantCount,
  activeSpeakerName,
  screenShareActive = false,
  controls,
  onExpand,
  bindLocalMedia,
  bindRemoteMedia,
  testId = "floating-call-window",
  activationRequired = false,
  activationLabel = "Permitir câmera e microfone",
  onActivate,
  identityStatus = "ready",
  onRetryIdentity,
  error,
  onRetry,
}: FloatingCallWindowProps) {
  const rootRef = useRef<HTMLElement>(null);
  const dragRef = useRef<{ pointerId: number; offset: Point } | null>(null);
  const [corner, setCorner] = useState<FloatingCorner>(storedCorner);
  const [position, setPosition] = useState<Point>(() =>
    positionForCorner(
      corner,
      { width: 320, height: 240 },
      { width: window.innerWidth, height: window.innerHeight },
      MARGIN,
    ),
  );

  const size = useCallback(() => {
    const rect = rootRef.current?.getBoundingClientRect();
    return { width: rect?.width || 320, height: rect?.height || 240 };
  }, []);
  const viewport = useCallback(
    () => ({ width: window.innerWidth, height: window.innerHeight }),
    [],
  );
  const placeAt = useCallback(
    (nextCorner: FloatingCorner) => {
      setCorner(nextCorner);
      setPosition(positionForCorner(nextCorner, size(), viewport(), MARGIN));
      try {
        localStorage.setItem(POSITION_KEY, nextCorner);
      } catch {
        // Position is an optional non-sensitive preference.
      }
    },
    [size, viewport],
  );

  useEffect(() => {
    const onResize = () =>
      setPosition((current) => clampPosition(current, size(), viewport(), MARGIN));
    const frame = window.requestAnimationFrame(() => placeAt(corner));
    window.addEventListener("resize", onResize);
    return () => {
      window.cancelAnimationFrame(frame);
      window.removeEventListener("resize", onResize);
    };
    // The initial corner is intentionally read only once.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [placeAt, size, viewport]);

  const desktopDrag = () => !window.matchMedia?.("(max-width: 720px)").matches;

  return (
    <aside
      ref={rootRef}
      className="floating-call"
      aria-label={`Chamada com ${title}`}
      data-testid={testId}
      style={{ transform: `translate3d(${position.x}px, ${position.y}px, 0)` }}
    >
      <header
        className="floating-call__handle"
        data-testid="floating-call-handle"
        onPointerDown={(event) => {
          if (!desktopDrag() || event.button !== 0) return;
          if (event.target instanceof Element && event.target.closest(INTERACTIVE_SELECTOR)) {
            return;
          }
          const rect = rootRef.current?.getBoundingClientRect();
          dragRef.current = {
            pointerId: event.pointerId,
            offset: {
              x: event.clientX - (rect?.left ?? position.x),
              y: event.clientY - (rect?.top ?? position.y),
            },
          };
          event.currentTarget.setPointerCapture?.(event.pointerId);
        }}
        onPointerMove={(event) => {
          const drag = dragRef.current;
          if (!drag || drag.pointerId !== event.pointerId) return;
          setPosition(
            clampPosition(
              { x: event.clientX - drag.offset.x, y: event.clientY - drag.offset.y },
              size(),
              viewport(),
              MARGIN,
            ),
          );
        }}
        onPointerUp={(event) => {
          const drag = dragRef.current;
          if (!drag || drag.pointerId !== event.pointerId) return;
          dragRef.current = null;
          event.currentTarget.releasePointerCapture?.(event.pointerId);
          const snapped = snapPosition(position, size(), viewport(), 48, MARGIN);
          setPosition(snapped.position);
          if (snapped.corner) placeAt(snapped.corner);
        }}
        onPointerCancel={(event) => {
          const drag = dragRef.current;
          if (!drag || drag.pointerId !== event.pointerId) return;
          dragRef.current = null;
          event.currentTarget.releasePointerCapture?.(event.pointerId);
        }}
      >
        <div>
          <strong>{title}</strong>
          <span role="status" aria-live="polite">
            {statuses[status]}
          </span>
        </div>
        <button type="button" aria-label="Expandir em nova aba" onClick={onExpand}>
          <span className="material-symbols-outlined" aria-hidden="true">
            open_in_new
          </span>
        </button>
      </header>
      <div className="floating-call__stage">
        <div ref={bindRemoteMedia} className="floating-call__remote" />
        <div ref={bindLocalMedia} className="floating-call__local" />
        <span className="floating-call__count">
          {participantCount} participante{participantCount === 1 ? "" : "s"}
        </span>
        {activeSpeakerName && <span>{activeSpeakerName} está falando</span>}
        {screenShareActive && <span>Compartilhando tela</span>}
      </div>
      <CallControls {...controls} />
      {identityStatus === "loading" && (
        <p className="floating-call__recovery" role="status">
          Preparando chamada…
        </p>
      )}
      {identityStatus === "error" && (
        <div className="floating-call__recovery">
          <span role="alert">Não foi possível preparar a chamada.</span>
          <button type="button" onClick={onRetryIdentity}>
            Tentar novamente
          </button>
        </div>
      )}
      {error && (
        <div className="floating-call__recovery">
          <span role="alert">{error}</span>
          {onRetry && (
            <button type="button" onClick={onRetry}>
              Tentar mídia novamente
            </button>
          )}
        </div>
      )}
      {activationRequired && (
        <button type="button" className="floating-call__activate" onClick={onActivate}>
          {activationLabel}
        </button>
      )}
    </aside>
  );
}
