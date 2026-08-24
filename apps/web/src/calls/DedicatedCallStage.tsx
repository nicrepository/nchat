import type { RefCallback } from "react";

import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
import CallControls, { type CallControlProps } from "./CallControls";
import "./CallPresentation.css";

interface DedicatedParticipant {
  identity: string;
  displayName: string;
  hasVideo: boolean;
  bindVideo?: RefCallback<HTMLDivElement>;
  /** This participant's own configured avatar, resolved server-side (issue #612) — never a resource-level avatar. */
  avatarUrl?: string;
}

export default function DedicatedCallStage({
  title,
  status,
  participantCount,
  participants,
  controls,
  onMinimize,
  bindLocalMedia,
  bindRemoteAudio,
  localScreenShareActive = false,
  bindLocalScreenShare,
  screenShareName,
  bindScreenShare,
  hasLocalVideo,
  localSeed,
  localDisplayName,
  localAvatarUrl,
  headerAvatar,
}: {
  title: string;
  status: "connecting" | "connected" | "reconnecting" | "failed";
  participantCount: number;
  participants: DedicatedParticipant[];
  controls: CallControlProps;
  onMinimize: () => unknown;
  bindLocalMedia?: RefCallback<HTMLDivElement>;
  bindRemoteAudio?: RefCallback<HTMLDivElement>;
  /**
   * Whether THIS tab is currently sharing its own screen (issue #611). When
   * true, the local screen share always wins the single primary tile over
   * any remote share — never a second, simultaneous screen tile.
   */
  localScreenShareActive?: boolean;
  bindLocalScreenShare?: RefCallback<HTMLDivElement>;
  screenShareName?: string;
  bindScreenShare?: RefCallback<HTMLDivElement>;
  /**
   * Whether the local preview currently has a usable video track. Required,
   * not defaulted: DedicatedCallPage is this component's only real callsite,
   * and it always knows media.hasLocalVideo — a missing wire-up should be a
   * type error, never a silent "assume true" that hides the fallback.
   */
  hasLocalVideo: boolean;
  /** Stable seed (the current user's id) for the local fallback avatar's color. */
  localSeed: string;
  /**
   * The local participant's call-presentation name (issue #612) — the real
   * profile name plus "(você)", or a bare "Você" fallback. Computed by
   * DedicatedCallPage via localParticipantDisplayName.
   */
  localDisplayName: string;
  /** The local participant's configured avatar, when available. */
  localAvatarUrl?: string;
  /**
   * The header identity avatar for a direct ("user"-targeted) call only
   * (issue #612) — `title` already carries the peer's real display name for
   * that case (DedicatedCallPage's `target.name`), so this adds only the
   * matching avatar/initials next to it. Undefined for a channel/group
   * resource call: the header must never wear an individual's avatar next to
   * a room name, and title there is the channel/group name, not a person's,
   * so there is nothing to attach a person's picture to.
   *
   * Deliberately not a new media/video tile — DedicatedCallStage has no
   * remote-video binding for a direct call today (that pipeline is
   * unchanged), so this is presentation-only, in the one non-media identity
   * surface the dedicated view already has: its header.
   */
  headerAvatar?: { seed: string; avatarUrl?: string };
}) {
  return (
    <main className="dedicated-call" aria-label={`Chamada ${title}`}>
      <header className="dedicated-call__header">
        <div className="dedicated-call__header-identity">
          {headerAvatar && (
            // Decorative: `title` right beside it already carries the same
            // real name, so the image itself needs no separate accessible
            // text (issue #612 accessibility rule — visible adjacent name
            // means the avatar may be aria-hidden).
            <div
              className={`dedicated-call__header-avatar call-avatar call-avatar--${avatarColorFor(headerAvatar.seed)}`}
              aria-hidden="true"
            >
              <PersonAvatarImage
                src={headerAvatar.avatarUrl}
                initials={initialsFrom(title)}
                imgClassName="call-avatar__img"
              />
            </div>
          )}
          <div>
            <strong>{title}</strong>
            <span role="status">
              {status === "reconnecting" ? "Reconectando" : `${participantCount} participantes`}
            </span>
          </div>
        </div>
        <button type="button" aria-label="Minimizar para janela flutuante" onClick={onMinimize}>
          <span className="material-symbols-outlined" aria-hidden="true">
            picture_in_picture_alt
          </span>
        </button>
      </header>
      <section className="dedicated-call__grid" aria-label="Participantes">
        {localScreenShareActive && (
          <article className="dedicated-call__tile dedicated-call__tile--screen">
            <div ref={bindLocalScreenShare} className="dedicated-call__media" />
            <span>Sua tela</span>
          </article>
        )}
        {
          // Primary source (issue #611): local share if active, otherwise
          // the selected remote share, otherwise no screen tile at all.
          // Never both — ending local sharing while a remote share is still
          // active immediately reveals it as primary with no extra state,
          // since this is derived fresh every render from the same
          // media.remoteScreenShare the hook already keeps current.
          !localScreenShareActive && bindScreenShare && (
            <article className="dedicated-call__tile dedicated-call__tile--screen">
              <div ref={bindScreenShare} className="dedicated-call__media" />
              <span>Tela de {screenShareName ?? "Participante"}</span>
            </article>
          )
        }
        <article className="dedicated-call__tile">
          <div ref={bindLocalMedia} className="dedicated-call__media" />
          {!hasLocalVideo && (
            <div
              className={`dedicated-call__avatar call-avatar call-avatar--${avatarColorFor(localSeed)}`}
              aria-hidden="true"
            >
              <PersonAvatarImage
                src={localAvatarUrl}
                initials={initialsFrom(localDisplayName)}
                imgClassName="call-avatar__img"
              />
            </div>
          )}
          <span>{localDisplayName}</span>
        </article>
        {participants.map((participant) => (
          <article key={participant.identity} className="dedicated-call__tile">
            <div ref={participant.bindVideo} className="dedicated-call__media" />
            {!participant.hasVideo && (
              <div
                className={`dedicated-call__avatar call-avatar call-avatar--${avatarColorFor(participant.identity)}`}
                aria-hidden="true"
              >
                <PersonAvatarImage
                  src={participant.avatarUrl}
                  initials={initialsFrom(participant.displayName)}
                  imgClassName="call-avatar__img"
                />
              </div>
            )}
            <span>{participant.displayName}</span>
          </article>
        ))}
      </section>
      <div ref={bindRemoteAudio} hidden />
      <footer>
        <CallControls {...controls} />
      </footer>
    </main>
  );
}
