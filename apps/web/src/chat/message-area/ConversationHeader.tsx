/**
 * The conversation header — the channel and DM variants, the details toggle,
 * and the call entry points each of them offers (moved out of ChatMessageArea,
 * issue #834).
 *
 * Everything here answers one question: what the strip above the timeline
 * shows about *this conversation and this reader's relationship to it*. It
 * knows nothing about scrolling, the timeline, or the message list — a change
 * to a header is a change to this file alone.
 */

import { forwardRef, useState, type RefObject } from "react";

import type { ResourceCallHeaderState } from "../../calls/resourceCallTypes";
import type { ChatOutletContext } from "../ChatShell";
import type { DMCounterpart } from "../chatTypes";
import { conversationDetailsPanelId } from "../conversationDetailsDisplay";
import { avatarColorFor, initialsFrom } from "../messageDisplay";
import PresenceDot from "../PresenceDot";
import { presenceLabel, usePresence, type PresenceState } from "../presence";
import type { ConversationDetailsPanelState } from "../useConversationDetailsPanel";
import { IconHash } from "./icons";

interface DetailsToggleProps {
  open: boolean;
  /**
   * Names the control after what it opens: a channel's details, a group's
   * details, or a named person's profile.
   */
  label: string;
  onToggle: () => void;
}

/**
 * The details toggle.
 *
 * A real <button>, so it is reachable and operable by keyboard for free.
 * aria-expanded carries the state and aria-controls points at the panel, which
 * is why each panel needs a stable id rather than a generated one. The ref is
 * what lets the panel hand focus back here when it closes itself.
 *
 * The two variants differ only in their accessible name and the id they point
 * at — the affordance, the icon and the keyboard behaviour are identical, so
 * one component with a discriminator beats two that would drift apart.
 */
const DetailsToggle = forwardRef<HTMLButtonElement, DetailsToggleProps>(function DetailsToggle(
  { open, label, onToggle },
  ref,
) {
  return (
    <div className="chat-msg-area__header-actions">
      <button
        ref={ref}
        type="button"
        className="chat-msg-area__header-btn"
        aria-label={label}
        aria-expanded={open}
        aria-controls={conversationDetailsPanelId}
        onClick={onToggle}
        data-testid="chat-details-toggle"
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          info
        </span>
      </button>
    </div>
  );
});

interface DirectCallActionsProps {
  onAudio: () => void;
  onVideo: () => void;
}

/**
 * RF-23 direct 1:1 call entry: audio and video remain distinct call types,
 * each its own icon button (issue #673) — never consolidated into one
 * control or an intermediate type-picker menu.
 */
function DirectCallActions({ onAudio, onVideo }: DirectCallActionsProps) {
  return (
    <div className="chat-msg-area__call-actions">
      <button
        type="button"
        className="chat-msg-area__header-btn"
        aria-label="Iniciar chamada de áudio"
        onClick={onAudio}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          call
        </span>
      </button>
      <button
        type="button"
        className="chat-msg-area__header-btn"
        aria-label="Iniciar chamada de vídeo"
        onClick={onVideo}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          videocam
        </span>
      </button>
    </div>
  );
}

/**
 * RF-24 channel/group entry (issue #540 follow-up). A resource room is one
 * multiparty call, never separate "audio" and "video" rooms, so there is a
 * single action instead of RF-23's two. An icon button (issue #673) rather
 * than the previous "Chamada" text label — the accessible name, disabled
 * state, and onCall action are unchanged.
 */
function ResourceCallAction({ state }: { state: ResourceCallHeaderState }) {
  return (
    <div className="chat-msg-area__call-actions">
      <button
        type="button"
        className="chat-msg-area__header-btn"
        aria-label="Iniciar chamada"
        onClick={state.onCall}
        disabled={Boolean(state.disabled)}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          call
        </span>
      </button>
    </div>
  );
}

interface HeaderChannelProps {
  name: string;
  detailsToggle?: React.ReactNode;
  /** RF-24/#622: absent only when this header is not showing a resource call at all (never the case for a channel). */
  resourceCall?: ResourceCallHeaderState;
}

export function HeaderChannel({ name, detailsToggle, resourceCall }: HeaderChannelProps) {
  return (
    <header className="chat-msg-area__header" data-testid="chat-msg-header">
      <span className="chat-msg-area__header-icon" aria-hidden="true">
        <IconHash />
      </span>
      <h1 className="chat-msg-area__header-title">{name}</h1>
      {resourceCall && <ResourceCallAction state={resourceCall} />}
      {detailsToggle}
    </header>
  );
}

