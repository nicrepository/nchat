import type {
  MessageAcknowledgement,
  MessageAcknowledgementRecipient,
  MessageAcknowledgementState,
} from "./chatTypes";

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
 */
const resolvedLabels: Record<Exclude<MessageAcknowledgementState, "pending">, string> = {
  acknowledged: "✓ Recebimento confirmado",
  responded: "✓ Resolvido pela sua resposta",
  expired: "Confirmação expirada",
  cancelled: "Confirmação cancelada",
};

/**
 * How one recipient's answer reads in the sender's list. The same four words
 * the recipient sees for their own state, so the two views agree.
 */
const recipientLabels: Record<MessageAcknowledgementState, string> = {
  pending: "Pendente",
  acknowledged: "Confirmou",
  responded: "Respondeu",
  expired: "Expirou",
  cancelled: "Cancelado",
};

/**
 * The sender's line, and — on demand — who is behind it.
 *
 * A summary strip with a disclosure rather than a modal or a table: the numbers
 * are what a sender reads while scrolling, and the names are what they ask for
 * when a number looks wrong. <details> is the platform's own disclosure, so it
 * is keyboard-operable and announced as expandable without a line of script.
 *
 * The list is drawn only from `recipients`, which is present only when the
 * server chose to send it. There is no second request to try, no fallback, and
 * no local decision about who may see it: absent means the disclosure is not
 * offered at all.
 */
function SenderSummary({ acknowledgement }: { acknowledgement: MessageAcknowledgement }) {
  const { total, acknowledged, pending, recipients } = acknowledgement;
  return (
    <div className="chat-msg-ack__summary" data-testid="acknowledgement-summary">
      <p className="chat-msg-ack__counts">
        <span className="chat-msg-ack__count">
          {acknowledged} de {total} confirmaram
        </span>
        {pending > 0 ? <span className="chat-msg-ack__pending">{pending} pendente(s)</span> : null}
      </p>
      {recipients?.length ? <RecipientList recipients={recipients} /> : null}
    </div>
  );
}

function RecipientList({ recipients }: { recipients: MessageAcknowledgementRecipient[] }) {
  return (
    <details className="chat-msg-ack__details" data-testid="acknowledgement-details">
      <summary className="chat-msg-ack__details-toggle">Ver detalhes</summary>
      <ul className="chat-msg-ack__recipients">
        {recipients.map((recipient) => (
          <li
            key={recipient.recipientId}
            className="chat-msg-ack__recipient"
            data-state={recipient.state}
            data-testid="acknowledgement-recipient"
          >
            <span className="chat-msg-ack__recipient-id">{recipient.recipientId}</span>
            <span className="chat-msg-ack__recipient-state">
              {recipientLabels[recipient.state]}
            </span>
          </li>
        ))}
      </ul>
    </details>
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
        <SenderSummary acknowledgement={acknowledgement} />
      )}
    </div>
  );
}
