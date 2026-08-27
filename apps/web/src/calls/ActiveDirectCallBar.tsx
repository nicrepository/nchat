import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
import { useElapsedLabel } from "./callBarTiming";
import "./CallPresentation.css";

export interface ActiveDirectCallBarProps {
  /** e.g. "Chamada de voz — Ana" / "Chamada de vídeo — Ana" — call_type plus the DM's own resolved name, never invented client-side. */
  title: string;
  /** Authoritative call-start instant (Call.created_at) — never Date.now() at mount. */
  startedAt: string;
  /** Server-resolved counterpart identity (never route/name-derived) — used only for the avatar's deterministic color, matching the sidebar/header. */
  peerUserId: string;
  peerName: string;
  peerAvatarUrl?: string;
  microphoneEnabled: boolean;
  microphonePending: boolean;
  onToggleMicrophone: () => void;
  onLeave: () => void;
  onOpenFullCall: () => void;
}

/**
 * Persistent, compact call-status bar for a 1:1 DM view (issue #673) — the
 * direct-call counterpart of ActiveResourceCallBar (issue #642), sharing its
 * `.voicebanner` presentation but deliberately not its component or its
 * discriminated union: a direct call has no "available/join" state (both
 * parties are already in it the instant it is active — IncomingCallPopup
 * owns the ringing surface) and exactly one remote identity, never a
 * roster/active-speaker stack. Folding this into ActiveResourceCallBar's
 * union would trade a handful of duplicated lines for a mode that doesn't
 * share that union's actual invariants — the elapsed-time tick is the one
 * real duplication, and it is shared via useElapsedLabel instead.
 *
 * Presentation only: no lifecycle beyond the elapsed-time tick, no owned
 * mute state — every control calls back into the authoritative
 * CallSessionProvider-derived callbacks the caller supplies (ChatShell's
 * directCallSession, mirroring resourceCallSession).
 */
export default function ActiveDirectCallBar({
  title,
  startedAt,
  peerUserId,
  peerName,
  peerAvatarUrl,
  microphoneEnabled,
  microphonePending,
  onToggleMicrophone,
  onLeave,
  onOpenFullCall,
}: ActiveDirectCallBarProps) {
  const elapsed = useElapsedLabel(startedAt);
  // Folded into the main button's own accessible name rather than an
  // aria-live region (matches ActiveResourceCallBar's own rationale): a
  // screen-reader user hears duration on their own terms, exactly once, the
  // instant they reach the control.
  const openLabel = `Abrir chamada — ${title} — ${elapsed}`;

  return (
    <div className="voicebanner" data-testid="active-direct-call-bar">
      <button
        type="button"
        className="voicebanner__open"
        aria-label={openLabel}
        onClick={onOpenFullCall}
      >
        <span className="voicebanner__icon" aria-hidden="true">
          <span className="material-symbols-outlined">call</span>
        </span>
        <span className="voicebanner__info">
          <span className="voicebanner__title">{title}</span>
          <span className="voicebanner__meta">
            <span className="voicebanner__dot" aria-hidden="true" />
            <span>{elapsed}</span>
          </span>
        </span>
      </button>
      <div className="voicebanner__avatars" aria-hidden="true">
        <span
          className={`voicebanner__avatar call-avatar call-avatar--${avatarColorFor(peerUserId)}`}
        >
          <PersonAvatarImage
            src={peerAvatarUrl}
            initials={initialsFrom(peerName)}
            imgClassName="call-avatar__img"
          />
        </span>
      </div>
      <div className="voicebanner__controls">
        <button
          type="button"
          className="voicebanner__btn"
          aria-label={microphoneEnabled ? "Mutar microfone" : "Ativar microfone"}
          aria-pressed={microphoneEnabled}
          disabled={microphonePending}
          onClick={onToggleMicrophone}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            {microphoneEnabled ? "mic" : "mic_off"}
          </span>
          {microphoneEnabled ? "Mutar" : "Ativar"}
        </button>
        <button
          type="button"
          className="voicebanner__btn voicebanner__btn--end"
          aria-label={`Sair da chamada — ${title}`}
          onClick={onLeave}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            call_end
          </span>
          Sair
        </button>
      </div>
    </div>
  );
}