interface HeaderDMProps {
  name: string;
  /** Same structured counterpart the sidebar uses — never a second request. */
  counterpart?: DMCounterpart;
  onStartCall?: (targetUserId: string, callType: "audio" | "video") => boolean;
  /**
   * RF-24/#622: a group's shared call room state. Always absent for a 1:1
   * (counterpart set) — issue #622 round 2 requires a direct DM keep exactly
   * Áudio/Vídeo and never show resource-call UI.
   */
  resourceCall?: ResourceCallHeaderState;
  /** A group opens its details, a 1:1 DM opens the other person's profile. */
  detailsToggle?: React.ReactNode;
  /** The conversation being read; presence is resolved within it (RF-58). */
  presenceTarget?: string;
}

/**
 * The counterpart's avatar: their picture when it loads, their initials when it
 * does not.
 *
 * A load failure is scoped to the URL that was current when it happened, so a
 * change of src must clear it — otherwise navigating A → B → A would never retry
 * A. This uses React's "adjust state when a prop changes" pattern (reset during
 * render, guarded so it runs ONLY when src actually changes, never every
 * render); an effect would trip react-hooks/set-state-in-effect. An unchanged
 * src that keeps failing stays on the initials fallback.
 */
function HeaderAvatar({
  name,
  counterpart,
  presence,
}: {
  name: string;
  counterpart: DMCounterpart | undefined;
  presence: PresenceState;
}) {
  const src = counterpart?.avatarUrl;
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const [trackedSrc, setTrackedSrc] = useState(src);
  if (src !== trackedSrc) {
    setTrackedSrc(src);
    setFailedSrc(null);
  }
  // Same deterministic colour the sidebar uses for this person, so the initials
  // fallback matches across both surfaces. Keyed on the counterpart user id
  // (stable per person); legacy DMs without a counterpart fall back to the name.
  const color = avatarColorFor(counterpart?.userId ?? name);
  return (
    <div
      className={`chat-msg-area__header-avatar chat-msg-area__header-avatar--${color}`}
      aria-hidden="true"
    >
      {Boolean(src) && failedSrc !== src ? (
        <img
          className="chat-msg-area__header-avatar-img"
          src={src}
          alt=""
          referrerPolicy="no-referrer"
          onError={() => setFailedSrc(src ?? null)}
        />
      ) : (
        initialsFrom(counterpart?.displayName ?? name)
      )}
      <PresenceDot state={presence} size="md" />
    </div>
  );
}

export function HeaderDM({
  name,
  counterpart,
  onStartCall,
  resourceCall,
  detailsToggle,
  presenceTarget,
}: HeaderDMProps) {
  const presence = usePresence(counterpart?.userId, presenceTarget);

  return (
    <header className="chat-msg-area__header" data-testid="chat-msg-header">
      <HeaderAvatar name={name} counterpart={counterpart} presence={presence} />
      <h1 className="chat-msg-area__header-title">{name}</h1>
      {/* The header states the status in words as well: this is the surface a
          reader is looking at while writing to this person, so "Ausente" being
          discoverable without hovering a 9px dot is the point. */}
      {presence !== "unknown" && (
        <span
          className={`chat-msg-area__header-presence chat-msg-area__header-presence--${presence}`}
          data-testid="chat-msg-header-presence"
        >
          {presenceLabel(presence)}
        </span>
      )}
      {counterpart && onStartCall && (
        <DirectCallActions
          onAudio={() => onStartCall(counterpart.userId, "audio")}
          onVideo={() => onStartCall(counterpart.userId, "video")}
        />
      )}
      {!counterpart && resourceCall && <ResourceCallAction state={resourceCall} />}
      {detailsToggle}
    </header>
  );
}

interface ConversationHeaderProps {
  kind: "channel" | "dm";
  name: string;
  counterpart: DMCounterpart | undefined;
  presenceTarget: string | undefined;
  onStartCall: ChatOutletContext["startCall"];
  resourceCall: ResourceCallHeaderState | undefined;
  details: ConversationDetailsPanelState;
  detailsToggleRef: RefObject<HTMLButtonElement | null>;
}

/** The channel or DM header, and the details control both of them offer. */
export default function ConversationHeader({
  kind,
  name,
  counterpart,
  presenceTarget,
  onStartCall,
  resourceCall,
  details,
  detailsToggleRef,
}: ConversationHeaderProps) {
  const detailsToggle = details.supportsDetails ? (
    <DetailsToggle
      ref={detailsToggleRef}
      open={details.showDetails}
      label={details.toggleLabel}
      onToggle={details.toggle}
    />
  ) : undefined;
  if (kind === "channel") {
    return <HeaderChannel name={name} resourceCall={resourceCall} detailsToggle={detailsToggle} />;
  }
  return (
    <HeaderDM
      name={name}
      counterpart={counterpart}
      presenceTarget={presenceTarget}
      onStartCall={onStartCall}
      resourceCall={resourceCall}
      detailsToggle={detailsToggle}
    />
  );
}
