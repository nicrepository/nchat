import type { RefCallback } from "react";

import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import CallControls, { type CallControlProps } from "./CallControls";
import "./CallPresentation.css";

const localDisplayName = "Você";

interface DedicatedParticipant {
  identity: string;
  displayName: string;
  hasVideo: boolean;
  bindVideo?: RefCallback<HTMLDivElement>;
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
  screenShareName,
  bindScreenShare,
  hasLocalVideo,
  localSeed,
}: {
  title: string;
  status: "connecting" | "connected" | "reconnecting" | "failed";
  participantCount: number;
  participants: DedicatedParticipant[];
  controls: CallControlProps;
  onMinimize: () => unknown;
  bindLocalMedia?: RefCallback<HTMLDivElement>;
  bindRemoteAudio?: RefCallback<HTMLDivElement>;
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
}) {
  return (
    <main className="dedicated-call" aria-label={`Chamada ${title}`}>
      <header className="dedicated-call__header">
        <div>
          <strong>{title}</strong>
          <span role="status">
            {status === "reconnecting" ? "Reconectando" : `${participantCount} participantes`}
          </span>
        </div>
        <button type="button" aria-label="Minimizar para janela flutuante" onClick={onMinimize}>
          <span className="material-symbols-outlined" aria-hidden="true">
            picture_in_picture_alt
          </span>
        </button>
      </header>
      <section className="dedicated-call__grid" aria-label="Participantes">
        {bindScreenShare && (
          <article className="dedicated-call__tile dedicated-call__tile--screen">
            <div ref={bindScreenShare} className="dedicated-call__media" />
            <span>Tela de {screenShareName ?? "Participante"}</span>
          </article>
        )}
        <article className="dedicated-call__tile">
          <div ref={bindLocalMedia} className="dedicated-call__media" />
          {!hasLocalVideo && (
            <div
              className={`dedicated-call__avatar call-avatar call-avatar--${avatarColorFor(localSeed)}`}
              aria-hidden="true"
            >
              {initialsFrom(localDisplayName)}
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
                {initialsFrom(participant.displayName)}
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
