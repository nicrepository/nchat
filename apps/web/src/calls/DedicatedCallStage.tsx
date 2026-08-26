import type { RefCallback } from "react";

import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
import CallControls, { type CallControlProps } from "./CallControls";
import "./CallPresentation.css";

interface DedicatedParticipant {
  identity: string;
  displayName: string;
  hasVideo: boolean;
  hasAudio: boolean;
  bindVideo?: RefCallback<HTMLDivElement>;
  /** This participant's own configured avatar, resolved server-side (issue #612) — never a resource-level avatar. */
  avatarUrl?: string;
}

interface RemoteDirectTileProps {
  identity: string;
  seed: string;
  displayName: string;
  avatarUrl?: string;
  hasVideo: boolean;
  bindVideo?: RefCallback<HTMLDivElement>;
  activeSpeakerId?: string | null;
  sidebar?: boolean;
  sharing?: boolean;
}

function SpeakerIndicator({ active, name }: { active: boolean; name: string }) {
  if (!active) return null;
  return (
    <span className="call-speaker-indicator" role="img" aria-label={`${name} está falando`}>
      <span className="material-symbols-outlined" aria-hidden="true">
        mic
      </span>
    </span>
  );
}

function ParticipantMediaStatus({
  name,
  microphoneEnabled,
  cameraEnabled,
  sharing,
}: {
  name: string;
  microphoneEnabled?: boolean;
  cameraEnabled: boolean;
  sharing: boolean;
}) {
  return (
    <span className="dedicated-call__participant-status">
      {typeof microphoneEnabled === "boolean" && (
        <span
          role="img"
          aria-label={`${name}: microfone ${microphoneEnabled ? "ligado" : "desligado"}`}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            {microphoneEnabled ? "mic" : "mic_off"}
          </span>
        </span>
      )}
      <span
        role="img"
        aria-label={`${name}: c\u00e2mera ${cameraEnabled ? "ligada" : "desligada"}`}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          {cameraEnabled ? "videocam" : "videocam_off"}
        </span>
      </span>
      {sharing && (
        <span role="img" aria-label={`${name} est\u00e1 compartilhando a tela`}>
          <span className="material-symbols-outlined" aria-hidden="true">
            screen_share
          </span>
        </span>
      )}
    </span>
  );
}

