/**
 * The "who confirmed" popover a sender opens from the acknowledgement summary
 * (issue #846).
 *
 * An overlay, never an expansion of the bubble (issue #846's own rule): the
 * message stays the visually dominant element, and the per-recipient detail —
 * which can run to dozens of rows in a large channel — lives in something that
 * opens and closes rather than growing the timeline row it hangs off.
 *
 * Anchored placement reuses useAnchoredPicker (issue #839's own fix for a
 * virtualized row's transformed origin), the same primitive the reaction
 * toolbar's emoji picker already trusts: open/dismiss, outside click, Escape,
 * and reposition on scroll/resize all come from there, so this file only
 * describes what the popover contains.
 */

import {
  useEffect,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
} from "react";

import "./MessageAcknowledgementDetails.css";
import type { CallParticipantProfile } from "./chatApi";
import type { MessageAcknowledgementRecipient } from "./chatTypes";
import { useAnchoredPicker } from "./emoji/useAnchoredPicker";
import { formatTime, initialsFrom } from "./messageDisplay";
import { PersonAvatarImage } from "./PersonAvatarImage";

const titleId = "ack-details-title";

type Section = "acknowledged" | "responded" | "pending";

const sectionLabels: Record<Section, string> = {
  acknowledged: "Confirmaram",
  responded: "Responderam",
  pending: "Pendentes",
};

/**
 * Only the three sections issue #846 names. expired/cancelled recipients are
 * not shown here — they are a terminal state the sender already sees summed
 * in the strip's own counts, and the Figma reference for this popover names
 * exactly these three.
 */
function groupBySection(
  recipients: MessageAcknowledgementRecipient[],
): Record<Section, MessageAcknowledgementRecipient[]> {
  const groups: Record<Section, MessageAcknowledgementRecipient[]> = {
    acknowledged: [],
    responded: [],
    pending: [],
  };
  for (const recipient of recipients) {
    if (
      recipient.state === "acknowledged" ||
      recipient.state === "responded" ||
      recipient.state === "pending"
    ) {
      groups[recipient.state].push(recipient);
    }
  }
  return groups;
}

function formatResolvedAt(iso?: string): string | null {
  if (!iso) return null;
  const formatted = formatTime(iso);
  return formatted || null;
}

/**
 * One row. Never the raw recipientId (issue #846's own rule): a profile that
 * has not resolved yet — still loading, or a recipient the caller could not
 * resolve — draws a neutral placeholder instead, exactly as a call tile
 * degrades to initials for the same reason.
 */
function RecipientRow({
  recipient,
  profile,
  showTimestamp,
}: {
  recipient: MessageAcknowledgementRecipient;
  profile: CallParticipantProfile | undefined;
  showTimestamp: boolean;
}) {
  const name = profile?.displayName || "";
  const time = showTimestamp ? formatResolvedAt(recipient.resolvedAt) : null;
  return (
    <li className="chat-msg-ack-details__recipient" data-testid="ack-details-recipient">
      <span className="chat-msg-ack-details__avatar" aria-hidden="true">
        <PersonAvatarImage
          src={profile?.avatarUrl}
          initials={initialsFrom(name)}
          imgClassName="chat-msg-ack-details__avatar-img"
        />
      </span>
      <span className="chat-msg-ack-details__name">{name || "Membro"}</span>
      {time ? <span className="chat-msg-ack-details__time">{time}</span> : null}
    </li>
  );
}

function RecipientSection({
  section,
  recipients,
  profiles,
}: {
  section: Section;
  recipients: MessageAcknowledgementRecipient[];
  profiles: Map<string, CallParticipantProfile>;
}) {
  if (recipients.length === 0) return null;
  return (
    <div className="chat-msg-ack-details__section">
      <h3 className="chat-msg-ack-details__section-title">{sectionLabels[section]}</h3>
      <ul className="chat-msg-ack-details__recipients">
        {recipients.map((recipient) => (
          <RecipientRow
            key={recipient.recipientId}
            recipient={recipient}
            profile={profiles.get(recipient.recipientId)}
            showTimestamp={section === "acknowledged"}
          />
        ))}
      </ul>
    </div>
  );
}

export interface MessageAcknowledgementDetailsProps {
  total: number;
  acknowledged: number;
  /** Undefined while the sender's detail read has not landed yet. */
  recipients?: MessageAcknowledgementRecipient[];
  anchorRef: RefObject<HTMLElement | null>;
  containerRef?: RefObject<HTMLElement | null>;
  resolveIdentities: (userIds: string[], signal?: AbortSignal) => Promise<CallParticipantProfile[]>;
  onClose: (restoreFocus: boolean) => void;
}

export default function MessageAcknowledgementDetails({
  total,
  acknowledged,
  recipients,
  anchorRef,
  containerRef,
  resolveIdentities,
  onClose,
}: MessageAcknowledgementDetailsProps) {
  const [profiles, setProfiles] = useState<Map<string, CallParticipantProfile>>(new Map());

  // Resolved once per distinct recipient set, keyed by the ids themselves —
  // not by a request counter — so a summary that merely re-renders (a realtime
  // count tick, say) does not re-issue the same lookup.
  const idsKey = (recipients ?? []).map((r) => r.recipientId).join(",");
  useEffect(() => {
    if (!idsKey) return;
    const controller = new AbortController();
    void resolveIdentities(idsKey.split(","), controller.signal).then(
      (resolved) => {
        if (controller.signal.aborted) return;
        setProfiles(new Map(resolved.map((profile) => [profile.userId, profile])));
      },
      () => {
        // A failed resolve leaves every row on its placeholder fallback —
        // never the raw id, and never a stale profile from a previous message.
      },
    );
    return () => controller.abort();
  }, [idsKey, resolveIdentities]);

  const dismiss = (restoreFocus: boolean) => {
    onClose(restoreFocus);
  };

  const pickerRef = useAnchoredPicker({
    open: true,
    anchorRef,
    onDismiss: dismiss,
    containerRef,
    align: "start",
  });

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "Escape") return;
    event.preventDefault();
    dismiss(true);
  };

  const groups = groupBySection(recipients ?? []);
  const loading = recipients === undefined;

  return (
    <div
      ref={pickerRef}
      className="chat-theme chat-msg-ack-details"
      role="dialog"
      aria-labelledby={titleId}
      data-testid="ack-details-dialog"
      style={{ visibility: "hidden" }}
      onKeyDown={handleKeyDown}
    >
      <div className="chat-msg-ack-details__header">
        <div>
          <h2 id={titleId} className="chat-msg-ack-details__title">
            Confirmações de recebimento
          </h2>
          <p className="chat-msg-ack-details__subtitle">
            {acknowledged} de {total} pessoas confirmaram
          </p>
        </div>
        <button
          type="button"
          className="chat-msg-ack-details__close"
          aria-label="Fechar"
          onClick={() => dismiss(true)}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            close
          </span>
        </button>
      </div>
      {loading ? (
        <p className="chat-msg-ack-details__status" role="status">
          Carregando confirmações…
        </p>
      ) : (
        <>
          <RecipientSection
            section="acknowledged"
            recipients={groups.acknowledged}
            profiles={profiles}
          />
          <RecipientSection section="responded" recipients={groups.responded} profiles={profiles} />
          <RecipientSection section="pending" recipients={groups.pending} profiles={profiles} />
        </>
      )}
    </div>
  );
}
