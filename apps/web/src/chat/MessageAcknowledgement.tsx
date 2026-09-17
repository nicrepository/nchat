import { useRef, useState } from "react";

import type { CallParticipantProfile } from "./chatApi";
import type { MessageAcknowledgement, MessageAcknowledgementState } from "./chatTypes";
import MessageAcknowledgementDetails from "./MessageAcknowledgementDetails";
import { formatTime } from "./messageDisplay";

/**
 * The acknowledgement strip under a message that asked for confirmation
 * (issue #824).
 *
 * One row, never a card: #820's rendering rules say the request belongs to the
 * message rather than wrapping it, and a panel per urgent message would make a
 * busy channel unreadable. What it draws depends on who is looking —
 *
 *   a recipient still pending  → the action
 *   a recipient who resolved   → the state they resolved into
 *   the sender                 → how many of the people asked have answered
 *
 * — and all three come from the server's own summary. Nothing is inferred here:
 * the absence of `viewerState` is what says "this message never asked you",
 * which is exactly what the server sends to a sender, so the branch below is a
 * rendering choice and not an authorisation one.
 */
export interface MessageAcknowledgementProps {
  messageId: string;
  /** Absent while the summary is still being read; the strip then draws nothing. */
  acknowledgement?: MessageAcknowledgement;
  /**
   * Who wrote the message, and who is looking at it. Both are needed because
   * the viewer's role is three-valued and the summary alone cannot express it:
   * a missing `viewerState` means "this message never asked you", which is true
   * of the sender *and* of anybody who joined the conversation afterwards.
   * Treating the two alike would show a late joiner the sender's view.
   */
  senderId: string;
  currentUserId: string;
  /** True while this message's confirmation is in flight. */
  submitting: boolean;
  onAcknowledge: (messageId: string) => void;
  /**
   * Reads this message's full per-recipient detail (issue #846), called when
   * the sender opens the details popover. Optional: without it the popover
   * still opens once recipients happen to already be cached, just without a
   * fresh read to fill a gap.
   */
  onOpenDetails?: (messageId: string) => void;
  /** Resolves recipient identities for the popover. See MessageBubbleProps. */
  resolveIdentities?: (
    userIds: string[],
    signal?: AbortSignal,
  ) => Promise<CallParticipantProfile[]>;
}

/**
 * What this reader is, to this message.
 *
 * Presentation only. The server decides what any of them may *see* — it sends a
 * `viewerState` to a recipient and a `recipients` list to whoever it authorised
 * — and this type decides which of those answers to draw. A client that guessed
 * the role from the absence of data would get the late joiner wrong, which is
 * exactly the bug this replaces.
 */
type ViewerRole = "sender" | "recipient" | "bystander";

function viewerRole(
  acknowledgement: MessageAcknowledgement,
  senderId: string,
  currentUserId: string,
): ViewerRole {
  // Identity first: being the author is a fact about the message, not an
  // inference from what the acknowledgement endpoint happened to return.
  if (currentUserId !== "" && currentUserId === senderId) return "sender";
  if (acknowledgement.viewerState) return "recipient";
  // Neither wrote it nor was asked: somebody who can read the conversation and
  // arrived after the question was put. They get no action and no detail.
  return "bystander";
}

/**
 * What a resolved recipient is told, in words rather than in colour alone.
 *
 * Each terminal state gets its own sentence because they do not mean the same
 * thing to the person who was asked: they confirmed, they answered instead, the
 * request was withdrawn, or it stopped being asked.
 *
 * `responded` is deliberately distinct from `acknowledged` (issue #846): a
 * reply is not a confirmation, and the checkmark on `acknowledged` alone would
 * read the two as the same outcome to a sender scanning the timeline. Neither
 * carries "confirmou"/"você confirmou" for the same reason.
 */
const resolvedLabels: Record<Exclude<MessageAcknowledgementState, "pending">, string> = {
  acknowledged: "✓ Recebimento confirmado",
  responded: "Respondido",
  expired: "Confirmação expirada",
  cancelled: "Confirmação cancelada",
};

/**
 * The sender's line for a message with exactly one recipient (issue #846): a
 * DM's "0 de 1"/"1 de 1" reads as a fraction of a person, so a single
 * recipient gets the same direct wording their own view would show them,
 * mirrored back to the sender.
 *
 * Prefers the per-recipient detail's own resolvedAt when it happens to be
 * loaded already (the sender opened the popover, or a realtime event refreshed
 * it); falls back to the aggregate counts alone otherwise; recipients is never
 * fetched just to find a timestamp for this line, since a page of a hundred DMs
 * must not cost a hundred extra reads.
 */
function directRecipientLabel(acknowledgement: MessageAcknowledgement): string {
  const { pending, acknowledged, responded, expired, cancelled, recipients } = acknowledgement;
  const detail = recipients?.[0];
  if (pending > 0) return "Aguardando confirmação";
  if (acknowledged > 0) {
    const at = detail?.state === "acknowledged" ? formatResolvedAt(detail.resolvedAt) : null;
    return at ? `✓ Confirmado às ${at}` : "✓ Confirmado";
  }
  if (responded > 0) return "Respondido";
  if (expired > 0) return "Confirmação expirada";
  if (cancelled > 0) return "Confirmação cancelada";
  return "Aguardando confirmação";
}

