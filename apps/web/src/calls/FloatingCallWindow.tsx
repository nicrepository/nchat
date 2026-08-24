import { useCallback, useEffect, useRef, useState } from "react";

import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
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
  /**
   * Compact screen-share status text (issue #611) — "Você está
   * compartilhando a tela" or "<displayName> está compartilhando a tela".
   * Undefined/absent renders no indicator at all. Deliberately just text:
   * no preview, no second grid — kept identical in shape/position to the
   * previous boolean indicator so there is no layout jump.
   */
  screenShareLabel?: string;
  startedAt?: string;
  controls: CallControlProps;
  onExpand: () => unknown;
  bindLocalMedia?: React.RefCallback<HTMLDivElement>;
  bindRemoteMedia?: React.RefCallback<HTMLDivElement>;
  /**
   * Whether the remote stage currently has a usable video track. Required,
   * not defaulted: a callsite that forgets to wire real media state must get
   * a type error, never a silent "assume video is present" that hides the
   * fallback forever.
   */
  hasRemoteVideo: boolean;
  /** Stable seed (peer/group id) for the remote fallback avatar's color. */
  remoteSeed: string;
  /** Direct-call peer's avatar; absent for a resource/group call or a peer with no configured avatar. */
  avatarUrl?: string;
  /** Whether the local preview currently has a usable video track. Same no-default rule as hasRemoteVideo. */
  hasLocalVideo: boolean;
  /** Stable seed (the current user's id) for the local fallback avatar's color. */
  localSeed: string;
  /**
   * The local participant's call-presentation name (issue #612) — the real
   * profile name plus "(você)", or a bare "Você" fallback. Computed by the
   * caller (CallSessionProvider) via localParticipantDisplayName, since only
   * the caller knows the self-profile state.
   */
  localName: string;
  /**
   * Initials for the local fallback avatar, derived from the raw profile
   * name — never from localName's "(você)" suffix (issue #612 blocker),
   * which would otherwise feed "(" in as a second initial for a one-word
   * name.
   */
  localInitials: string;
  /** The local participant's configured avatar, when available. */
  localAvatarUrl?: string;
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
  screenShareLabel,
  controls,
  onExpand,
  bindLocalMedia,
  bindRemoteMedia,
  hasRemoteVideo,
  remoteSeed,
  avatarUrl,
  hasLocalVideo,
  localSeed,
  localName,
  localInitials,
  localAvatarUrl,
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
    const reclamp = () =>
      setPosition((current) => clampPosition(current, size(), viewport(), MARGIN));
    const frame = window.requestAnimationFrame(() => placeAt(corner));
    window.addEventListener("resize", reclamp);
    // The window's own height changes after mount (a denied getUserMedia
    // prompt adds a recovery banner above the activation button, for
    // example), and that must reclamp position too, not just viewport
    // resizes — otherwise a bottom-anchored corner keeps the stale, smaller
    // height baked into its y offset and pushes trailing controls off-screen.
    const node = rootRef.current;
    const observer =
      node && typeof ResizeObserver !== "undefined" ? new ResizeObserver(reclamp) : null;
    if (node && observer) observer.observe(node);
    return () => {
      window.cancelAnimationFrame(frame);
      window.removeEventListener("resize", reclamp);
      observer?.disconnect();
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
        {!hasRemoteVideo && (
          <div className="floating-call__avatar-wrap">
            <div
              className={`floating-call__avatar call-avatar call-avatar--${avatarColorFor(remoteSeed)}`}
              aria-hidden="true"
            >
              <PersonAvatarImage
                src={avatarUrl}
                initials={initialsFrom(title)}
                imgClassName="call-avatar__img"
              />
            </div>
          </div>
        )}
        <div className="floating-call__local">
          <div ref={bindLocalMedia} className="floating-call__local-media" />
          {!hasLocalVideo && (
            <div
              className={`floating-call__local-avatar call-avatar call-avatar--${avatarColorFor(localSeed)}`}
              role="img"
              aria-label={localName}
            >
              <PersonAvatarImage
                src={localAvatarUrl}
                initials={localInitials}
                imgClassName="call-avatar__img"
              />
            </div>
          )}
        </div>
        <span className="floating-call__count">
          {participantCount} participante{participantCount === 1 ? "" : "s"}
        </span>
        {activeSpeakerName && (
          <span className="floating-call__speaker">{activeSpeakerName} está falando</span>
        )}
        {screenShareLabel && <span className="floating-call__share">{screenShareLabel}</span>}
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
