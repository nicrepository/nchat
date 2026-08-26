import { useEffect, useState } from "react";

import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
import "./CallPresentation.css";

export interface ActiveResourceCallBarParticipant {
  identity: string;
  displayName: string;
}

// ── Discriminated union: every mode carries exactly its valid fields ─────────

interface BarBase {
  title: string;
  /** Authoritative call-start instant (Call.created_at) — never Date.now() at mount. */
  startedAt: string;
}

export interface BarAvailable extends BarBase {
  mode: "available";
  /** True while lifecycle/arbitration forbids joining (e.g. direct call busy). */
  joinDisabled: boolean;
  onJoin: () => void;
}

export interface BarParticipatingLocal extends BarBase {
  mode: "participating-local";
  /** Remote participants only; the local user is added internally. */
  participants: ActiveResourceCallBarParticipant[];
  localId: string;
  localName: string;
  localInitials: string;
  localAvatarUrl?: string;
  activeSpeakerId?: string | null;
  microphoneEnabled: boolean;
  microphonePending: boolean;
  onToggleMicrophone: () => void;
  onLeave: () => void;
  onOpenFullCall: () => void;
}

export interface BarParticipatingInfo extends BarBase {
  mode: "participating-info";
}

export type ActiveResourceCallBarProps =
  | BarAvailable
  | BarParticipatingLocal
  | BarParticipatingInfo;

const MAX_VISIBLE_AVATARS = 3;

