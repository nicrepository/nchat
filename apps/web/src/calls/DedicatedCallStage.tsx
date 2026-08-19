import type { RefCallback } from "react";

import CallControls, { type CallControlProps } from "./CallControls";
import "./CallPresentation.css";

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
          <span>Você</span>
        </article>
        {participants.map((participant) => (
          <article key={participant.identity} className="dedicated-call__tile">
            <div ref={participant.bindVideo} className="dedicated-call__media" />
            {!participant.hasVideo && (
              <div className="dedicated-call__avatar" aria-hidden="true">
                {participant.displayName.at(0)}
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
