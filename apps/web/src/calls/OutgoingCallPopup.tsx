import { initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
import "./CallPresentation.css";

interface OutgoingCallPopupProps {
  name: string;
  avatarUrl?: string;
  callType: "audio" | "video";
  /** True while this popup's own cancel() is in flight — never a second lifecycle. */
  cancelling?: boolean;
  onCancel: () => unknown;
}

/**
 * Global, non-modal surface for the CALLER's side of a direct 1:1 ringing
 * call (issue #615) — IncomingCallPopup's counterpart for the other end of
 * the same call.ringing. Presentation only: knows nothing about
 * WebSocket/ownership/LiveKit/CallSessionProvider, exactly like
 * IncomingCallPopup.
 */
export default function OutgoingCallPopup({
  name,
  avatarUrl,
  callType,
  cancelling = false,
  onCancel,
}: OutgoingCallPopupProps) {
  return (
    <aside className="outgoing-call" role="region" aria-label={`Ligando para ${name}`}>
      <div className="outgoing-call__identity">
        <div className="outgoing-call__avatar" aria-hidden="true">
          <PersonAvatarImage
            src={avatarUrl}
            initials={initialsFrom(name)}
            imgClassName="outgoing-call__avatar-img"
          />
        </div>
        <div>
          <strong>{name}</strong>
          <span>{callType === "video" ? "Chamada de vídeo" : "Chamada de voz"}</span>
          {/* role="status" carries its own implicit polite live region — the
              single element that actually needs to be announced, never the
              whole interactive card (which would repeat name/type on every
              change too). */}
          <span role="status">{cancelling ? "Cancelando…" : "Ligando…"}</span>
        </div>
      </div>
      <div className="outgoing-call__actions">
        <button
          type="button"
          className="outgoing-call__cancel"
          disabled={cancelling}
          aria-label={`Cancelar chamada para ${name}`}
          onClick={onCancel}
        >
          Cancelar
        </button>
      </div>
    </aside>
  );
}