function formatResolvedAt(iso?: string): string | null {
  if (!iso) return null;
  const formatted = formatTime(iso);
  return formatted || null;
}

/**
 * The sender's line, and — for more than one recipient — a summary that opens
 * the details popover (issue #846).
 *
 * A single recipient never offers the popover: there is nothing a list of one
 * name tells a sender that the line itself does not already say, and the
 * task's own rule against "0 de 1"/"1 de 1" applies to the affordance too, not
 * only to the wording.
 */
function SenderSummary({
  messageId,
  acknowledgement,
  onOpenDetails,
  resolveIdentities,
}: {
  messageId: string;
  acknowledgement: MessageAcknowledgement;
  onOpenDetails?: (messageId: string) => void;
  resolveIdentities?: (
    userIds: string[],
    signal?: AbortSignal,
  ) => Promise<CallParticipantProfile[]>;
}) {
  const { total, acknowledged, recipients } = acknowledgement;
  const triggerRef = useRef<HTMLButtonElement>(null);
  const [detailsOpen, setDetailsOpen] = useState(false);

  if (total === 1) {
    return (
      <p className="chat-msg-ack__state" data-testid="acknowledgement-summary">
        {directRecipientLabel(acknowledgement)}
      </p>
    );
  }

  const canOpenDetails = Boolean(resolveIdentities);
  const openDetails = () => {
    if (!canOpenDetails) return;
    onOpenDetails?.(messageId);
    setDetailsOpen(true);
  };

  return (
    <div className="chat-msg-ack__summary" data-testid="acknowledgement-summary">
      {canOpenDetails ? (
        <button
          type="button"
          ref={triggerRef}
          className="chat-msg-ack__count chat-msg-ack__count--action"
          aria-haspopup="dialog"
          aria-expanded={detailsOpen}
          aria-label={`${acknowledged} de ${total} pessoas confirmaram. Ver confirmações.`}
          onClick={openDetails}
        >
          {acknowledged} de {total} confirmaram
        </button>
      ) : (
        <span className="chat-msg-ack__count">
          {acknowledged} de {total} confirmaram
        </span>
      )}
      {detailsOpen && resolveIdentities ? (
        <MessageAcknowledgementDetails
          total={total}
          acknowledged={acknowledged}
          recipients={recipients}
          anchorRef={triggerRef}
          resolveIdentities={resolveIdentities}
          onClose={(restoreFocus) => {
            setDetailsOpen(false);
            if (restoreFocus) triggerRef.current?.focus();
          }}
        />
      ) : null}
    </div>
  );
}

/** The recipient's half: the action while pending, the outcome once resolved. */
function RecipientState({
  messageId,
  state,
  submitting,
  onAcknowledge,
}: {
  messageId: string;
  state: MessageAcknowledgementState;
  submitting: boolean;
  onAcknowledge: (messageId: string) => void;
}) {
  if (state !== "pending") {
    return (
      <p className="chat-msg-ack__state" data-state={state} data-testid="acknowledgement-state">
        {resolvedLabels[state]}
      </p>
    );
  }
  return (
    <button
      type="button"
      className="chat-msg-ack__action"
      // Disabled only while this very message's request is in flight, so a
      // second click cannot start a second one and the control still reads as
      // available the rest of the time.
      disabled={submitting}
      aria-busy={submitting}
      onClick={() => onAcknowledge(messageId)}
    >
      {/*
        The label does not change while the request is running. A control whose
        accessible name changes mid-action is announced twice as two different
        controls; aria-busy and the disabled styling carry "working on it"
        without renaming the thing being worked on.
      */}
      Confirmar recebimento
    </button>
  );
}

export default function MessageAcknowledgementStrip({
  messageId,
  acknowledgement,
  senderId,
  currentUserId,
  submitting,
  onAcknowledge,
  onOpenDetails,
  resolveIdentities,
}: MessageAcknowledgementProps) {
  // Nothing to draw until the server has answered, and nothing to draw for a
  // message whose request it reports as asking nobody.
  if (!acknowledgement?.required) return null;
  const role = viewerRole(acknowledgement, senderId, currentUserId);
  // Somebody who neither asked nor was asked has nothing to do here and nothing
  // to be told: no action, no counts, no names. Drawing the sender's view for
  // them was the defect this branch exists to close.
  if (role === "bystander") return null;
  return (
    <div className="chat-msg-ack" data-testid="acknowledgement" data-role={role}>
      {role === "recipient" && acknowledgement.viewerState ? (
        <RecipientState
          messageId={messageId}
          state={acknowledgement.viewerState}
          submitting={submitting}
          onAcknowledge={onAcknowledge}
        />
      ) : (
        <SenderSummary
          messageId={messageId}
          acknowledgement={acknowledgement}
          onOpenDetails={onOpenDetails}
          resolveIdentities={resolveIdentities}
        />
      )}
    </div>
  );
}