// A dedicated function component — not inlined into the parent's JSX — so its
// own props destructuring gives each field a fresh local binding, the same
// shape the participants.map() callback already gets for free. Needed only to
// satisfy react-hooks/refs: reading a plain field off a props object that also
// carries a ref-callback field, right after that field was handed to `ref=`,
// reads as "accessing a ref during render" to that rule even though bindVideo
// here is just one more field of a plain object, never an actual ref value.
function RemoteDirectTile({
  identity,
  seed,
  displayName,
  avatarUrl,
  hasVideo,
  bindVideo,
  activeSpeakerId,
  sidebar = false,
  sharing = false,
}: RemoteDirectTileProps) {
  const active = identity === activeSpeakerId;
  return (
    <article
      className={`dedicated-call__tile${sidebar ? " dedicated-call__participant" : ""} call-speaker-surface${active ? " call-speaker-surface--active" : ""}`}
    >
      <div ref={bindVideo} className="dedicated-call__media" />
      {!hasVideo && (
        <div
          className={`dedicated-call__avatar call-avatar call-avatar--${avatarColorFor(seed)}`}
          aria-hidden="true"
        >
          <PersonAvatarImage
            src={avatarUrl}
            initials={initialsFrom(displayName)}
            imgClassName="call-avatar__img"
          />
        </div>
      )}
      <span className="dedicated-call__name">{displayName}</span>
      <SpeakerIndicator active={active} name={displayName} />
      {sidebar && (
        <ParticipantMediaStatus name={displayName} cameraEnabled={hasVideo} sharing={sharing} />
      )}
    </article>
  );
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
  localParticipantId,
  localDisplayName,
  localInitials,
  localAvatarUrl,
  headerAvatar,
  remoteDirect,
  activeSpeakerId,
  resourceCall = false,
  remoteScreenShareParticipantId,
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
  /** Canonical current-user identity used only for active-speaker comparison. */
  localParticipantId?: string;
  /**
   * The local participant's call-presentation name (issue #612) — the real
   * profile name plus "(você)", or a bare "Você" fallback. Computed by
   * DedicatedCallPage via localParticipantDisplayName.
   */
  localDisplayName: string;
  /**
   * Initials for the local fallback avatar, derived from the raw profile
   * name — never from localDisplayName's "(você)" suffix (issue #612
   * blocker), which would otherwise feed "(" in as a second initial for a
   * one-word name.
   */
  localInitials: string;
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
   */
  headerAvatar?: { seed: string; avatarUrl?: string };
  /**
   * The direct call's remote-peer tile (issue #612 follow-up). Direct calls
   * carry their remote video through the same legacy hasRemoteVideo/
   * bindRemoteMedia fields FloatingCallWindow already uses (never through
   * `participants`, which useCallMedia documents as resource-room-only) —
   * this wires that existing, unmodified binding into a tile here too, so
   * camera-off shows the peer's real avatar/initials instead of nothing.
   * Undefined for a channel/group resource call: those participants arrive
   * through `participants` instead, one tile per real person, never this.
   */
  remoteDirect?: Omit<RemoteDirectTileProps, "activeSpeakerId" | "sidebar" | "sharing">;
  /** Stabilized canonical identity from useCallMedia; null means no highlighted person. */
  activeSpeakerId?: string | null;
  /** Channel/group resource calls source remote sidebar tiles from participants. */
  resourceCall?: boolean;
  /** Canonical identity of the selected remote screen sharer. */
  remoteScreenShareParticipantId?: string;
}) {
  const localActive = activeSpeakerId != null && localParticipantId === activeSpeakerId;
  const hasActiveScreenShare = localScreenShareActive || bindScreenShare != null;
  const hasSidebarParticipantSource = resourceCall || remoteDirect != null;
  const screenShareLayoutActive = hasActiveScreenShare && hasSidebarParticipantSource;
  const screenShareTile = localScreenShareActive ? (
    <article className="dedicated-call__tile dedicated-call__tile--screen">
      <div ref={bindLocalScreenShare} className="dedicated-call__media" />
      <span className="dedicated-call__name">Sua tela</span>
    </article>
  ) : bindScreenShare ? (
    <article className="dedicated-call__tile dedicated-call__tile--screen">
      <div ref={bindScreenShare} className="dedicated-call__media" />
      <span className="dedicated-call__name">Tela de {screenShareName ?? "Participante"}</span>
    </article>
  ) : null;
  const localParticipantTile = (
    <article
      key="local"
      className={`dedicated-call__tile dedicated-call__participant call-speaker-surface${localActive ? " call-speaker-surface--active" : ""}`}
    >
      <div ref={bindLocalMedia} className="dedicated-call__media" />
      {!hasLocalVideo && (
        <div
          className={`dedicated-call__avatar call-avatar call-avatar--${avatarColorFor(localSeed)}`}
          aria-hidden="true"
        >
          <PersonAvatarImage
            src={localAvatarUrl}
            initials={localInitials}
            imgClassName="call-avatar__img"
          />
        </div>
      )}
      <span className="dedicated-call__name">{localDisplayName}</span>
      <SpeakerIndicator active={localActive} name={localDisplayName} />
      <ParticipantMediaStatus
        name={localDisplayName}
        microphoneEnabled={controls.microphoneEnabled}
        cameraEnabled={hasLocalVideo}
        sharing={localScreenShareActive}
      />
    </article>
  );
  const remoteParticipantTiles = participants.map((participant) => {
    const active = participant.identity === activeSpeakerId;
    return (
      <article
        key={participant.identity}
        className={`dedicated-call__tile dedicated-call__participant call-speaker-surface${active ? " call-speaker-surface--active" : ""}`}
      >
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
        <span className="dedicated-call__name">{participant.displayName}</span>
        <SpeakerIndicator active={active} name={participant.displayName} />
        <ParticipantMediaStatus
          name={participant.displayName}
          microphoneEnabled={participant.hasAudio}
          cameraEnabled={participant.hasVideo}
          sharing={participant.identity === remoteScreenShareParticipantId}
        />
      </article>
    );
  });
  const remoteSidebarTiles = resourceCall
    ? remoteParticipantTiles
    : remoteDirect
      ? [
          <RemoteDirectTile
            key={remoteDirect.identity}
            {...remoteDirect}
            activeSpeakerId={activeSpeakerId}
            sidebar
            sharing={remoteDirect.identity === remoteScreenShareParticipantId}
          />,
        ]
      : [];
  return (
    <main
      className={`dedicated-call${screenShareLayoutActive ? " dedicated-call--screen-share" : ""}`}
      aria-label={`Chamada ${title}`}
    >
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
      {screenShareLayoutActive ? (
        <section
          className="dedicated-call__screen-share-layout"
          aria-label={"Conte\u00fado compartilhado"}
        >
          <div className="dedicated-call__screen-stage">{screenShareTile}</div>
          <aside
            className="dedicated-call__participant-sidebar"
            aria-label="Participantes"
            tabIndex={0}
          >
            <div className="dedicated-call__participant-list">
              {localParticipantTile}
              {remoteSidebarTiles}
            </div>
          </aside>
        </section>
      ) : (
        <section className="dedicated-call__grid" aria-label="Participantes">
          {screenShareTile}
          {localParticipantTile}
          {remoteDirect && <RemoteDirectTile {...remoteDirect} activeSpeakerId={activeSpeakerId} />}
          {remoteParticipantTiles}
        </section>
      )}
      <div ref={bindRemoteAudio} hidden />
      <footer>
        <CallControls {...controls} />
      </footer>
    </main>
  );
}