function formatElapsed(elapsedMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(elapsedMs / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const mm = String(minutes).padStart(2, "0");
  const ss = String(seconds).padStart(2, "0");
  return hours > 0 ? `${hours}:${mm}:${ss}` : `${mm}:${ss}`;
}

/**
 * Persistent, compact call-status bar for a channel/group-DM view (issue #642,
 * updated by issue #657). Renders in three modes:
 *
 * - **available**: an active call exists but this user is NOT in it. Shows
 *   title, timer, and a "Entrar na chamada" button. Never shows fake
 *   participant counts or avatars.
 *
 * - **participating-local**: this user is in the call AND the local session
 *   (resourcePresentationCall) is ready. Shows roster, active speaker, mute,
 *   leave, and open-full-call. FloatingCallWindow coexists — this bar never
 *   replaces it.
 *
 * - **participating-info**: this user is in the call but the local session is
 *   not ready (connecting, reconnecting, error, remote ownership). Shows
 *   title and timer only — no controls, no join button. The actual call
 *   surface (FloatingCallWindow / GlobalCallIndicator) handles the
 *   operational state.
 *
 * Presentation only: no lifecycle beyond the elapsed-time tick, no owned mute
 * state — every control calls back into the authoritative CallSessionProvider-
 * derived callbacks the caller supplies.
 */
export default function ActiveResourceCallBar(props: ActiveResourceCallBarProps) {
  const { title, startedAt, mode } = props;
  const startedAtMs = Date.parse(startedAt);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  const elapsed = formatElapsed(now - startedAtMs);

  if (mode === "available") {
    return (
      <div
        className="voicebanner"
        data-testid="active-resource-call-bar"
        role="group"
        aria-label="Chamada ativa"
      >
        <div className="voicebanner__open voicebanner__open--info">
          <span className="voicebanner__icon" aria-hidden="true">
            <span className="material-symbols-outlined">headphones</span>
          </span>
          <span className="voicebanner__info">
            <span className="voicebanner__title">{title}</span>
            <span className="voicebanner__meta">
              <span className="voicebanner__dot" aria-hidden="true" />
              <span>{elapsed}</span>
            </span>
          </span>
        </div>
        <div className="voicebanner__controls">
          <button
            type="button"
            className="voicebanner__btn voicebanner__btn--join"
            disabled={props.joinDisabled}
            onClick={props.onJoin}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              call
            </span>
            Entrar na chamada
          </button>
        </div>
      </div>
    );
  }

  if (mode === "participating-info") {
    return (
      <div
        className="voicebanner"
        data-testid="active-resource-call-bar"
        role="group"
        aria-label="Chamada ativa"
      >
        <div className="voicebanner__open voicebanner__open--info">
          <span className="voicebanner__icon" aria-hidden="true">
            <span className="material-symbols-outlined">headphones</span>
          </span>
          <span className="voicebanner__info">
            <span className="voicebanner__title">{title}</span>
            <span className="voicebanner__meta">
              <span className="voicebanner__dot" aria-hidden="true" />
              <span>{elapsed}</span>
            </span>
          </span>
        </div>
      </div>
    );
  }

  // mode === "participating-local"
  const {
    participants,
    localId,
    localName,
    localInitials,
    localAvatarUrl,
    activeSpeakerId,
    microphoneEnabled,
    microphonePending,
    onToggleMicrophone,
    onLeave,
    onOpenFullCall,
  } = props;

  const participantCount = Math.max(1, participants.length + 1);
  const stack = [
    { id: localId, name: localName, avatarUrl: localAvatarUrl },
    ...participants.map((participant) => ({
      id: participant.identity,
      name: participant.displayName,
      avatarUrl: undefined as string | undefined,
    })),
  ].map((entry) => ({
    ...entry,
    // The local entry's initials are the caller-provided, pre-computed
    // localInitials — never initialsFrom(localName), which would risk
    // feeding "(" from the "(você)" suffix in as a second initial for a
    // one-word name (issue #612 blocker; see CallSessionProvider's own
    // identical localInitials derivation).
    initials: entry.id === localId ? localInitials : initialsFrom(entry.name),
  }));
  // Local always stays visible (index 0). When the active speaker falls
  // outside the natural head slice, swap them into the LAST visible slot
  // instead of silently hiding them behind "+N" (issue #642 review, HIGH
  // finding) — never duplicated (they simply replace whoever was last), and
  // the overflow count stays exactly stack.length - MAX_VISIBLE_AVATARS
  // regardless of which entries end up hidden.
  const speakerIndex = activeSpeakerId
    ? stack.findIndex((entry) => entry.id === activeSpeakerId)
    : -1;
  const visible =
    speakerIndex >= MAX_VISIBLE_AVATARS
      ? [...stack.slice(0, MAX_VISIBLE_AVATARS - 1), stack[speakerIndex]]
      : stack.slice(0, MAX_VISIBLE_AVATARS);
  const overflow = stack.length - visible.length;
  const speaking = stack.find((entry) => entry.id === activeSpeakerId);

  const participantCountLabel = `${participantCount} participante${participantCount === 1 ? "" : "s"}`;
  // The visual meta line is a plain (non-live) span — never announced
  // proactively — but duration/count must still be DISCOVERABLE without
  // aria-live spam (issue #642 review): folding them into the main button's
  // own accessible name means a screen-reader user hears them the instant
  // they reach the control, on their own terms, exactly once.
  const openLabel = `Abrir chamada — ${title} — ${elapsed} — ${participantCountLabel}`;

  return (
    <div className="voicebanner" data-testid="active-resource-call-bar">
      <button
        type="button"
        className="voicebanner__open"
        aria-label={openLabel}
        onClick={onOpenFullCall}
      >
        <span className="voicebanner__icon" aria-hidden="true">
          <span className="material-symbols-outlined">headphones</span>
        </span>
        <span className="voicebanner__info">
          <span className="voicebanner__title">{title}</span>
          <span className="voicebanner__meta">
            <span className="voicebanner__dot" aria-hidden="true" />
            <span>{elapsed}</span>
            <span aria-hidden="true">·</span>
            <span>{participantCountLabel}</span>
          </span>
        </span>
      </button>
      <div className="voicebanner__avatars" aria-hidden="true">
        {visible.map((entry) => (
          <span
            key={entry.id}
            className={`voicebanner__avatar call-avatar call-avatar--${avatarColorFor(entry.id)}${
              entry.id === activeSpeakerId ? " voicebanner__avatar--speaking" : ""
            }`}
          >
            <PersonAvatarImage
              src={entry.avatarUrl}
              initials={entry.initials}
              imgClassName="call-avatar__img"
            />
          </span>
        ))}
        {overflow > 0 && (
          <span className="voicebanner__avatar voicebanner__avatar--overflow">+{overflow}</span>
        )}
      </div>
      {speaking && <span className="voicebanner__sr">{speaking.name} está falando</span>}
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
